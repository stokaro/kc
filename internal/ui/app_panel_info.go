package ui

import (
	"context"

	models "github.com/sttts/kc/internal/models"
	infowidget "github.com/sttts/kc/internal/ui/panelcontent/info"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var infoNodesGVR = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}

func (a *App) newPanelInfoWidget(p *Panel) PanelWidget {
	return infowidget.New(p.manifestWidgetDeps(), infowidget.Options{
		Resolve: func(ctx context.Context) infowidget.Target { return a.infoTarget(ctx, p) },
	})
}

// infoTarget is what the Info mode of panel shows: the namespace scope of
// the other panel (the namespace of the list it shows, as M-i uses), and
// the node under its cursor when it is on a node. It runs on the UI
// goroutine and does no I/O; the engine behind Open starts on first read.
func (a *App) infoTarget(ctx context.Context, panel *Panel) infowidget.Target {
	idx, ok := a.panelIndexFor(panel)
	if !ok {
		return infowidget.Target{Hint: "No panel."}
	}
	other := 1 - idx
	if a.cl == nil {
		return infowidget.Target{Hint: "Not connected to a cluster."}
	}
	ns := a.overviewNamespace(other)
	key := a.currentClusterKey()
	label := key.ContextName + ": " + namespaceScopeLabel(ns)
	t := infowidget.Target{
		Key:   key.ContextName + "|" + key.KubeconfigPath + "|" + ns,
		Label: label,
	}
	if op := a.panelByIndex(other); op != nil {
		if item, ok := op.SelectedNavItem(ctx); ok {
			if obj, ok := item.(models.ObjectItem); ok && obj.GVR() == infoNodesGVR {
				t.Node = obj.Name()
			}
		}
	}
	cl, cfg := a.cl, a.ensurePanelConfig(a.panelByIndex(other))
	t.Open = func() infowidget.Summarizer {
		return models.NewOverviewInfo(a.makeDeps(cl, cfg, key), ns)
	}
	return t
}
