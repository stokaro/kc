package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/ui/viewer"
)

// runPagedCmds runs the commands of cmd that a paged viewer issued and
// feeds their messages to the app, until none are left. Other messages
// (access prefetches, redraw ticks) are dropped.
func runPagedCmds(t *testing.T, a *App, cmd tea.Cmd) {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 1000 {
			t.Fatal("the viewer keeps issuing commands")
		}
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case viewer.PagedMsg:
			if strings.Contains(fmt.Sprintf("%T", msg), "Tick") {
				continue
			}
			_, next := a.Update(msg)
			queue = append(queue, next)
		}
	}
}

func writeLargeLocalFile(t *testing.T, dir string, lines int) string {
	t.Helper()
	var b strings.Builder
	for i := range lines {
		fmt.Fprintf(&b, "row %07d of the large file\n", i)
	}
	if b.Len() <= models.LocalViewWholeBytes {
		t.Fatalf("the test file has %d bytes, not more than the paging limit", b.Len())
	}
	p := filepath.Join(dir, "large.log")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLocalF3PagesALargeFile(t *testing.T) {
	dir := t.TempDir()
	const lines = 120000 // about 3.4 MiB
	writeLargeLocalFile(t, dir, lines)
	a := newLocalPanelApp(t, dir, "large.log")

	_, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyF3})
	if !a.modalManager.IsModalVisible() {
		t.Fatal("F3 on a large local file opened no viewer")
	}
	pv, ok := a.modalManager.GetActiveModal().content.(*PagedViewer)
	if !ok {
		t.Fatalf("the viewer is %T, want *PagedViewer", a.modalManager.GetActiveModal().content)
	}
	runPagedCmds(t, a, cmd)

	view := ansi.Strip(a.View().Content)
	if !strings.Contains(view, "row 0000000 of the large file") {
		t.Fatalf("the viewer does not show the top of the file:\n%s", view)
	}
	if !strings.Contains(view, "paged, 3.4 MiB") {
		t.Fatalf("the footer does not say the file is paged:\n%s", view)
	}
	if status := ansi.Strip(pv.FooterStatus(200)); !strings.Contains(status, "paged, 3.4 MiB, highlighting off · line 1") {
		t.Fatalf("status = %q", status)
	}

	_, cmd = a.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	runPagedCmds(t, a, cmd)
	if view := ansi.Strip(a.View().Content); !strings.Contains(view, fmt.Sprintf("row %07d of the large file", lines-1)) {
		t.Fatalf("End does not show the last line:\n%s", view)
	}
	if idx, _ := pv.Widget().Document().CachedBlocks(); len(idx) > 16 {
		t.Fatalf("the viewer caches %d blocks", len(idx))
	}

	// F3 closes the viewer and the file.
	a.Update(tea.KeyPressMsg{Code: tea.KeyF3})
	if a.modalManager.IsModalVisible() {
		t.Fatal("F3 did not close the viewer")
	}
	if next, _ := pv.Widget().Update(tea.KeyPressMsg{Code: tea.KeyHome}); next != nil {
		t.Fatal("the closed viewer still reads")
	}
}

func TestLocalF3ShowsASmallFileWhole(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small.yaml"), []byte("kind: Pod\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newLocalPanelApp(t, dir, "small.yaml")
	a.Update(tea.KeyPressMsg{Code: tea.KeyF3})
	if _, ok := a.modalManager.GetActiveModal().content.(*TextViewer); !ok {
		t.Fatalf("a small file opened in %T, want the highlighting *TextViewer", a.modalManager.GetActiveModal().content)
	}
}
