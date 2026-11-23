package ui

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestLocalF3PagedRereadShowsTheNewFile(t *testing.T) {
	dir := t.TempDir()
	const lines = 120000
	p := writeLargeLocalFile(t, dir, lines)
	a := newLocalPanelApp(t, dir, "large.log")
	_, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyF3})
	pv, ok := a.modalManager.GetActiveModal().content.(*PagedViewer)
	if !ok {
		t.Fatalf("the viewer is %T, want *PagedViewer", a.modalManager.GetActiveModal().content)
	}
	runPagedCmds(t, a, cmd)
	for range 3 {
		_, cmd = a.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
		runPagedCmds(t, a, cmd)
	}
	top := pv.Widget().Top()
	if top == 0 {
		t.Fatal("setup: PgDown did not scroll")
	}

	// An editor writes a new file in its place: same line lengths, more
	// lines, a new prefix. The open file handle still reads the old one.
	var b strings.Builder
	for i := range lines + 40000 {
		fmt.Fprintf(&b, "ROW %07d of the large file\n", i)
	}
	tmp := p + ".new"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, p); err != nil {
		t.Fatal(err)
	}

	_, cmd = a.Update(rereadKeyPress)
	reread, ok := findMsg[pagedRereadMsg](runCmd(cmd))
	if !ok {
		t.Fatal("C-r in the paged viewer reopened nothing")
	}
	_, cmd = a.Update(reread)
	if toast := findToast(cmd); toast == nil || toast.text != "Reread" {
		t.Fatalf("toast = %+v, want Reread", toast)
	}
	runPagedCmds(t, a, cmd)

	if got := pv.Widget().Top(); got != top {
		t.Fatalf("top = %d after the reread, want %d", got, top)
	}
	view := ansi.Strip(a.View().Content)
	if !strings.Contains(view, "ROW ") || strings.Contains(view, "row ") {
		t.Fatalf("the viewer does not show the new content:\n%s", view)
	}
	if status := ansi.Strip(pv.FooterStatus(200)); !strings.Contains(status, "paged, 4.6 MiB") {
		t.Fatalf("status = %q, want the new size", status)
	}
	hints := pv.FooterHints()
	if !slices.ContainsFunc(hints, func(h FooterHint) bool { return h.Key == "C-r" && h.Label == "Reread" && h.Enabled }) {
		t.Fatalf("the footer has no C-r Reread hint: %+v", hints)
	}
	if !a.modalManager.IsModalVisible() {
		t.Fatal("the reread closed the viewer")
	}
}
