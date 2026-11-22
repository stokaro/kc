package ui

// White-box: the App has no exported API that runs a save; these tests drive
// its Update with the messages the options dialogs send and read the toasts.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/pkg/appconfig"
)

// runCmd runs cmd and every command a batch holds, and returns the messages.
func runCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, runCmd(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func findMsg[T tea.Msg](msgs []tea.Msg) (T, bool) {
	for _, m := range msgs {
		if v, ok := m.(T); ok {
			return v, true
		}
	}
	var zero T
	return zero, false
}

// newConfigApp returns an App whose configuration file is p.
func newConfigApp(t *testing.T, p string) (*App, tea.Cmd) {
	t.Helper()
	app := NewApp()
	t.Cleanup(app.cancel)
	app.cfgStore = appconfig.NewStore(p)
	return app, app.loadConfig()
}

// saveObjectsDefaults sends C-s from the object options and returns the
// toast the app shows for the result.
func saveObjectsDefaults(t *testing.T, app *App) showToastMsg {
	t.Helper()
	app.modalManager.Show("view_options")
	_, cmd := app.Update(ObjectOptionsChangedMsg{TableMode: "fit", Columns: "wide", ObjectsOrder: "-creation", SaveDefault: true})
	saved, ok := findMsg[configSavedMsg](runCmd(cmd))
	if !ok {
		t.Fatal("C-s did not start a save")
	}
	_, cmd = app.Update(saved)
	toast, ok := findMsg[showToastMsg](runCmd(cmd))
	if !ok {
		t.Fatal("the save result showed no toast")
	}
	return toast
}

func TestBrokenConfigIsReportedAndNotOverwritten(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "config.yaml")
	original := []byte("commands:\n  - name: Mine\n    command: echo\nviewer: [\n")
	if err := os.WriteFile(p, original, 0o644); err != nil {
		t.Fatal(err)
	}
	app, loadCmd := newConfigApp(t, p)

	loadToast, ok := findMsg[showToastMsg](runCmd(loadCmd))
	if !ok || loadToast.info || !strings.Contains(loadToast.text, p) {
		t.Fatalf("load toast = %+v, want an error naming %s", loadToast, p)
	}
	if app.cfg.Viewer.Theme != appconfig.Default().Viewer.Theme {
		t.Fatalf("theme = %q, want the default", app.cfg.Viewer.Theme)
	}

	toast := saveObjectsDefaults(t, app)
	if toast.info || !strings.Contains(toast.text, "session only") || !strings.Contains(toast.text, p) {
		t.Fatalf("save toast = %+v, want a refusal naming %s", toast, p)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("config changed:\n%s", got)
	}
	// The session still uses the new values.
	if app.cfg.Objects.Columns != appconfig.ColumnsModeWide {
		t.Fatalf("session columns = %q, want wide", app.cfg.Objects.Columns)
	}
}

func TestSaveDefaultsKeepsCustomCommands(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("viewer:\n  theme: monokai\ncommands:\n  - name: Mine\n    command: echo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app, loadCmd := newConfigApp(t, p)
	if msgs := runCmd(loadCmd); len(msgs) != 0 {
		t.Fatalf("a good config showed %v", msgs)
	}

	toast := saveObjectsDefaults(t, app)
	if !toast.info || !strings.Contains(toast.text, "Saved") {
		t.Fatalf("save toast = %+v, want a saved notice", toast)
	}
	cfg, err := appconfig.NewStore(p).Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Objects.Columns != appconfig.ColumnsModeWide || cfg.Objects.Order != appconfig.ObjectsOrderCreationDesc || cfg.Panel.Table.Mode != appconfig.TableModeFit {
		t.Fatalf("saved objects %+v table %q, want wide/-creation/fit", cfg.Objects, cfg.Panel.Table.Mode)
	}
	if cfg.Viewer.Theme != "monokai" || len(cfg.Commands) != 1 || cfg.Commands[0].Name != "Mine" {
		t.Fatalf("unrelated settings lost: theme %q, commands %+v", cfg.Viewer.Theme, cfg.Commands)
	}
}

func TestPanelWidthSaveAndSessionNotice(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "config.yaml")
	app, _ := newConfigApp(t, p)

	app.modalManager.Show("view_options")
	_, cmd := app.Update(ViewOptionsCommittedMsg{PanelIndex: 0, SetPanelWidth: true, PanelWidthPercent: 33, Accept: true, Close: true})
	msgs := runCmd(cmd)
	if notice, ok := findMsg[showToastMsg](msgs); !ok || !notice.info || notice.text != sessionOnlyNotice {
		t.Fatalf("Enter showed %+v, want the session-only notice", notice)
	}
	if _, ok := findMsg[configSavedMsg](msgs); ok {
		t.Fatal("Enter saved the configuration")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("Enter wrote %s", p)
	}

	app.modalManager.Show("view_options")
	_, cmd = app.Update(ViewOptionsCommittedMsg{PanelIndex: 0, SetPanelWidth: true, PanelWidthPercent: 66, SaveDefault: true})
	if _, ok := findMsg[configSavedMsg](runCmd(cmd)); !ok {
		t.Fatal("C-s did not save the panel width")
	}
	cfg, err := appconfig.NewStore(p).Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Panel.Width.LeftPercent != 66 {
		t.Fatalf("saved left width = %d, want 66", cfg.Panel.Width.LeftPercent)
	}
}
