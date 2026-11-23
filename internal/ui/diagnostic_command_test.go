package ui

// White-box tests: a diagnostic command starts from the command menu (or
// its hotkey) and ends in App's modal stack; neither has an exported
// surface. The tool is a shell script in a temporary directory, so no
// real tool or cluster is involved.

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/diagnostic"
	"github.com/stokaro/kc/internal/execctx"
	models "github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/pkg/appconfig"
)

// fakeTool writes an executable shell script and returns its path.
func fakeTool(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake tool is a POSIX shell script")
	}
	path := filepath.Join(t.TempDir(), "fake-tool")
	// #nosec G306 -- the fake tool must be executable; it lives in t.TempDir.
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func krrFixturePath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "diagnostic", "testdata", "krr-simple.json"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func diagnosticCommand(tool string, args ...string) appconfig.CommandConfig {
	return appconfig.CommandConfig{
		Name: "KRR", Type: appconfig.CommandTypeGlobal, Hotkey: "M-1",
		Diagnostic: &appconfig.DiagnosticConfig{Adapter: appconfig.DiagnosticAdapterKRR, Args: append([]string{tool}, args...)},
	}
}

func viewerText(t *testing.T, app *App) string {
	t.Helper()
	m := app.modalManager.GetActiveModal()
	if m == nil || app.modalManager.modals[diagnosticReportModal] != m {
		t.Fatalf("the report viewer is not on top")
	}
	v, ok := m.content.(*TextViewer)
	if !ok {
		t.Fatalf("report modal holds %T", m.content)
	}
	return ansi.Strip(v.inner.PlainText())
}

func TestDiagnosticHotkeyShowsReport(t *testing.T) {
	t.Parallel()
	argsFile := filepath.Join(t.TempDir(), "args")
	tool := fakeTool(t, `printf '%s\n' "$@" > '`+argsFile+`'; cat '`+krrFixturePath(t)+`'`)
	app := newHotkeyApp(t, diagnosticCommand(tool, "simple", "-f", "json", "--name={{.Name}}"))
	cmd := pressHotkeyApp(t, app, tea.Key{Code: '1', Mod: tea.ModAlt})
	if app.modalManager.IsModalVisible() {
		t.Fatalf("a diagnostic runs in the background, no dialog")
	}
	done := collectMsgs[diagnosticDoneMsg](cmd)
	if len(done) != 1 {
		t.Fatalf("got %d results, want 1", len(done))
	}
	if done[0].res.Outcome != diagnostic.Finished {
		t.Fatalf("outcome = %v (%v)", done[0].res.Outcome, done[0].res.Err)
	}
	app.Update(done[0])
	text := viewerText(t, app)
	for _, want := range []string{
		"Outcome: finished",
		"Source: krr (output of an external tool, not checked by kc)",
		"Observation window: the last 14 days",
		"100m -> 250m",
		"--- raw output of " + tool + " ---",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	// A global command without a selection renders its templates too.
	if want := "simple\n-f\njson\n--name=\n"; string(got) != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
	if len(app.diagnostics.runs) != 0 {
		t.Fatalf("the run must be forgotten once done")
	}
}

func TestDiagnosticPickAgainStopsTheRun(t *testing.T) {
	t.Parallel()
	tool := fakeTool(t, "exec sleep 60")
	app := newHotkeyApp(t, diagnosticCommand(tool))
	cmd := pressHotkeyApp(t, app, tea.Key{Code: '1', Mod: tea.ModAlt})
	results := make(chan []diagnosticDoneMsg, 1)
	go func() { results <- collectMsgs[diagnosticDoneMsg](cmd) }()

	pressHotkeyApp(t, app, tea.Key{Code: '1', Mod: tea.ModAlt})
	view := modalText(t, app, diagnosticStopModal)
	for _, want := range []string{"Stop KRR?", "Scope", "the whole context", "[ Stop ] [< Cancel >]"} {
		if !strings.Contains(view, want) {
			t.Errorf("stop dialog lacks %q:\n%s", want, view)
		}
	}
	// Cancel keeps it running.
	pressHotkeyApp(t, app, tea.Key{Code: tea.KeyEnter})
	select {
	case <-results:
		t.Fatal("Cancel must not stop the run")
	case <-time.After(100 * time.Millisecond):
	}
	pressHotkeyApp(t, app, tea.Key{Code: '1', Mod: tea.ModAlt})
	pressHotkeyApp(t, app, tea.Key{Code: tea.KeyTab})
	pressHotkeyApp(t, app, tea.Key{Code: tea.KeyEnter})
	var done []diagnosticDoneMsg
	select {
	case done = <-results:
	case <-time.After(20 * time.Second):
		t.Fatal("Stop did not end the run")
	}
	if len(done) != 1 || done[0].res.Outcome != diagnostic.Canceled {
		t.Fatalf("results = %+v, want one canceled", done)
	}
	_, after := app.Update(done[0])
	if app.modalManager.IsModalVisible() {
		t.Fatalf("a stopped run opens no report")
	}
	toasts := collectMsgs[showToastMsg](after)
	if len(toasts) != 1 || toasts[0].text != "KRR: stopped" {
		t.Fatalf("toasts = %+v", toasts)
	}
	if len(app.diagnostics.runs) != 0 {
		t.Fatalf("the stopped run must be forgotten")
	}
}

func TestDiagnosticMissingToolIsUnavailable(t *testing.T) {
	t.Parallel()
	app := newHotkeyApp(t, diagnosticCommand("kc-no-such-tool-198", "simple"))
	cmd := pressHotkeyApp(t, app, tea.Key{Code: '1', Mod: tea.ModAlt})
	if len(app.diagnostics.runs) != 0 {
		t.Fatalf("a missing tool must not start a run")
	}
	if got := collectMsgs[diagnosticDoneMsg](cmd); len(got) != 0 {
		t.Fatalf("a missing tool must not run, got %+v", got)
	}
	toasts := collectMsgs[showToastMsg](cmd)
	if len(toasts) != 1 || !strings.Contains(toasts[0].text, "kc-no-such-tool-198 is not installed or not in PATH; kc does not install tools") {
		t.Fatalf("toasts = %+v", toasts)
	}
}

func TestDiagnosticReadOnlyRefusesDangerous(t *testing.T) {
	t.Parallel()
	cmd := diagnosticCommand(fakeTool(t, "true"))
	cmd.Dangerous = true
	app := newHotkeyApp(t, cmd)
	app.cfg.ReadOnly.Enabled = true
	pressHotkeyApp(t, app, tea.Key{Code: '1', Mod: tea.ModAlt})
	if len(app.diagnostics.runs) != 0 {
		t.Fatalf("read-only mode must not run a dangerous diagnostic")
	}
}

// A result names the context and scope the run started with, also when
// the user switched since.
func TestDiagnosticLateResultKeepsItsScope(t *testing.T) {
	t.Parallel()
	app := newHotkeyApp(t)
	run := &diagnosticRun{
		id: 7, cmd: diagnosticCommand("krr"), argv: []string{"krr", "simple"},
		context: "prod-eu", ns: "shop",
		scopes:  []diagnostic.Scope{{Namespace: "shop", Kind: "Deployment", Name: "web"}},
		started: time.Now().Add(-time.Minute), cancel: func() {},
	}
	data, err := os.ReadFile(krrFixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	app.Update(diagnosticDoneMsg{run: run, res: diagnostic.Result{Outcome: diagnostic.Finished, Stdout: data}})
	text := viewerText(t, app)
	for _, want := range []string{"Context: prod-eu", "Scope: Deployment shop/web", "No rows.", "Scans: 0 shown, 3 outside the scope."} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
}

func TestDiagnosticReportOutcomesAreDistinct(t *testing.T) {
	t.Parallel()
	run := &diagnosticRun{cmd: diagnosticCommand("krr"), argv: []string{"krr"}, started: time.Now()}
	tests := []struct {
		name      string
		res       diagnostic.Result
		wantTitle string
		wantText  string
	}{
		{name: "finished", res: diagnostic.Result{Outcome: diagnostic.Finished, Stdout: []byte(`{"scans": [], "strategy": {"name": "simple"}}`)}, wantTitle: "KRR: finished", wantText: "Outcome: finished"},
		{name: "malformed", res: diagnostic.Result{Outcome: diagnostic.Finished, Stdout: []byte("| table |")}, wantTitle: "KRR: output not understood", wantText: "kc shows no results for it"},
		{name: "timed out", res: diagnostic.Result{Outcome: diagnostic.TimedOut, Stderr: "partial"}, wantTitle: "KRR: timed out", wantText: "timed out after 5m0s"},
		{name: "failed", res: diagnostic.Result{Outcome: diagnostic.Failed, Err: errString("exit status 2"), Stderr: "no prometheus\x1b[2J"}, wantTitle: "KRR: failed", wantText: "failed: exit status 2"},
	}
	titles := make(map[string]bool)
	for _, tc := range tests {
		title, text := diagnosticReportText(run, tc.res)
		if title != tc.wantTitle || !strings.Contains(text, tc.wantText) {
			t.Errorf("%s: title %q, text:\n%s", tc.name, title, text)
		}
		if strings.Contains(text, "\x1b") {
			t.Errorf("%s: the text keeps control characters", tc.name)
		}
		titles[title] = true
	}
	if len(titles) != len(tests) {
		t.Fatalf("outcomes share titles: %v", titles)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestRenderDiagnosticArgsKeepsNamesAsOneArgument(t *testing.T) {
	t.Parallel()
	row := targetRow(deploymentsGVR, "prod", "web; rm -rf / #")
	ec := execctx.Context{Context: "kind-dev", Namespace: "fallback"}
	got, err := RenderDiagnosticArgs([]string{"krr", "simple", "-n", "{{.Namespace}}", "--name={{.Name}}", "-c", "{{.Context}}"}, []models.Item{row}, deploymentsGVR, ec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"krr", "simple", "-n", "prod", "--name=web; rm -rf / #", "-c", "kind-dev"}
	if !slices.Equal(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
	got, err = RenderDiagnosticArgs([]string{"{{.Name}}", "-n", "{{.Namespace}}"}, nil, deploymentsGVR, ec)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"{{.Name}}", "-n", "fallback"}; !slices.Equal(got, want) {
		t.Fatalf("the executable is not a template and .Namespace falls back to the run's: %q", got)
	}
}

func TestDiagnosticScopes(t *testing.T) {
	t.Parallel()
	nsGVR := targetRow(podsGVR, "", "").GVR()
	nsGVR.Resource = "namespaces"
	tests := []struct {
		name  string
		items []models.Item
		want  []diagnostic.Scope
	}{
		{name: "nothing"},
		{name: "workload", items: []models.Item{targetRow(deploymentsGVR, "prod", "web")}, want: []diagnostic.Scope{{Namespace: "prod", Kind: "Deployment", Name: "web"}}},
		{name: "pod", items: []models.Item{targetRow(podsGVR, "prod", "web-1")}, want: []diagnostic.Scope{{Namespace: "prod"}}},
		{name: "namespace row", items: []models.Item{targetRow(nsGVR, "", "shop")}, want: []diagnostic.Scope{{Namespace: "shop"}}},
		{name: "namespace command item", items: []models.Item{namespaceCommandItem{namespace: "dev"}}, want: []diagnostic.Scope{{Namespace: "dev"}}},
	}
	for _, tc := range tests {
		if got := diagnosticScopes(tc.items); !slices.Equal(got, tc.want) {
			t.Errorf("%s: scopes = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}
