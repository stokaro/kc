package ui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/nodeaction"
)

// runApp runs cmd and feeds every message it produces back through
// app.Update until no command is left, as the Bubble Tea loop does. It
// fails after limit messages.
func runApp(t *testing.T, app *App, cmd tea.Cmd, limit int) []tea.Msg {
	t.Helper()
	var seen []tea.Msg
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := c()
		if batch, ok := msg.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		if msg == nil {
			continue
		}
		seen = append(seen, msg)
		if len(seen) > limit {
			t.Fatalf("more than %d messages: %T ...", limit, msg)
		}
		_, next := app.Update(msg)
		queue = append(queue, next)
	}
	return seen
}

// busyTestApp returns an App with a dialog open on top.
func busyTestApp(t *testing.T) *App {
	t.Helper()
	app, _ := readOnlyTestApp(t, true)
	modal := NewModal("Dialog", newNodeActionMenu(nodeTarget{nodes: []string{"a"}}))
	app.modalManager.Register("busy_test_dialog", modal)
	app.showModal("busy_test_dialog")
	if app.modalManager.TopName() != "busy_test_dialog" {
		t.Fatalf("dialog not open: top %q", app.modalManager.TopName())
	}
	return app
}

// The result of withBusy work reaches the App while a dialog is open, and
// the spinner it showed goes away. Before, the dialog swallowed the
// busyDoneMsg: the result was lost and the spinner kept turning.
func TestBusyResultPassesAnOpenDialog(t *testing.T) {
	for _, delay := range []time.Duration{0, 20 * time.Millisecond} {
		t.Run(delay.String(), func(t *testing.T) {
			app := busyTestApp(t)
			dir := t.TempDir()
			cmd := app.withBusy("Copy", delay, func() tea.Msg {
				// Long enough for the spinner to show before the result.
				time.Sleep(2 * delay)
				return copyFinishedMsg{path: filepath.Join(dir, "out.yaml"), subject: "pod/x"}
			})
			runApp(t, app, cmd, 100)
			if app.lastCopyDir != dir {
				t.Errorf("lastCopyDir = %q, want %q: the copy result did not reach the App", app.lastCopyDir, dir)
			}
			if app.busyActive {
				t.Error("the spinner is still on after the work finished")
			}
			if app.modalManager.TopName() != "busy_test_dialog" {
				t.Errorf("the dialog closed: top %q", app.modalManager.TopName())
			}
		})
	}
}

// A message the dialog does handle still goes to it: only the busy
// messages skip it.
func TestBusyBypassLeavesOtherMessagesToTheDialog(t *testing.T) {
	app := busyTestApp(t)
	menu := app.modalManager.modals["busy_test_dialog"].content.(*nodeActionMenuModel)
	app.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	if menu.selected != 1 {
		t.Fatalf("Down went past the dialog: selected %d", menu.selected)
	}
}

// The drain options stay open while kc reads the pods with withBusy; the
// result arrives and is shown in the form.
func TestDrainOptionsGetTheBusyResult(t *testing.T) {
	app, _ := readOnlyTestApp(t, true)
	app.showDrainOptions(nodeTarget{nodes: []string{"a"}, env: nodeaction.Env{}})
	form := app.modalManager.modals[nodeOptionsModal].content.(*drainOptionsModel)
	_, cmd := app.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	runApp(t, app, cmd, 100)
	if form.checking || !strings.Contains(form.err, "pod lister") {
		t.Fatalf("checking %v, err %q: the prepared drain did not reach the form", form.checking, form.err)
	}
	if app.modalManager.TopName() != nodeOptionsModal {
		t.Errorf("top dialog = %q, want the options", app.modalManager.TopName())
	}
}
