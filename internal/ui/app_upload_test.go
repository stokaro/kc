package ui

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/sttts/kc/internal/filexfer"
	"github.com/sttts/kc/internal/models"
	nav "github.com/sttts/kc/internal/navigation"
	"github.com/sttts/kc/internal/podfs"
)

// uploadFS is a container session that receives uploads: a fake exec
// stream that reads each archive like tar -xf - and records its members.
type uploadFS struct {
	transferFS
	listing []podfs.FileEntry

	mu       sync.Mutex
	requests []podfs.ExtractRequest
	members  []string
}

func (f *uploadFS) List(context.Context, string) ([]podfs.FileEntry, error) { return f.listing, nil }

func (f *uploadFS) ExtractTar(ctx context.Context, req podfs.ExtractRequest, archive io.Reader) error {
	tr := tar.NewReader(archive)
	var names []string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		names = append(names, hdr.Name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	f.members = append(f.members, names...)
	return ctx.Err()
}

func uploadJob(t *testing.T, sess podfs.ExecSession, conflicts int, names ...string) (*transferJob, *TransferModel) {
	t.Helper()
	src := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(src, n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dest := models.ContainerFSSource{Context: "kind", Namespace: "ns", Pod: "web", Container: "app", Dir: "/srv"}
	model := NewUploadModel(nil, conflicts, []string{"kubectl cp " + src + "/a.txt ns/web:/srv/a.txt -c app"}, dest)
	return &transferJob{
		model:     model,
		upload:    &filexfer.UploadRequest{Src: src, Names: names, Dir: "/srv"},
		source:    &fakeTransferSource{sess: sess},
		destPanel: 1,
	}, model
}

func TestUploadDialogConfirmsInRedAndCopies(t *testing.T) {
	a := newTransferApp(t)
	sess := &uploadFS{listing: []podfs.FileEntry{{Name: "b.txt", Type: podfs.EntryTypeFile}}}
	job, model := uploadJob(t, sess, 1, "a.txt", "b.txt")
	a.openTransfer(job)

	if modal := a.modalManager.modals[transferModalKey]; modal == nil || !modal.danger {
		t.Fatalf("an upload must ask in a red dialog")
	}
	labels := model.labels()
	if got := strings.Join(labels, ","); got != "Skip existing,Overwrite,Cancel" {
		t.Fatalf("labels = %s", got)
	}
	if labels[model.focus] != "Cancel" {
		t.Fatalf("focus on %q, want Cancel so Enter alone writes nothing", labels[model.focus])
	}
	body := strings.Join(model.lines(), "\n")
	for _, want := range []string{"Copy into the container?", "Same as:", "kubectl cp "} {
		if !strings.Contains(body, want) {
			t.Errorf("dialog lacks %q:\n%s", want, body)
		}
	}

	_, cmd := a.Update(transferChoiceMsg{ok: true, policy: filexfer.Overwrite})
	if model.phase != transferPhaseRunning {
		t.Fatalf("phase = %v after the choice", model.phase)
	}
	a.Update(runUntilDone(t, cmd))
	var got []string
	for _, r := range model.results {
		got = append(got, r.Name+"="+string(r.Status))
	}
	if strings.Join(got, ",") != "a.txt=copied,b.txt=copied" {
		t.Fatalf("results = %v", got)
	}
	if len(sess.requests) != 2 || sess.requests[0].Replace || !sess.requests[1].Replace || sess.requests[1].Dir != "/srv" {
		t.Fatalf("requests = %+v", sess.requests)
	}
	if want := "a.txt," + podfs.UploadCompleteMarker + ",b.txt," + podfs.UploadCompleteMarker; strings.Join(sess.members, ",") != want {
		t.Fatalf("members = %v", sess.members)
	}
	if a.lastCopyDir != "" {
		t.Fatalf("an upload must not move the local copy directory: %q", a.lastCopyDir)
	}
}

func TestUploadDialogEnterAloneCancels(t *testing.T) {
	a := newTransferApp(t)
	sess := &uploadFS{}
	job, model := uploadJob(t, sess, 0, "a.txt")
	a.openTransfer(job)
	_, cmd := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	a.Update(cmd())
	if a.transfer != nil || len(sess.requests) != 0 {
		t.Fatalf("Enter on the default button started the copy")
	}
}

func TestUploadRefusedInReadOnlyMode(t *testing.T) {
	a := newTransferApp(t)
	sess := &uploadFS{}
	job, model := uploadJob(t, sess, 0, "a.txt")
	a.openTransfer(job)
	// The context turns read-only while the dialog is open.
	a.readOnlyOverride = ReadOnlyForced
	_, cmd := a.Update(transferChoiceMsg{ok: true, policy: filexfer.SkipExisting})
	if cmd == nil {
		t.Fatalf("no refusal")
	}
	if a.transfer != nil || model.phase != transferPhaseConfirm || len(sess.requests) != 0 {
		t.Fatalf("a read-only context ran the upload")
	}
	if a.refuseUpload(models.ContainerFSSource{Namespace: "ns", Pod: "web"}) == nil {
		t.Fatalf("refuseUpload permits a read-only context")
	}
	a.readOnlyOverride = ReadOnlyWriteForced
	if a.refuseUpload(models.ContainerFSSource{Namespace: "ns", Pod: "web"}) != nil {
		t.Fatalf("refuseUpload refuses a writable context")
	}
}

func TestStartUploadFromLocalPanel(t *testing.T) {
	a := newTransferApp(t)
	ctx := t.Context()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.leftNav = nav.NewNavigator(models.NewLocalDirFolder(models.Deps{Ctx: ctx}, src))
	a.leftPanel.UseFolder(ctx, true)
	a.leftPanel.SetFolder(ctx, a.leftNav.Current(), false)
	// A container folder without a session: kc cannot reach it.
	a.rightNav = nav.NewNavigator(models.NewPodContainerFSFolder(models.Deps{Ctx: ctx}, []string{"pods"}, "/srv", nil, false))

	cmd, ok := a.startTransfer(a.leftPanel)
	if !ok || cmd == nil || a.transfer != nil {
		t.Fatalf("startTransfer = %v, %v, transfer %v; want a toast and no dialog", cmd, ok, a.transfer)
	}
	// Both panels local: not a copy, F5 keeps its other behavior.
	a.rightNav = nav.NewNavigator(models.NewLocalDirFolder(models.Deps{Ctx: ctx}, src))
	if _, ok := a.startTransfer(a.leftPanel); ok {
		t.Fatalf("two local panels form a copy")
	}
}

func TestMarkedLocalNames(t *testing.T) {
	a := newTransferApp(t)
	ctx := t.Context()
	src := t.TempDir()
	for _, n := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(src, n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(src, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a.activePanel = 0
	a.leftPanel.UseFolder(ctx, true)
	a.handleFolderNav(false, "", models.NewLocalDirFolder(models.Deps{Ctx: ctx}, src))
	// The cursor starts on "..", which is no entry.
	if got := markedLocalNames(ctx, a.leftPanel); len(got) != 0 {
		t.Fatalf("names on .. = %v", got)
	}
	// Nothing marked: the row under the cursor, directories first.
	a.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := markedLocalNames(ctx, a.leftPanel); len(got) != 1 || got[0] != "dir" {
		t.Fatalf("names = %v, want [dir]", got)
	}
	// Marked rows win over the cursor.
	a.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	a.Update(tea.KeyPressMsg{Code: tea.KeyInsert})
	a.Update(tea.KeyPressMsg{Code: tea.KeyInsert})
	if got := markedLocalNames(ctx, a.leftPanel); strings.Join(got, ",") != "a.txt,b.txt" {
		t.Fatalf("marked names = %v, want [a.txt b.txt]", got)
	}
}
