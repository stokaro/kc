package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/models"
)

// newLocalPanelApp opens dir in the left panel and moves the cursor to the
// row of name.
func newLocalPanelApp(t *testing.T, dir, name string) *App {
	t.Helper()
	a := newTransferApp(t)
	ctx := t.Context()
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a.activePanel = 0
	a.leftPanel.UseFolder(ctx, true)
	a.handleFolderNav(false, "", models.NewLocalDirFolder(models.Deps{Ctx: ctx}, dir))
	want := filepath.Join(dir, name)
	for range 20 {
		item, ok := a.leftPanel.SelectedNavItem(ctx)
		if le, isLocal := item.(models.LocalEntryItem); ok && isLocal && le.LocalPath() == want {
			return a
		}
		a.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	t.Fatalf("no row for %s", want)
	return nil
}

type localTestNopMsg struct{}

// runEditorInline makes F4 run the editor at once instead of handing it
// the terminal, and counts the runs.
func runEditorInline(a *App) *int {
	runs := new(int)
	a.execProcess = func(cmd *exec.Cmd, cb tea.ExecCallback) tea.Cmd {
		*runs++
		return func() tea.Msg { return cb(cmd.Run()) }
	}
	return runs
}

// fakeEditor writes a script that records its arguments in log and
// appends a line to the file named by its last argument.
func fakeEditor(t *testing.T, exitCode int) (script, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake editor is a shell script")
	}
	dir := t.TempDir()
	script = filepath.Join(dir, "editor.sh")
	log = filepath.Join(dir, "args.log")
	body := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> '" + log + "'; done\n" +
		"for last in \"$@\"; do :; done\n" +
		"printf 'edited\\n' >> \"$last\"\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil { // #nosec G306 -- the test runs it
		t.Fatal(err)
	}
	return script, log
}

func TestLocalFileF3OpensViewer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newLocalPanelApp(t, dir, "notes.txt")
	if !a.capabilitiesForPanel(a.leftPanel).CanView {
		t.Fatal("F3 is disabled on a local file")
	}
	a.Update(tea.KeyPressMsg{Code: tea.KeyF3})
	if !a.modalManager.IsModalVisible() {
		t.Fatal("F3 on a local file opened no viewer")
	}
	if !strings.Contains(a.View().Content, "hello local") {
		t.Fatalf("the viewer does not show the file:\n%s", a.View().Content)
	}
}

func TestLocalF3RefusesWithMessage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte("a\x00b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ entry, want string }{
		{"blob.bin", "is a binary file"},
		{"sub", "is a directory"},
	} {
		t.Run(tc.entry, func(t *testing.T) {
			a := newLocalPanelApp(t, dir, tc.entry)
			a.Update(tea.KeyPressMsg{Code: tea.KeyF3})
			if a.modalManager.IsModalVisible() {
				t.Fatal("F3 opened a viewer")
			}
			// The viewer queues its error; the next message delivers it.
			_, cmd := a.Update(localTestNopMsg{})
			toast := findToast(cmd)
			if toast == nil || !strings.Contains(toast.text, tc.want) {
				t.Fatalf("toast = %+v, want %q", toast, tc.want)
			}
		})
	}
}

func TestLocalFileF4RunsEditorAndRefreshes(t *testing.T) {
	script, log := fakeEditor(t, 0)
	t.Setenv("KUBE_EDITOR", "")
	t.Setenv("EDITOR", script)
	dir := t.TempDir()
	file := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(file, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newLocalPanelApp(t, dir, "notes.txt")
	runs := runEditorInline(a)
	if !a.capabilitiesForPanel(a.leftPanel).CanEdit {
		t.Fatal("F4 is disabled on a local file")
	}

	_, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyF4})
	done, ok := findMsg[localEditFinishedMsg](runCmd(cmd))
	if !ok || *runs != 1 {
		t.Fatalf("F4 ran the editor %d times, finished %v", *runs, ok)
	}
	if done.err != nil || done.path != file {
		t.Fatalf("finished = %+v", done)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "hello\nedited\n" {
		t.Fatalf("file = %q, %v; the editor did not write it", got, err)
	}
	if args, err := os.ReadFile(log); err != nil || string(args) != file+"\n" {
		t.Fatalf("editor args = %q, %v; want the file path only", args, err)
	}

	a.Update(done)
	if !strings.Contains(a.View().Content, "13 B") {
		t.Fatalf("the listing does not show the new size:\n%s", a.View().Content)
	}
}

func TestLocalFileF4EditorWithArguments(t *testing.T) {
	script, log := fakeEditor(t, 0)
	t.Setenv("KUBE_EDITOR", script+" --wait")
	t.Setenv("EDITOR", "must-not-run")
	dir := t.TempDir()
	// The quote and the dollar would break a command line built by hand.
	name := `my "notes" $HOME.txt`
	file := filepath.Join(dir, name)
	if err := os.WriteFile(file, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newLocalPanelApp(t, dir, name)
	runEditorInline(a)
	_, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyF4})
	if done, ok := findMsg[localEditFinishedMsg](runCmd(cmd)); !ok || done.err != nil {
		t.Fatalf("finished = %+v, %v", done, ok)
	}
	if args, err := os.ReadFile(log); err != nil || string(args) != "--wait\n"+file+"\n" {
		t.Fatalf("editor args = %q, %v", args, err)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "x\nedited\n" {
		t.Fatalf("file = %q, %v", got, err)
	}
}

func TestLocalF4RefusesWithMessage(t *testing.T) {
	t.Setenv("KUBE_EDITOR", "")
	t.Setenv("EDITOR", "must-not-run")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte("a\x00b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ entry, want string }{
		{"blob.bin", "is a binary file"},
		{"sub", "is a directory"},
	} {
		t.Run(tc.entry, func(t *testing.T) {
			a := newLocalPanelApp(t, dir, tc.entry)
			runs := runEditorInline(a)
			_, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyF4})
			toast := findToast(cmd)
			if *runs != 0 {
				t.Fatal("F4 ran the editor")
			}
			if toast == nil || !strings.Contains(toast.text, tc.want) {
				t.Fatalf("toast = %+v, want %q", toast, tc.want)
			}
		})
	}
}

func TestLocalFileF4EditorFailure(t *testing.T) {
	script, _ := fakeEditor(t, 3)
	t.Setenv("KUBE_EDITOR", script)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newLocalPanelApp(t, dir, "a.txt")
	runEditorInline(a)
	_, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyF4})
	done, ok := findMsg[localEditFinishedMsg](runCmd(cmd))
	if !ok || done.err == nil {
		t.Fatalf("finished = %+v, %v; want the editor's exit status", done, ok)
	}
	_, cmd = a.Update(done)
	if toast := findToast(cmd); toast == nil || !strings.Contains(toast.text, "failed") {
		t.Fatalf("toast = %+v", toast)
	}
}

func TestLocalEditor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"kube editor first", map[string]string{"KUBE_EDITOR": "nano", "EDITOR": "vim"}, "nano"},
		{"editor", map[string]string{"EDITOR": " vim "}, "vim"},
		{"blank values", map[string]string{"KUBE_EDITOR": " ", "EDITOR": ""}, "vi"},
		{"nothing set", nil, "vi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := localEditor(func(k string) string { return tc.env[k] }); got != tc.want {
				t.Fatalf("localEditor = %q, want %q", got, tc.want)
			}
		})
	}
}
