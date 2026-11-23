package ui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/help"
	"github.com/stokaro/kc/internal/investigation"
	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/promhistory"
	"github.com/stokaro/kc/internal/ui/dashboard"
	"github.com/stokaro/kc/internal/ui/menubar"
	infowidget "github.com/stokaro/kc/internal/ui/panelcontent/info"
	"github.com/stokaro/kc/pkg/appconfig"
)

// dashboardKey opens the dashboard after C-x (C-x g).
const dashboardKey = "g"

// dashboardCacheTTL is how long a Prometheus result serves an identical
// read of another chart or a reopened dashboard.
const dashboardCacheTTL = 10 * time.Second

var dashboardPodsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

// dashboardState is the open dashboard and what it reads from. The App
// keeps the model after closing, so the next C-x g restores the views,
// the time and the session history.
type dashboardState struct {
	model *dashboard.Model
	open  bool
	// engine serves the Metrics API numbers of scope; key names the
	// cluster connection it belongs to.
	engine *gatedInfo
	scope  string
	key    kccluster.Key
	// window is the investigation window as the dashboard last saw it, to
	// tell when w changed it.
	window investigation.Window
}

func (a *App) dashboardMenuItem() menubar.Item {
	return menubar.Item{Label: "&Dashboard", Shortcut: "C-x g", Run: a.showDashboard}
}

// dashboardOpen reports whether the dashboard owns the screen.
func (a *App) dashboardOpen() bool { return a.dash != nil && a.dash.open }

// showDashboard opens the dashboard of the active panel: its namespace
// scope, narrowed to the node, workload or pod under the cursor. It
// restores the last dashboard when that was of the same context, scope
// and target.
func (a *App) showDashboard() tea.Cmd {
	if a.cl == nil {
		return a.ShowToast("Dashboard: not connected to a cluster", linkToastTTL)
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	scope := a.overviewNamespace(a.activePanel)
	return a.openDashboard(scope, a.dashboardTarget(ctx, scope))
}

// dashboardTarget is the dashboard target of the row under the active
// panel's cursor, else of scope.
func (a *App) dashboardTarget(ctx context.Context, scope string) dashboard.Target {
	t := dashboard.Target{Kind: dashboard.KindScope, Namespace: scope}
	p := a.panelByIndex(a.activePanel)
	if p == nil {
		return t
	}
	item, ok := p.SelectedNavItem(ctx)
	if !ok {
		return t
	}
	obj, ok := item.(models.ObjectItem)
	if !ok {
		return t
	}
	switch gvr := obj.GVR(); {
	case gvr == infoNodesGVR:
		return dashboard.Target{Kind: dashboard.KindNode, Namespace: scope, Name: obj.Name()}
	case gvr == dashboardPodsGVR && obj.Namespace() != "":
		return dashboard.Target{Kind: dashboard.KindPod, Namespace: obj.Namespace(), Name: obj.Name()}
	case models.IsInfoWorkload(gvr) && obj.Namespace() != "":
		return dashboard.Target{Kind: dashboard.KindWorkload, Namespace: obj.Namespace(), Name: obj.Name(), Workload: gvr}
	}
	return t
}

// openDashboard shows the dashboard of target in scope.
func (a *App) openDashboard(scope string, target dashboard.Target) tea.Cmd {
	key := a.currentClusterKey()
	if a.dash != nil && (a.dash.key != key || a.dash.scope != scope) {
		a.dash.engine.Close()
		a.dash.engine = nil
	}
	if a.dash == nil {
		a.dash = &dashboardState{}
	}
	d := a.dash
	if d.engine == nil {
		cl, cfg, root := a.cl, a.ensurePanelConfig(a.panelByIndex(a.activePanel)), a.ctx
		d.engine = newGatedInfo(root, cl.CanListWatchAllNamespaces, cl.CanListWatchInNamespace, scope, func() infowidget.Summarizer {
			return models.NewOverviewInfo(a.makeDeps(cl, cfg, key), scope)
		})
		d.scope, d.key = scope, key
	}
	src := a.dashboardSources(key, d.engine, scope)
	switch {
	case d.model == nil:
		d.model = dashboard.New(src, target)
	case d.restores(src, target):
		d.model = d.model.Resume(src, target)
	default:
		// Another target: start there, keep the session history.
		d.model = d.model.Resume(src, target).Retarget(target)
	}
	d.open = true
	a.dash.window = a.window
	d.model.Update(dashboardSize(a.width, a.height))
	ctrllog.FromContext(a.ctx).WithName("dashboard").Info("dashboard opened", "context", key.ContextName, "scope", scope, "target", target.Key())
	a.invalidateView("dashboard open")
	return d.model.Start()
}

// restores reports whether reopening for target brings the last
// dashboard back: C-x g on the same row it was opened from.
func (d *dashboardState) restores(src dashboard.Sources, target dashboard.Target) bool {
	return d.model.Root() == target && d.model.Context() == src.Context
}

// dashboardSources reads the overview engine and, when metricsHistory
// names a backend for the context, Prometheus through the API server's
// service proxy.
func (a *App) dashboardSources(key kccluster.Key, engine *gatedInfo, scope string) dashboard.Sources {
	src := dashboard.Sources{
		Context:   key.ContextName,
		Scope:     scope,
		Amounts:   engine.Amounts,
		Now:       time.Now,
		Loc:       time.Local,
		Window:    func() investigation.Window { return a.window },
		SetWindow: func(w investigation.Window) { a.window = w },
	}
	if a.cfg == nil {
		return src
	}
	backend, ok := a.cfg.MetricsHistory.BackendFor(key.ContextName)
	if !ok {
		return src
	}
	if a.promShared == nil {
		a.promShared = promhistory.NewShared(promhistory.DefaultConcurrency, promhistory.DefaultCacheEntries)
	}
	src.Backend = backend.Namespace + "/" + backend.Service
	src.Usage = dashboardUsage(a.ctx, a.cl.GetConfig, key, backend, a.cfg.MetricsHistory, a.promShared)
	return src
}

// dashboardUsageFunc is dashboard.Sources.Usage.
type dashboardUsageFunc = func(promhistory.Scope, promhistory.Range) (func() (promhistory.Usage, error), func())

// dashboardUsage builds a Prometheus read per chart range. Identical reads
// of the same cluster connection share one request; the key carries the
// impersonation, which changes what the proxy lets through.
func dashboardUsage(root context.Context, restConfig func() *rest.Config, key kccluster.Key, backend appconfig.MetricsBackendConfig, mh appconfig.MetricsHistoryConfig, shared *promhistory.Shared) dashboardUsageFunc {
	timeout := mh.EffectiveTimeout()
	return func(scope promhistory.Scope, r promhistory.Range) (func() (promhistory.Usage, error), func()) {
		ctx, cancel := context.WithTimeout(root, timeout)
		return func() (promhistory.Usage, error) {
			c, err := promhistory.NewServiceProxyClient(restConfig(), backend)
			if err != nil {
				return promhistory.Usage{}, err
			}
			v, err := shared.Do(ctx, fmt.Sprintf("%+v|", key)+c.UsageKey(scope, r), dashboardCacheTTL, func(ctx context.Context) (any, error) {
				return c.Usage(ctx, scope, r)
			})
			if err != nil {
				return promhistory.Usage{}, err
			}
			u, _ := v.(promhistory.Usage)
			return u, nil
		}, cancel
	}
}

// dashboardSize is the size the dashboard draws at: the screen less the
// F-key bar, which the App draws under it.
func dashboardSize(w, h int) tea.WindowSizeMsg {
	return tea.WindowSizeMsg{Width: w, Height: max(0, h-1)}
}

// updateDashboard routes a message while the dashboard is open. A help
// window over it takes the keys, and so does its open menu bar. handled
// is false for messages the rest of kc also needs (window size, other
// async results).
func (a *App) updateDashboard(msg tea.Msg) (tea.Cmd, bool) {
	d := a.dash
	if dashboard.IsMsg(msg) {
		cmd, _ := d.model.Update(msg)
		a.invalidateView("dashboard")
		return cmd, true
	}
	if a.modalManager != nil && a.modalManager.IsModalVisible() {
		return nil, false
	}
	if a.menuBar.IsOpen() {
		switch msg := msg.(type) {
		case tea.KeyPressMsg:
			return a.handleMenuBarKey(msg), true
		case tea.MouseMsg:
			return a.handleMenuBarMouse(msg), true
		}
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return a.dashboardKey(msg.String()), true
	case tea.MouseClickMsg:
		m := msg.Mouse()
		switch {
		case m.Y == a.height-1:
			return a.dashboardFunctionKeyClick(m.X), true
		case m.Y == 0 && m.Button == tea.MouseLeft:
			if i := menubar.TitleIndex(d.model.Menus(nil), m.X); i >= 0 {
				a.menuBar.OpenAt(d.model.Menus(a.dashboardKey), a.width, i)
				a.invalidateView("dashboard menu")
			}
			return nil, true
		}
	case tea.WindowSizeMsg:
		d.model.Update(dashboardSize(msg.Width, msg.Height))
		return nil, false
	case tea.MouseMsg:
	default:
		return nil, false
	}
	return a.dashboardResult(d.model.Update(msg)), true
}

// dashboardKey runs a key of the dashboard, typed or chosen from its menu
// bar or F-key bar. F1 shows help, F2 drops down the Range menu, F9 opens
// the menu bar; the dashboard runs the rest.
func (a *App) dashboardKey(key string) tea.Cmd {
	d := a.dash
	if d == nil || !d.open {
		return nil
	}
	switch key {
	case "f1":
		return a.showHelp(help.Dashboard)
	case "f2":
		a.menuBar.OpenAt(d.model.Menus(a.dashboardKey), a.width, dashboard.RangeMenu)
		a.invalidateView("dashboard range menu")
		return nil
	case "f9":
		a.menuBar.Open(d.model.Menus(a.dashboardKey), a.width)
		a.invalidateView("dashboard menu")
		return nil
	}
	return a.dashboardResult(d.model.Key(key))
}

// dashboardFunctionKeyClick runs the F-key under column x of the bar.
func (a *App) dashboardFunctionKeyClick(x int) tea.Cmd {
	keys := a.dash.model.FunctionKeys()
	i := functionKeyAt(len(keys), a.width, x)
	if i < 0 || !keys[i].Enabled || keys[i].Label == "" {
		return nil
	}
	return a.dashboardKey(fmt.Sprintf("f%d", i+1))
}

// dashboardResult carries out what an update of the dashboard asks for.
func (a *App) dashboardResult(cmd tea.Cmd, act dashboard.Action) tea.Cmd {
	a.invalidateView("dashboard")
	cmds := []tea.Cmd{cmd}
	if a.window != a.dash.window {
		// w made the dashboard's range the investigation window.
		a.dash.window = a.window
		cmds = append(cmds, a.applyWindow()...)
		cmds = append(cmds, a.ShowInfo("Investigation window "+a.window.Label(time.Local)+" (Logs, Info and the dashboard)", 3*time.Second))
	}
	switch {
	case act.Note != "":
		cmds = append(cmds, a.ShowToast(act.Note, linkToastTTL))
	case act.Logs != nil:
		a.closeDashboard()
		cmds = append(cmds, a.openDashboardLogs(*act.Logs, act.Window))
	case act.Open != nil:
		a.closeDashboard()
		cmds = append(cmds, a.openDashboardObject(*act.Open), a.revisionNote(act.Event))
	case act.Close:
		a.closeDashboard()
	}
	return tea.Batch(cmds...)
}

// dashboardKeys are the F-key bar under the dashboard.
func (a *App) dashboardKeys() []functionKey {
	fk := a.dash.model.FunctionKeys()
	keys := make([]functionKey, len(fk))
	for i, k := range fk {
		keys[i] = functionKey{Key: fmt.Sprintf("%2d", i+1), Label: k.Label, Enabled: k.Enabled}
	}
	return keys
}

// openDashboardLogs makes w the investigation window and shows the logs
// of ref: ref selected in the other panel, the panel the dashboard was
// opened from in Logs mode, following it.
func (a *App) openDashboardLogs(ref models.ObjectRef, w investigation.Window) tea.Cmd {
	from := a.activePanel
	a.window = w
	cmds := a.applyWindow()
	cmds = append(cmds, a.openObjectRef(ref, ""))
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	if p := a.panelByIndex(from); p != nil && p.Mode() != PanelModeLogs {
		cmds = append(cmds, a.setPanelMode(ctx, p, PanelModeLogs, "dashboard logs"))
	}
	cmds = append(cmds, a.ShowInfo("Logs of "+ref.Name+", investigation window "+w.Label(time.Local), 3*time.Second))
	return tea.Batch(cmds...)
}

// revisionNote says where the revisions of the object a rollout Event is
// about are. A Deployment's are in its /rollout, built from its
// ReplicaSets (ADR 0264). For other kinds kc has them only while the
// object's /timeline records, so a rollout before that has none.
func (a *App) revisionNote(e *models.UsageEvent) tea.Cmd {
	if e == nil || !dashboard.IsRollout(*e) || e.Object.GVR.Empty() {
		return nil
	}
	o := e.Object
	if o.GVR.GroupResource() == (schema.GroupResource{Group: "apps", Resource: "deployments"}) {
		return a.ShowInfo("Deployment "+o.Name+": its revisions are in /rollout, from its ReplicaSets", linkToastTTL)
	}
	since, running, ok := models.RecordedRevisions(a.currentClusterKey(), o.GVR, o.Namespace, o.Name)
	switch {
	case ok && !since.After(e.Last):
		state := "recorded"
		if running {
			state = "recording"
		}
		return a.ShowInfo(fmt.Sprintf("%s %s: revisions %s since %s; open its /timeline to compare them", e.Kind, o.Name, state, since.In(time.Local).Format("15:04:05")), linkToastTTL)
	case ok:
		return a.ShowToast(fmt.Sprintf("%s %s: revision not recorded; the /timeline recording started at %s, after this Event", e.Kind, o.Name, since.In(time.Local).Format("15:04:05")), linkToastTTL)
	}
	return a.ShowToast(e.Kind+" "+o.Name+": revision not recorded; kc records revisions only while the object's /timeline runs. Showing the object and its Event.", linkToastTTL)
}

// openDashboardObject shows ref in Commander: in the other panel, which
// takes the focus.
func (a *App) openDashboardObject(ref models.ObjectRef) tea.Cmd {
	idx := 1 - a.activePanel
	cmd := a.openObjectRef(ref, "")
	a.setActivePanel(idx, "dashboard open object")
	return cmd
}

// closeDashboard returns to the panels, which the dashboard never
// changed. The engine stops; the model stays for C-x g.
func (a *App) closeDashboard() {
	d := a.dash
	if d == nil || !d.open {
		return
	}
	d.open = false
	d.model.Stop()
	d.engine.Close()
	d.engine = nil
	a.invalidateView("dashboard close")
}

func (a *App) renderDashboard() tea.View {
	bottom := renderFunctionKeySlots(a.dashboardKeys(), a.width)
	if a.toastActive && !a.toastOnCommandLine() {
		bottom = renderToast(a.toastText, a.toastInfo, a.width)
	}
	content := a.dash.model.View() + "\n" + bottom
	content = a.overlayMenuBar(content)
	if a.modalManager != nil && a.modalManager.IsModalVisible() {
		content = viewString(a.modalManager.View())
	}
	v := viewWithCursor(content, nil)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// applyStartupIntentDashboard opens the dashboard of the namespace scope
// kc dashboard names (all namespaces with -A).
func (a *App) applyStartupIntentDashboard() tea.Cmd {
	ns := a.startupIntentNamespace()
	return a.openDashboard(ns, dashboard.Target{Kind: dashboard.KindScope, Namespace: ns})
}
