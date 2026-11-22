package ui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/sttts/kc/internal/access"
	"github.com/sttts/kc/pkg/appconfig"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// ReadOnlyOverride is what the command line says about read-only mode.
type ReadOnlyOverride int

const (
	// ReadOnlyFromConfig leaves the decision to the readOnly config.
	ReadOnlyFromConfig ReadOnlyOverride = iota
	// ReadOnlyForced (--readonly) makes every context read-only.
	ReadOnlyForced
	// ReadOnlyWriteForced (--write) ignores the readOnly config.
	ReadOnlyWriteForced
)

// readOnlyToastTTL is how long a read-only refusal stays on screen.
const readOnlyToastTTL = 3 * time.Second

// resolveReadOnly decides read-only mode for a context. A flag wins over the
// config; without one, readOnly.enabled or a matching readOnly.contexts
// pattern makes the context read-only.
func resolveReadOnly(override ReadOnlyOverride, cfg appconfig.ReadOnlyConfig, context string) bool {
	switch override {
	case ReadOnlyForced:
		return true
	case ReadOnlyWriteForced:
		return false
	default:
		return cfg.For(context)
	}
}

// readOnly reports whether the current context is in read-only mode.
func (a *App) readOnly() bool {
	if a == nil {
		return false
	}
	return a.readOnlyFor(a.currentContextName())
}

// readOnlyFor reports whether the named context is in read-only mode.
func (a *App) readOnlyFor(context string) bool {
	var cfg appconfig.ReadOnlyConfig
	if a.cfg != nil {
		cfg = a.cfg.ReadOnly
	}
	return resolveReadOnly(a.readOnlyOverride, cfg, context)
}

func (a *App) currentContextName() string {
	if a.currentCtx == nil {
		return ""
	}
	return a.currentCtx.Name
}

// refuseReadOnly returns a command that tells the user read-only mode
// refuses action, or nil when the policy permits the checks. Every mutating
// handler calls it first, so a key that slips past the function bar still
// changes nothing.
func (a *App) refuseReadOnly(action string, checks ...access.Check) tea.Cmd {
	if a.accessPolicy().Permits(nil, checks...) {
		return nil
	}
	return a.readOnlyRefusal(action)
}

// readOnlyRefusal logs and shows the refusal of action.
func (a *App) readOnlyRefusal(action string) tea.Cmd {
	ctrllog.FromContext(a.ctx).WithName("readOnly").Info("refused in read-only mode", "action", action, "context", a.currentContextName())
	return a.ShowToast(access.ReadOnlyRefusal(action), readOnlyToastTTL)
}

// refuseCommand returns a refusal when read-only mode does not let cmd run.
func (a *App) refuseCommand(cmd appconfig.CommandConfig) tea.Cmd {
	if a.accessPolicy().PermitsCommand(cmd.Dangerous) {
		return nil
	}
	return a.readOnlyRefusal(fmt.Sprintf("command %q", cmd.Name))
}

// mutatingFunctionKey reports whether a function bar key changes the cluster.
func mutatingFunctionKey(id functionKeyID) bool {
	switch id {
	case functionKeyEdit, functionKeyNamespace, functionKeyDelete:
		return true
	}
	return false
}
