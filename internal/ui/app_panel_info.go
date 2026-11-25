package ui

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/investigation"
	models "github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/promhistory"
	infowidget "github.com/stokaro/kc/internal/ui/panelcontent/info"
	"github.com/stokaro/kc/pkg/appconfig"
)

var infoNodesGVR = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}

func (a *App) newPanelInfoWidget(p *Panel) PanelWidget {
	return infowidget.New(p.manifestWidgetDeps(), infowidget.Options{
		Resolve: func(ctx context.Context) infowidget.Target { return a.infoTarget(ctx, p) },
		Window:  func() (investigation.Window, *time.Location) { return a.window, time.Local },
		Images:  a.infoImages(p),
	})
}

// infoTarget is what the Info mode of panel shows: the namespace scope of
// the other panel (the namespace of the list it shows, as M-i uses), and
// the node or workload under its cursor when it is on one. A pinned panel
// keeps the scope and row it was pinned to. It runs on the UI
// goroutine and does no I/O; the engine behind Open starts on first read.
func (a *App) infoTarget(ctx context.Context, panel *Panel) infowidget.Target {
	idx, ok := a.panelIndexFor(panel)
	if !ok {
		return infowidget.Target{Hint: "No panel."}
	}
	other := 1 - idx
	// The other panel's connection, or the pin's: the panels may show two
	// contexts.
	conn := a.targetConn(idx)
	if conn.cluster() == nil {
		return infowidget.Target{Hint: "Not connected to a cluster."}
	}
	ns := a.overviewNamespace(other)
	pin := panel.pinned()
	if pin != nil {
		ns = pin.scope
	}
	key := conn.clusterKey()
	label := key.ContextName + ": " + namespaceScopeLabel(ns)
	t := infowidget.Target{
		// The connection's identity: a switch of the other panel, even to
		// a context of the same name, starts another engine.
		Key:   conn.connID() + "|" + key.ContextName + "|" + key.KubeconfigPath + "|" + ns,
		Label: label,
	}
	if pin != nil {
		narrowInfoTarget(&t, pin.item)
	} else if op := a.panelByIndex(other); op != nil {
		if item, ok := op.SelectedNavItem(ctx); ok {
			narrowInfoTarget(&t, item)
		}
	}
	cl, cfg, root := conn.cluster(), a.ensurePanelConfig(a.panelByIndex(other)), a.ctx
	deps := a.connDeps(conn, cfg)
	if t.Workload != nil && t.Finding == nil && a.cfg != nil {
		t.PromHistory = infoHistory(root, cl.GetConfig, key, a.cfg.MetricsHistory, *t.Workload, a.window)
	}
	t.Open = func() infowidget.Summarizer {
		return newGatedInfo(root, cl.CanListWatchAllNamespaces, cl.CanListWatchInNamespace, ns, func() infowidget.Summarizer {
			return models.NewOverviewInfo(deps, ns)
		})
	}
	return t
}

// infoHistory is the history source of workload wl when metricsHistory
// names a backend for the context, nil otherwise. The client is built in
// the read, off the UI goroutine. While an investigation window is set
// (C-x w) it reads that period instead of the configured window.
func infoHistory(root context.Context, restConfig func() *rest.Config, key kccluster.Key, mh appconfig.MetricsHistoryConfig, wl models.InfoWorkload, win investigation.Window) *infowidget.PromHistorySource {
	backend, ok := mh.BackendFor(key.ContextName)
	if !ok {
		return nil
	}
	kind, ok := promhistory.KindFor(wl.GVR.Group, wl.GVR.Resource)
	if !ok {
		return nil
	}
	window, timeout := mh.EffectiveWindow(), mh.EffectiveTimeout()
	target := promhistory.Target{Kind: kind, Namespace: wl.Namespace, Name: wl.Name}
	label := win.Label(time.Local)
	return &infowidget.PromHistorySource{
		// The cluster key carries the impersonation, which changes what
		// the proxy lets through; the window text changes what is read.
		Key:    fmt.Sprintf("%+v|%s/%s|%s|%s/%s|%s|%s", key, backend.Namespace, backend.Service, wl.GVR, wl.Namespace, wl.Name, window, win.Text(time.UTC)),
		Window: window,
		Label:  label,
		Start: func() (func() (promhistory.History, error), func()) {
			ctx, cancel := context.WithTimeout(root, timeout)
			opts := historyOptions(window, win, time.Now())
			return func() (promhistory.History, error) {
				c, err := promhistory.NewServiceProxyClient(restConfig(), backend)
				if err != nil {
					return promhistory.History{}, err
				}
				return c.Fetch(ctx, target, opts)
			}, cancel
		},
	}
}

// historyOptions reads the investigation window when it is set, else the
// configured window up to now.
func historyOptions(window time.Duration, win investigation.Window, now time.Time) promhistory.Options {
	start, end, ok := win.Bounds(now)
	if !ok {
		return promhistory.Options{Window: window}
	}
	if end.IsZero() {
		end = now
	}
	return promhistory.Options{Window: max(end.Sub(start), time.Minute), Now: end}
}

// narrowInfoTarget points t at the row under the other panel's cursor: an
// overview finding, a node or a workload. A finding describes itself from
// the overview's own report, the one its row shows (ADR 0173).
func narrowInfoTarget(t *infowidget.Target, item models.Item) {
	if fp, ok := item.(models.FindingPreviewer); ok {
		if p, ok := fp.FindingPreview(); ok {
			t.Finding = &p
		}
	}
	obj, ok := item.(models.ObjectItem)
	if !ok {
		return
	}
	switch gvr := obj.GVR(); {
	case gvr == infoNodesGVR:
		t.Node = obj.Name()
	case models.IsInfoWorkload(gvr) && obj.Namespace() != "":
		t.Workload = &models.InfoWorkload{GVR: gvr, Namespace: obj.Namespace(), Name: obj.Name()}
	}
}
