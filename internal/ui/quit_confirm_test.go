package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	corev1 "k8s.io/api/core/v1"

	"github.com/stokaro/kc/internal/portforward"
	"github.com/stokaro/kc/pkg/appconfig"
)

// quitTestApp is an app at the panels with forwards port forwards running.
func quitTestApp(t *testing.T, forwards int) *App {
	t.Helper()
	app := execTestApp(t)
	if forwards == 0 {
		return app
	}
	ctx := t.Context()
	reg := portforward.NewRegistry(ctx)
	t.Cleanup(func() { reg.StopAll(time.Second) })
	runner := portforward.RunnerFunc(func(ctx context.Context, spec portforward.Spec, ready func(int32)) error {
		ready(spec.LocalPort)
		<-ctx.Done()
		return nil
	})
	for i := range forwards {
		spec := portforward.Spec{Context: "dev", Namespace: "shop", Kind: "pod", Name: "web-0", Pod: "web-0",
			Address: "127.0.0.1", LocalPort: int32(8000 + i), RemotePort: 80}
		if _, err := reg.Start(ctx, spec, runner); err != nil {
			t.Fatalf("Start: %v", err)
		}
	}
	app.portForwards = reg
	return app
}

func quits(cmd tea.Cmd) bool { return len(collectMsgs[tea.QuitMsg](cmd)) > 0 }

// quitPaths are the ways to quit kc from the panels.
var quitPaths = []struct {
	name string
	run  func(*App) tea.Cmd
}{
	{"F10", func(a *App) tea.Cmd {
		_, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyF10})
		return cmd
	}},
	{"Esc 0", func(a *App) tea.Cmd {
		a.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		_, cmd := a.Update(tea.KeyPressMsg{Code: '0', Text: "0"})
		return cmd
	}},
	{"File > Exit", func(a *App) tea.Cmd {
		for _, it := range a.fileMenuItems() {
			if it.Label == "E&xit" {
				return it.Run()
			}
		}
		panic("File menu has no Exit")
	}},
	{"click on Quit", func(a *App) tea.Cmd { return a.handleFunctionKeyClick(functionKeyX(a, functionKeyQuit)) }},
}

// With nothing running, every quit path quits at once: one key, as
// before #375.
func TestQuitWithNothingRunningQuitsAtOnce(t *testing.T) {
	for _, path := range quitPaths {
		t.Run(path.name, func(t *testing.T) {
			app := quitTestApp(t, 0)
			if cmd := path.run(app); !quits(cmd) {
				t.Fatalf("%s with nothing running did not quit", path.name)
			}
			if app.modalManager.IsModalVisible() {
				t.Fatalf("%s with nothing running opened %q", path.name, app.modalManager.TopName())
			}
		})
	}
}

// While port forwards run, every quit path asks first and names what
// stops; kc quits only on Yes (#375).
func TestQuitAsksWhileForwardsRun(t *testing.T) {
	for _, path := range quitPaths {
		t.Run(path.name, func(t *testing.T) {
			app := quitTestApp(t, 2)
			if cmd := path.run(app); quits(cmd) {
				t.Fatalf("%s quit with 2 port forwards running", path.name)
			}
			if got := app.modalManager.TopName(); got != quitModal {
				t.Fatalf("%s opened %q, want the quit question", path.name, got)
			}
			view := viewString(app.View())
			for _, want := range []string{"2 port forwards will stop. Quit?", "127.0.0.1:8000 -> pod/web-0:80 in shop (dev)", "127.0.0.1:8001"} {
				if !strings.Contains(view, want) {
					t.Errorf("the quit question does not show %q:\n%s", want, view)
				}
			}
			_, cmd := app.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModAlt})
			if !quits(cmd) {
				t.Fatal("M-y in the quit question did not quit")
			}
		})
	}
}

// The quit question keeps the red dialog rule (ADR 0373): No has the
// focus, a letter alone does nothing, and Enter, M-n, Esc, C-c, C-g and F10
// keep kc running.
func TestQuitQuestionAnswersNo(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []tea.KeyPressMsg
	}{
		{"Enter on the focused No", []tea.KeyPressMsg{{Code: tea.KeyEnter}}},
		{"M-n", []tea.KeyPressMsg{{Code: 'n', Mod: tea.ModAlt}}},
		{"Esc", []tea.KeyPressMsg{{Code: tea.KeyEscape}}},
		{"C-c", []tea.KeyPressMsg{{Code: 'c', Mod: tea.ModCtrl}}},
		{"C-g", []tea.KeyPressMsg{{Code: 'g', Mod: tea.ModCtrl}}},
		{"F10 again", []tea.KeyPressMsg{{Code: tea.KeyF10}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := quitTestApp(t, 1)
			app.Update(tea.KeyPressMsg{Code: tea.KeyF10})
			// A letter alone answers nothing.
			if _, cmd := app.Update(tea.KeyPressMsg{Code: 'y', Text: "y"}); quits(cmd) || app.modalManager.TopName() != quitModal {
				t.Fatal("y alone answered the quit question")
			}
			var cmd tea.Cmd
			for _, k := range tc.keys {
				_, cmd = app.Update(k)
			}
			if quits(cmd) {
				t.Fatalf("%s quit kc", tc.name)
			}
			if app.modalManager.IsModalVisible() {
				t.Fatalf("%s left %q open", tc.name, app.modalManager.TopName())
			}
			if app.portForwards.Len() != 1 {
				t.Fatal("the forward stopped although kc kept running")
			}
		})
	}
}

// confirm.quit: always asks with nothing running too.
func TestQuitAlwaysAsks(t *testing.T) {
	app := quitTestApp(t, 0)
	app.cfg.Confirm.Quit = appconfig.QuitConfirmAlways
	_, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyF10})
	if quits(cmd) || app.modalManager.TopName() != quitModal {
		t.Fatal("F10 with confirm.quit: always did not ask")
	}
	if view := viewString(app.View()); !strings.Contains(view, "Quit kc?") {
		t.Fatalf("the question does not say Quit kc?:\n%s", view)
	}
	if _, cmd := app.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModAlt}); !quits(cmd) {
		t.Fatal("M-y did not quit")
	}
}

// The question names each kind of session that stops.
func TestQuitQuestionNamesWhatStops(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		s    liveSessions
		want string
	}{
		{"one forward", liveSessions{forwards: 1}, "1 port forward will stop. Quit?"},
		{"forwards and a node shell", liveSessions{forwards: 3, nodeShell: 1}, "3 port forwards and 1 node shell will stop. Quit?"},
		{"all kinds", liveSessions{forwards: 2, nodeShell: 1, copies: 1}, "2 port forwards, 1 node shell and 1 copy will stop. Quit?"},
		{"nothing", liveSessions{}, "Quit kc?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.s.quitQuestion(); got != tc.want {
				t.Fatalf("quitQuestion = %q, want %q", got, tc.want)
			}
		})
	}
}

// A node shell and a running copy count as sessions; the node shell's
// note says what happens to its pod.
func TestLiveSessionsCountNodeShellAndCopy(t *testing.T) {
	app := quitTestApp(t, 0)
	app.nodeShell = &nodeShellSession{pod: &corev1.Pod{
		Namespace: "kube-system", Name: "kc-node-shell-abc",
		Spec: corev1.PodSpec{NodeName: "worker-1"},
	}}
	app.transfer = &transferJob{running: true}
	s := app.liveSessions()
	if s.nodeShell != 1 || s.copies != 1 || s.empty() {
		t.Fatalf("sessions = %+v, want a node shell and a copy", s)
	}
	if !strings.Contains(strings.Join(s.lines, "\n"), "node shell on worker-1 (pod kube-system/kc-node-shell-abc)") {
		t.Fatalf("lines = %q", s.lines)
	}
	if len(s.notes) != 1 || !strings.Contains(s.notes[0], "deletes the pod") {
		t.Fatalf("notes = %q, want the pod note", s.notes)
	}
	app.nodeShell = nil
	app.nodeShellStarting = true
	if s := app.liveSessions(); s.nodeShell != 1 || !strings.Contains(s.lines[0], "starting") {
		t.Fatalf("a starting node shell: %+v", s)
	}
}
