package ui

import (
	"fmt"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

// panelName is how messages name panel idx.
func panelName(idx int) string {
	if idx == 1 {
		return "right"
	}
	return "left"
}

// contextChoices lists the kubeconfig contexts for the picker, the current
// one marked. Values are indexes into contexts, since names may repeat
// across kubeconfig files; the note then names the file.
func contextChoices(contexts []*kubeconfig.Context, current *kubeconfig.Context) []scopeChoice {
	return panelContextChoices(contexts, current, nil, "")
}

// panelContextChoices is contextChoices that also marks other, the
// context of the other panel, with otherNote.
func panelContextChoices(contexts []*kubeconfig.Context, current, other *kubeconfig.Context, otherNote string) []scopeChoice {
	files := make(map[string]bool)
	for _, c := range contexts {
		if c.Kubeconfig != nil {
			files[c.Kubeconfig.Path] = true
		}
	}
	out := make([]scopeChoice, 0, len(contexts))
	for i, c := range contexts {
		note := ""
		if len(files) > 1 && c.Kubeconfig != nil {
			note = filepath.Base(c.Kubeconfig.Path)
		}
		switch {
		case c == current:
			note = "current"
		case other != nil && c == other && otherNote != "":
			note = otherNote
		}
		out = append(out, scopeChoice{Label: c.Name, Value: fmt.Sprint(i), Note: note})
	}
	return out
}

// showContextPicker opens the context picker of panel idx. Choosing a
// context switches that panel; the other panel and kubeconfig are not
// changed.
func (a *App) showContextPicker(idx int) tea.Cmd {
	return a.openContextPicker(idx, false)
}

// showBothContextPicker opens the picker that switches both panels.
func (a *App) showBothContextPicker() tea.Cmd {
	return a.openContextPicker(a.activePanel, true)
}

func (a *App) openContextPicker(idx int, both bool) tea.Cmd {
	if a.kubeMgr == nil || idx < 0 || idx > 1 {
		return nil
	}
	contexts := a.kubeMgr.GetContexts()
	if len(contexts) == 0 {
		return a.ShowToast("No kubeconfig contexts found", 3*time.Second)
	}
	own, other := a.ownConn(idx), a.ownConn(1-idx)
	var currentCtx, otherCtx *kubeconfig.Context
	if own != nil {
		currentCtx = own.kctx
	}
	if other != nil {
		otherCtx = other.kctx
	}
	otherNote := panelName(1 - idx)
	if both {
		otherNote = "current " + otherNote
	}
	choices := panelContextChoices(contexts, currentCtx, otherCtx, otherNote)
	current := ""
	for _, c := range choices {
		if c.Note == "current" {
			current = c.Value
		}
	}
	picker := NewScopePickerModel(choices, current, func(c scopeChoice) tea.Cmd {
		a.hideModal()
		var i int
		if _, err := fmt.Sscan(c.Value, &i); err != nil || i < 0 || i >= len(contexts) {
			return nil
		}
		if both {
			return a.switchContexts([]int{0, 1}, contexts[i])
		}
		return a.switchContexts([]int{idx}, contexts[i])
	}, func() tea.Cmd {
		a.hideModal()
		return nil
	})
	width := 30
	for _, c := range choices {
		width = max(width, len([]rune(c.Label))+len([]rune(c.Note))+6)
	}
	title := "Context (" + panelName(idx) + " panel)"
	if both {
		title = "Context (both panels)"
	}
	modal := NewModal(title, picker)
	a.modalManager.Register("context_picker", modal)
	a.configureModalWindow(modal, picker, idx, "", min(width, 70), min(len(choices)+3, 20))
	a.modalManager.Show("context_picker")
	a.invalidateView("context picker")
	return nil
}

// panelSwitch is one panel a context switch is for: the panel (a pointer,
// so a swap meanwhile does not misroute the result), the request's
// sequence and, once connected, the panel's lease.
type panelSwitch struct {
	panel *Panel
	seq   uint64
	lease *kccluster.Lease
}

// contextSwitchedMsg carries the clusters acquired for a context switch of
// one or both panels.
type contextSwitchedMsg struct {
	target *kubeconfig.Context
	key    kccluster.Key
	panels []panelSwitch
	infos  []kccluster.ResourceInfo
	err    error
}

// switchContext switches both panels to target. Startup and tests use it;
// the pickers switch one panel or both on purpose.
func (a *App) switchContext(target *kubeconfig.Context) tea.Cmd {
	return a.switchContexts([]int{0, 1}, target)
}

// switchContexts connects panels idxs to target in the background;
// applyContextSwitch finishes on the UI thread. A panel already on target
// is left as it is.
func (a *App) switchContexts(idxs []int, target *kubeconfig.Context) tea.Cmd {
	if target == nil || target.Kubeconfig == nil || a.clPool == nil {
		return nil
	}
	if a.switchSeq == nil {
		a.switchSeq = make(map[*Panel]uint64)
	}
	var panels []panelSwitch
	for _, idx := range idxs {
		p := a.panelByIndex(idx)
		if p == nil {
			continue
		}
		// A switch replaces a jump still waiting for an earlier one;
		// jumpToView sets its own after this.
		a.pendingJumps[idx] = nil
		if c := a.ownConn(idx); c != nil && c.kctx == target {
			// A newer request replaces an older one, even when it asks
			// for the context the panel already has.
			a.switchSeq[p]++
			continue
		}
		a.switchSeq[p]++
		panels = append(panels, panelSwitch{panel: p, seq: a.switchSeq[p]})
	}
	if len(panels) == 0 {
		return nil
	}
	key := a.clusterKeyFor(target)
	pool, root := a.clPool, a.ctx
	load := func() tea.Msg {
		msg := contextSwitchedMsg{target: target, key: key, panels: panels}
		ctrllog.FromContext(root).WithName("context").Info("acquiring cluster", "context", target.Name, "panels", len(panels))
		for i := range msg.panels {
			lease, err := pool.Acquire(root, key, nil)
			if err != nil {
				msg.releaseLeases()
				msg.err = err
				return msg
			}
			msg.panels[i].lease = lease
		}
		infos, err := msg.panels[0].lease.Cluster().GetResourceInfos()
		if err != nil {
			msg.releaseLeases()
			msg.err = err
			return msg
		}
		msg.infos = infos
		return msg
	}
	return tea.Batch(a.ShowInfo("Connecting to "+target.Name+"...", 10*time.Second), load)
}

// releaseLeases gives up every lease of the message.
func (m *contextSwitchedMsg) releaseLeases() {
	for i := range m.panels {
		m.panels[i].lease.Release()
		m.panels[i].lease = nil
	}
}

// applyContextSwitch makes the new cluster the connection of each panel
// the switch is still for, and puts that panel at the root of the
// context's namespace. A failed switch, or a result that a newer request
// of the panel replaced, changes nothing.
func (a *App) applyContextSwitch(msg contextSwitchedMsg) tea.Cmd {
	if msg.err != nil {
		msg.releaseLeases()
		for _, ps := range msg.panels {
			if idx, ok := a.panelIndexFor(ps.panel); ok && a.switchSeq[ps.panel] == ps.seq {
				a.dropPendingJump(idx)
			}
		}
		return a.ShowToast(fmt.Sprintf("Cannot switch to %s: %v", msg.target.Name, msg.err), 5*time.Second)
	}
	var switched []int
	for _, ps := range msg.panels {
		idx, ok := a.panelIndexFor(ps.panel)
		if !ok || a.switchSeq[ps.panel] != ps.seq {
			// A newer switch of the panel is on its way, or done.
			ps.lease.Release()
			continue
		}
		c := a.newPanelConn(msg.target, msg.key, ps.lease)
		a.installPanelConn(idx, c, msg.infos)
		switched = append(switched, idx)
	}
	if len(switched) == 0 {
		return nil
	}
	a.ensureTermContext()
	a.syncPanelScopeLabels()
	a.invalidateView("context switch")
	a.invalidateFunctionBar("context switch")
	info := "Context: " + msg.target.Name
	if len(switched) == 1 {
		info = titleCase(panelName(switched[0])) + " panel context: " + msg.target.Name
	}
	if a.readOnlyFor(msg.target.Name) {
		info += " (read-only)"
	}
	cmds := []tea.Cmd{a.ShowInfo(info, 2*time.Second)}
	for _, idx := range switched {
		cmds = append(cmds, a.resumePendingJump(idx))
	}
	return tea.Batch(cmds...)
}

// installPanelConn makes c panel idx's connection and puts the panel at
// the root of c's namespace. infos is c's discovery.
func (a *App) installPanelConn(idx int, c *panelConn, infos []kccluster.ResourceInfo) {
	a.setPanelConn(idx, c)
	a.pruneDescribeRenderers()
	if p := a.panelByIndex(idx); p != nil {
		p.SetResourceCatalog(infos)
	}
	// The panel's own visibility choice was made for the old context; the
	// new one lists under its binding or the global default.
	a.resetPanelVisibilityOf(idx)
	// A running jump walked the old navigator. A jump waiting for this
	// switch stays: applyContextSwitch resumes it.
	a.jumpToken[idx]++
	a.rescopePanel(idx, contextNamespace(c.kctx))
}

// rescopePanel puts panel idx at the root of namespace scope ns of its
// connection.
func (a *App) rescopePanel(idx int, ns string) {
	a.panelScope[idx] = ns
	a.rememberPanelNamespace(ns)
	a.replaceNavigator(idx, a.newScopedNavigator(idx))
	if a.clPool != nil {
		// Unheld namespace caches other than ns can go; the folders the
		// old navigator held were closed above.
		a.clPool.PruneNamespaces(ns)
	}
	a.showNavigatorRoot(idx)
	a.installFolderNavHandlers()
	a.recordHistory(idx)
}

// contextNamespace is the namespace kc starts with in a context.
func contextNamespace(c *kubeconfig.Context) string {
	if c != nil && c.Namespace != "" {
		return c.Namespace
	}
	return "default"
}
