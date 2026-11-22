package models

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sttts/kc/internal/podfs"
)

// consentFactory needs a helper and refuses to add one without consent.
type consentFactory struct {
	mu    sync.Mutex
	specs []podfs.SessionSpec
	sess  podfs.ExecSession
}

func (f *consentFactory) NewSession(_ context.Context, spec podfs.SessionSpec) (podfs.ExecSession, error) {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	f.mu.Unlock()
	if !spec.AllowHelper {
		return nil, fmt.Errorf("%w: %w", podfs.ErrHelperNeeded, podfs.ErrShellMissing)
	}
	return f.sess, nil
}

func TestPodFSAsksBeforeAddingHelper(t *testing.T) {
	factory := &consentFactory{sess: &fakeExecSession{entries: []podfs.FileEntry{{Name: "etc", Type: podfs.EntryTypeDir}}}}
	deps := Deps{Ctx: t.Context(), PodFSFactory: factory}
	deps.ClusterKey.ContextName = "mba"
	handle := newContainerSessionHandle(factory, "kube-system", "coredns-x", "coredns")
	folder := NewPodContainerFSFolder(deps, []string{"containers", "coredns", "root"}, "/", handle, false)

	rows := loadedLines(t, folder, 5)
	action, ok := rows[1].(ConfirmedAction)
	if !ok {
		t.Fatalf("row %T is not a confirmed action", rows[1])
	}
	c := action.Confirmation()
	want := map[string]string{"Context": "mba", "Namespace": "kube-system", "Pod": "coredns-x", "Container": "coredns"}
	for _, d := range c.Details {
		if want[d[0]] != d[1] {
			t.Errorf("%s = %q, want %q", d[0], d[1], want[d[0]])
		}
		delete(want, d[0])
	}
	if len(want) != 0 {
		t.Errorf("missing details: %v", want)
	}

	if err := action.Confirm(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rows = folder.Lines(t.Context(), 0, 5)
		if len(rows) > 1 && rows[1].(interface{ ID() string }).ID() == "/etc" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if id := rows[1].(interface{ ID() string }).ID(); id != "/etc" {
		t.Fatalf("after consent the first row is %q", id)
	}
	factory.mu.Lock()
	defer factory.mu.Unlock()
	if last := factory.specs[len(factory.specs)-1]; !last.AllowHelper {
		t.Fatalf("the session after consent must allow the helper")
	}
	factory.mu.Unlock()
	// The helper exits when idle; the next session must not add another
	// one without asking.
	handle.Invalidate()
	if _, err := handle.Session(t.Context()); !errors.Is(err, podfs.ErrHelperNeeded) {
		t.Fatalf("second session err = %v, want ErrHelperNeeded", err)
	}
	factory.mu.Lock()
}
