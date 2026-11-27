package ui

// White-box: the read-only decision and the error bar are App internals;
// these tests load real files through the App's config store.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	models "github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/pkg/appconfig"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

// brokenLayout is a layout value that does not load, as in issue 372.
const brokenLayout = "layout:\n  rightPanelMode: describ\n"

// notYAML has an indentation slip: nothing of it parses.
const notYAML = "readOnly:\n  contexts: [staging]\n layout:\n  rightPanelMode: info\n"

// configDeleteTestApp is readOnlyTestApp with the configuration loaded from
// a file: the left panel shows a deletable object read from context
// "eu-prod".
func configDeleteTestApp(t *testing.T, file string, override ReadOnlyOverride) (*App, *Panel) {
	t.Helper()
	app := NewApp()
	t.Cleanup(app.cancel)
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	app.cfgStore = appconfig.NewStore(p)
	app.loadConfig()
	app.readOnlyOverride = override
	app.width, app.height = 120, 30
	model, _ := app.Update(tea.WindowSizeMsg{Width: app.width, Height: app.height})
	app = model.(*App)
	useTestCluster(app, &kubeconfig.Context{Name: "eu-prod", Kubeconfig: &kubeconfig.Kubeconfig{Path: "/nonexistent/kubeconfig"}}, nil)
	panel := app.leftPanel
	panel.SetEnvironmentSupplier(func() PanelEnvironment {
		env := app.panelEnvironment(panel)
		env.AllowDeleteObjects = true
		return env
	})
	ctx := t.Context()
	panel.UseFolder(ctx, true)
	panel.SetFolder(ctx, newStubFolder([]models.Item{readOnlyTestObject}), false)
	panel.SelectByRowID(ctx, readOnlyTestObject.id)
	panel.SetCurrentPath("/namespaces")
	app.activePanel = 0
	return app, panel
}

// TestConfigThatDoesNotLoadKeepsReadOnly presses F8 on an object of context
// eu-prod with configuration files that do not load in full. The context
// stays read-only whenever the file asks for it, or when kc cannot tell
// what the file asks for; --write still allows the change.
func TestConfigThatDoesNotLoadKeepsReadOnly(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		override ReadOnlyOverride
		readOnly bool
	}{
		{
			name:     "an invalid unrelated field keeps readOnly.contexts",
			file:     "readOnly:\n  contexts: [\"*prod*\"]\n" + brokenLayout,
			readOnly: true,
		},
		{
			name:     "an invalid unrelated field leaves other contexts writable",
			file:     "readOnly:\n  contexts: [staging]\n" + brokenLayout,
			readOnly: false,
		},
		{
			name:     "a file that is not valid YAML makes every context read-only",
			file:     notYAML,
			readOnly: true,
		},
		{
			name:     "a readOnly section that does not load makes every context read-only",
			file:     "readOnly:\n  contexts: staging\n",
			readOnly: true,
		},
		{
			name:     "--write allows writes with a file that is not valid YAML",
			file:     notYAML,
			override: ReadOnlyWriteForced,
			readOnly: false,
		},
		{
			name:     "--readonly with a file whose readOnly loaded",
			file:     "readOnly:\n  contexts: [staging]\n" + brokenLayout,
			override: ReadOnlyForced,
			readOnly: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app, panel := configDeleteTestApp(t, tc.file, tc.override)
			if app.cfgLoadErr == nil {
				t.Fatal("the file loaded; the test needs one that does not")
			}
			if got := app.readOnlyFor("eu-prod"); got != tc.readOnly {
				t.Fatalf("readOnlyFor(eu-prod) = %v, want %v", got, tc.readOnly)
			}
			cmd := app.deleteResourceForPanel(panel)
			if tc.readOnly {
				wantRefusal(t, cmd, "Delete")
				if app.pendingDelete != nil || app.modalManager.IsModalVisible() {
					t.Fatal("F8 went past the read-only gate")
				}
				return
			}
			if isRefusal(cmd) {
				t.Fatal("F8 refused in a writable context")
			}
			if app.pendingDelete == nil && !app.modalManager.IsModalVisible() {
				t.Fatal("F8 did not ask to delete in a writable context")
			}
		})
	}
}

// configErrorLayoutApp loads ~/.kc/config.yaml from a temporary home and
// sizes the screen. HOME is set, so the test is not parallel.
func configErrorLayoutApp(t *testing.T, file string, width, height int) (*App, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := filepath.Join(home, ".kc", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	t.Cleanup(app.cancel)
	app.cfgStore = appconfig.NewStore(p)
	app.loadConfig()
	model, _ := app.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return model.(*App), p
}

// screenText is the screen as one line of words, so a check does not
// depend on where the error bar wraps.
func screenText(app *App) string {
	return strings.Join(strings.Fields(strings.Join(screenLines(app), " ")), " ")
}

// TestConfigErrorStaysOnScreen checks the bar between the panels and the
// command line: it names the file and the field, it outlives toasts, and
// it is gone once the file loads.
func TestConfigErrorStaysOnScreen(t *testing.T) {
	const want = `Config layout not loaded, using its defaults: ~/.kc/config.yaml: layout.rightPanelMode: unknown panel mode "describ"; use list, describe, manifest, logs or info`
	app, p := configErrorLayoutApp(t, "readOnly:\n  contexts: [good]\n"+brokenLayout, 120, 30)

	rows := app.configErrorRows()
	if rows != 2 {
		t.Fatalf("configErrorRows() = %d at 120 columns, want 2", rows)
	}
	want120 := app.height - 1 - app.menuBarRows() - rows - app.inlineTerminalHeight() - app.commandLineHeight()
	if got := app.panelsHeight(); got != want120 {
		t.Fatalf("panelsHeight() = %d, want %d", got, want120)
	}
	lines := screenLines(app)
	if len(lines) != app.height {
		t.Fatalf("view has %d lines, want %d", len(lines), app.height)
	}
	bottom := app.menuBarRows() + app.panelsHeight() - 1
	if !strings.HasPrefix(lines[bottom], "└") {
		t.Fatalf("line %d = %q, want the bottom border of the panels", bottom, lines[bottom])
	}
	if !strings.HasPrefix(lines[bottom+1], "Config layout not loaded") || app.commandLineY() != bottom+rows+1 {
		t.Fatalf("lines after the panels = %q, command line at %d; want the bar, then the command line", lines[bottom+1:bottom+rows+1], app.commandLineY())
	}
	if got := screenText(app); !strings.Contains(got, want) {
		t.Fatalf("screen does not show %q:\n%s", want, strings.Join(screenLines(app), "\n"))
	}

	// A toast covers the command line, not the bar, and the bar stays
	// after the toast has gone.
	model, _ := app.Update(showToastMsg{text: "Saved as defaults", ttl: time.Second, info: true})
	app = model.(*App)
	if got := screenText(app); !strings.Contains(got, "Saved as defaults") || !strings.Contains(got, want) {
		t.Fatalf("with a toast, the screen is:\n%s", strings.Join(screenLines(app), "\n"))
	}
	app.toastUntil = time.Now().Add(-time.Second)
	model, _ = app.Update(toastTickMsg{})
	app = model.(*App)
	if app.toastActive {
		t.Fatal("the toast did not go away")
	}
	if got := screenText(app); !strings.Contains(got, want) {
		t.Fatalf("after the toast, the bar is gone:\n%s", strings.Join(screenLines(app), "\n"))
	}

	// The full-screen terminal has no command line and no bar.
	app.showTerminal = true
	if got := app.configErrorRows(); got != 0 {
		t.Fatalf("configErrorRows() = %d with the terminal full screen, want 0", got)
	}
	app.showTerminal = false

	// kc reads the file at start; once it loads, the bar is gone and the
	// panels take its rows back.
	if err := os.WriteFile(p, []byte("readOnly:\n  contexts: [good]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app.loadConfig()
	model, _ = app.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	app = model.(*App)
	if app.cfgLoadErr != nil || app.configErrorRows() != 0 {
		t.Fatalf("after the fix: load error %v, %d bar rows", app.cfgLoadErr, app.configErrorRows())
	}
	if got := app.panelsHeight(); got != want120+rows {
		t.Fatalf("panelsHeight() = %d after the fix, want %d", got, want120+rows)
	}
	if got := screenText(app); strings.Contains(got, "not loaded") {
		t.Fatalf("the bar is still drawn:\n%s", strings.Join(screenLines(app), "\n"))
	}
}

// TestConfigErrorBarRows checks how the bar fits the screen: one row when
// the text fits, two at most, one on a short screen, none on a tiny one.
func TestConfigErrorBarRows(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		wantRows      int
		wantEllipsis  bool
	}{
		{name: "wide screen", width: 220, height: 40, wantRows: 1},
		{name: "two rows", width: 120, height: 30, wantRows: 2},
		{name: "cut after two rows", width: 50, height: 30, wantRows: 2, wantEllipsis: true},
		{name: "short screen", width: 120, height: 12, wantRows: 1, wantEllipsis: true},
		{name: "tiny screen", width: 120, height: 7, wantRows: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app, _ := configErrorLayoutApp(t, brokenLayout, tc.width, tc.height)
			lines := app.configErrorLines()
			if len(lines) != tc.wantRows {
				t.Fatalf("bar rows = %d %q, want %d", len(lines), lines, tc.wantRows)
			}
			if tc.wantRows > 0 && !strings.HasPrefix(lines[0], "Config layout not loaded") {
				t.Fatalf("first row = %q", lines[0])
			}
			if got := tc.wantRows > 0 && strings.HasSuffix(lines[len(lines)-1], "…"); got != tc.wantEllipsis {
				t.Fatalf("last row = %q, ends in … = %v, want %v", lines[len(lines)-1], got, tc.wantEllipsis)
			}
			if got := len(screenLines(app)); tc.wantRows > 0 && got != tc.height {
				t.Fatalf("view has %d lines, want %d", got, tc.height)
			}
		})
	}
}

func TestConfigErrorText(t *testing.T) {
	t.Parallel()
	const home = "/home/me"
	path := filepath.Join(home, ".kc", "config.yaml")
	yamlErr := errors.New("yaml: line 2: did not find expected key")
	layoutErr := errors.New(`layout.rightPanelMode: unknown panel mode "describ"`)
	tests := []struct {
		name     string
		err      *appconfig.LoadError
		override ReadOnlyOverride
		home     string
		want     string
	}{
		{
			name: "nothing loaded",
			err:  &appconfig.LoadError{Path: path, Err: yamlErr},
			home: home,
			want: "Config not loaded, every context read-only: ~/.kc/config.yaml: yaml: line 2: did not find expected key",
		},
		{
			name:     "nothing loaded, --write",
			err:      &appconfig.LoadError{Path: path, Err: yamlErr},
			override: ReadOnlyWriteForced,
			home:     home,
			want:     "Config not loaded, using the defaults: ~/.kc/config.yaml: yaml: line 2: did not find expected key",
		},
		{
			name: "readOnly not loaded",
			err:  &appconfig.LoadError{Path: path, Err: errors.New(`readOnly.contexts: want a list, got "prod"`), Sections: []string{"readOnly"}},
			home: home,
			want: `Config readOnly not loaded, every context read-only: ~/.kc/config.yaml: readOnly.contexts: want a list, got "prod"`,
		},
		{
			name: "another section",
			err:  &appconfig.LoadError{Path: path, Err: layoutErr, Sections: []string{"layout"}},
			home: home,
			want: `Config layout not loaded, using its defaults: ~/.kc/config.yaml: layout.rightPanelMode: unknown panel mode "describ"`,
		},
		{
			name: "two sections",
			err: &appconfig.LoadError{Path: path, Err: errors.Join(layoutErr, errors.New(`charts.graphics: unknown value "sixel"`)),
				Sections: []string{"layout", "charts"}},
			home: home,
			want: `Config layout, charts not loaded, using their defaults: ~/.kc/config.yaml: layout.rightPanelMode: unknown panel mode "describ"; charts.graphics: unknown value "sixel"`,
		},
		{
			name: "a file outside the home directory",
			err:  &appconfig.LoadError{Path: "/etc/kc/config.yaml", Err: layoutErr, Sections: []string{"layout"}},
			home: home,
			want: `Config layout not loaded, using its defaults: /etc/kc/config.yaml: layout.rightPanelMode: unknown panel mode "describ"`,
		},
		{
			name: "a home directory that is a prefix of another",
			err:  &appconfig.LoadError{Path: "/home/me2/.kc/config.yaml", Err: layoutErr, Sections: []string{"layout"}},
			home: home,
			want: `Config layout not loaded, using its defaults: /home/me2/.kc/config.yaml: layout.rightPanelMode: unknown panel mode "describ"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := configErrorText(tc.err, tc.override, tc.home); got != tc.want {
				t.Fatalf("text =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}
