package ui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	navui "github.com/sttts/kc/internal/navigation"
	"github.com/sttts/kc/pkg/kubeconfig"
)

// A jump moves a panel to a saved location (a hotlist entry or a history
// entry). Rows of a folder appear when its informer has synced, so a step
// whose row is missing is retried until the row shows up or the attempts run
// out. Each attempt runs on the update goroutine with a short timeout; the
// wait between attempts warms the folder off it.
const (
	jumpRetry    = 250 * time.Millisecond
	jumpAttempts = 40
	jumpWarmup   = time.Second
)

// pathJump is one jump in progress.
type pathJump struct {
	panel   int
	token   int
	nav     *navui.Navigator
	target  navui.Location
	done    int // steps of target already entered
	attempt int
	// record adds the reached location to the panel history; history
	// moves themselves do not record.
	record bool
}

// pathJumpMsg resumes a jump after a wait.
type pathJumpMsg struct{ jump pathJump }

// pendingJump waits for a context switch to finish.
type pendingJump struct {
	panel  int
	target navui.Location
	record bool
}

// currentContextName is the name of kc's kubeconfig context, "" before one
// is selected.
func (a *App) currentContextName() string {
	if a.currentCtx == nil {
		return ""
	}
	return a.currentCtx.Name
}

// panelLocation is where panel idx is now.
func (a *App) panelLocation(idx int) (navui.Location, bool) {
	nav := a.navigatorFor(idx)
	if nav == nil || nav.Current() == nil {
		return navui.Location{}, false
	}
	return navui.Location{
		Context:   a.currentContextName(),
		Namespace: a.panelScope[idx],
		Steps:     nav.Steps(),
		Path:      a.navigatorPath(nav),
	}, true
}

// panelHistory returns the history of panel idx, creating it on first use.
func (a *App) panelHistory(idx int) *navui.History {
	if a.history[idx] == nil {
		a.history[idx] = navui.NewHistory(navui.DefaultHistoryLimit)
	}
	return a.history[idx]
}

// recordHistory adds the current location of panel idx to its history.
func (a *App) recordHistory(idx int) {
	if idx < 0 || idx > 1 {
		return
	}
	if p := a.pendingJump; p != nil && p.panel == idx {
		// The panel is on its way somewhere else.
		return
	}
	if loc, ok := a.panelLocation(idx); ok {
		a.panelHistory(idx).Record(loc)
	}
}

// cancelJump stops any jump of panel idx; the user moved the panel.
func (a *App) cancelJump(idx int) {
	if idx < 0 || idx > 1 {
		return
	}
	a.jumpToken[idx]++
	if p := a.pendingJump; p != nil && p.panel == idx {
		a.pendingJump = nil
	}
}

// findContext returns the kubeconfig context called name.
func (a *App) findContext(name string) *kubeconfig.Context {
	if a.kubeMgr == nil {
		return nil
	}
	for _, c := range a.kubeMgr.GetContexts() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// jumpTo moves panel idx to target. A target in another context switches kc
// to that context first, which puts both panels at their roots.
func (a *App) jumpTo(idx int, target navui.Location, record bool) tea.Cmd {
	if idx < 0 || idx > 1 {
		return nil
	}
	a.cancelJump(idx)
	if target.Context != "" && target.Context != a.currentContextName() {
		c := a.findContext(target.Context)
		if c == nil {
			return a.ShowToast(fmt.Sprintf("Context %s not found", target.Context), 3*time.Second)
		}
		cmd := a.switchContext(c)
		if cmd == nil {
			return nil
		}
		a.pendingJump = &pendingJump{panel: idx, target: target, record: record}
		return cmd
	}
	nav := a.navigatorFor(idx)
	if nav == nil {
		return nil
	}
	done := 0
	if a.panelScope[idx] != target.Namespace {
		a.panelScope[idx] = target.Namespace
		nav = a.newScopedNavigator(idx)
		a.replaceNavigator(idx, nav)
		if idx == a.activePanel {
			a.updateTermContextNamespace(target.Namespace)
		}
	} else if cur, ok := a.panelLocation(idx); ok {
		done = cur.CommonSteps(target)
		nav.BackTo(done)
	}
	a.syncPanelWithNavigator(idx)
	a.syncPanelScopeLabels()
	return a.continueJump(pathJump{
		panel:  idx,
		token:  a.jumpToken[idx],
		nav:    nav,
		target: target,
		done:   done,
		record: record,
	})
}

// resumePendingJump starts the jump that waited for a context switch.
func (a *App) resumePendingJump() tea.Cmd {
	p := a.pendingJump
	a.pendingJump = nil
	if p == nil || p.target.Context != a.currentContextName() {
		return nil
	}
	return a.jumpTo(p.panel, p.target, p.record)
}

// continueJump enters the remaining steps of j. It gives up quietly when the
// panel was moved or its navigator replaced since the jump started.
func (a *App) continueJump(j pathJump) tea.Cmd {
	nav := a.navigatorFor(j.panel)
	if j.token != a.jumpToken[j.panel] || nav != j.nav || a.navigatorDepth(nav) != j.done+1 {
		return nil
	}
	steps := make([]navui.GoToStep, 0, len(j.target.Steps)-j.done)
	for _, id := range j.target.Steps[j.done:] {
		steps = append(steps, navui.GoToStep{SelectionID: id, Enter: true})
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	res, err := navui.GoTo(ctx, nav, steps)
	cancel()
	j.done += len(res.Entered)
	var notFound *navui.ItemNotFoundError
	if errors.As(err, &notFound) && j.attempt+1 < jumpAttempts {
		if len(res.Entered) > 0 {
			a.syncPanelWithNavigator(j.panel)
		}
		j.attempt++
		return a.waitJump(j)
	}
	a.finishJump(j)
	if err != nil {
		ctrllog.FromContext(a.ctx).WithName("jump").Info("jump stopped", "path", j.target.Path, "error", err.Error())
		return a.ShowToast(fmt.Sprintf("Cannot open %s: %v", j.target.Path, err), 3*time.Second)
	}
	return nil
}

// waitJump resumes j after a pause in which it loads the folder the jump is
// waiting on, off the update goroutine.
func (a *App) waitJump(j pathJump) tea.Cmd {
	folder := j.nav.Current()
	parent := a.ctx
	return func() tea.Msg {
		timer := time.NewTimer(jumpRetry)
		defer timer.Stop()
		select {
		case <-parent.Done():
			return nil
		case <-timer.C:
		}
		if folder != nil {
			ctx, cancel := context.WithTimeout(parent, jumpWarmup)
			folder.Len(ctx)
			cancel()
		}
		return pathJumpMsg{jump: j}
	}
}

// finishJump shows where the jump ended: the row it came back from is
// highlighted, as after Back.
func (a *App) finishJump(j pathJump) {
	a.syncPanelWithNavigator(j.panel)
	panel := a.panelByIndex(j.panel)
	if panel != nil {
		ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
		if id := j.nav.CurrentSelectionID(); id != "" && j.done == len(j.target.Steps) {
			panel.SelectByRowID(ctx, id)
		} else {
			panel.ResetSelectionTop(ctx)
		}
		cancel()
		a.applyResourceOptions(panel)
	}
	a.syncTerminalNamespaceForNavigator(j.nav)
	if j.record {
		a.recordHistory(j.panel)
	}
	a.invalidateView("jump")
}

// historyBack moves the active panel to the previous location of its history.
func (a *App) historyBack() tea.Cmd {
	idx := a.activePanel
	loc, ok := a.panelHistory(idx).Back()
	if !ok {
		return nil
	}
	return a.jumpTo(idx, loc, false)
}

// historyForward moves the active panel to the next location of its history.
func (a *App) historyForward() tea.Cmd {
	idx := a.activePanel
	loc, ok := a.panelHistory(idx).Forward()
	if !ok {
		return nil
	}
	return a.jumpTo(idx, loc, false)
}
