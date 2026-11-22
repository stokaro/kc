package ui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/filexfer"
	"github.com/stokaro/kc/internal/models"
	nav "github.com/stokaro/kc/internal/navigation"
	"github.com/stokaro/kc/internal/podfs"
)

// transferFS is a container filesystem session for the copy tests.
type transferFS struct {
	files map[string]string
	// block makes ReadFile of this path wait for the context.
	block   string
	started chan struct{}
}

func (f *transferFS) List(context.Context, string) ([]podfs.FileEntry, error) { return nil, nil }

func (f *transferFS) ReadFile(ctx context.Context, p string, _ int64) (io.ReadCloser, error) {
	if p == f.block {
		close(f.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	data, ok := f.files[p]
	if !ok {
		return nil, fmt.Errorf("ENOENT %s", p)
	}
	return io.NopCloser(bytes.NewReader([]byte(data))), nil
}

func (f *transferFS) Close() error { return nil }

type fakeTransferSource struct {
	sess        podfs.ExecSession
	invalidated atomic.Bool
	// held counts sessions handed out and not released yet.
	held atomic.Int32
}

func (s *fakeTransferSource) TransferSession(context.Context) (podfs.ExecSession, func(), error) {
	s.held.Add(1)
	return s.sess, func() { s.held.Add(-1) }, nil
}
func (s *fakeTransferSource) InvalidateSession() { s.invalidated.Store(true) }

func newTransferApp(t *testing.T) *App {
	t.Helper()
	a := NewApp()
	a.ctx = t.Context()
	a.leftNav = nav.NewNavigator(mkFolder("left"))
	a.rightNav = nav.NewNavigator(mkFolder("right"))
	return a
}

func fileEntries(names ...string) []podfs.FileEntry {
	out := make([]podfs.FileEntry, 0, len(names))
	for _, n := range names {
		out = append(out, podfs.FileEntry{Name: n, Type: podfs.EntryTypeFile, Mode: 0o100644})
	}
	return out
}

func TestTransferDialogRunsCopyAndShowsResults(t *testing.T) {
	a := newTransferApp(t)
	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(dest, "b.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := &fakeTransferSource{sess: &transferFS{files: map[string]string{"/data/a.txt": "A", "/data/b.txt": "B"}}}
	model := NewTransferModel(nil, 1)
	a.openTransfer(&transferJob{
		model:     model,
		req:       filexfer.Request{Dir: "/data", Entries: fileEntries("a.txt", "b.txt"), Dest: dest},
		source:    src,
		destPanel: 1,
	})
	if got := strings.Join(model.labels(), ","); got != "Skip existing,Overwrite,Cancel" {
		t.Fatalf("labels with a conflict = %s", got)
	}

	// The dialog is open, so the answer must pass it to reach the app.
	_, cmd := a.Update(transferChoiceMsg{ok: true, policy: filexfer.SkipExisting})
	if model.phase != transferPhaseRunning {
		t.Fatalf("phase = %v after the choice", model.phase)
	}
	done := runUntilDone(t, cmd)
	a.Update(done)
	if model.phase != transferPhaseDone {
		t.Fatalf("phase = %v after the result", model.phase)
	}
	// A held session never closes for being idle; the copy must let go.
	if n := src.held.Load(); n != 0 {
		t.Fatalf("%d filesystem sessions still held after the copy", n)
	}
	var got []string
	for _, r := range model.results {
		got = append(got, r.Name+"="+string(r.Status))
	}
	if strings.Join(got, ",") != "a.txt=copied,b.txt=skipped" {
		t.Fatalf("results = %v", got)
	}
	if data, _ := os.ReadFile(filepath.Join(dest, "b.txt")); string(data) != "old" {
		t.Fatalf("skip existing replaced b.txt: %q", data)
	}
	if title := model.title(); title != "Copy finished: 1 copied, 1 skipped" {
		t.Fatalf("title = %q", title)
	}
	if a.lastCopyDir != dest {
		t.Fatalf("lastCopyDir = %q", a.lastCopyDir)
	}

	a.Update(transferCloseMsg{})
	if a.transfer != nil || a.modalManager.IsModalVisible() {
		t.Fatalf("dialog still open after close")
	}
}

// runUntilDone runs the commands of the start batch and returns the result.
func runUntilDone(t *testing.T, cmd tea.Cmd) transferDoneMsg {
	t.Helper()
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("start returned %T, want a batch", msg)
	}
	results := make(chan tea.Msg, len(batch))
	for _, c := range batch {
		go func() { results <- c() }()
	}
	for range batch {
		select {
		case m := <-results:
			if d, ok := m.(transferDoneMsg); ok {
				return d
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("copy did not finish")
		}
	}
	t.Fatalf("no result message")
	return transferDoneMsg{}
}

func TestTransferDialogCancel(t *testing.T) {
	a := newTransferApp(t)
	dest := t.TempDir()
	fsys := &transferFS{files: map[string]string{"/data/a.txt": "A"}, block: "/data/slow.bin", started: make(chan struct{})}
	src := &fakeTransferSource{sess: fsys}
	model := NewTransferModel(nil, 0)
	a.openTransfer(&transferJob{
		model:  model,
		req:    filexfer.Request{Dir: "/data", Entries: fileEntries("a.txt", "slow.bin", "c.txt"), Dest: dest},
		source: src,
	})
	_, cmd := a.Update(transferChoiceMsg{ok: true})

	doneCh := make(chan transferDoneMsg, 1)
	go func() { doneCh <- runUntilDone(t, cmd) }()
	<-fsys.started

	// Esc while the copy runs cancels it and keeps the dialog open.
	handled, esc := model.HandleModalEscape(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !handled || esc == nil {
		t.Fatalf("Esc not handled while running")
	}
	a.Update(esc())
	if again := model.escape(); again != nil {
		t.Fatalf("a second Esc sent another cancel")
	}
	var done transferDoneMsg
	select {
	case done = <-doneCh:
	case <-time.After(10 * time.Second):
		t.Fatalf("cancel did not stop the copy")
	}
	a.Update(done)
	var got []string
	for _, r := range model.results {
		got = append(got, r.Name+"="+string(r.Status))
	}
	if strings.Join(got, ",") != "a.txt=copied,slow.bin=canceled,c.txt=canceled" {
		t.Fatalf("results = %v", got)
	}
	if !src.invalidated.Load() {
		t.Fatalf("a canceled copy must drop the filesystem session")
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != 1 || entries[0].Name() != "a.txt" {
		t.Fatalf("destination after cancel = %v", entries)
	}
	if !a.modalManager.IsModalVisible() {
		t.Fatalf("the dialog must stay open to show the result")
	}
}

func TestTransferDialogEscBeforeStartCloses(t *testing.T) {
	a := newTransferApp(t)
	model := NewTransferModel(nil, 0)
	a.openTransfer(&transferJob{model: model, source: &fakeTransferSource{}})
	if got := strings.Join(model.labels(), ","); got != "Copy,Cancel" {
		t.Fatalf("labels = %s", got)
	}
	_, esc := model.HandleModalEscape(tea.KeyPressMsg{Code: tea.KeyEscape})
	a.Update(esc())
	if a.transfer != nil || a.modalManager.IsModalVisible() {
		t.Fatalf("Esc before the start must close the dialog")
	}
}

func TestToggleLocalPanel(t *testing.T) {
	a := newTransferApp(t)
	ctx := t.Context()
	a.leftPanel.UseFolder(ctx, true)
	a.leftPanel.SetFolder(ctx, a.leftNav.Current(), false)
	dir := filepath.Join(t.TempDir(), "x")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	a.lastCopyDir = dir
	a.activePanel = 0

	a.toggleLocalPanel()
	cur, ok := a.leftNav.Current().(*models.LocalDirFolder)
	if !ok || cur.LocalDir() != dir {
		t.Fatalf("current = %T, want the local folder %s", a.leftNav.Current(), dir)
	}
	if got := a.leftPanel.GetCurrentPath(); got != models.LocalPathPrefix+dir {
		t.Fatalf("panel path = %q", got)
	}
	// Back walks up the directory tree.
	a.handleFolderNav(true, "", nil)
	if up, ok := a.leftNav.Current().(*models.LocalDirFolder); !ok || up.LocalDir() != filepath.Dir(dir) {
		t.Fatalf("Back went to %T", a.leftNav.Current())
	}
	// M-l again leaves the local tree.
	a.toggleLocalPanel()
	if _, ok := a.leftNav.Current().(*models.LocalDirFolder); ok || a.leftNav.HasBack() {
		t.Fatalf("toggle did not return to the cluster root")
	}
}

func TestTransferMessagesPassModals(t *testing.T) {
	for _, msg := range []tea.Msg{transferChoiceMsg{}, transferCancelMsg{}, transferCloseMsg{}, transferTickMsg{}, transferDoneMsg{}} {
		if !passesModal(msg) {
			t.Errorf("%T is swallowed by an open dialog", msg)
		}
	}
}

func TestShortenPathKeepsTheEnd(t *testing.T) {
	long := "/private/tmp/some/very/long/path/that/does/not/fit/into/the/dialog/dl"
	got := shortenPath(long)
	if len([]rune(got)) != transferDetailWidth || !strings.HasPrefix(got, "...") || !strings.HasSuffix(got, "/dialog/dl") {
		t.Fatalf("shortenPath = %q", got)
	}
	if shortenPath("/tmp") != "/tmp" {
		t.Fatalf("a short path must stay as it is")
	}
}
