package ui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/openapi"
	"k8s.io/client-go/rest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/explain"
	models "github.com/stokaro/kc/internal/models"
)

// explainKey opens the field documentation of the selected object or
// resource type. M-e reads as "explain"; Esc e is the fallback where
// Option does not act as Meta.
const explainKey = "alt+e"

// explainLoadedMsg carries a schema folder built in the background back to
// the panel that asked for it.
type explainLoadedMsg struct {
	panel  int
	origin models.Folder // folder the request was made from
	selID  string
	folder *models.ExplainFolder
}

// explainLoader returns the app's schema cache, creating it on first use.
func (a *App) explainLoader() *explain.Loader {
	if a.explainDocs == nil {
		a.explainDocs = explain.NewLoader(explain.DefaultCacheSize)
	}
	return a.explainDocs
}

// explainSelection opens the schema tree for the active panel's row. The
// requests run in a command; the folder is pushed when they finish.
func (a *App) explainSelection() tea.Cmd {
	panel := a.activePanelRef()
	if panel == nil || a.showTerminal || panel.Mode() != PanelModeList {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	item, ok := panel.SelectedNavItem(ctx)
	if !ok {
		return nil
	}
	gvr, gvk, ok := explainTargetOf(item)
	if !ok {
		return a.ShowToast(models.ErrNoExplainTarget.Error(), 3*time.Second)
	}
	origin := panel.Folder()
	deps, ok := a.explainDeps(origin)
	if !ok {
		return a.ShowToast("Explain needs a cluster connection", 3*time.Second)
	}
	msg := explainLoadedMsg{panel: a.activePanel, origin: origin, selID: panel.currentSelectionID(ctx)}
	loader := a.explainLoader()
	root := a.ctx
	return a.withBusy("Loading schema", 300*time.Millisecond, func() tea.Msg {
		ctx, cancel := context.WithTimeout(root, requestTimeout)
		defer cancel()
		client, err := openapiClient(deps.Cl.GetConfig())
		if err == nil {
			msg.folder = loadExplainFolder(ctx, loader, deps, client, gvr, gvk)
		} else {
			msg.folder = models.NewExplainFolder(deps, models.ExplainSpec{GVR: gvr, GVK: gvk}, nil, err)
		}
		return msg
	})
}

// explainTargetOf returns the type a row names: a resource type row or an
// object row.
func explainTargetOf(item models.Item) (schema.GroupVersionResource, schema.GroupVersionKind, bool) {
	if t, ok := item.(models.ExplainTarget); ok {
		gvr, gvk := t.ExplainType()
		return gvr, gvk, gvr.Resource != ""
	}
	if obj, ok := item.(models.ObjectItem); ok {
		gvr := obj.GVR()
		return gvr, schema.GroupVersionKind{}, gvr.Resource != ""
	}
	return schema.GroupVersionResource{}, schema.GroupVersionKind{}, false
}

// explainDeps uses the folder's own cluster, so a panel browsing another
// context documents that context's types. A folder without Deps reads
// the active panel's connection.
func (a *App) explainDeps(folder models.Folder) (models.Deps, bool) {
	if f, ok := folder.(interface{ FolderDeps() models.Deps }); ok {
		if deps := f.FolderDeps(); deps.Cl != nil {
			return deps, true
		}
	}
	conn := a.activeConn()
	if conn.cluster() == nil {
		return models.Deps{}, false
	}
	return a.connDeps(conn, nil), true
}

// openapiClient talks to /openapi/v3 without discovery's memory cache; the
// Loader keeps documents and checks their hash on every open.
func openapiClient(cfg *rest.Config) (openapi.ClientWithContext, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(rest.CopyConfig(cfg))
	if err != nil {
		return nil, fmt.Errorf("creating the OpenAPI client: %w", err)
	}
	return openapi.NewClientWithContext(dc.RESTClient()), nil
}

// loadExplainFolder resolves the kind, loads the group-version document
// and builds the root folder. Failures and missing documentation become
// the folder's status row, so the user always gets a place to go back from.
func loadExplainFolder(ctx context.Context, loader *explain.Loader, deps models.Deps, client openapi.ClientWithContext, gvr schema.GroupVersionResource, gvk schema.GroupVersionKind) *models.ExplainFolder {
	log := ctrllog.FromContext(ctx).WithName("explain")
	if gvk.Kind == "" {
		kind, err := deps.Cl.RESTMapper().KindFor(gvr)
		if err != nil {
			return models.NewExplainFolder(deps, models.ExplainSpec{GVR: gvr, GVK: gvr.GroupVersion().WithKind("")}, nil, fmt.Errorf("finding the kind of %s: %w", gvr.String(), err))
		}
		gvk = kind
	}
	// The schema must match the version the panel lists, not the kind's
	// preferred version.
	gvk.Group, gvk.Version = gvr.Group, gvr.Version
	spec := models.ExplainSpec{GVR: gvr, GVK: gvk}
	doc, err := loader.Load(ctx, clusterCacheKey(deps), client, gvr.GroupVersion())
	if err != nil {
		log.Info("schema unavailable", "gvk", gvk.String(), "err", err.Error())
		return models.NewExplainFolder(deps, spec, nil, err)
	}
	node, err := doc.Root(gvk)
	return models.NewExplainFolder(deps, spec, node, err)
}

// clusterCacheKey identifies the API server a schema came from. Namespace
// and selector scopes share one server and one schema.
func clusterCacheKey(deps models.Deps) string {
	key := deps.ClusterKey
	key.Namespace = ""
	key.SelectorKey = ""
	host := ""
	if deps.Cl != nil && deps.Cl.GetConfig() != nil {
		host = deps.Cl.GetConfig().Host
	}
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s", host, key.KubeconfigPath, key.ContextName, key.ImpersonateUser, key.ImpersonateUID, key.ImpersonateGroupsKey)
}

// handleExplainLoaded pushes the schema folder onto the panel the request
// came from, unless the user has left that folder meanwhile.
func (a *App) handleExplainLoaded(msg explainLoadedMsg) tea.Cmd {
	panel := a.panelByIndex(msg.panel)
	if panel == nil || msg.folder == nil {
		return nil
	}
	if panel.Folder() != msg.origin {
		return a.ShowInfo("Schema loaded after the panel moved; press M-e again", 3*time.Second)
	}
	if a.activePanel != msg.panel {
		a.setActivePanel(msg.panel, "explain loaded")
	}
	a.handleFolderNav(false, msg.selID, msg.folder)
	a.invalidateView("explain opened")
	a.invalidateFunctionBar("explain opened")
	if err := msg.folder.Err(); err != nil && !explain.IsAbsent(err) {
		return a.ShowToast("Schema request failed: "+err.Error(), 5*time.Second)
	}
	return nil
}
