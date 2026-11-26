package ui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/access"
	"github.com/stokaro/kc/internal/workloadaction"
)

// Workload actions (scale, restart, rollback) run in steps: prepare (read
// the object), optionally ask for a value or a choice, confirm in the red
// dialog, run. Every step that calls the API server runs in a tea.Cmd.
//
// Follow-up after #80: drop this file and run the actions through the
// object-action registry, its confirm path (showClusterActionConfirm) and
// its input step.

// workloadMessage marks the messages of this flow. They pass an open
// dialog, since the dialogs of the flow send them.
type workloadMessage interface{ workloadMessage() }

// workloadStepMsg carries a prepared change. ask is true right after
// Prepare, when the change may still want a value or a choice. Every step
// carries kubeContext, the context of the row the action came from, for
// the read-only check when the change is approved.
type workloadStepMsg struct {
	change      workloadaction.Change
	err         error
	ask         bool
	kubeContext string
}

// workloadInputMsg is the answer of the value dialog.
type workloadInputMsg struct {
	res         TextInputModalResult
	asker       workloadaction.Asker
	kubeContext string
}

// workloadConfirmMsg is the answer of the red dialog.
type workloadConfirmMsg struct {
	ok          bool
	change      workloadaction.Change
	kubeContext string
}

// workloadDoneMsg reports a finished change.
type workloadDoneMsg struct {
	res workloadaction.Result
	err error
}

func (workloadStepMsg) workloadMessage()    {}
func (workloadInputMsg) workloadMessage()   {}
func (workloadConfirmMsg) workloadMessage() {}
func (workloadDoneMsg) workloadMessage()    {}

const (
	workloadInputModal   = "workload_input"
	workloadPickModal    = "workload_pick"
	workloadConfirmModal = "workload_confirm"
)

// startWorkloadAction prepares the action of an entered row.
func (a *App) startWorkloadAction(req workloadaction.Request) tea.Cmd {
	kubeContext := req.Env.Context
	return a.withBusy(req.Action.Label, 300*time.Millisecond, func() tea.Msg {
		ctx, cancel := context.WithTimeout(a.ctx, requestTimeout)
		defer cancel()
		change, err := req.Prepare(ctx)
		return workloadStepMsg{change: change, err: err, ask: true, kubeContext: kubeContext}
	})
}

func (a *App) handleWorkloadMsg(msg workloadMessage) tea.Cmd {
	switch msg := msg.(type) {
	case workloadStepMsg:
		if msg.err != nil {
			return a.ShowToast("Failed: "+msg.err.Error(), 5*time.Second)
		}
		if msg.ask {
			if asker, ok := msg.change.(workloadaction.Asker); ok {
				return a.showWorkloadInput(asker, msg.kubeContext)
			}
			if chooser, ok := msg.change.(workloadaction.Chooser); ok {
				return a.showWorkloadPicker(chooser, msg.kubeContext)
			}
		}
		return a.showWorkloadConfirm(msg.change, msg.kubeContext)
	case workloadInputMsg:
		if msg.res.Close {
			a.hideModalName(workloadInputModal)
		}
		if !msg.res.Confirm {
			return nil
		}
		change, err := msg.asker.Answer(msg.res.Value)
		kubeContext := msg.kubeContext
		return func() tea.Msg { return workloadStepMsg{change: change, err: err, kubeContext: kubeContext} }
	case workloadConfirmMsg:
		a.hideModalName(workloadConfirmModal)
		if !msg.ok || msg.change == nil {
			return nil
		}
		return a.runWorkloadChange(msg.change, msg.kubeContext)
	case workloadDoneMsg:
		if msg.err != nil {
			return a.ShowToast("Failed: "+msg.err.Error(), 5*time.Second)
		}
		return a.ShowInfo(msg.res.Message, 3*time.Second)
	}
	return nil
}

// workloadInputModel is the value dialog; one Esc cancels it.
type workloadInputModel struct{ *TextInputModal }

func (m *workloadInputModel) HandleModalEscape(tea.KeyMsg) (bool, tea.Cmd) {
	return true, m.emitResult(false, true)
}

func (a *App) showWorkloadInput(asker workloadaction.Asker, kubeContext string) tea.Cmd {
	q := asker.Question()
	model := &workloadInputModel{NewTextInputModal(TextInputModalConfig{
		Title:        q.Title,
		Label:        q.Label,
		Description:  q.Description,
		InitialValue: q.Initial,
		MinWidth:     36,
		HideTitle:    true, // the frame already shows it
		Validate: func(v string) error {
			_, err := asker.Answer(v)
			return err
		},
		ResultMsg: func(res TextInputModalResult) tea.Msg {
			return workloadInputMsg{res: res, asker: asker, kubeContext: kubeContext}
		},
	})}
	modal := NewModal(q.Title, model)
	a.modalManager.Register(workloadInputModal, modal)
	a.configureModalWindow(modal, model, a.activePanel, "", 40, 7)
	a.modalManager.Show(workloadInputModal)
	a.invalidateView("workload input")
	return nil
}

func (a *App) showWorkloadPicker(chooser workloadaction.Chooser, kubeContext string) tea.Cmd {
	title, list, selected := chooser.Choices()
	choices := make([]scopeChoice, 0, len(list))
	width := len([]rune(title)) + 4
	for _, c := range list {
		choices = append(choices, scopeChoice{Label: c.Label, Value: c.Value, Note: c.Note})
		width = max(width, len([]rune(c.Label))+len([]rune(c.Note))+6)
	}
	picker := NewScopePickerModel(choices, selected, func(c scopeChoice) tea.Cmd {
		a.hideModalName(workloadPickModal)
		change, err := chooser.Choose(c.Value)
		return func() tea.Msg { return workloadStepMsg{change: change, err: err, kubeContext: kubeContext} }
	}, func() tea.Cmd {
		a.hideModalName(workloadPickModal)
		return nil
	})
	modal := NewModal(title, picker)
	a.modalManager.Register(workloadPickModal, modal)
	a.configureModalWindow(modal, picker, a.activePanel, "", min(width, 80), min(len(choices)+3, 20))
	a.modalManager.Show(workloadPickModal)
	a.invalidateView("workload picker")
	return nil
}

// workloadConfirmModel is the red dialog for a workload change. It answers
// with workloadConfirmMsg, so the row confirmations of the app keep their
// own path.
type workloadConfirmModel struct {
	*ConfirmModel
	change      workloadaction.Change
	kubeContext string
}

func (m *workloadConfirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := m.ConfirmModel.Update(msg)
	return m, m.translate(cmd)
}

func (m *workloadConfirmModel) HandleModalEscape(k tea.KeyMsg) (bool, tea.Cmd) {
	handled, cmd := m.ConfirmModel.HandleModalEscape(k)
	return handled, m.translate(cmd)
}

func (m *workloadConfirmModel) translate(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := cmd()
		if r, ok := msg.(confirmResultMsg); ok {
			return workloadConfirmMsg{ok: r.ok, change: m.change, kubeContext: m.kubeContext}
		}
		return msg
	}
}

func (a *App) showWorkloadConfirm(change workloadaction.Change, kubeContext string) tea.Cmd {
	model := &workloadConfirmModel{ConfirmModel: NewConfirmModel(change.Confirmation()), change: change, kubeContext: kubeContext}
	modal := NewModal("Confirm", model)
	modal.SetDanger(true)
	a.modalManager.Register(workloadConfirmModal, modal)
	w, h := model.size()
	a.configureModalWindow(modal, model, a.activePanel, "", min(w+2, max(30, a.width-6)), h+2)
	a.modalManager.Show(workloadConfirmModal)
	a.invalidateView("workload confirm")
	return nil
}

// runWorkloadChange runs a confirmed change in the background.
func (a *App) runWorkloadChange(change workloadaction.Change, kubeContext string) tea.Cmd {
	// Read-only mode refuses the write here, before anything is sent. It is
	// checked on approval, so a context that became read-only while the
	// dialog was open is refused too. Every workload change is a patch
	// (scale patches the scale subresource).
	if cmd := a.refuseReadOnly(kubeContext, confirmationName(change.Confirmation()), access.Check{Verb: "patch"}); cmd != nil {
		return cmd
	}
	return a.withBusy(change.Confirmation().Action, 300*time.Millisecond, func() tea.Msg {
		ctx, cancel := context.WithTimeout(a.ctx, requestTimeout)
		defer cancel()
		res, err := change.Run(ctx)
		return workloadDoneMsg{res: res, err: err}
	})
}
