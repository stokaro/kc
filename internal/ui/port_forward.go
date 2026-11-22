package ui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sttts/kc/internal/models"
	"github.com/sttts/kc/internal/portforward"
	uistyles "github.com/sttts/kc/internal/ui/styles"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// portForwardKey opens the port forward dialog on a pod, container or
// service row (k9s uses Shift-F; in kc plain letters go to the shell line).
const portForwardKey = "alt+f"

const (
	portForwardPrepareTimeout = 15 * time.Second
	portForwardStartTimeout   = 30 * time.Second
	portForwardStopTimeout    = 2 * time.Second
	portForwardModal          = "port_forward"
)

// portForwardPlanMsg carries the prepared forward, or why there is none.
type portForwardPlanMsg struct {
	plan *models.PortForwardPlan
	err  error
}

// portForwardInputMsg is the answer of the port forward dialog.
type portForwardInputMsg struct {
	value   string
	confirm bool
	close   bool
}

// portForwardStartedMsg reports a forward that listens, or why it does not.
type portForwardStartedMsg struct {
	fwd portforward.Forward
	err error
}

// newPortForwardRegistry creates the process-wide registry. Forwards that
// end on their own show as a red message.
func (a *App) newPortForwardRegistry() *portforward.Registry {
	reg := portforward.NewRegistry(a.ctx)
	reg.OnEnded(func(fwd portforward.Forward, err error) {
		a.reportReadError(fmt.Errorf("port forward %s -> %s ended: %w", fwd.Local(), fwd.Spec.Target(), err))
	})
	return reg
}

// stopPortForwards ends all forwards when kc exits.
func (a *App) stopPortForwards() {
	if a.portForwards != nil {
		a.portForwards.StopAll(portForwardStopTimeout)
	}
}

// portForwardForPanel starts the port forward dialog for the selected row.
// Reading the target happens in the returned command, off the UI goroutine.
func (a *App) portForwardForPanel(panel *Panel) tea.Cmd {
	if panel == nil || panel.Mode() != PanelModeList || panel.HasCommandFocus() {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	item, ok := panel.SelectedNavItem(ctx)
	cancel()
	if !ok || item == nil {
		return nil
	}
	if _, ok := models.PortForwardTargetFor(item); !ok {
		return a.ShowToast("Port forward works on pods, containers and services", 3*time.Second)
	}
	folder := panel.Folder()
	root := a.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(root, portForwardPrepareTimeout)
		defer cancel()
		plan, err := models.PreparePortForward(ctx, folder, item)
		return portForwardPlanMsg{plan: plan, err: err}
	}
}

// handlePortForwardMsg drives the dialog and the start of a forward.
func (a *App) handlePortForwardMsg(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case portForwardPlanMsg:
		if msg.err != nil {
			return a.ShowToast("Port forward: "+msg.err.Error(), 5*time.Second)
		}
		return a.showPortForwardDialog(msg.plan)
	case portForwardInputMsg:
		plan := a.pendingPortForward
		if msg.close || !msg.confirm {
			a.hideModalName(portForwardModal)
			a.pendingPortForward = nil
		}
		if !msg.confirm || plan == nil {
			return nil
		}
		root := a.ctx
		value := msg.value
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(root, portForwardStartTimeout)
			defer cancel()
			log := ctrllog.FromContext(ctx).WithName("portforward")
			spec, err := plan.Resolve(ctx, value)
			if err != nil {
				return portForwardStartedMsg{err: err}
			}
			log.Info("starting port forward", "target", spec.Target(), "pod", spec.Pod, "namespace", spec.Namespace, "context", spec.Context, "local", spec.LocalPort, "remote", spec.RemotePort)
			fwd, err := plan.Registry().Start(ctx, spec, plan.Runner())
			return portForwardStartedMsg{fwd: fwd, err: err}
		}
	case portForwardStartedMsg:
		if msg.err != nil {
			return a.ShowToast("Port forward failed: "+msg.err.Error(), 6*time.Second)
		}
		text := fmt.Sprintf("Forwarding %s -> %s (pod %s:%d); see /%s",
			msg.fwd.Local(), msg.fwd.Spec.Target(), msg.fwd.Spec.Pod, msg.fwd.Spec.RemotePort, models.PortForwardsPathSegment)
		return a.ShowInfo(text, 5*time.Second)
	}
	return nil
}

func (a *App) showPortForwardDialog(plan *models.PortForwardPlan) tea.Cmd {
	if plan == nil {
		return nil
	}
	model := newPortForwardModel(plan)
	modal := NewModal("Port forward "+plan.Subject(), model)
	modal.SetOnClose(func() tea.Cmd {
		a.pendingPortForward = nil
		return nil
	})
	a.pendingPortForward = plan
	a.modalManager.Register(portForwardModal, modal)
	a.configureModalWindow(modal, model, a.activePanel, "", 60, 9)
	a.modalManager.Show(portForwardModal)
	a.invalidateView("port forward dialog")
	return model.FocusInput()
}

// portForwardModel is the dialog: one "local:remote" field, the ports the
// target declares, and the kubectl command for the current value.
type portForwardModel struct {
	modal *TextInputModal
	plan  *models.PortForwardPlan
}

func newPortForwardModel(plan *models.PortForwardPlan) *portForwardModel {
	m := &portForwardModel{plan: plan}
	confirmStyle := lipgloss.NewStyle().
		Padding(0, 3).
		Background(lipgloss.Color(uistyles.ColorModalButtonBg)).
		Foreground(lipgloss.Color(uistyles.ColorWhite))
	confirmFocused := confirmStyle.
		Background(lipgloss.Color(uistyles.ColorModalButtonSelBg)).
		Foreground(lipgloss.Color(uistyles.ColorBlack)).
		Bold(true)
	m.modal = NewTextInputModal(TextInputModalConfig{
		Title:        "Port forward",
		Label:        "Local:remote",
		Description:  m.description(plan.Default),
		ConfirmLabel: "Forward",
		CancelLabel:  "Cancel",
		Placeholder:  "8080:80",
		InitialValue: plan.Default,
		MinWidth:     40,
		MaxWidth:     100,
		MinHeight:    7,
		HideTitle:    true,
		Validate: func(v string) error {
			_, err := portforward.ParsePair(v)
			return err
		},
		ResultMsg: func(r TextInputModalResult) tea.Msg {
			return portForwardInputMsg{value: r.Value, confirm: r.Confirm, close: r.Close}
		},
		ButtonStyles: TextInputModalButtonStyles{Confirm: &confirmStyle, ConfirmFocused: &confirmFocused},
	})
	return m
}

func (m *portForwardModel) description(value string) string {
	return m.plan.PortsHint() + "\nListens on " + m.plan.Address + ". " + m.plan.Command(value)
}

func (m *portForwardModel) Init() tea.Cmd          { return m.modal.Init() }
func (m *portForwardModel) SetDimensions(w, h int) { m.modal.SetDimensions(w, h) }
func (m *portForwardModel) PreferredSize(maxW, maxH int) (int, int) {
	return m.modal.PreferredSize(maxW, maxH)
}
func (m *portForwardModel) FooterHints() []FooterHint { return m.modal.FooterHints() }
func (m *portForwardModel) FocusInput() tea.Cmd       { return m.modal.FocusInput() }
func (m *portForwardModel) View() tea.View            { return m.modal.View() }

// Update keeps the kubectl command in step with the typed value.
func (m *portForwardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := m.modal.Update(msg)
	m.modal.SetDescription(m.description(m.modal.Value()))
	return m, cmd
}

// stopActionFor returns the F8 action of a row that stops something kc
// runs locally, such as a port forward.
func stopActionFor(item models.Item) (models.ConfirmedAction, bool) {
	s, ok := item.(models.Stoppable)
	if !ok {
		return nil, false
	}
	return s.StopAction(), true
}

// stopSelectionForPanel confirms and stops the selected forward. It reports
// false when the selection is not stoppable.
func (a *App) stopSelectionForPanel(panel *Panel) (tea.Cmd, bool) {
	if panel == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	item, ok := panel.SelectedNavItem(ctx)
	cancel()
	if !ok || item == nil {
		return nil, false
	}
	action, ok := stopActionFor(item)
	if !ok {
		return nil, false
	}
	return a.showConfirm(action), true
}
