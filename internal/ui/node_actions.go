package ui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/access"
	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/nodeaction"
)

// nodeActionKey opens the node actions (cordon, uncordon, drain) for the
// marked nodes, or the node under the cursor. Esc n does the same where
// Option does not act as Meta. See docs/adr/0034-node-cordon-drain.md.
const nodeActionKey = "alt+n"

// nodePrepareTimeout bounds reading the nodes and their pods before a
// dialog opens. The first drain of a node starts its node-scoped pod
// cache, which has to sync first.
const nodePrepareTimeout = 30 * time.Second

// nodeTarget is the set of nodes an action works on.
type nodeTarget struct {
	env      nodeaction.Env
	nodes    []string
	panelIdx int
}

// nodeActionMsg marks the messages of the node action flow. They pass the
// modal filter, since the drain reports progress while its dialog is open.
type nodeActionMsg interface{ nodeActionMsg() }

type (
	nodeMenuChosenMsg struct {
		kind   nodeActionKind
		target nodeTarget
	}
	nodeDrainOptionsMsg struct {
		target nodeTarget
		opts   nodeaction.DrainOptions
		ok     bool
	}
	nodeCordonPreparedMsg struct {
		action *nodeaction.CordonAction
		err    error
	}
	nodeDrainPreparedMsg struct {
		action *nodeaction.DrainAction
		err    error
	}
	nodeConfirmResultMsg struct {
		ok     bool
		cordon *nodeaction.CordonAction
		drain  *nodeaction.DrainAction
	}
	nodeCordonDoneMsg struct {
		res nodeaction.Result
		err error
	}
	nodeDrainProgressMsg struct {
		view *drainProgressModel
		p    nodeaction.Progress
		ch   <-chan tea.Msg
	}
	nodeDrainDoneMsg struct {
		view *drainProgressModel
		res  nodeaction.Result
		err  error
	}
	nodeDrainCloseMsg struct{}
)

func (nodeMenuChosenMsg) nodeActionMsg()     {}
func (nodeDrainOptionsMsg) nodeActionMsg()   {}
func (nodeCordonPreparedMsg) nodeActionMsg() {}
func (nodeDrainPreparedMsg) nodeActionMsg()  {}
func (nodeConfirmResultMsg) nodeActionMsg()  {}
func (nodeCordonDoneMsg) nodeActionMsg()     {}
func (nodeDrainProgressMsg) nodeActionMsg()  {}
func (nodeDrainDoneMsg) nodeActionMsg()      {}
func (nodeDrainCloseMsg) nodeActionMsg()     {}

const (
	nodeMenuModal    = "node_actions"
	nodeOptionsModal = "node_drain_options"
	nodeConfirmModal = "confirm_node_action"
	nodeDrainModal   = "node_drain"
)

// nodeTargetFor returns the nodes the node actions work on in panel p:
// the marked node rows, or the node under the cursor. ok is false when p
// does not list core/v1 nodes. A non-empty reason says why there is
// nothing to act on.
func (a *App) nodeTargetFor(p *Panel) (target nodeTarget, reason string, ok bool) {
	if p == nil || p.Mode() != PanelModeList {
		return nodeTarget{}, "", false
	}
	folder, isProvider := p.Folder().(models.NodeActionProvider)
	if !isProvider {
		return nodeTarget{}, "", false
	}
	nt, isNodes := folder.NodeActions()
	if !isNodes {
		return nodeTarget{}, "", false
	}
	var items []models.Item
	for _, it := range p.GetSelectedItems() {
		items = append(items, it.Item)
	}
	if len(items) == 0 {
		ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
		item, found := p.SelectedNavItem(ctx)
		cancel()
		if found {
			items = append(items, item)
		}
	}
	var nodes []string
	for _, it := range items {
		obj, isObj := it.(models.ObjectItem)
		if !isObj || obj.GVR() != nodeaction.NodesGVR {
			continue
		}
		if !obj.SupportsVerb("patch") {
			return nodeTarget{}, "Nodes do not offer the patch verb here", true
		}
		nodes = append(nodes, obj.Name())
	}
	if len(nodes) == 0 {
		return nodeTarget{}, "Select a node first", true
	}
	env := nodeaction.Env{Client: nt.Client, Context: nt.Context, Kubectl: nt.Kubectl, Pods: nt.Pods}
	return nodeTarget{env: env, nodes: nodes, panelIdx: a.panelIndex(p)}, "", true
}

// openNodeActions shows the node action menu for the active panel. handled
// is false when the panel does not list nodes, so the key goes on to its
// usual handler.
func (a *App) openNodeActions() (tea.Cmd, bool) {
	p := a.activePanelRef()
	target, reason, ok := a.nodeTargetFor(p)
	if !ok {
		return nil, false
	}
	if reason != "" {
		return a.ShowToast(reason, 3*time.Second), true
	}
	// Read-only mode refuses every node action here, before any dialog
	// opens: cordon, uncordon and drain all patch the nodes.
	if cmd := a.refuseReadOnly("Node actions", access.Check{Resource: nodeaction.NodesGVR, Verb: "patch"}); cmd != nil {
		return cmd, true
	}
	menu := newNodeActionMenu(target)
	modal := NewModal("Node actions", menu)
	a.modalManager.Register(nodeMenuModal, modal)
	w, h := menu.PreferredSize(a.width, a.height)
	a.configureModalWindow(modal, menu, a.activePanel, "", w+2, h+2)
	a.showModal(nodeMenuModal)
	return nil, true
}

func (a *App) handleNodeActionMsg(msg nodeActionMsg) tea.Cmd {
	switch m := msg.(type) {
	case nodeMenuChosenMsg:
		a.hideModalName(nodeMenuModal)
		switch m.kind {
		case nodeActionCordon, nodeActionUncordon:
			return a.prepareCordon(m.target, m.kind == nodeActionCordon)
		case nodeActionDrain:
			return a.showDrainOptions(m.target)
		}
	case nodeDrainOptionsMsg:
		a.hideModalName(nodeOptionsModal)
		if !m.ok {
			return nil
		}
		return a.prepareDrain(m.target, m.opts)
	case nodeCordonPreparedMsg:
		if m.err != nil {
			return a.nodeActionError(m.err)
		}
		if len(m.action.Nodes()) == 0 {
			// Every node is in the target state already; nothing to confirm.
			res, _ := m.action.Run(a.ctx)
			return a.ShowInfo(res.Message, 5*time.Second)
		}
		return a.showNodeConfirm(&nodeConfirmModel{cordon: m.action}, m.action.Confirmation(), m.action.Command())
	case nodeDrainPreparedMsg:
		if m.err != nil {
			return a.nodeActionError(m.err)
		}
		return a.showNodeConfirm(&nodeConfirmModel{drain: m.action}, m.action.Confirmation(), m.action.Command())
	case nodeConfirmResultMsg:
		a.hideModalName(nodeConfirmModal)
		switch {
		case !m.ok:
			return nil
		case m.cordon != nil:
			return a.runCordon(m.cordon)
		case m.drain != nil:
			return a.startDrain(m.drain)
		}
	case nodeCordonDoneMsg:
		if m.err != nil {
			return a.nodeActionError(m.err)
		}
		return a.ShowInfo(m.res.Message, 5*time.Second)
	case nodeDrainProgressMsg:
		m.view.setProgress(m.p)
		a.invalidateView("drain progress")
		return waitDrain(m.ch)
	case nodeDrainDoneMsg:
		m.view.finish(m.res, m.err)
		a.invalidateView("drain done")
		if m.err != nil {
			return a.nodeActionError(m.err)
		}
		return a.ShowInfo(m.res.Message, 5*time.Second)
	case nodeDrainCloseMsg:
		a.hideModalName(nodeDrainModal)
	}
	return nil
}

func (a *App) nodeActionError(err error) tea.Cmd {
	if a.toastLogger != nil {
		return a.toastLogger.Errorf("%v", err)
	}
	return a.ShowToast("Failed: "+err.Error(), 5*time.Second)
}

func (a *App) prepareCordon(target nodeTarget, cordon bool) tea.Cmd {
	label := "Uncordon"
	if cordon {
		label = "Cordon"
	}
	return a.withBusy(label, 300*time.Millisecond, func() tea.Msg {
		ctx, cancel := context.WithTimeout(a.ctx, nodePrepareTimeout)
		defer cancel()
		action, err := nodeaction.PrepareCordon(ctx, target.env, target.nodes, cordon)
		return nodeCordonPreparedMsg{action: action, err: err}
	})
}

func (a *App) runCordon(action *nodeaction.CordonAction) tea.Cmd {
	return a.withBusy("Apply", 300*time.Millisecond, func() tea.Msg {
		ctx, cancel := context.WithTimeout(a.ctx, requestTimeout)
		defer cancel()
		res, err := action.Run(ctx)
		return nodeCordonDoneMsg{res: res, err: err}
	})
}

func (a *App) showDrainOptions(target nodeTarget) tea.Cmd {
	form := newDrainOptionsModel(target, nodeaction.DefaultDrainOptions())
	modal := NewModal("Drain", form)
	a.modalManager.Register(nodeOptionsModal, modal)
	w, h := form.PreferredSize(a.width, a.height)
	a.configureModalWindow(modal, form, a.activePanel, "", w+2, h+2)
	a.showModal(nodeOptionsModal)
	return nil
}

func (a *App) prepareDrain(target nodeTarget, opts nodeaction.DrainOptions) tea.Cmd {
	return a.withBusy("Drain", 300*time.Millisecond, func() tea.Msg {
		ctx, cancel := context.WithTimeout(a.ctx, nodePrepareTimeout)
		defer cancel()
		action, err := nodeaction.PrepareDrain(ctx, target.env, target.nodes, opts)
		return nodeDrainPreparedMsg{action: action, err: err}
	})
}

// showNodeConfirm opens the red dialog. models.Confirmation has no command
// field on main yet (PR #80 adds one), so the kubectl command goes into
// the notes.
func (a *App) showNodeConfirm(m *nodeConfirmModel, c models.Confirmation, command string) tea.Cmd {
	// The dialog is no wider than a panel; wrap long notes instead of
	// cutting them.
	const noteWidth = 52
	var notes []string
	for _, n := range c.Notes {
		if len(n) <= noteWidth {
			notes = append(notes, n)
			continue
		}
		notes = append(notes, wrapCommand(n, noteWidth)...)
	}
	if command != "" {
		notes = append(notes, "", "Same as:")
		notes = append(notes, wrapCommand(command, noteWidth)...)
	}
	c.Notes = notes
	m.ConfirmModel = NewConfirmModel(c)
	modal := NewModal("Confirm", m)
	modal.SetDanger(true)
	a.modalManager.Register(nodeConfirmModal, modal)
	w, h := m.size()
	a.configureModalWindow(modal, m, a.activePanel, "", min(w+2, max(30, a.width-6)), h+2)
	a.showModal(nodeConfirmModal)
	return nil
}

// startDrain runs the drain on its own goroutine and shows its progress.
// Closing the dialog with Esc or Cancel cancels the drain's context.
func (a *App) startDrain(action *nodeaction.DrainAction) tea.Cmd {
	ctx, cancel := context.WithCancel(a.ctx)
	view := newDrainProgressModel(action.Nodes(), cancel)
	modal := NewModal("Drain", view)
	a.modalManager.Register(nodeDrainModal, modal)
	w, h := view.PreferredSize(a.width, a.height)
	a.configureModalWindow(modal, view, a.activePanel, "", w+2, h+2)
	a.showModal(nodeDrainModal)

	ch := make(chan tea.Msg)
	go func() {
		defer close(ch)
		defer cancel()
		res, err := action.Run(ctx, func(p nodeaction.Progress) {
			ch <- nodeDrainProgressMsg{view: view, p: p, ch: ch}
		})
		ch <- nodeDrainDoneMsg{view: view, res: res, err: err}
	}()
	return waitDrain(ch)
}

// waitDrain delivers the drain's next message. The App re-arms it after
// every progress message, so the drain goroutine never blocks for long.
func waitDrain(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// wrapCommand breaks a shell command at spaces into lines of at most width
// characters; continuation lines are indented by two spaces.
func wrapCommand(cmd string, width int) []string {
	var lines []string
	line := ""
	for w := range strings.FieldsSeq(cmd) {
		switch {
		case line == "":
			line = w
		case len(line)+1+len(w) > width:
			lines = append(lines, line)
			line = "  " + w
		default:
			line += " " + w
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// nodeConfirmModel is the red confirmation dialog for a node action. It
// turns the dialog's answer into a nodeConfirmResultMsg that carries the
// action, so the App keeps no pending state for it.
type nodeConfirmModel struct {
	*ConfirmModel
	cordon *nodeaction.CordonAction
	drain  *nodeaction.DrainAction
}

func (m *nodeConfirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := m.ConfirmModel.Update(msg)
	return m, m.wrap(cmd)
}

func (m *nodeConfirmModel) HandleModalEscape(tea.KeyMsg) (bool, tea.Cmd) {
	return true, m.wrap(answer(false))
}

func (m *nodeConfirmModel) wrap(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := cmd()
		if r, ok := msg.(confirmResultMsg); ok {
			return nodeConfirmResultMsg{ok: r.ok, cordon: m.cordon, drain: m.drain}
		}
		return msg
	}
}
