package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/models"
)

// execRecorder replaces tea.Exec and records the sessions kc hands the
// terminal to.
type execRecorder struct{ sessions []*execSession }

func (r *execRecorder) exec(c tea.ExecCommand, _ tea.ExecCallback) tea.Cmd {
	if s, ok := c.(*execSession); ok {
		r.sessions = append(r.sessions, s)
	}
	return nil
}

// attachTestApp is an app on a container row whose M-t resolved to an
// attach; it returns the app after the resolved message arrived.
func attachTestApp(t *testing.T, keys ...tea.KeyPressMsg) (*App, *execRecorder) {
	t.Helper()
	app := execTestApp(t, containerRow())
	rec := &execRecorder{}
	app.execCommand = rec.exec
	if len(keys) == 0 {
		keys = []tea.KeyPressMsg{{Code: 't', Mod: tea.ModAlt}}
	}
	var cmd tea.Cmd
	for _, k := range keys {
		_, cmd = app.Update(k)
	}
	resolved := collectMsgs[execResolvedMsg](cmd)
	if len(resolved) != 1 || resolved[0].mode != models.ExecAttach {
		t.Fatalf("resolved %+v, want one attach", resolved)
	}
	app.Update(resolved[0])
	return app, rec
}

// M-t and Esc t ask before kubectl attach runs: the dialog names the
// container and the context and says where the keys go (#375).
func TestAttachAsksFirst(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []tea.KeyPressMsg
	}{
		{"M-t", []tea.KeyPressMsg{{Code: 't', Mod: tea.ModAlt}}},
		{"Esc t", []tea.KeyPressMsg{{Code: tea.KeyEscape}, {Code: 't', Text: "t"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, rec := attachTestApp(t, tc.keys...)
			if len(rec.sessions) != 0 {
				t.Fatal("kubectl attach ran before the user answered")
			}
			if got := app.modalManager.TopName(); got != attachModal {
				t.Fatalf("open dialog %q, want the attach question", got)
			}
			view := viewString(app.View())
			for _, want := range []string{
				"Attach to the main process of web-0/app?",
				"Context:    dev", "Namespace:  shop", "Pod:        web-0", "Container:  app",
				"Keys go to the container's main process",
				"and with it the container.",
			} {
				if !strings.Contains(view, want) {
					t.Errorf("the attach question does not show %q:\n%s", want, view)
				}
			}
		})
	}
}

// No, by every key that means it, keeps kubectl attach from running.
func TestAttachQuestionNoDoesNotAttach(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"Enter on the focused No", tea.KeyPressMsg{Code: tea.KeyEnter}},
		{"M-n", tea.KeyPressMsg{Code: 'n', Mod: tea.ModAlt}},
		{"Esc", tea.KeyPressMsg{Code: tea.KeyEscape}},
		{"C-c", tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, rec := attachTestApp(t)
			// A letter alone answers nothing (ADR 0373).
			app.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
			if len(rec.sessions) != 0 || app.modalManager.TopName() != attachModal {
				t.Fatal("y alone answered the attach question")
			}
			app.Update(tc.key)
			if len(rec.sessions) != 0 {
				t.Fatalf("%s ran kubectl attach", tc.name)
			}
			if app.modalManager.IsModalVisible() {
				t.Fatalf("%s left %q open", tc.name, app.modalManager.TopName())
			}
		})
	}
}

// Yes runs kubectl attach for the container the question named.
func TestAttachQuestionYesAttaches(t *testing.T) {
	app, rec := attachTestApp(t)
	app.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModAlt})
	if len(rec.sessions) != 1 {
		t.Fatalf("M-y ran %d sessions, want 1", len(rec.sessions))
	}
	args := strings.Join(rec.sessions[0].args, " ")
	if want := "--context dev attach -it web-0 -n shop -c app"; args != want {
		t.Fatalf("kubectl %s, want kubectl %s", args, want)
	}
	if app.modalManager.IsModalVisible() {
		t.Fatalf("%q still open after Yes", app.modalManager.TopName())
	}
}

// A context that became read-only while the question was open refuses
// the attach on Yes.
func TestAttachQuestionRechecksReadOnly(t *testing.T) {
	app, rec := attachTestApp(t)
	app.readOnlyOverride = ReadOnlyForced
	app.toastLogger = nil
	_, cmd := app.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModAlt})
	if len(rec.sessions) != 0 {
		t.Fatal("kubectl attach ran in read-only mode")
	}
	toasts := collectMsgs[showToastMsg](cmd)
	if len(toasts) != 1 || !strings.Contains(toasts[0].text, "read-only mode") {
		t.Fatalf("toasts %+v, want the read-only refusal", toasts)
	}
}

// A shell (M-x) does not ask: it starts a new process and ends nothing.
func TestShellDoesNotAsk(t *testing.T) {
	app := execTestApp(t, containerRow())
	rec := &execRecorder{}
	app.execCommand = rec.exec
	_, cmd := app.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt})
	resolved := collectMsgs[execResolvedMsg](cmd)
	if len(resolved) != 1 {
		t.Fatalf("resolved %+v", resolved)
	}
	app.Update(resolved[0])
	if len(rec.sessions) != 1 || !strings.Contains(strings.Join(rec.sessions[0].args, " "), " exec ") {
		t.Fatalf("M-x ran %d sessions, want one shell at once", len(rec.sessions))
	}
	if app.modalManager.IsModalVisible() {
		t.Fatalf("M-x opened %q", app.modalManager.TopName())
	}
}

// The notes follow what the container offers.
func TestAttachNotes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		stdin, tty  bool
		want, never string
	}{
		{"no stdin", false, false, "only shows the output", "C-d"},
		{"stdin, no TTY", true, false, "without a terminal", "no stdin"},
		{"stdin and TTY", true, true, "not to a new shell", "no stdin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			notes := strings.Join(attachNotes(models.ExecTarget{Stdin: tc.stdin, TTY: tc.tty}), " ")
			if !strings.Contains(notes, tc.want) || strings.Contains(notes, tc.never) {
				t.Fatalf("notes %q: want %q, never %q", notes, tc.want, tc.never)
			}
		})
	}
}
