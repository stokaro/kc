package models

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func newTestGroupItem(deps Deps) *ResourceGroupItem {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	return NewResourceGroupItem(deps, gvr, schema.GroupVersionKind{}, "default", "id", []string{"/pods", "v1", ""}, []string{"pods"}, "pods", nil, true, nil)
}

func TestActivityDefersRecountsWhilePaused(t *testing.T) {
	act := &Activity{}
	r := newTestGroupItem(Deps{Ctx: t.Context(), Activity: act})

	act.Pause()
	r.scheduleRecount()
	if r.recounting {
		t.Fatalf("a paused recount must not start")
	}
	if _, ok := act.deferred[r]; !ok {
		t.Fatalf("the recount must be deferred until Resume")
	}

	// A closed item is dropped and never recounted on Resume.
	r.Close()
	if _, ok := act.deferred[r]; ok {
		t.Fatalf("Close must drop the deferred recount")
	}
	act.Resume()
	if r.recounting {
		t.Fatalf("a closed item must not recount")
	}
}

func TestResourceGroupItemCloseStopsWork(t *testing.T) {
	r := newTestGroupItem(Deps{Ctx: t.Context()})
	r.Close()
	r.Close() // idempotent
	r.scheduleRecount()
	if r.recounting {
		t.Fatalf("a closed item must not recount")
	}
	select {
	case <-r.stop:
	default:
		t.Fatalf("Close must stop the peek timers")
	}
}
