package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sttts/kc/internal/models"
	"github.com/sttts/kc/internal/workloadaction"
)

// fakeWorkloadChange counts its runs. With ask set it asks for a number,
// with choose set it offers revisions.
type fakeWorkloadChange struct {
	n      int
	runs   *[]int
	ask    bool
	choose bool
}

func (f *fakeWorkloadChange) Confirmation() models.Confirmation {
	return models.Confirmation{Title: fmt.Sprintf("Set to %d?", f.n), Action: "Set"}
}

func (f *fakeWorkloadChange) Run(context.Context) (workloadaction.Result, error) {
	*f.runs = append(*f.runs, f.n)
	return workloadaction.Result{Message: fmt.Sprintf("Set to %d", f.n)}, nil
}

type fakeAsker struct{ fakeWorkloadChange }

func (f *fakeAsker) Question() workloadaction.Question {
	return workloadaction.Question{Title: "Scale", Label: "Replicas:", Initial: strconv.Itoa(f.n)}
}

func (f *fakeAsker) Answer(v string) (workloadaction.Change, error) {
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil, fmt.Errorf("not a number")
	}
	return &fakeWorkloadChange{n: n, runs: f.runs}, nil
}

type fakeChooser struct{ fakeWorkloadChange }

func (f *fakeChooser) Choices() (string, []workloadaction.Choice, string) {
	return "Revision", []workloadaction.Choice{{Label: "3", Value: "3", Note: "current"}, {Label: "2", Value: "2"}, {Label: "1", Value: "1"}}, "2"
}

func (f *fakeChooser) Choose(v string) (workloadaction.Change, error) {
	n, _ := strconv.Atoi(v)
	return &fakeWorkloadChange{n: n, runs: f.runs}, nil
}

func newWorkloadTestApp(t *testing.T) *App {
	return &App{ctx: t.Context(), width: 100, height: 30, modalManager: NewModalManager(nil),
		leftPanel: NewPanel("l"), rightPanel: NewPanel("r"), leftPanelWidthPercent: 50, rightPanelWidthPercent: 50}
}

// collect runs cmd and the commands it batches, and returns the messages.
func collect(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	switch m := msg.(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range m {
			out = append(out, collect(c)...)
		}
		return out
	case busyDoneMsg:
		return []tea.Msg{m.msg}
	case nil:
		return nil
	}
	return []tea.Msg{msg}
}

// pressDialog sends keys to the open dialog and returns its messages.
func pressDialog(t *testing.T, app *App, keys ...tea.KeyPressMsg) []tea.Msg {
	t.Helper()
	modal := app.modalManager.GetActiveModal()
	if modal == nil {
		t.Fatal("no dialog open")
	}
	var out []tea.Msg
	for _, k := range keys {
		_, cmd := modal.Update(k)
		out = append(out, collect(cmd)...)
	}
	return out
}

// deliver hands the flow's messages to the app until none are left and
// returns the final ones that are not part of the flow.
func deliver(t *testing.T, app *App, msgs []tea.Msg) []tea.Msg {
	t.Helper()
	var rest []tea.Msg
	for len(msgs) > 0 {
		msg := msgs[0]
		msgs = msgs[1:]
		wm, ok := msg.(workloadMessage)
		if !ok {
			rest = append(rest, msg)
			continue
		}
		if !passesModal(msg) {
			t.Fatalf("%T does not pass an open dialog", msg)
		}
		msgs = append(msgs, collect(app.handleWorkloadMsg(wm))...)
	}
	return rest
}

func dialogText(app *App) string {
	modal := app.modalManager.GetActiveModal()
	if modal == nil || !app.modalManager.IsModalVisible() {
		return ""
	}
	return ansi.Strip(viewString(modal.View()))
}

var (
	keyEnter = tea.KeyPressMsg{Code: tea.KeyEnter}
	keyTab   = tea.KeyPressMsg{Code: tea.KeyTab}
	keyBack  = tea.KeyPressMsg{Code: tea.KeyBackspace}
	// keyEsc is shared with menu_bar_test.go.
)

func keyText(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: rune(s[0]), Text: s} }

func TestWorkloadScaleAsksThenConfirmsThenRuns(t *testing.T) {
	app := newWorkloadTestApp(t)
	var runs []int
	deliver(t, app, []tea.Msg{workloadStepMsg{change: &fakeAsker{fakeWorkloadChange{n: 3, runs: &runs}}, ask: true}})
	if got := dialogText(app); !strings.Contains(got, "Replicas:") || !strings.Contains(got, "3") {
		t.Fatalf("value dialog not open with the current count:\n%s", got)
	}

	deliver(t, app, pressDialog(t, app, keyBack, keyText("5"), keyEnter))
	if got := dialogText(app); !strings.Contains(got, "Set to 5?") {
		t.Fatalf("red dialog does not confirm the typed count:\n%s", got)
	}
	if len(runs) != 0 {
		t.Fatalf("ran before the confirmation: %v", runs)
	}

	// Enter on the default button cancels.
	deliver(t, app, pressDialog(t, app, keyEnter))
	if len(runs) != 0 || app.modalManager.IsModalVisible() {
		t.Fatalf("cancel ran the change (%v) or left a dialog open", runs)
	}
}

func TestWorkloadConfirmRunsOnce(t *testing.T) {
	app := newWorkloadTestApp(t)
	var runs []int
	deliver(t, app, []tea.Msg{workloadStepMsg{change: &fakeWorkloadChange{n: 7, runs: &runs}, ask: true}})
	if got := dialogText(app); !strings.Contains(got, "Set to 7?") {
		t.Fatalf("a change without a question must go straight to the red dialog:\n%s", got)
	}
	rest := deliver(t, app, pressDialog(t, app, keyTab, keyEnter))
	if len(runs) != 1 || runs[0] != 7 {
		t.Fatalf("runs = %v, want one run of 7", runs)
	}
	var toast string
	for _, m := range rest {
		if s, ok := m.(showToastMsg); ok {
			toast = s.text
		}
	}
	if toast != "Set to 7" {
		t.Errorf("result message = %q, want the change's message", toast)
	}
	if app.modalManager.IsModalVisible() {
		t.Error("dialog still open after the run")
	}
}

// Read-only mode refuses an approved change before it is sent, also when
// the context became read-only while the dialog was open.
func TestWorkloadChangeRefusedInReadOnlyMode(t *testing.T) {
	app := newWorkloadTestApp(t)
	var runs []int
	deliver(t, app, []tea.Msg{workloadStepMsg{change: &fakeWorkloadChange{n: 7, runs: &runs}, ask: true}})
	app.readOnlyOverride = ReadOnlyForced
	rest := deliver(t, app, pressDialog(t, app, keyTab, keyEnter))
	if len(runs) != 0 {
		t.Fatalf("read-only mode ran the change: %v", runs)
	}
	var toast string
	for _, m := range rest {
		if s, ok := m.(showToastMsg); ok {
			toast = s.text
		}
	}
	if want := "Read-only mode: Set to 7 is disabled"; toast != want {
		t.Fatalf("toast = %q, want %q", toast, want)
	}
}

func TestWorkloadEscCancelsEveryStep(t *testing.T) {
	var runs []int
	for name, change := range map[string]workloadaction.Change{
		"input":   &fakeAsker{fakeWorkloadChange{n: 3, runs: &runs}},
		"picker":  &fakeChooser{fakeWorkloadChange{n: 3, runs: &runs}},
		"confirm": &fakeWorkloadChange{n: 3, runs: &runs},
	} {
		t.Run(name, func(t *testing.T) {
			app := newWorkloadTestApp(t)
			deliver(t, app, []tea.Msg{workloadStepMsg{change: change, ask: true}})
			if !app.modalManager.IsModalVisible() {
				t.Fatal("no dialog open")
			}
			deliver(t, app, pressDialog(t, app, keyEsc))
			if app.modalManager.IsModalVisible() {
				t.Fatalf("one Esc left the dialog open:\n%s", dialogText(app))
			}
		})
	}
	if len(runs) != 0 {
		t.Fatalf("Esc ran a change: %v", runs)
	}
}

func TestWorkloadRollbackPicksThenConfirms(t *testing.T) {
	app := newWorkloadTestApp(t)
	var runs []int
	deliver(t, app, []tea.Msg{workloadStepMsg{change: &fakeChooser{fakeWorkloadChange{n: 3, runs: &runs}}, ask: true}})
	if got := dialogText(app); !strings.Contains(got, "current") {
		t.Fatalf("revision list not open:\n%s", got)
	}
	// The preselected entry is revision 2; Down moves to 1.
	deliver(t, app, pressDialog(t, app, tea.KeyPressMsg{Code: tea.KeyDown}, keyEnter))
	if got := dialogText(app); !strings.Contains(got, "Set to 1?") {
		t.Fatalf("red dialog does not name the chosen revision:\n%s", got)
	}
	deliver(t, app, pressDialog(t, app, keyTab, keyEnter))
	if len(runs) != 1 || runs[0] != 1 {
		t.Fatalf("runs = %v, want one run of 1", runs)
	}
}

func TestWorkloadPrepareErrorShowsToast(t *testing.T) {
	app := newWorkloadTestApp(t)
	rest := deliver(t, app, []tea.Msg{workloadStepMsg{err: fmt.Errorf("can't restart paused deployment web"), ask: true}})
	if len(rest) != 1 || !strings.Contains(rest[0].(showToastMsg).text, "paused") {
		t.Fatalf("messages = %#v, want one failure toast", rest)
	}
	if app.modalManager.IsModalVisible() {
		t.Fatal("a failed prepare opened a dialog")
	}
}
