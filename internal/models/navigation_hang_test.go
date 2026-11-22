package models

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	table "github.com/stokaro/kc/internal/table"
)

// A slow apiserver peek must not block the UI thread: EmptyCached returns at
// once, and the locked accessors used while building rows stay responsive.
func TestEmptyCachedDoesNotBlockOnSlowPeek(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	item := NewResourceGroupItem(Deps{Ctx: t.Context()}, gvr, schema.GroupVersionKind{}, "default", "pods", []string{"/pods"}, nil, "", nil, true, nil)
	release := make(chan struct{})
	started := make(chan struct{})
	item.hasAny = func(ctx context.Context, _ schema.GroupVersionResource, _ string) (bool, error) {
		close(started)
		<-release
		return false, nil
	}
	changed := make(chan struct{}, 1)
	item.SetOnChange(func() { changed <- struct{}{} })

	done := make(chan bool)
	go func() { done <- item.EmptyCached() }()
	select {
	case empty := <-done:
		if empty {
			t.Fatalf("unknown emptiness must count as not empty")
		}
	case <-time.After(time.Second):
		t.Fatalf("EmptyCached blocked on the peek")
	}
	<-started
	accessors := make(chan struct{})
	go func() {
		item.TryCount()
		item.Forbidden()
		item.EmptyCached() // a peek is in flight; must not start another or block
		close(accessors)
	}()
	select {
	case <-accessors:
	case <-time.After(time.Second):
		t.Fatalf("item accessors blocked while a peek was in flight")
	}

	close(release)
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatalf("finished peek should notify the folder")
	}
	if !item.EmptyCached() {
		t.Fatalf("peek reported no objects; item should now be empty")
	}
}

func TestLiveRowSourceReportsPersistentListFailure(t *testing.T) {
	failure := errors.New("boom")
	src := newLiveObjectRowSourceWithHooks(
		func(context.Context) ([]table.Row, error) { return nil, failure },
		func() {},
		nil,
	)
	var reported []error
	src.reportErr = func(err error) { reported = append(reported, err) }

	src.Lines(t.Context(), 0, 10)
	if len(reported) != 0 {
		t.Fatalf("a fresh failure (informer still syncing) must not be reported yet")
	}
	src.mu.Lock()
	src.failingSince = time.Now().Add(-2 * listFailureGrace)
	src.mu.Unlock()
	src.Lines(t.Context(), 0, 10)
	if len(reported) != 1 || !errors.Is(reported[0], failure) {
		t.Fatalf("expected the persistent failure to be reported, got %v", reported)
	}
}

func TestReportReadErrorWrapsWhat(t *testing.T) {
	var got error
	deps := Deps{ReportError: func(err error) { got = err }}
	deps.reportReadError("pods in default", context.DeadlineExceeded)
	if got == nil || got.Error() != "could not read pods in default: context deadline exceeded" || !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("unexpected report %v", got)
	}
	Deps{}.reportReadError("x", errors.New("ignored")) // nil reporter is fine
}
