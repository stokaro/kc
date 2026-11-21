package models

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// A type is pending until its emptiness is known once; afterwards a recount,
// which forgets the emptiness for a moment, keeps the last answer. A failed
// peek ends pending, so an error never hides a type.
func TestEmptyStatePendingUntilKnown(t *testing.T) {
	cases := []struct {
		name        string
		hasAny      bool
		err         error
		wantEmpty   bool
		wantPending bool
	}{
		{name: "empty", hasAny: false, wantEmpty: true},
		{name: "not empty", hasAny: true, wantEmpty: false},
		{name: "peek failed", err: errors.New("boom"), wantEmpty: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
			item := NewResourceGroupItem(Deps{Ctx: t.Context()}, gvr, schema.GroupVersionKind{}, "default", "pods", []string{"/pods"}, nil, "", nil, true, nil)
			release := make(chan struct{})
			item.hasAny = func(context.Context, schema.GroupVersionResource, string) (bool, error) {
				<-release
				return tc.hasAny, tc.err
			}
			changed := make(chan struct{}, 1)
			item.SetOnChange(func() {
				select {
				case changed <- struct{}{}:
				default:
				}
			})
			if empty, pending := item.EmptyState(); empty || !pending {
				t.Fatalf("before the peek: empty=%v pending=%v, want false true", empty, pending)
			}
			close(release)
			deadline := time.Now().Add(2 * time.Second)
			var empty, pending bool
			for time.Now().Before(deadline) {
				if empty, pending = item.EmptyState(); !pending {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if empty != tc.wantEmpty || pending != tc.wantPending {
				t.Fatalf("after the peek: empty=%v pending=%v, want %v %v", empty, pending, tc.wantEmpty, tc.wantPending)
			}
			if tc.err != nil {
				return
			}
			// What a recount does before it counts again.
			item.mu.Lock()
			item.emptyKnown = false
			item.mu.Unlock()
			if e, p := item.EmptyState(); e != tc.wantEmpty || p {
				t.Fatalf("during a recount: empty=%v pending=%v, want %v false", e, p, tc.wantEmpty)
			}
		})
	}
}
