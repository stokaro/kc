package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/ui/menubar"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

var rereadKeyPress = tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}

func TestRereadDescribePanelAsksItsWidget(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt")
	a := newLocalPanelApp(t, dir, "a.txt")
	a.leftPanel.SetMode(t.Context(), PanelModeDescribe)
	if a.leftPanel.Mode() != PanelModeDescribe {
		t.Fatal("setup: the panel is not in Describe mode")
	}
	_, cmd := a.Update(rereadKeyPress)
	done, ok := findMsg[panelcontent.RereadDoneMsg](runCmd(cmd))
	if !ok {
		t.Fatal("C-r in Describe did not ask the widget to reread")
	}
	// A local file is not an object: there is nothing to describe.
	_, cmd = a.Update(done)
	if toast := findToast(cmd); toast == nil || toast.text != panelcontent.NothingToReread {
		t.Fatalf("toast = %+v, want %q", toast, panelcontent.NothingToReread)
	}
}

// writeFiles creates empty files named names in dir.
func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// cursorName is the base name of the local entry under the left cursor.
func cursorName(t *testing.T, a *App) string {
	t.Helper()
	item, ok := a.leftPanel.SelectedNavItem(t.Context())
	le, isLocal := item.(models.LocalEntryItem)
	if !ok || !isLocal {
		t.Fatalf("no local entry under the cursor: %#v", item)
	}
	return filepath.Base(le.LocalPath())
}

// markedNames are the base names of the marked local entries, in order.
func markedNames(a *App) []string {
	var out []string
	for _, it := range a.leftPanel.GetSelectedItems() {
		if le, ok := it.Item.(models.LocalEntryItem); ok {
			out = append(out, filepath.Base(le.LocalPath()))
		}
	}
	return out
}

// pressReread sends C-r and returns the text of the toast it shows.
func pressReread(t *testing.T, a *App) string {
	t.Helper()
	_, cmd := a.Update(rereadKeyPress)
	toast := findToast(cmd)
	if toast == nil {
		return ""
	}
	return toast.text
}

func TestRereadLocalPanelListsNewFileAndKeepsCursor(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt", "c.txt")
	a := newLocalPanelApp(t, dir, "c.txt")
	writeFiles(t, dir, "b.txt")
	if strings.Contains(a.View().Content, "b.txt") {
		t.Fatal("the directory was read again before C-r")
	}

	if got := pressReread(t, a); got != "Reread" {
		t.Fatalf("toast = %q, want Reread", got)
	}
	if !strings.Contains(a.View().Content, "b.txt") {
		t.Fatalf("C-r did not list the new file:\n%s", a.View().Content)
	}
	if got := cursorName(t, a); got != "c.txt" {
		t.Fatalf("cursor on %q after the reread, want c.txt: it moved with the new row above it", got)
	}
}

func TestRereadKeepsMarksAndMovesOffGoneRow(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt", "b.txt", "c.txt", "d.txt", "e.txt")
	a := newLocalPanelApp(t, dir, "a.txt")
	mark := tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}
	a.Update(mark) // a, cursor to b
	a.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	a.Update(mark) // c, cursor to d
	a.Update(mark) // d, cursor to e
	a.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := cursorName(t, a); got != "d.txt" {
		t.Fatalf("setup: cursor on %q, want d.txt", got)
	}
	if got := markedNames(a); !slices.Equal(got, []string{"a.txt", "c.txt", "d.txt"}) {
		t.Fatalf("setup: marks %v", got)
	}

	// The row under the cursor and a marked row go away.
	for _, n := range []string{"c.txt", "d.txt"} {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			t.Fatal(err)
		}
	}
	pressReread(t, a)
	if got := markedNames(a); !slices.Equal(got, []string{"a.txt"}) {
		t.Fatalf("marks after the reread = %v, want [a.txt]: marks stay on rows that are still listed", got)
	}
	if got := cursorName(t, a); got != "e.txt" {
		t.Fatalf("cursor on %q after its row went, want the next row e.txt", got)
	}
}

// With typed text on the shell line C-r is the shell's history search
// (ADR 0376); it rereads the panel once the line is empty.
func TestRereadGoesToTypedShellLine(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt")
	a := newLocalPanelApp(t, dir, "a.txt")
	shell := attachShell(t, a)
	typeOnShellLine(a, "kubectl get")
	writeFiles(t, dir, "b.txt")

	if got := pressReread(t, a); got != "" {
		t.Fatalf("toast = %q: the panel took C-r from the typed shell line", got)
	}
	if got := shell.take(); got != "\x12" {
		t.Fatalf("the shell read %q, want C-r", got)
	}
	if strings.Contains(a.View().Content, "b.txt") {
		t.Fatal("the panel was reread while the shell line had text")
	}
}

func TestRereadGoesToFullScreenTerminal(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt")
	a := newLocalPanelApp(t, dir, "a.txt")
	folder := a.leftPanel.Folder()
	before := folder.Len(t.Context())
	a.showTerminal = true
	writeFiles(t, dir, "b.txt")

	// In the full-screen terminal C-r is the shell's history search.
	if got := pressReread(t, a); got != "" {
		t.Fatalf("toast = %q: the panel took C-r from the full-screen terminal", got)
	}
	if n := folder.Len(t.Context()); n != before {
		t.Fatalf("folder Len = %d, want %d: the panel was reread behind the terminal", n, before)
	}
}

func TestRereadMenuEntry(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt")
	a := newLocalPanelApp(t, dir, "a.txt")
	writeFiles(t, dir, "b.txt")

	var entry *menubar.Item
	for _, it := range a.panelMenuItems(0) {
		if it.Label == "&Reread" {
			entry = &it
		}
	}
	if entry == nil {
		t.Fatal("the Left menu has no Reread entry")
	}
	if entry.Shortcut != "C-r" || entry.Disabled {
		t.Fatalf("Reread entry: shortcut %q, disabled %v", entry.Shortcut, entry.Disabled)
	}
	a.activePanel = 1
	toast := findToast(entry.Run())
	if toast == nil || toast.text != "Reread" {
		t.Fatalf("toast = %+v, want Reread", toast)
	}
	if a.activePanel != 0 {
		t.Fatal("the Left menu entry did not act on the left panel")
	}
	if !strings.Contains(a.View().Content, "b.txt") {
		t.Fatal("the Reread menu entry did not reread the panel")
	}
}

func TestRereadF3ViewerKeepsScroll(t *testing.T) {
	dir := t.TempDir()
	var body strings.Builder
	for i := range 200 {
		body.WriteString("line ")
		body.WriteString(strings.Repeat("x", i%7))
		body.WriteString("\n")
	}
	file := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(file, []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newLocalPanelApp(t, dir, "notes.txt")
	a.Update(tea.KeyPressMsg{Code: tea.KeyF3})
	if !a.modalManager.IsModalVisible() {
		t.Fatal("F3 opened no viewer")
	}
	view, ok := a.modalManager.modals["yaml_viewer"].content.(*TextViewer)
	if !ok {
		t.Fatal("the F3 modal holds no text viewer")
	}
	for range 3 {
		a.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	offset, _ := view.inner.Position()
	if offset <= 1 {
		t.Fatalf("setup: PgDown did not scroll, position %d", offset)
	}

	if err := os.WriteFile(file, []byte("rewritten first line\n"+body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	_, cmd := a.Update(rereadKeyPress)
	msg, ok := findMsg[textViewerRereadMsg](runCmd(cmd))
	if !ok {
		t.Fatal("C-r in the viewer read nothing")
	}
	_, cmd = a.Update(msg)
	if toast := findToast(cmd); toast == nil || toast.text != "Reread" {
		t.Fatalf("toast = %+v, want Reread", toast)
	}
	if !strings.Contains(view.inner.PlainText(), "rewritten first line") {
		t.Fatal("the viewer does not show the reread file")
	}
	if got, _ := view.inner.Position(); got != offset {
		t.Fatalf("position = %d after the reread, want %d", got, offset)
	}
	if !a.modalManager.IsModalVisible() {
		t.Fatal("the reread closed the viewer")
	}
}
