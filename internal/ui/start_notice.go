package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/stokaro/kc/internal/kubeerr"
	listwidget "github.com/stokaro/kc/internal/ui/panelcontent/list"
)

// noKubeconfigError says that kc has no context to start in because no
// kubeconfig file it reads defines one. Its message names the files.
type noKubeconfigError struct{ msg string }

func (e *noKubeconfigError) Error() string { return e.msg }

// Hints of the notices about the start: kc has no connection, so a
// reread cannot help.
const (
	hintKubeconfig  = "kc reads $KUBECONFIG, else ~/.kube/config and the other files in ~/.kube"
	hintPickContext = "Context... (Left/Right menu) picks another"
)

// noContextsError explains why no kubeconfig gave kc a context: the files
// it read define none, or the files it looked for do not exist. It reads
// local state only.
func (a *App) noContextsError() error {
	loaded := ""
	if a.kubeMgr != nil {
		loaded = a.loadedKubeconfigPaths()
	}
	if env := strings.TrimSpace(os.Getenv("KUBECONFIG")); env != "" {
		if loaded == "" {
			return &noKubeconfigError{msg: "No kubeconfig: $KUBECONFIG=" + env + " not found"}
		}
		return &noKubeconfigError{msg: "No contexts in $KUBECONFIG=" + env}
	}
	if loaded != "" {
		return &noKubeconfigError{msg: "No contexts in " + loaded}
	}
	main := filepath.Join("~", ".kube", "config")
	if home, err := os.UserHomeDir(); err == nil {
		main = filepath.Join(home, ".kube", "config")
	}
	return &noKubeconfigError{msg: "No kubeconfig: " + main + " not found"}
}

// startNoticeFor is what the lists say when kc has no connection because
// of err: the missing kubeconfig, or why the start context could not be
// reached.
func startNoticeFor(err error, contextName string) listwidget.Notice {
	if nk, ok := errors.AsType[*noKubeconfigError](err); ok {
		return listwidget.Notice{Problem: nk.msg, Hint: hintKubeconfig}
	}
	n := listwidget.NoticeFor(err)
	if contextName == "" {
		// kc could not read its kubeconfig files; no retry key helps.
		n.Hint = hintKubeconfig
		return n
	}
	n.Problem = "Cannot connect to " + contextName + ": " + errorCause(err)
	n.Hint = hintPickContext
	return n
}

// showStartProblem keeps err, the reason kc has no connection, and shows
// it in both lists and in Info instead of empty panels. It runs on the UI
// goroutine and does no I/O.
func (a *App) showStartProblem(err error, contextName string) {
	if err == nil {
		return
	}
	a.startProblem = startNoticeFor(err, contextName)
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	for _, p := range []*Panel{a.leftPanel, a.rightPanel} {
		if p == nil {
			continue
		}
		if lw := p.listWidget(ctx); lw != nil {
			lw.SetNotice(a.startProblem)
		}
	}
	a.invalidateView("start problem")
}

// notConnectedHint is Info's text for a panel without a connection.
func (a *App) notConnectedHint() string {
	if a.startProblem.Problem == "" {
		return "Not connected to a cluster."
	}
	return "Not connected to a cluster. " + a.startProblem.Problem
}

// describeError is err in plain words for a line of its own: the cause,
// and the host instead of the request URL.
func describeError(err error) string { return kubeerr.Describe(err) }

// errorCause is describeError for text after a colon, in lower case.
func errorCause(err error) string { return kubeerr.Cause(err) }
