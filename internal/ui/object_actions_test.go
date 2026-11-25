package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/objectaction"
	"github.com/stokaro/kc/pkg/appconfig"
)

type fakeClusterAction struct{ runs int }

func (f *fakeClusterAction) Confirmation() models.Confirmation {
	return models.Confirmation{Title: "Run CronJob now?", Command: "kubectl create job --from=cronjob/x x-manual-abcde", Action: "Run"}
}

func (f *fakeClusterAction) Run(context.Context) (models.ActionResult, error) {
	f.runs++
	return models.ActionResult{Message: "Created job x-manual-abcde"}, nil
}

func TestCommandSelectorPutsObjectActionsFirst(t *testing.T) {
	commands := []appconfig.CommandConfig{{Name: "shell", Type: appconfig.CommandTypeGlobal}}
	var ranCommand string
	m := NewCommandSelectorModel(commands, "right panel", "ops", false, func(c appconfig.CommandConfig) tea.Cmd {
		ranCommand = c.Name
		return nil
	}, nil)
	var ranAction string
	actions := objectaction.Default().For(objectaction.CronJobsGVR, nil)
	m.AddObjectActions(actions, func(a objectaction.Action) tea.Cmd {
		ranAction = a.ID
		return nil
	})
	m.SetDimensions(40, 10)
	view := ansi.Strip(viewString(m.View()))
	lines := strings.Split(view, "\n")
	if len(lines) < 5 || strings.TrimSpace(lines[0]) != objectActionsTitle {
		t.Fatalf("object actions must come first:\n%s", view)
	}
	for _, want := range []string{"> Run now...", "Suspend/Resume...", "Global Commands", "shell"} {
		if !strings.Contains(view, want) {
			t.Errorf("menu lacks %q:\n%s", want, view)
		}
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if ranAction != "cronjob.trigger" || ranCommand != "" {
		t.Fatalf("Enter ran action %q and command %q, want the first action only", ranAction, ranCommand)
	}
	// Down past the second action and the header reaches the user command.
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if ranCommand != "shell" {
		t.Fatalf("the user command did not run after the actions, ran %q", ranCommand)
	}
}

func TestConfirmModelShowsKubectlCommand(t *testing.T) {
	m := NewConfirmModel((&fakeClusterAction{}).Confirmation())
	m.SetDimensions(70, 10)
	view := ansi.Strip(viewString(m.View()))
	for _, want := range []string{"Same as:", "kubectl create job --from=cronjob/x x-manual-abcde", "[ Run ]"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
}

func TestClusterActionRunsOffTheUpdateLoop(t *testing.T) {
	action := &fakeClusterAction{}
	app := &App{ctx: t.Context(), width: 100, height: 30, modalManager: NewModalManager(nil),
		leftPanel: NewPanel("l"), rightPanel: NewPanel("r"), leftPanelWidthPercent: 50, rightPanelWidthPercent: 50}

	app.showClusterActionConfirm(action, 0, "")
	if cmd := app.handleConfirmResult(confirmResultMsg{ok: false}); cmd != nil || action.runs != 0 {
		t.Fatalf("cancel must not run the action")
	}

	app.showClusterActionConfirm(action, 0, "")
	cmd := app.handleConfirmResult(confirmResultMsg{ok: true})
	if action.runs != 0 {
		t.Fatalf("the action ran on the update goroutine")
	}
	if cmd == nil {
		t.Fatalf("no command to run the action")
	}
	done, ok := runUntil[objectActionDoneMsg](cmd)
	if !ok {
		t.Fatalf("the command did not report the action's outcome")
	}
	if action.runs != 1 || done.err != nil || done.result.Message != "Created job x-manual-abcde" {
		t.Fatalf("runs = %d, done = %+v", action.runs, done)
	}
}

// runUntil runs cmd and the batches it returns, skipping timers, until a
// message of type T comes out of one.
func runUntil[T any](cmd tea.Cmd) (T, bool) {
	var zero T
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case T:
			return msg, true
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case busyDoneMsg:
			if m, ok := msg.msg.(T); ok {
				return m, true
			}
		}
	}
	return zero, false
}
