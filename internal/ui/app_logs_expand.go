package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/models"
	logswidget "github.com/stokaro/kc/internal/ui/panelcontent/logs"
)

// errSessionReopen is returned when an expanded panel session is asked to
// open its stream a second time. The stream belongs to the panel, which
// changes its options with F2.
var errSessionReopen = errors.New("this view follows the Logs panel; change the options in the panel with F2")

// expandPanelLogs shows the Logs panel's session in the full-screen logs
// viewer (docs/adr/0271-logs-session-workspace.md): the lines the panel
// keeps, then every line its stream reads. No stream is opened, so the
// view starts where the panel is, without a refetch, a gap or a repeat.
// Closing the viewer leaves the panel's stream, lines and position as
// they are.
func (a *App) expandPanelLogs(panel *Panel) tea.Cmd {
	w := panel.logsWidget()
	if w == nil {
		return nil
	}
	sess, err := w.Follow()
	if err != nil {
		return a.ShowToast(err.Error(), linkToastTTL)
	}
	open := sessionOpener(sess.Follower)
	title := fmt.Sprintf("logs:%s/%s/%s", sess.Spec.Namespace, sess.Spec.Pod, sess.Spec.Container)
	if sess.AllContainers {
		title = fmt.Sprintf("logs:%s/%s (all containers)", sess.Spec.Namespace, sess.Spec.Pod)
	}
	v := NewLogsViewer(title, sess.Spec, open, a.viewerTheme())
	v.SetLimits(a.logLimits())
	// The panel merges every container with a plain [pod/container]
	// prefix, which the viewer reads as the record's source.
	v.SetStyledLines(sess.AllContainers)
	v.appendRecords(sess.Lines)
	v.SetCommand(models.PanelLogsCommand(a.makeDeps(a.cl, a.ensurePanelConfig(panel), a.currentClusterKey()), sess.Spec, sess.AllContainers))
	return a.presentLogsViewer(v, title+" (Logs panel)")
}

// sessionOpener returns the opener of a viewer that reads f: the first open
// returns f, and a later one fails, because the options of the stream are
// the panel's.
func sessionOpener(f *logswidget.Follower) logStreamOpener {
	var once sync.Once
	return func(models.LogsSpec) (io.ReadCloser, context.CancelFunc, error) {
		var r io.ReadCloser
		once.Do(func() { r = f })
		if r == nil {
			return nil, nil, errSessionReopen
		}
		return r, func() { _ = f.Close() }, nil
	}
}
