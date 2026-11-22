package ui

import (
	"github.com/sttts/kc/internal/access"
	models "github.com/sttts/kc/internal/models"
)

// accessPolicy is the user's own restriction on actions: the read-only mode
// of the current context. Every gated action consults it.
func (a *App) accessPolicy() access.Policy {
	return access.Policy{ReadOnly: a.readOnly()}
}

// permitAccess gates panel actions. It never sends a request; today only
// the policy decides.
func (a *App) permitAccess(checks ...access.Check) bool {
	return a.accessPolicy().Permits(nil, checks...)
}

func objectAccessTarget(obj models.ObjectItem) access.Target {
	return access.Target{Resource: obj.GVR(), Namespace: obj.Namespace(), Name: obj.Name()}
}
