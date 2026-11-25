package ui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// panelWalkMsg asks a waiting panel navigation to look again.
type panelWalkMsg struct {
	panel int
	gen   uint64
}

// panelNavigation is a running NavigatePanel call.
type panelNavigation struct {
	gen    uint64
	path   PanelPath
	walk   *pathWalk
	onDone func(error) tea.Cmd
}

// NavigatePanel moves panel idx (0 left, 1 right) to p. Elements that are
// not there yet (a type row whose emptiness is still unknown, an object the
// informer has not delivered) are waited for, up to 10 seconds each,
// with ticks; the update goroutine never blocks. The panel shows every
// folder as soon as the walk enters it. On failure the panel stays where
// the walk stopped.
//
// onDone runs on the update goroutine when the walk ends: with nil after
// success, or with the error. A nil onDone shows errors as a toast. A
// newer NavigatePanel call on the same panel, or the user navigating that
// panel, ends the walk without calling onDone.
//
// The location the panel had before the call becomes the target of cd -.
// Where the walk ends is recorded in the panel history (C-x h, M-, M-.).
func (a *App) NavigatePanel(idx int, p PanelPath, onDone func(error) tea.Cmd) tea.Cmd {
	if onDone == nil {
		onDone = func(err error) tea.Cmd {
			if err != nil {
				a.notifyIntentErrorf("%v", err)
			}
			return nil
		}
	}
	if idx < 0 || idx > 1 || a.navigatorFor(idx) == nil || a.panelByIndex(idx) == nil {
		return onDone(errors.New("panel not ready"))
	}
	log := ctrllog.FromContext(a.ctx).WithName("navigatePanel")
	log.V(1).Info("navigate", "panel", idx, "path", p.String())

	prev := a.PanelLocation(idx)
	a.prevLocation[idx] = &prev

	switch {
	case p.Rescope:
		a.panelToRoot(idx, p.Namespace)
	case p.Anchor == AnchorHome:
		a.panelToRoot(idx, a.initialNamespace())
	case p.Anchor == AnchorRoot || (p.Anchor == AnchorCurrent && a.typeShortcut(idx, p)):
		a.panelToRoot(idx, a.panelScope[idx])
	}

	a.panelNavGen++
	nav := a.navigatorFor(idx)
	a.panelNavs[idx] = &panelNavigation{
		gen:  a.panelNavGen,
		path: p,
		walk: newPathWalk(nav, p.Elems, p.SelectLast, func(name string) (schema.GroupResource, error) {
			return a.resolveTypeName(idx, name)
		}, time.Now()),
		onDone: onDone,
	}
	return a.continuePanelNavigation(idx, a.panelNavGen)
}

// PanelLocation returns where panel idx is: its namespace scope and the
// rows entered from the root. NavigatePanel(idx, PanelLocation(idx), nil)
// comes back to it, as long as those rows still exist.
func (a *App) PanelLocation(idx int) PanelPath {
	p := PanelPath{Anchor: AnchorRoot, Rescope: true}
	if idx < 0 || idx > 1 {
		return p
	}
	p.Namespace = a.panelScope[idx]
	if nav := a.navigatorFor(idx); nav != nil {
		for _, id := range nav.Steps() {
			p.Elems = append(p.Elems, PathElem{ID: id})
		}
	}
	return p
}

// continuePanelNavigation advances the walk of panel idx if gen is still
// its current walk, and schedules the next attempt while it waits.
func (a *App) continuePanelNavigation(idx int, gen uint64) tea.Cmd {
	pn := a.panelNavs[idx]
	if pn == nil || pn.gen != gen {
		return nil
	}
	nav := a.navigatorFor(idx)
	if nav != pn.walk.nav {
		// The navigator was replaced (context or scope change).
		a.panelNavs[idx] = nil
		return nil
	}
	before := nav.Current()
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	done, err := pn.walk.advance(ctx, time.Now())
	cancel()
	if nav.Current() != before {
		a.showWalkedFolder(idx)
	}
	switch {
	case errors.Is(err, errWalkMoved):
		a.panelNavs[idx] = nil
		return nil
	case err != nil:
		a.panelNavs[idx] = nil
		ctrllog.FromContext(a.ctx).WithName("navigatePanel").Info("navigation failed", "panel", idx, "path", pn.path.String(), "error", err.Error())
		// The panel stays where the walk stopped; that is a place it went.
		a.recordHistory(idx)
		return pn.onDone(err)
	case done:
		a.panelNavs[idx] = nil
		if id := pn.walk.selected; id != "" {
			ctxSel, cancelSel := context.WithTimeout(a.ctx, panelContextTimeout)
			a.panelByIndex(idx).SelectByRowID(ctxSel, id)
			cancelSel()
		}
		// A cd, an overview open or a startup intent lands in the panel
		// history like a move made with Enter.
		a.recordHistory(idx)
		return pn.onDone(nil)
	}
	return tea.Tick(pathWaitInterval, func(time.Time) tea.Msg {
		return panelWalkMsg{panel: idx, gen: gen}
	})
}

// typeShortcut reports whether a relative path starts with a resource type
// while the panel is somewhere that does not list types; then the path is
// taken from the root.
func (a *App) typeShortcut(idx int, p PanelPath) bool {
	if len(p.Elems) == 0 {
		return false
	}
	first := p.Elems[0]
	if first.Name == "" || first.isParent() {
		return false
	}
	nav := a.navigatorFor(idx)
	if _, ok := nav.Current().(typeFolder); ok || !nav.HasBack() {
		return false
	}
	_, err := a.resolveTypeName(idx, first.Name)
	return err == nil
}

// resolveTypeName maps "po", "pods", "pod" or "deployments.apps" to the
// resource type with the RESTMapper of panel idx's cluster (which knows
// short names).
func (a *App) resolveTypeName(idx int, name string) (schema.GroupResource, error) {
	cl := a.ownConn(idx).cluster()
	if cl == nil {
		return schema.GroupResource{}, errors.New("cluster not ready")
	}
	gvr, err := cl.RESTMapper().ResourceFor(schema.ParseGroupResource(name).WithVersion(""))
	if err != nil {
		return schema.GroupResource{}, fmt.Errorf("unknown resource type %q", name)
	}
	return gvr.GroupResource(), nil
}

// panelToRoot puts panel idx at the root of namespace scope ns. The
// navigator is replaced only when the scope changes, so folders of the same
// scope keep their informers.
func (a *App) panelToRoot(idx int, ns string) {
	if ns != a.panelScope[idx] {
		a.rescopePanelRoot(idx, ns)
		return
	}
	nav := a.navigatorFor(idx)
	if !nav.HasBack() {
		return
	}
	for nav.HasBack() {
		nav.Back()
	}
	a.showWalkedFolder(idx)
}

// rescopePanelRoot puts panel idx at the root of namespace ns.
func (a *App) rescopePanelRoot(idx int, ns string) {
	a.panelScope[idx] = ns
	a.replaceNavigator(idx, a.newScopedNavigator(idx))
	a.showNavigatorRoot(idx)
	if idx == a.activePanel {
		a.ensureTermContext()
	}
}

// showWalkedFolder shows the navigator's current folder in the panel with
// the cursor at the top, like entering a folder by hand.
func (a *App) showWalkedFolder(idx int) {
	a.syncPanelWithNavigator(idx)
	panel := a.panelByIndex(idx)
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	panel.ResetSelectionTop(ctx)
	cancel()
	a.applyResourceOptions(panel)
	a.ensureTermContext()
}
