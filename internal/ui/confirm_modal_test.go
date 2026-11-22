package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/models"
)

type fakeAction struct {
	*models.SimpleItem
	confirmed int
}

func (f *fakeAction) Confirmation() models.Confirmation {
	return models.Confirmation{Title: "Start?", Details: [][2]string{{"Context", "mba"}, {"Pod", "p"}}, Action: "Start"}
}

func (f *fakeAction) Confirm() error { f.confirmed++; return nil }

func answerOf(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		t.Fatalf("no answer")
	}
	return cmd().(confirmResultMsg).ok
}

func TestConfirmModelDefaultsToCancel(t *testing.T) {
	m := NewConfirmModel((&fakeAction{}).Confirmation())
	m.SetDimensions(40, 8)
	view := ansi.Strip(viewString(m.View()))
	for _, want := range []string{"Start?", "Context:  mba", "Pod:      p", "[ Start ]", "[ Cancel ]"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if answerOf(t, cmd) {
		t.Fatalf("Enter on the default button must cancel")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !answerOf(t, cmd) {
		t.Fatalf("Tab then Enter must confirm")
	}
	if _, cmd := m.HandleModalEscape(tea.KeyPressMsg{Code: tea.KeyEscape}); answerOf(t, cmd) {
		t.Fatalf("Esc must cancel")
	}
}

func TestConfirmRunsActionOnlyWhenApproved(t *testing.T) {
	action := &fakeAction{SimpleItem: models.NewSimpleItem("a", []string{"a"}, nil, models.WhiteStyle())}
	app := &App{ctx: t.Context(), width: 100, height: 30, modalManager: NewModalManager(nil),
		leftPanel: NewPanel("l"), rightPanel: NewPanel("r"), leftPanelWidthPercent: 50, rightPanelWidthPercent: 50}
	if !passesModal(confirmResultMsg{}) {
		t.Fatalf("the dialog's answer must reach the app")
	}
	app.showConfirm(action)
	if !app.modalManager.IsModalVisible() {
		t.Fatalf("no confirmation dialog")
	}
	app.handleConfirmResult(confirmResultMsg{ok: false})
	if action.confirmed != 0 || app.modalManager.IsModalVisible() {
		t.Fatalf("cancel must close without running the action")
	}
	app.showConfirm(action)
	app.handleConfirmResult(confirmResultMsg{ok: true})
	if action.confirmed != 1 {
		t.Fatalf("confirmed %d times, want 1", action.confirmed)
	}
}
