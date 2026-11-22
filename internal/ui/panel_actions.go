package ui

import (
	"context"
	"maps"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/access"
	models "github.com/stokaro/kc/internal/models"
)

// PanelAction identifies high-level actions exposed by a panel.
type PanelAction int

const (
	PanelActionHelp PanelAction = iota
	PanelActionOptions
	PanelActionView
	PanelActionEdit
	PanelActionCopy
	PanelActionCreateNamespace
	PanelActionDelete
	PanelActionMenu
)

// PanelActionHandler executes an action for a panel and may return a command.
type PanelActionHandler func(*Panel) tea.Cmd

// PanelActionHandlers enumerates handlers for supported actions.
type PanelActionHandlers map[PanelAction]PanelActionHandler

// PanelEnvironment captures app-level allowances that affect panel capabilities.
type PanelEnvironment struct {
	AllowEditObjects      bool
	AllowDeleteObjects    bool
	AllowCreateNamespaces bool
	// Permit reports whether an action needing these access checks stays
	// enabled (RBAC answers and the read-only preference). Nil permits all.
	Permit func(checks ...access.Check) bool
	// ReadOnly is set when the current context is read-only. A mutating key
	// pressed then still reaches its handler, which refuses it with a
	// message instead of doing nothing silently.
	ReadOnly bool
}

func (e PanelEnvironment) permits(checks ...access.Check) bool {
	return e.Permit == nil || e.Permit(checks...)
}

// PanelEnvironmentSupplier resolves the current environment prior to computing capabilities.
type PanelEnvironmentSupplier func() PanelEnvironment

// PanelCapabilities describes which actions are currently enabled.
type PanelCapabilities struct {
	CanView        bool
	CanCopy        bool
	CanEdit        bool
	CanDelete      bool
	CanCreateNS    bool
	HasOptions     bool
	HasContextMenu bool
	HasHelp        bool
}

// SetActionHandlers installs the action handler map for the panel.
func (p *Panel) SetActionHandlers(h PanelActionHandlers) {
	if h == nil {
		p.actionHandlers = nil
		return
	}
	// Copy to avoid external mutation.
	p.actionHandlers = make(PanelActionHandlers, len(h))
	maps.Copy(p.actionHandlers, h)
}

// SetEnvironmentSupplier wires a supplier for environment-level capability hints.
func (p *Panel) SetEnvironmentSupplier(s PanelEnvironmentSupplier) { p.envSupplier = s }

// SetCommandExecSupplier sets where custom commands started in this panel
// get their cluster and identity.
func (p *Panel) SetCommandExecSupplier(s CommandExecSupplier) { p.commandExec = s }

// InvokeAction runs the handler for the requested action when available.
func (p *Panel) InvokeAction(action PanelAction) tea.Cmd {
	if p == nil {
		return nil
	}
	if handler, ok := p.actionHandlers[action]; ok && handler != nil {
		return handler(p)
	}
	return nil
}

// Capabilities reports the current action capabilities for the panel.
//
//nolint:gocognit,gocyclo,nestif // one capability check per item interface
func (p *Panel) Capabilities(ctx context.Context) PanelCapabilities {
	var caps PanelCapabilities
	if p == nil {
		return caps
	}
	env := p.environment()
	caps.HasHelp = p.actionHandlers[PanelActionHelp] != nil
	caps.HasOptions = p.actionHandlers[PanelActionOptions] != nil
	caps.HasContextMenu = p.actionHandlers[PanelActionMenu] != nil

	item, ok := p.SelectedNavItem(ctx)
	if ok && item != nil {
		if _, isBack := item.(models.Back); !isBack {
			if isViewableItem(item) {
				caps.CanView = true
				caps.CanCopy = true
				if logs, ok := item.(models.LogsProvider); ok {
					spec := logs.LogsSpec()
					if !env.permits(access.LogsChecks(spec.Namespace, spec.Pod)...) {
						caps.CanView = false
						caps.CanCopy = false
					}
				}
			}
			if _, ok := item.(models.Stoppable); ok {
				caps.CanDelete = true
			}
			if _, ok := item.(models.FSEntryItem); ok {
				// Directories copy too, to a local panel.
				caps.CanCopy = true
			}
			if _, ok := item.(models.LocalEntryItem); ok {
				// A local row copies into a container folder in the other
				// panel; the handler explains when there is none.
				caps.CanCopy = true
			}
			if obj, ok := item.(models.ObjectItem); ok {
				target := objectAccessTarget(obj)
				if env.AllowEditObjects && obj.SupportsVerb("update") && env.permits(access.EditChecks(target)...) {
					caps.CanEdit = true
				}
				if env.AllowDeleteObjects && obj.SupportsVerb("delete") && env.permits(access.DeleteChecks(target)...) {
					caps.CanDelete = true
				}
			}
		}
	}
	// Namespace creation depends on both environment and location.
	if env.AllowCreateNamespaces && env.permits(access.CreateNamespaceChecks()...) {
		if strings.EqualFold(strings.TrimSpace(p.GetCurrentPath()), "/namespaces") {
			caps.CanCreateNS = true
		}
	}
	return caps
}

func (p *Panel) environment() PanelEnvironment {
	if p == nil || p.envSupplier == nil {
		return PanelEnvironment{}
	}
	return p.envSupplier()
}

func isViewableItem(item models.Item) bool {
	if item == nil {
		return false
	}
	if _, ok := item.(models.LogsProvider); ok {
		return true
	}
	if _, ok := item.(models.Viewable); ok {
		return true
	}
	type viewContentProvider interface {
		ViewContent() (string, string, string, string, string, error)
	}
	_, ok := item.(viewContentProvider)
	return ok
}

// actionAllowed evaluates whether the action is currently available.
func (p *Panel) actionAllowed(ctx context.Context, action PanelAction) bool {
	caps := p.Capabilities(ctx)
	switch action {
	case PanelActionHelp:
		return caps.HasHelp
	case PanelActionOptions:
		return caps.HasOptions
	case PanelActionView:
		return caps.CanView
	case PanelActionCopy:
		return caps.CanCopy
	case PanelActionEdit:
		return caps.CanEdit
	case PanelActionCreateNamespace:
		return caps.CanCreateNS
	case PanelActionDelete:
		return caps.CanDelete
	case PanelActionMenu:
		return caps.HasContextMenu
	default:
		return false
	}
}

func (p *Panel) invokeActionIfAllowed(ctx context.Context, action PanelAction) tea.Cmd {
	action = p.modeAction(action)
	if !p.actionAllowed(ctx, action) {
		if mutatingPanelAction(action) && p.environment().ReadOnly {
			// The handler refuses with a read-only message.
			return p.InvokeAction(action)
		}
		return nil
	}
	return p.InvokeAction(action)
}

// modeAction maps an action to the one the panel's mode gives its key. In
// Logs mode F2 opens the options, as in the fullscreen logs viewer; the
// panel shows no rows a user menu could act on.
func (p *Panel) modeAction(action PanelAction) PanelAction {
	if action == PanelActionMenu && p.mode == PanelModeLogs {
		return PanelActionOptions
	}
	return action
}

// mutatingPanelAction reports whether action changes the cluster.
func mutatingPanelAction(action PanelAction) bool {
	switch action {
	case PanelActionEdit, PanelActionCreateNamespace, PanelActionDelete:
		return true
	}
	return false
}
