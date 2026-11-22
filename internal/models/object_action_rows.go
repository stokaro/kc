package models

import (
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"

	table "github.com/stokaro/kc/internal/table"
)

// MessageRow is a row whose Enter sends EnterMessage to the UI instead of
// opening a folder. Packages outside models use it for rows whose work
// needs the UI, such as built-in object actions.
type MessageRow interface {
	Item
	EnterMessage() any
}

// ObjectRowsFunc returns extra rows shown when entering one object, below
// its child resource types. It runs while the folder populates and must
// not call the API server.
type ObjectRowsFunc func(deps Deps, namespace, name string, path []string) []table.Row

var (
	objectRowsMu sync.RWMutex
	objectRows   = make(map[schema.GroupVersionResource][]ObjectRowsFunc)
)

// RegisterObjectRows adds rows to the folder shown when entering an object
// of gvr.
func RegisterObjectRows(gvr schema.GroupVersionResource, fn ObjectRowsFunc) {
	objectRowsMu.Lock()
	objectRows[gvr] = append(objectRows[gvr], fn)
	objectRowsMu.Unlock()
}

func extraObjectRows(deps Deps, gvr schema.GroupVersionResource, namespace, name string, path []string) []table.Row {
	objectRowsMu.RLock()
	fns := append([]ObjectRowsFunc(nil), objectRows[gvr]...)
	objectRowsMu.RUnlock()
	var rows []table.Row
	for _, fn := range fns {
		rows = append(rows, fn(deps, namespace, name, path)...)
	}
	return rows
}

// KubectlCommand formats a kubectl command line for deps' context, quoting
// arguments and adding --context when plain kubectl uses another context.
func KubectlCommand(deps Deps, args ...string) string {
	return kubectlCommand(deps, args...)
}
