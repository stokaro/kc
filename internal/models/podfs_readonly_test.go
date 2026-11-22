package models

import (
	"strings"
	"testing"
)

// In read-only mode a container without a shell shows why it cannot be
// browsed and offers no row that would add a debug helper.
func TestPodFSReadOnlyOffersNoHelper(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		name := "writable"
		if readOnly {
			name = "read-only"
		}
		t.Run(name, func(t *testing.T) {
			factory := &consentFactory{}
			deps := Deps{Ctx: t.Context(), PodFSFactory: factory, ReadOnly: readOnly}
			handle := newContainerSessionHandle(t.Context(), factory, "kube-system", "coredns-x", "coredns")
			folder := NewPodContainerFSFolder(deps, []string{"containers", "coredns", "root"}, "/", handle, false)

			rows := loadedLines(t, folder, 5)
			if len(rows) < 2 {
				t.Fatalf("got %d rows, want the back row and the helper row", len(rows))
			}
			_, isAction := rows[1].(ConfirmedAction)
			if isAction == readOnly {
				t.Fatalf("row %T: confirmed action = %v with read-only = %v", rows[1], isAction, readOnly)
			}
			if !readOnly {
				return
			}
			_, cells, _, _ := rows[1].Columns()
			if len(cells) == 0 || !strings.Contains(cells[0], "read-only") {
				t.Fatalf("read-only row = %q, want a read-only notice", cells)
			}
		})
	}
}

// A helperStartItem created before the context became read-only still
// refuses to approve the helper.
func TestPodFSHelperConfirmRefusedInReadOnly(t *testing.T) {
	factory := &consentFactory{}
	handle := newContainerSessionHandle(t.Context(), factory, "ns", "pod", "c")
	folder := NewPodContainerFSFolder(Deps{Ctx: t.Context(), PodFSFactory: factory, ReadOnly: true}, []string{"root"}, "/", handle, false)
	item := &helperStartItem{SimpleItem: NewSimpleItem("start_helper", []string{"x"}, nil, WhiteStyle()), folder: folder}
	if err := item.Confirm(); err == nil {
		t.Fatal("Confirm succeeded in read-only mode")
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.allowHelper {
		t.Fatal("read-only Confirm approved the helper")
	}
}
