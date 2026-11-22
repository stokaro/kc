package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/filexfer"
	"github.com/stokaro/kc/internal/models"
	nav "github.com/stokaro/kc/internal/navigation"
)

// The copy-to-local dialog proposes the directory of the other panel when
// it lists the local filesystem, and the system temp directory otherwise;
// neither the last copy directory nor the working directory.
func TestCopyDialogDefaultDirectory(t *testing.T) {
	t.Parallel()
	local := t.TempDir()
	cases := []struct {
		name       string
		otherLocal bool
		want       string
	}{
		{name: "other panel is local", otherLocal: true, want: local},
		{name: "other panel is a cluster folder", want: os.TempDir()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newTransferApp(t)
			a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
			a.lastCopyDir = t.TempDir()
			a.activePanel = 0
			if tc.otherLocal {
				chain := models.LocalAncestors(models.Deps{Ctx: t.Context()}, local)
				a.rightNav = nav.NewNavigator(chain[len(chain)-1])
			}
			a.showCopyDialog(viewContentCopyRequest("pods/web", "web.yaml", "kind: Pod\n"))
			want := filepath.Join(tc.want, "web.yaml")
			if abs, err := filepath.Abs(want); err == nil {
				want = abs
			}
			if got := a.copyInput.input.Value(); got != want {
				t.Fatalf("default target = %q, want %q", got, want)
			}
			if !a.modalManager.IsModalVisible() {
				t.Fatal("the copy dialog is not open")
			}
		})
	}
}

// The copy dialogs render on mc's layout, inside a frame whose rules are
// joined with tees, at a fixed screen size.
func TestCopyDialogsRenderedLayout(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		open func(t *testing.T, a *App)
		want []string
	}{
		{
			name: "object to local file",
			open: func(_ *testing.T, a *App) {
				a.showCopyDialog(viewContentCopyRequest("pods/web", "web.yaml", "kind: Pod\n"))
			},
			want: []string{"─ Copy ─", `│ Copy "pods/web" to:`, "├──", "──┤", "│ [ ] Overwrite an existing file", "[< OK >] [ Cancel ]"},
		},
		{
			name: "container to local",
			open: func(t *testing.T, a *App) {
				a.openTransfer(&transferJob{
					model:  NewTransferModel([][2]string{{"Pod", "demo/web"}, {"Entries", "a.txt"}}, t.TempDir(), nil),
					req:    filexfer.Request{Dir: "/data", Entries: fileEntries("a.txt")},
					source: &fakeTransferSource{},
				})
			},
			want: []string{"Copy from the container?", "│ to:", "├──", "│ [ ] Overwrite existing entries", "[< OK >] [ Cancel ]"},
		},
		{
			name: "local to container",
			open: func(t *testing.T, a *App) {
				job, _ := uploadJob(t, &uploadFS{}, 1, "a.txt")
				a.openTransfer(job)
			},
			want: []string{"Copy into the container?", "├──", "│ [ ] Overwrite existing entries", "[ OK ] [< Cancel >]"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newTransferApp(t)
			a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
			tc.open(t, a)
			requireDrawn(t, dialogScreen(t, a), tc.want...)
		})
	}
}

// Editing the "to:" line of a copy from a container recounts the entries
// that exist there and sends the new directory with the answer.
func TestTransferDialogEditsTarget(t *testing.T) {
	t.Parallel()
	first, second := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(second, "a.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewTransferModel(nil, first, existingEntries(fileEntries("a.txt")))
	m.SetDimensions(60, 20)
	if body := strings.Join(m.lines(), "\n"); strings.Contains(body, "exist in the destination") {
		t.Fatalf("a conflict in an empty directory:\n%s", body)
	}
	m.dest.SetValue(second[:len(second)-1])
	m.Update(tea.KeyPressMsg{Code: rune(second[len(second)-1]), Text: second[len(second)-1:]})
	if body := ansi.Strip(strings.Join(m.lines(), "\n")); !strings.Contains(body, "1 of the entries exist in the destination.") {
		t.Fatalf("no conflict note after the edit:\n%s", body)
	}
	m.Update(tea.KeyPressMsg{Code: 'w', Mod: tea.ModAlt})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	choice, ok := cmd().(transferChoiceMsg)
	if !ok || !choice.ok || choice.dest != second || choice.policy != filexfer.Overwrite {
		t.Fatalf("choice = %+v", choice)
	}

	m.dest.SetValue(filepath.Join(second, "missing"))
	if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("a missing target directory started the copy")
	}
	if body := strings.Join(m.lines(), "\n"); !strings.Contains(body, "Not a local directory") {
		t.Fatalf("no error for a missing directory:\n%s", body)
	}
}
