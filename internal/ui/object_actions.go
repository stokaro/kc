package ui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/sttts/kc/internal/models"
	"github.com/sttts/kc/internal/objectaction"
)

// objectActionTarget is the object the F2 user menu offers built-in actions for.
type objectActionTarget struct {
	actions  []objectaction.Action
	env      models.ActionEnv
	obj      models.ObjectRef
	panelIdx int
}

// objectActionPreparedMsg carries the change to confirm, or why it could
// not be prepared.
type objectActionPreparedMsg struct {
	label    string
	action   models.ClusterAction
	panelIdx int
	err      error
}

// objectActionDoneMsg reports a confirmed action's outcome.
type objectActionDoneMsg struct {
	result   models.ActionResult
	panelIdx int
	err      error
}

// selectCreatedMsg retries showing an object an action created. The new
// object reaches the lists through informers, so neither its type's row in
// the panel root (empty types may be hidden) nor its own row may be there
// yet.
type selectCreatedMsg struct {
	panelIdx int
	ref      models.ObjectRef
	path     string // the object list, once the panel has opened it
	attempt  int
}

const (
	createdSelectRetry    = 250 * time.Millisecond
	createdSelectAttempts = 40
)

func (a *App) objectActionRegistry() *objectaction.Registry {
	if a.objectActions == nil {
		a.objectActions = objectaction.Default()
	}
	return a.objectActions
}

// objectActionsFor returns the built-in actions for the object selected in
// p. Actions work on one object, so a multi-selection offers none.
func (a *App) objectActionsFor(p *Panel) objectActionTarget {
	if p == nil || p.Mode() != PanelModeList || len(p.GetSelectedItems()) > 1 {
		return objectActionTarget{}
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	item, ok := p.SelectedNavItem(ctx)
	cancel()
	if !ok || item == nil {
		return objectActionTarget{}
	}
	obj, ok := item.(models.ObjectItem)
	if !ok {
		return objectActionTarget{}
	}
	actions := a.objectActionRegistry().For(obj.GVR(), obj.SupportsVerb)
	if len(actions) == 0 {
		return objectActionTarget{}
	}
	provider, ok := p.Folder().(models.ActionEnvProvider)
	if !ok {
		return objectActionTarget{}
	}
	env, ok := provider.ActionEnv()
	if !ok {
		return objectActionTarget{}
	}
	return objectActionTarget{
		actions:  actions,
		env:      env,
		obj:      models.ObjectRef{GVR: obj.GVR(), Namespace: obj.Namespace(), Name: obj.Name()},
		panelIdx: a.panelIndex(p),
	}
}

// prepareObjectAction reads the object in the background and then asks
// for confirmation.
func (a *App) prepareObjectAction(target objectActionTarget, action objectaction.Action) tea.Cmd {
	return a.withBusy(action.Label, 300*time.Millisecond, func() tea.Msg {
		ctx, cancel := context.WithTimeout(a.ctx, requestTimeout)
		defer cancel()
		ca, err := action.Prepare(ctx, target.env, target.obj)
		return objectActionPreparedMsg{label: action.Label, action: ca, panelIdx: target.panelIdx, err: err}
	})
}

func (a *App) handleObjectActionPrepared(msg objectActionPreparedMsg) tea.Cmd {
	if msg.err != nil {
		return a.objectActionError(fmt.Sprintf("%s %v", msg.label, msg.err))
	}
	if msg.action == nil {
		return nil
	}
	return a.showClusterActionConfirm(msg.action, msg.panelIdx)
}

// runClusterAction runs a confirmed action off the update goroutine.
func (a *App) runClusterAction(action models.ClusterAction, panelIdx int) tea.Cmd {
	return a.withBusy("Apply", 300*time.Millisecond, func() tea.Msg {
		ctx, cancel := context.WithTimeout(a.ctx, requestTimeout)
		defer cancel()
		res, err := action.Run(ctx)
		return objectActionDoneMsg{result: res, panelIdx: panelIdx, err: err}
	})
}

func (a *App) handleObjectActionDone(msg objectActionDoneMsg) tea.Cmd {
	if msg.err != nil {
		return a.objectActionError("Failed: " + msg.err.Error())
	}
	var cmds []tea.Cmd
	if msg.result.Message != "" {
		cmds = append(cmds, a.ShowInfo(msg.result.Message, 5*time.Second))
	}
	if ref := msg.result.Created; ref != nil {
		cmds = append(cmds, a.showCreatedObject(1-msg.panelIdx, *ref))
	}
	return tea.Batch(cmds...)
}

func (a *App) objectActionError(text string) tea.Cmd {
	if a.toastLogger != nil {
		return a.toastLogger.Errorf("%s", text)
	}
	return a.ShowToast(text, 5*time.Second)
}

// showCreatedObject opens the list of ref's resource in panel idx and
// selects ref. A panel that shows a viewer or a command is left alone.
func (a *App) showCreatedObject(idx int, ref models.ObjectRef) tea.Cmd {
	return a.selectCreatedRow(selectCreatedMsg{panelIdx: idx, ref: ref})
}

// selectCreatedRow opens the object list and selects the row until both
// work, the attempts run out, or the user has left the folder.
func (a *App) selectCreatedRow(msg selectCreatedMsg) tea.Cmd {
	panel := a.panelByIndex(msg.panelIdx)
	if panel == nil || panel.Mode() != PanelModeList || a.navigatorFor(msg.panelIdx) == nil {
		return nil
	}
	last := msg.attempt+1 >= createdSelectAttempts
	if msg.path == "" {
		ref := msg.ref
		namespaced := ref.Namespace != ""
		res := resolvedResource{
			name:       ref.GVR.Resource,
			gvr:        ref.GVR,
			namespaced: namespaced,
			rowID:      resourceRowID(ref.Namespace, ref.GVR, namespaced),
			namespace:  ref.Namespace,
		}
		if err := a.goToResourceFolderIn(msg.panelIdx, res); err != nil {
			if last {
				return a.objectActionError(fmt.Sprintf("Show %s %s: %v", ref.GVR.Resource, ref.Name, err))
			}
			return a.retrySelectCreated(msg)
		}
		msg.path = panel.GetCurrentPath()
		a.invalidateView("show created object")
	} else if panel.GetCurrentPath() != msg.path {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	panel.SelectByRowID(ctx, msg.ref.Name)
	selected := panel.currentSelectionID(ctx) == msg.ref.Name
	cancel()
	if selected {
		a.invalidateView("select created object")
		return nil
	}
	if last {
		return nil
	}
	return a.retrySelectCreated(msg)
}

func (a *App) retrySelectCreated(msg selectCreatedMsg) tea.Cmd {
	msg.attempt++
	return tea.Tick(createdSelectRetry, func(time.Time) tea.Msg { return msg })
}
