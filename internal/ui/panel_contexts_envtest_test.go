package ui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/manifestdiff"
	"github.com/stokaro/kc/internal/models"
	navui "github.com/stokaro/kc/internal/navigation"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/pkg/appconfig"
	"github.com/stokaro/kc/pkg/describe"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

// twoClusters is an App on two isolated API servers. Context a-dev uses
// the package control plane, b-prod (read-only by the config) one of its
// own, a-mirror is a second context on the first server and missing names
// a cluster the kubeconfig does not define. Both servers have namespace ns
// with a ConfigMap web: same namespace, same name, another UID and data.
// kc starts with both panels in a-dev, as with --context a-dev.
type twoClusters struct {
	app                        *App
	ns                         string
	dev, prod, mirror, missing *kubeconfig.Context
	clientA, clientB           crclient.Client
	webA, webB                 *corev1.ConfigMap
}

func newTwoClusters(t *testing.T) *twoClusters {
	t.Helper()
	kcenvtest.Require(t, testCfg)
	cfgB := kcenvtest.Start(t)
	e := &twoClusters{ns: kctesting.UniqueName("shop")}

	kc := clientcmdapi.NewConfig()
	for name, cfg := range map[string]*rest.Config{"a": testCfg, "b": cfgB} {
		kc.Clusters[name] = &clientcmdapi.Cluster{Server: cfg.Host, CertificateAuthorityData: cfg.CAData}
		kc.AuthInfos[name] = &clientcmdapi.AuthInfo{ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData, Token: cfg.BearerToken}
	}
	for name, server := range map[string]string{"a-dev": "a", "a-mirror": "a", "b-prod": "b", "missing": "nowhere"} {
		kc.Contexts[name] = &clientcmdapi.Context{Cluster: server, AuthInfo: "a", Namespace: e.ns}
	}
	kc.Contexts["b-prod"].AuthInfo = "b"
	kc.CurrentContext = "a-dev"
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*kc, path); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	kctx := func(name string) *kubeconfig.Context {
		return &kubeconfig.Context{Name: name, Namespace: e.ns, Kubeconfig: &kubeconfig.Kubeconfig{Path: path, Context: name}}
	}
	e.dev, e.prod, e.mirror, e.missing = kctx("a-dev"), kctx("b-prod"), kctx("a-mirror"), kctx("missing")

	e.clientA, e.webA = seedShop(t, testCfg, e.ns, "served-by-a")
	e.clientB, e.webB = seedShop(t, cfgB, e.ns, "served-by-b")
	if e.webA.UID == e.webB.UID {
		t.Fatalf("the two servers gave web one UID %q", e.webA.UID)
	}

	app := NewApp()
	app.cancel()
	app.ctx, app.cancel = context.WithCancel(t.Context())
	t.Cleanup(app.cancel)
	app.width, app.height = 120, 30
	model, _ := app.Update(tea.WindowSizeMsg{Width: app.width, Height: app.height})
	app = model.(*App)
	app.cfg.ReadOnly = appconfig.ReadOnlyConfig{Contexts: []string{"*prod*"}}
	app.clPool = kccluster.NewPool(time.Minute, 0)
	app.clPool.Start()
	t.Cleanup(app.clPool.Stop)
	usePoolContext(t, app, e.dev)
	app.goToNamespace(e.ns)
	e.app = app
	return e
}

// seedShop creates namespace ns with ConfigMap web on the server of cfg.
func seedShop(t *testing.T, cfg *rest.Config, ns, served string) (crclient.Client, *corev1.ConfigMap) {
	t.Helper()
	c, err := crclient.New(cfg, crclient.Options{})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx := t.Context()
	nsObj := &corev1.Namespace{}
	nsObj.Name = ns
	if err := c.Create(ctx, nsObj); err != nil {
		t.Fatalf("create namespace %s: %v", ns, err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		_ = c.Delete(cleanupCtx, nsObj)
	})
	cm := &corev1.ConfigMap{Data: map[string]string{"served": served}}
	cm.Namespace, cm.Name = ns, "web"
	if err := c.Create(ctx, cm); err != nil {
		t.Fatalf("create configmap %s/web: %v", ns, err)
	}
	return c, cm
}

// switchResult starts a context switch of panels idxs to target and
// returns its result without applying it.
func switchResult(t *testing.T, app *App, idxs []int, target *kubeconfig.Context) contextSwitchedMsg {
	t.Helper()
	cmd := app.switchContexts(idxs, target)
	if cmd == nil {
		t.Fatalf("no switch of panels %v to %s", idxs, target.Name)
	}
	for _, msg := range flattenCmd(cmd) {
		if c, ok := msg.(tea.Cmd); ok {
			msg = c()
		}
		if m, ok := msg.(contextSwitchedMsg); ok {
			return m
		}
	}
	t.Fatalf("the switch to %s produced no contextSwitchedMsg", target.Name)
	return contextSwitchedMsg{}
}

// switchPanel switches panel idx to target as the context picker does.
func (e *twoClusters) switchPanel(t *testing.T, idx int, target *kubeconfig.Context) {
	t.Helper()
	msg := switchResult(t, e.app, []int{idx}, target)
	if msg.err != nil {
		t.Fatalf("switch the %s panel to %s: %v", panelName(idx), target.Name, msg.err)
	}
	e.app.Update(msg)
	if got := e.app.ownConn(idx).name(); got != target.Name {
		t.Fatalf("the %s panel is in %q after the switch, want %s", panelName(idx), got, target.Name)
	}
}

// openWeb opens the ConfigMaps of ns in panel idx and puts the cursor on
// web.
func (e *twoClusters) openWeb(t *testing.T, idx int) models.Item {
	t.Helper()
	ctx := t.Context()
	app := e.app
	nav := app.navigatorFor(idx)
	for nav.HasBack() {
		nav.Back()
	}
	typeID := resourceRowID(e.ns, gvrConfigMaps, true)
	kctesting.Eventually(t, 15*time.Second, 50*time.Millisecond, func() bool {
		_, err := navui.GoTo(ctx, nav, []navui.GoToStep{{SelectionID: typeID, Enter: true}})
		return err == nil
	})
	kctesting.Eventually(t, 15*time.Second, 50*time.Millisecond, func() bool {
		_, ok := nav.Current().ItemByID(ctx, "web")
		return ok
	})
	app.syncPanelWithNavigator(idx)
	panel := app.panelByIndex(idx)
	panel.SelectByRowID(ctx, "web")
	item, ok := panel.SelectedNavItem(ctx)
	if !ok {
		t.Fatalf("the %s panel has no row under the cursor", panelName(idx))
	}
	if obj, isObj := item.(models.ObjectItem); !isObj || obj.Name() != "web" {
		t.Fatalf("the %s panel's cursor is on %v, want web", panelName(idx), item)
	}
	return item
}

// assertInstance checks that item is the object with uid, read from c.
func assertInstance(t *testing.T, side string, item models.Item, uid string, c *panelConn) {
	t.Helper()
	inst, ok := item.(models.InstanceItem)
	if !ok {
		t.Fatalf("%s: row %T carries no instance", side, item)
	}
	origin := inst.Origin()
	if string(inst.UID()) != uid || origin.Cluster != c.cluster() || origin.Context != c.name() {
		t.Fatalf("%s: row is uid %s from %p/%q, want uid %s from %p/%q", side, inst.UID(), origin.Cluster, origin.Context, uid, c.cluster(), c.name())
	}
}

// Two panels browse two clusters at once, in the same namespace and on
// objects of the same name. Each panel reads its own cluster, and F8 deletes
// on the cluster of the panel's row, under that context's read-only policy
// and with the row's UID as the precondition.
func TestPanelContextsRouteReadsAndDeletes(t *testing.T) {
	e := newTwoClusters(t)
	app, ctx := e.app, t.Context()
	leftNav := app.navigatorFor(0)
	leftWeb := e.openWeb(t, 0)

	e.switchPanel(t, 1, e.prod)
	if app.navigatorFor(0) != leftNav {
		t.Fatal("switching the right panel replaced the left panel's navigator")
	}
	if item, ok := app.leftPanel.SelectedNavItem(ctx); !ok || item.(models.ObjectItem).Name() != "web" {
		t.Fatalf("switching the right panel moved the left cursor to %v", item)
	}
	if app.panelScope[1] != e.ns {
		t.Fatalf("right panel scope = %q, want the context's namespace %s", app.panelScope[1], e.ns)
	}
	rightWeb := e.openWeb(t, 1)

	assertInstance(t, "left", leftWeb, string(e.webA.UID), app.ownConn(0))
	assertInstance(t, "right", rightWeb, string(e.webB.UID), app.ownConn(1))
	if l, r := app.leftPanel.scope, app.rightPanel.scope; l.Context != "a-dev" || l.ReadOnly || r.Context != "b-prod" || !r.ReadOnly {
		t.Fatalf("frames name %+v and %+v, want a-dev writable and b-prod read-only", l, r)
	}

	// The protected context refuses F8 whichever panel is active.
	app.setActivePanel(0, "test")
	wantRefusal(t, app.deleteResourceForPanel(app.rightPanel), "Delete")
	if app.pendingDelete != nil {
		t.Fatal("a read-only context got a delete confirmation")
	}

	// The writable context deletes its own object only.
	app.setActivePanel(1, "test")
	if cmd := app.deleteResourceForPanel(app.leftPanel); isRefusal(cmd) {
		t.Fatal("the writable context refused F8")
	}
	target := app.pendingDelete
	if target == nil || target.cluster != app.ownConn(0).cluster() || target.uid != e.webA.UID || target.context != "a-dev" {
		t.Fatalf("delete target = %+v, want web %s on a-dev", target, e.webA.UID)
	}
	_, cmd := app.Update(DeleteConfirmMsg{Confirm: true, Close: true})
	if res, ok := findDeleteResult(cmd); !ok || res.err != nil {
		t.Fatalf("delete = %+v (found %v)", res, ok)
	}
	key := crclient.ObjectKeyFromObject(e.webA)
	if err := e.clientA.Get(ctx, key, &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
		t.Fatalf("web on a-dev after the delete: %v, want NotFound", err)
	}
	var b corev1.ConfigMap
	if err := e.clientB.Get(ctx, key, &b); err != nil || b.UID != e.webB.UID {
		t.Fatalf("web on b-prod = %s (%v), want the untouched %s", b.UID, err, e.webB.UID)
	}

	// With --write the protected context deletes too; the UID precondition
	// keeps the delete off an object that replaced the listed one.
	app.readOnlyOverride = ReadOnlyWriteForced
	if cmd := app.deleteResourceForPanel(app.rightPanel); isRefusal(cmd) {
		t.Fatal("--write still refused F8")
	}
	target = app.pendingDelete
	if target == nil || target.cluster != app.ownConn(1).cluster() || target.uid != e.webB.UID {
		t.Fatalf("delete target = %+v, want web %s on b-prod", target, e.webB.UID)
	}
	if err := e.clientB.Delete(ctx, &b); err != nil {
		t.Fatalf("delete web on b-prod: %v", err)
	}
	replacement := &corev1.ConfigMap{Data: map[string]string{"served": "replacement"}}
	replacement.Namespace, replacement.Name = e.ns, "web"
	if err := e.clientB.Create(ctx, replacement); err != nil {
		t.Fatalf("recreate web on b-prod: %v", err)
	}
	_, cmd = app.Update(DeleteConfirmMsg{Confirm: true, Close: true})
	if res, ok := findDeleteResult(cmd); !ok || !apierrors.IsConflict(res.err) {
		t.Fatalf("delete of a replaced object = %+v (found %v), want a Conflict", res, ok)
	}
	if err := e.clientB.Get(ctx, key, &b); err != nil || b.UID != replacement.UID {
		t.Fatalf("web on b-prod = %s (%v), want the replacement %s", b.UID, err, replacement.UID)
	}
}

// describeText describes the object of sel the way a Describe panel does.
func describeText(t *testing.T, app *App, sel string, obj models.ObjectItem) (string, error) {
	t.Helper()
	reads := app.describeFunc()(sel, describe.Target{GVR: obj.GVR(), Namespace: obj.Namespace(), Name: obj.Name()})
	res, err := reads.Describe()
	return res.Body, err
}

// A follower reads the context of the panel it follows; a pin keeps the
// pinned object's context when the followed panel switches, and holds its
// connection until it is unpinned.
func TestPanelContextFollowersAndPins(t *testing.T) {
	e := newTwoClusters(t)
	app, ctx := e.app, t.Context()
	e.openWeb(t, 0)
	e.switchPanel(t, 1, e.prod)
	rightNav := app.navigatorFor(1)
	devConn := app.ownConn(0)

	app.setPanelMode(ctx, app.rightPanel, PanelModeDescribe, "test")
	app.Update(PanelSelectionChangedMsg{Panel: app.leftPanel, Selection: app.leftPanel.CurrentSelection(ctx)})
	if app.targetConn(1) != devConn {
		t.Fatal("the Describe panel does not read the left panel's context")
	}
	if s := app.rightPanel.scope; s.Context != "a-dev" || s.ReadOnly {
		t.Fatalf("the follower's frame names %+v, want the followed a-dev", s)
	}
	sel := app.rightPanel.CurrentSelection(ctx)
	if sel.Source != devConn.connID() {
		t.Fatalf("follower selection source = %q, want %q", sel.Source, devConn.connID())
	}
	text, err := describeText(t, app, sel.Source, sel.Item.(models.ObjectItem))
	if err != nil || !strings.Contains(text, "served-by-a") || strings.Contains(text, "served-by-b") {
		t.Fatalf("describe read (%v):\n%s\nwant a-dev's web", err, text)
	}

	app.setActivePanel(1, "test")
	app.togglePin()
	pin := app.rightPanel.pinned()
	if pin == nil || pin.conn != devConn {
		t.Fatalf("pin = %+v, want one on a-dev's connection", pin)
	}
	e.switchPanel(t, 0, e.prod)
	if devConn.holders() != 1 {
		t.Fatalf("a-dev's connection has %d holders, want the pin's", devConn.holders())
	}
	if app.targetConn(1) != devConn || app.rightPanel.scope.Context != "a-dev" {
		t.Fatalf("the pinned panel moved to %q", app.rightPanel.scope.Context)
	}
	sel = app.rightPanel.CurrentSelection(ctx)
	if text, err := describeText(t, app, sel.Source, sel.Item.(models.ObjectItem)); err != nil || !strings.Contains(text, "served-by-a") {
		t.Fatalf("the pinned describe read (%v):\n%s\nwant a-dev's web", err, text)
	}

	// Unpinning follows the left panel again, now in b-prod, and lets
	// a-dev go: its lease and its discovery listener with it.
	app.togglePin()
	clA := devConn.cluster()
	if devConn.holders() != 0 || app.clPool.Leases(devConn.clusterKey()) != 0 || clA.DiscoveryListeners() != 0 {
		t.Fatalf("after unpinning: holders %d, leases %d, listeners %d; want none",
			devConn.holders(), app.clPool.Leases(devConn.clusterKey()), clA.DiscoveryListeners())
	}
	if app.targetConn(1) != app.ownConn(0) || app.rightPanel.scope.Context != "b-prod" {
		t.Fatalf("after unpinning the panel shows %q, want the followed b-prod", app.rightPanel.scope.Context)
	}
	// A read started for the released connection fails; it never reads
	// the cluster the panel shows now.
	if _, err := describeText(t, app, devConn.connID(), sel.Item.(models.ObjectItem)); !errors.Is(err, errContextChanged) {
		t.Fatalf("describe on a released connection: %v, want errContextChanged", err)
	}

	// Back in List mode the panel is where it was, in its own context.
	app.setPanelMode(ctx, app.rightPanel, PanelModeList, "test")
	if app.navigatorFor(1) != rightNav || app.targetConn(1) != app.ownConn(1) || app.rightPanel.scope.Context != "b-prod" {
		t.Fatalf("List mode: navigator kept %v, context %q", app.navigatorFor(1) == rightNav, app.rightPanel.scope.Context)
	}
}

// Every switch leases per panel; a result that a newer request of the
// panel replaced, or one that failed, changes nothing and keeps no lease.
func TestPanelContextSwitchLeases(t *testing.T) {
	e := newTwoClusters(t)
	app := e.app
	leases := func(c *kubeconfig.Context) int { return app.clPool.Leases(app.clusterKeyFor(c)) }
	clA := app.ownConn(0).cluster()
	if leases(e.dev) != 2 || clA.DiscoveryListeners() != 2 {
		t.Fatalf("at startup: %d leases and %d listeners on a-dev, want one each per panel", leases(e.dev), clA.DiscoveryListeners())
	}

	oldRight := app.ownConn(1)
	e.switchPanel(t, 1, e.prod)
	if leases(e.dev) != 1 || leases(e.prod) != 1 || oldRight.holders() != 0 || clA.DiscoveryListeners() != 1 {
		t.Fatalf("after the switch: a-dev %d leases, b-prod %d, old holders %d, a-dev listeners %d",
			leases(e.dev), leases(e.prod), oldRight.holders(), clA.DiscoveryListeners())
	}

	// Two quick switches: the newer one wins in either order of arrival.
	older := switchResult(t, app, []int{1}, e.dev)
	newer := switchResult(t, app, []int{1}, e.mirror)
	app.Update(newer)
	app.Update(older)
	if got := app.ownConn(1).name(); got != "a-mirror" {
		t.Fatalf("right panel in %q, want the newer a-mirror", got)
	}
	if leases(e.dev) != 1 || leases(e.mirror) != 1 || leases(e.prod) != 0 {
		t.Fatalf("leases a-dev %d, a-mirror %d, b-prod %d; want 1, 1, 0", leases(e.dev), leases(e.mirror), leases(e.prod))
	}

	// A request for the context the panel has replaces an older request.
	stale := switchResult(t, app, []int{1}, e.prod)
	if cmd := app.switchContexts([]int{1}, e.mirror); cmd != nil {
		t.Fatal("a switch to the panel's own context connected again")
	}
	app.Update(stale)
	if got := app.ownConn(1).name(); got != "a-mirror" || leases(e.prod) != 0 {
		t.Fatalf("right panel in %q with %d b-prod leases, want a-mirror and none", got, leases(e.prod))
	}

	// A failed switch keeps the panel and drops the jump waiting for it.
	before, leftBefore := app.ownConn(1), app.ownConn(0)
	failed := switchResult(t, app, []int{1}, e.missing)
	if failed.err == nil {
		t.Fatal("a context without a cluster connected")
	}
	app.pendingJumps[1] = &pendingJump{target: navui.Location{Context: "missing", Namespace: e.ns}}
	app.Update(failed)
	if app.ownConn(1) != before || app.ownConn(0) != leftBefore || app.pendingJumps[1] != nil || leases(e.missing) != 0 {
		t.Fatalf("after a failed switch: right %q, left %q, pending jump %v, leases %d",
			app.ownConn(1).name(), app.ownConn(0).name(), app.pendingJumps[1], leases(e.missing))
	}

	// Switching both panels at once moves them together.
	e.app.Update(switchResult(t, app, []int{0, 1}, e.prod))
	if app.ownConn(0).name() != "b-prod" || app.ownConn(1).name() != "b-prod" || leases(e.prod) != 2 || leases(e.dev) != 0 || leases(e.mirror) != 0 {
		t.Fatalf("both panels: %q and %q, leases b-prod %d, a-dev %d, a-mirror %d",
			app.ownConn(0).name(), app.ownConn(1).name(), leases(e.prod), leases(e.dev), leases(e.mirror))
	}
}

// Switching one panel leaves the other where it was: its connection, its
// location, the row under its cursor and its row filter.
func TestPanelContextSwitchKeepsTheOtherPanel(t *testing.T) {
	e := newTwoClusters(t)
	app := e.app
	ctx := t.Context()
	e.openWeb(t, 0)
	app.applyRowFilterText(0, "we")
	left, path := app.ownConn(0), app.leftPanel.GetCurrentPath()
	before, ok := app.leftPanel.SelectedNavItem(ctx)
	if !ok {
		t.Fatal("the left panel lost its row to the filter")
	}

	e.switchPanel(t, 1, e.prod)
	e.switchPanel(t, 1, e.mirror)

	if app.ownConn(0) != left {
		t.Fatalf("the left panel moved to %q", app.ownConn(0).name())
	}
	if got := app.leftPanel.GetCurrentPath(); got != path {
		t.Fatalf("the left panel is at %q, want %q", got, path)
	}
	if f, _ := app.leftPanel.RowFilter(); f.Text() != "we" {
		t.Fatalf("the left panel's filter is %q, want we", f.Text())
	}
	item, conn := app.selectedRow(ctx, app.leftPanel)
	if item == nil || conn != left {
		t.Fatalf("the left panel's row is %v from %q, want web from a-dev", item, conn.name())
	}
	assertInstance(t, "left", item, string(before.(models.InstanceItem).UID()), left)
}

// C-x C-d compares the objects of two contexts, each side read from its
// own cluster and labeled with its context; a side that is gone reports
// the read error instead of a diff.
func TestPanelContextsCompareObjects(t *testing.T) {
	e := newTwoClusters(t)
	app := e.app
	leftWeb := e.openWeb(t, 0)
	e.switchPanel(t, 1, e.prod)
	rightWeb := e.openWeb(t, 1)

	left, right, ok := compareSides(leftWeb, rightWeb)
	if !ok {
		t.Fatal("the two rows are no comparison")
	}
	ctxs := [2]string{app.ownConn(0).name(), app.ownConn(1).name()}
	msg, ok := app.readComparison(nil, 1, left, right, ctxs, manifestdiff.Options{})().(compareResultMsg)
	if !ok || msg.err != nil {
		t.Fatalf("compare = %#v", msg)
	}
	for _, want := range []string{"a-dev", "b-prod", "served-by-a", "served-by-b"} {
		if !strings.Contains(msg.result.Text, want) {
			t.Fatalf("the diff lacks %q:\n%s", want, msg.result.Text)
		}
	}

	if err := e.clientB.Delete(t.Context(), e.webB); err != nil {
		t.Fatalf("delete web on b-prod: %v", err)
	}
	kctesting.Eventually(t, 10*time.Second, 50*time.Millisecond, func() bool {
		msg, _ = app.readComparison(nil, 2, left, right, ctxs, manifestdiff.Options{})().(compareResultMsg)
		return msg.err != nil
	})
	if !strings.Contains(msg.err.Error(), "web") {
		t.Fatalf("compare with a deleted side: %v", msg.err)
	}
}
