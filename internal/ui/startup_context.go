package ui

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/stokaro/kc/pkg/kubeconfig"
)

// The errors of a context kc cannot start in. Run returns them before the
// UI takes the terminal, as usage errors.
var (
	// ErrUnknownContext reports a --context name that no loaded kubeconfig
	// defines.
	ErrUnknownContext = errors.New("unknown context")
	// ErrUndefinedCurrentContext reports a current-context that names a
	// context its kubeconfig does not define.
	ErrUndefinedCurrentContext = errors.New("current-context is not defined")
	// ErrNoCurrentContext reports a kubeconfig that names no
	// current-context and defines more than one context.
	ErrNoCurrentContext = errors.New("no current-context")
)

// startContextError is one of the errors above with its message for the
// user, which names the context, the kubeconfig files and the contexts
// they define.
type startContextError struct {
	kind error
	msg  string
}

func (e *startContextError) Error() string { return e.msg }
func (e *startContextError) Unwrap() error { return e.kind }

// onlyContextNoticeTTL is how long the startup notice of
// onlyContextNotice stays.
const onlyContextNoticeTTL = 10 * time.Second

// onlyContextNotice is the startup notice when no kubeconfig file names a
// current-context and kc opened the only context there is.
func onlyContextNotice(c *kubeconfig.Context) string {
	return "No current-context set; opened the only context, " + c.Name
}

// startupContext returns the context called name. When several kubeconfig
// files define it, the first one wins, as when kubectl merges $KUBECONFIG.
func (a *App) startupContext(name string) (*kubeconfig.Context, error) {
	if c := a.kubeMgr.GetContextByName(name); c != nil {
		return c, nil
	}
	files := a.loadedKubeconfigPaths()
	if files == "" {
		return nil, &startContextError{kind: ErrUnknownContext, msg: fmt.Sprintf("unknown context %q (no kubeconfig loaded)", name)}
	}
	msg := fmt.Sprintf("unknown context %q in %s; %s", name, files, a.contextList())
	return nil, &startContextError{kind: ErrUnknownContext, msg: msg}
}

// selectCurrentContext returns the context the kubeconfig's
// current-context selects (see kubeconfig.Manager.CurrentContext). It
// fails with ErrUndefinedCurrentContext when that context is not defined,
// and with ErrNoCurrentContext when no file names a current-context and
// there are several contexts to choose from: kc does not pick one for the
// user. With exactly one context, it returns that one with only set, and
// the caller says so. With none, it returns nil and no error.
func (a *App) selectCurrentContext() (c *kubeconfig.Context, only bool, err error) {
	cur := a.kubeMgr.CurrentContext()
	switch {
	case cur.Context != nil:
		return cur.Context, false, nil
	case cur.Name != "":
		where := "is not defined"
		if other := a.kubeMgr.GetContextByName(cur.Name); other != nil && other.Kubeconfig != nil {
			// The files under ~/.kube stand alone, as for kubectl.
			where = "is not defined there, only in " + other.Kubeconfig.Path
		}
		msg := fmt.Sprintf("current-context %q in %s %s; %s", cur.Name, cur.File, where, a.pickContextHint())
		return nil, false, &startContextError{kind: ErrUndefinedCurrentContext, msg: msg}
	}
	cs := a.kubeMgr.GetContexts()
	switch len(cs) {
	case 0:
		return nil, false, nil
	case 1:
		return cs[0], true, nil
	}
	msg := fmt.Sprintf("no current-context is set in %s; %s", a.loadedKubeconfigPaths(), a.pickContextHint())
	return nil, false, &startContextError{kind: ErrNoCurrentContext, msg: msg}
}

// loadedKubeconfigPaths is the loaded kubeconfig files, in load order,
// joined with the path list separator.
func (a *App) loadedKubeconfigPaths() string {
	kcs := a.kubeMgr.GetKubeconfigs()
	paths := make([]string, 0, len(kcs))
	for _, kc := range kcs {
		paths = append(paths, kc.Path)
	}
	return strings.Join(paths, string(os.PathListSeparator))
}

// contextList names the loaded contexts, sorted: "contexts: a, b".
func (a *App) contextList() string {
	names := a.kubeMgr.ContextNames()
	if len(names) == 0 {
		return "the kubeconfig defines no contexts"
	}
	for i, n := range names {
		names[i] = quoteContextName(n)
	}
	return "contexts: " + strings.Join(names, ", ")
}

// pickContextHint is contextList with the flag that picks one.
func (a *App) pickContextHint() string {
	if len(a.kubeMgr.ContextNames()) == 0 {
		return a.contextList()
	}
	return a.contextList() + "; pick one with --context"
}

// quoteContextName quotes a context name that would not read as one item
// of a comma-separated list, or that holds a control character, so the
// message stays one line of plain text.
func quoteContextName(name string) string {
	if name == "" || strings.ContainsFunc(name, func(r rune) bool {
		return r == ',' || r == '"' || unicode.IsSpace(r) || !unicode.IsPrint(r)
	}) {
		return strconv.Quote(name)
	}
	return name
}
