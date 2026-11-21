package ui

import (
	"fmt"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	kccluster "github.com/sttts/kc/internal/cluster"
	"github.com/sttts/kc/internal/podfs"
	"github.com/sttts/kc/pkg/kubeconfig"
)

// contextChoices lists the kubeconfig contexts for the picker, the current
// one marked. Values are indexes into contexts, since names may repeat
// across kubeconfig files; the note then names the file.
func contextChoices(contexts []*kubeconfig.Context, current *kubeconfig.Context) []scopeChoice {
	files := map[string]bool{}
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
		if c == current {
			note = "current"
		}
		out = append(out, scopeChoice{Label: c.Name, Value: fmt.Sprint(i), Note: note})
	}
	return out
}

// showContextPicker opens the context picker. Choosing a context switches
// kc to it; kubeconfig is not changed.
func (a *App) showContextPicker() tea.Cmd {
	if a.kubeMgr == nil {
		return nil
	}
	contexts := a.kubeMgr.GetContexts()
	if len(contexts) == 0 {
		return a.ShowToast("No kubeconfig contexts found", 3*time.Second)
	}
	choices := contextChoices(contexts, a.currentCtx)
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
		if contexts[i] == a.currentCtx {
			return nil
		}
		return a.switchContext(contexts[i])
	}, func() tea.Cmd {
		a.hideModal()
		return nil
	})
	width := 30
	for _, c := range choices {
		width = max(width, len([]rune(c.Label))+len([]rune(c.Note))+6)
	}
	modal := NewModal("Context", picker)
	a.modalManager.Register("context_picker", modal)
	a.configureModalWindow(modal, picker, a.activePanel, "", min(width, 70), min(len(choices)+3, 20))
	a.modalManager.Show("context_picker")
	a.invalidateView("context picker")
	return nil
}

// contextSwitchedMsg carries a cluster acquired for a new context.
type contextSwitchedMsg struct {
	target *kubeconfig.Context
	key    kccluster.Key
	cl     *kccluster.Cluster
	infos  []kccluster.ResourceInfo
	err    error
}

// switchContext connects to target in the background; applyContextSwitch
// finishes on the UI thread.
func (a *App) switchContext(target *kubeconfig.Context) tea.Cmd {
	if target == nil || target.Kubeconfig == nil || a.clPool == nil {
		return nil
	}
	key := kccluster.Key{
		KubeconfigPath:       target.Kubeconfig.Path,
		ContextName:          target.Name,
		ImpersonateUser:      a.currentClusterKey().ImpersonateUser,
		ImpersonateUID:       a.currentClusterKey().ImpersonateUID,
		ImpersonateGroupsKey: a.impersonationGroupsKey(),
	}
	pool, root := a.clPool, a.ctx
	load := func() tea.Msg {
		ctrllog.FromContext(root).WithName("context").Info("acquiring cluster", "context", target.Name)
		cl, err := pool.Get(root, key)
		if err != nil {
			return contextSwitchedMsg{target: target, err: err}
		}
		infos, err := cl.GetResourceInfos()
		return contextSwitchedMsg{target: target, key: key, cl: cl, infos: infos, err: err}
	}
	return tea.Batch(a.ShowInfo("Connecting to "+target.Name+"...", 10*time.Second), load)
}

// applyContextSwitch makes msg's cluster the app's cluster and scopes both
// panels to the context's namespace.
func (a *App) applyContextSwitch(msg contextSwitchedMsg) tea.Cmd {
	if msg.err != nil {
		return a.ShowToast(fmt.Sprintf("Cannot switch to %s: %v", msg.target.Name, msg.err), 5*time.Second)
	}
	oldKey := a.currentClusterKey()
	a.currentCtx = msg.target
	a.cl = msg.cl
	a.clPool.Pin(msg.key)
	if oldKey != msg.key {
		a.clPool.Unpin(oldKey)
	}
	if factory, err := podfs.NewFactory(a.cl.GetConfig()); err == nil {
		a.podfsFactory = factory
	}
	a.leftPanel.SetResourceCatalog(msg.infos)
	a.rightPanel.SetResourceCatalog(msg.infos)
	if a.discoveryCancel != nil {
		a.discoveryCancel()
	}
	a.discoveryCancel = a.cl.AddDiscoveryListener(func() {
		select {
		case a.discoveryCh <- struct{}{}:
		default:
		}
	})
	a.ensureTermContext()
	a.goToNamespace(contextNamespace(msg.target))
	a.invalidateView("context switch")
	return a.ShowInfo("Context: "+msg.target.Name, 2*time.Second)
}

// contextNamespace is the namespace kc starts with in a context.
func contextNamespace(c *kubeconfig.Context) string {
	if c != nil && c.Namespace != "" {
		return c.Namespace
	}
	return "default"
}
