package models

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	table "github.com/stokaro/kc/internal/table"
)

// fakeOverviewConn serves a fake client as the cache and lets the test fire
// informer events.
type fakeOverviewConn struct {
	reader crclient.Reader

	mu         sync.Mutex
	onEvent    func()
	watches    int
	keepAlives int
	missing    map[schema.GroupVersionResource]string
}

func (c *fakeOverviewConn) Reader() crclient.Reader { return c.reader }

func (c *fakeOverviewConn) Watch(_ context.Context, onEvent func()) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onEvent = onEvent
	c.watches++
	return func() {
		c.mu.Lock()
		c.onEvent = nil
		c.mu.Unlock()
	}
}

func (c *fakeOverviewConn) Unavailable(gvr schema.GroupVersionResource) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.missing[gvr], false
}

func (c *fakeOverviewConn) KeepAlive(context.Context) {
	c.mu.Lock()
	c.keepAlives++
	c.mu.Unlock()
}

func (c *fakeOverviewConn) fire() {
	c.mu.Lock()
	fn := c.onEvent
	c.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (c *fakeOverviewConn) watching() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onEvent != nil
}

func newTestOverviewEngine(t *testing.T, deps Deps, namespace string, objs ...crclient.Object) (*overviewEngine, *fakeOverviewConn) {
	t.Helper()
	conn := &fakeOverviewConn{reader: newOverviewFakeClient(objs...)}
	if deps.Ctx == nil {
		deps.Ctx = t.Context()
	}
	e := newOverviewEngine(deps, namespace)
	e.connect = func(context.Context) (overviewConn, error) { return conn, nil }
	e.idleAfter = time.Minute
	e.keepEvery = 20 * time.Second
	return e, conn
}

// TestOverviewEngineCoalescesEvents checks the cost rule: a burst of
// informer events leads to at most one recompute per second.
func TestOverviewEngineCoalescesEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, conn := newTestOverviewEngine(t, Deps{}, AllNamespaces)
		defer e.close()
		if r, _ := e.snapshot(); r != nil {
			t.Fatalf("report before the first recompute = %v, want nil", r)
		}
		synctest.Wait()
		if got := e.computeCount(); got != 1 {
			t.Fatalf("recomputes after start = %d, want 1", got)
		}
		for range 100 {
			conn.fire()
		}
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		if got := e.computeCount(); got != 1 {
			t.Fatalf("recomputes within the first second = %d, want still 1", got)
		}
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		if got := e.computeCount(); got != 2 {
			t.Fatalf("recomputes after the burst = %d, want 2", got)
		}
		// No events: no recompute until the periodic refresh.
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if got := e.computeCount(); got != 2 {
			t.Fatalf("recomputes without events = %d, want 2", got)
		}
		_, _ = e.snapshot()
		time.Sleep(overviewRefreshEvery)
		synctest.Wait()
		if got := e.computeCount(); got != 3 {
			t.Fatalf("recomputes after %v = %d, want 3 (periodic refresh)", overviewRefreshEvery, got)
		}
	})
}

// TestOverviewEngineStopsWhenNotRead checks that the engine drops its
// informer handlers once nothing reads it, renews the pool entry while it
// runs, and starts again on the next read.
func TestOverviewEngineStopsWhenNotRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, conn := newTestOverviewEngine(t, Deps{}, AllNamespaces)
		defer e.close()
		_, _ = e.snapshot()
		synctest.Wait()
		if !e.isRunning() || !conn.watching() {
			t.Fatal("engine does not run after the first read")
		}
		// Reads keep it alive past the idle time.
		for range 4 {
			time.Sleep(30 * time.Second)
			_, _ = e.snapshot()
		}
		synctest.Wait()
		if !e.isRunning() {
			t.Fatal("engine stopped while it was read")
		}
		conn.mu.Lock()
		keeps := conn.keepAlives
		conn.mu.Unlock()
		if keeps < 5 {
			t.Fatalf("keep-alives over 2m with a 20s period = %d, want at least 5", keeps)
		}
		time.Sleep(e.idleAfter + 2*time.Second)
		synctest.Wait()
		if e.isRunning() || conn.watching() {
			t.Fatalf("engine running=%v watching=%v after %v without reads, want stopped", e.isRunning(), conn.watching(), e.idleAfter)
		}
		_, _ = e.snapshot()
		synctest.Wait()
		if !e.isRunning() || conn.watches != 2 {
			t.Fatalf("engine running=%v watches=%d after a new read, want running and a second watch", e.isRunning(), conn.watches)
		}
	})
}

// TestOverviewEnginePausesWhileADialogIsOpen checks that events behind a
// dialog wait until it closes.
func TestOverviewEnginePausesWhileADialogIsOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		activity := &Activity{}
		e, conn := newTestOverviewEngine(t, Deps{Activity: activity}, AllNamespaces)
		defer e.close()
		_, _ = e.snapshot()
		synctest.Wait()
		activity.Pause()
		conn.fire()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if got := e.computeCount(); got != 1 {
			t.Fatalf("recomputes while paused = %d, want 1", got)
		}
		activity.Resume()
		time.Sleep(time.Second)
		synctest.Wait()
		if got := e.computeCount(); got != 2 {
			t.Fatalf("recomputes after resume = %d, want 2", got)
		}
	})
}

func TestOverviewEngineCloseStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, conn := newTestOverviewEngine(t, Deps{}, AllNamespaces)
		_, _ = e.snapshot()
		synctest.Wait()
		e.close()
		synctest.Wait()
		if e.isRunning() || conn.watching() {
			t.Fatal("engine still runs after close")
		}
		_, _ = e.snapshot()
		synctest.Wait()
		if e.isRunning() {
			t.Fatal("a closed engine started again")
		}
	})
}

func overviewRowIDs(rows []table.Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		id, _, _, _ := r.Columns()
		out = append(out, id)
	}
	return out
}

func rowCells(t *testing.T, rows []table.Row, id string) []string {
	t.Helper()
	for _, r := range rows {
		rid, cells, _, _ := r.Columns()
		if rid == id {
			return cells
		}
	}
	t.Fatalf("no row %q in %v", id, overviewRowIDs(rows))
	return nil
}

// TestOverviewFolderRowsEnterAndExplain drives the folder like a panel:
// rows by section, Enter on a row lists exactly the objects behind it with
// their full GVR identity, and F3 explains the value with the kubectl
// command.
func TestOverviewFolderRowsEnterAndExplain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps := Deps{CtxName: "prod", KubectlContext: "dev"}
		objs := []crclient.Object{
			readyNode("n1", "4", "8Gi", 110),
			waiting(overviewRunningPod("a", "loop"), "CrashLoopBackOff"),
			waiting(overviewRunningPod("b", "loop"), "CrashLoopBackOff"),
			overviewRunningPod("a", "fine"),
			&corev1.Event{ObjectMeta: om("a", "e1"), Type: corev1.EventTypeWarning, Reason: "BackOff", LastTimestamp: ago(time.Minute)},
		}
		e, _ := newTestOverviewEngine(t, deps, AllNamespaces, objs...)
		e.now = func() time.Time { return overviewTestNow }
		folder := newOverviewFolder(deps, AllNamespaces, []string{"overview"}, e)
		defer folder.Close()
		ctx := t.Context()

		if ids := overviewRowIDs(folder.Lines(ctx, 0, 100)); !slices.Contains(ids, "overview/loading") {
			t.Fatalf("rows before the first report = %v, want a loading row", ids)
		}
		synctest.Wait()
		rows := folder.Lines(ctx, 0, 100)
		ids := overviewRowIDs(rows)
		// The node checks pass with nothing to report and fold into the
		// section's quiet row (ADR 0172).
		wantOrder := []string{"__back__", "overview/attention", "section/capacity", "capacity/cpu", "capacity/memory", "capacity/pods", "capacity/disk", "section/health", "health/pods-crashloop"}
		if !slices.Equal(ids[:len(wantOrder)], wantOrder) {
			t.Fatalf("first rows = %v, want %v", ids[:len(wantOrder)], wantOrder)
		}
		for _, s := range []string{"section/risks", "section/activity", "activity/events-warning"} {
			if !slices.Contains(ids, s) {
				t.Fatalf("rows %v miss %s", ids, s)
			}
		}
		if got := rowCells(t, rows, "health/pods-crashloop"); !slices.Equal(got, []string{"/pods crash looping", "2", "failed"}) {
			t.Fatalf("crash loop row = %v", got)
		}
		if got := rowCells(t, rows, "capacity/cpu"); got[1] != "pods n/a req 0% lim 0% of alloc, node n/a" {
			t.Fatalf("cpu row = %v, want use n/a", got)
		}

		item, ok := folder.ItemByID(ctx, "health/pods-crashloop")
		if !ok {
			t.Fatal("crash loop row not found by ID")
		}
		_, _, styles, _ := item.Columns()
		if styles[0] != JobFailedStyle() {
			t.Fatal("a failed row does not use the failed row style of object lists")
		}
		enter, ok := item.(Enterable)
		if !ok {
			t.Fatal("overview row is not enterable")
		}
		child, err := enter.Enter()
		if err != nil {
			t.Fatal(err)
		}
		targets := child.(*OverviewTargetsFolder)
		defer targets.Close()
		trows := targets.Lines(ctx, 0, 100)
		if got, want := overviewRowIDs(trows), []string{"__back__", "v1/pods/a/loop", "v1/pods/b/loop"}; !slices.Equal(got, want) {
			t.Fatalf("targets = %v, want %v", got, want)
		}
		target, ok := targets.ItemByID(ctx, "v1/pods/a/loop")
		if !ok {
			t.Fatal("target not found by ID")
		}
		obj, ok := target.(ObjectItem)
		if !ok || obj.GVR() != ovPodsGVR || obj.Namespace() != "a" || obj.Name() != "loop" {
			t.Fatalf("target = %#v, want the pod a/loop", target)
		}
		if _, ok := target.(Enterable); !ok {
			t.Fatal("a pod target does not enter its containers")
		}
		if got := rowCells(t, trows, "v1/pods/a/loop"); !slices.Equal(got, []string{"/loop", "a", "pods", "CrashLoopBackOff (app)"}) {
			t.Fatalf("target cells = %v", got)
		}

		_, body, _, _, _, err := item.(Viewable).ViewContent()
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"How it is computed", "CrashLoopBackOff", "kubectl --context prod get pods -A", "Scope: all namespaces"} {
			if !strings.Contains(body, want) {
				t.Fatalf("F3 text misses %q:\n%s", want, body)
			}
		}
		if vc := item.(interface{ ViewCommand() string }).ViewCommand(); !strings.HasPrefix(vc, "kubectl --context prod get pods -A") {
			t.Fatalf("view command = %q", vc)
		}

		events, _ := folder.ItemByID(ctx, "activity/events-warning")
		echild, _ := events.(Enterable).Enter()
		defer echild.(*OverviewTargetsFolder).Close()
		if got := overviewRowIDs(echild.Lines(ctx, 0, 10)); !slices.Equal(got, []string{"__back__", "v1/events/a/e1"}) {
			t.Fatalf("event targets = %v", got)
		}
	})
}

func TestOverviewFolderNamespaceScope(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		objs := []crclient.Object{
			waiting(overviewRunningPod("a", "loop"), "CrashLoopBackOff"),
			waiting(overviewRunningPod("b", "loop"), "CrashLoopBackOff"),
		}
		e, _ := newTestOverviewEngine(t, Deps{}, "b", objs...)
		folder := newOverviewFolder(Deps{}, "b", []string{"overview"}, e)
		defer folder.Close()
		ctx := t.Context()
		folder.Lines(ctx, 0, 10)
		synctest.Wait()
		item, ok := folder.ItemByID(ctx, "health/pods-crashloop")
		if !ok {
			t.Fatalf("no crash loop row in %v", overviewRowIDs(folder.Lines(ctx, 0, 100)))
		}
		child, _ := item.(Enterable).Enter()
		defer child.(*OverviewTargetsFolder).Close()
		if got := overviewRowIDs(child.Lines(ctx, 0, 10)); !slices.Equal(got, []string{"__back__", "v1/pods/b/loop"}) {
			t.Fatalf("targets in namespace b = %v", got)
		}
		_, body, _, _, _, _ := item.(Viewable).ViewContent()
		if !strings.Contains(body, "Scope: namespace b") || !strings.Contains(body, "get pods -n b") {
			t.Fatalf("F3 text does not name the scope:\n%s", body)
		}
	})
}

func TestOverviewRootItemOpensTheScopedOverview(t *testing.T) {
	t.Parallel()
	item := overviewRootItem(Deps{Ctx: t.Context()}, "kube-system", nil)
	_, cells, _, _ := item.Columns()
	if cells[0] != "/overview" {
		t.Fatalf("root row = %v", cells)
	}
	f, err := item.(Enterable).Enter()
	if err != nil {
		t.Fatal(err)
	}
	ov := f.(*OverviewFolder)
	defer ov.Close()
	if ov.NamespaceScope() != "kube-system" || !slices.Equal(ov.Path(), []string{"overview"}) {
		t.Fatalf("overview scope %q path %v", ov.NamespaceScope(), ov.Path())
	}
}

func TestOverviewClusterKeyIsItsOwnPoolEntry(t *testing.T) {
	t.Parallel()
	k := overviewClusterKey(kccluster.Key{ContextName: "ctx", Namespace: "x", SelectorKey: "y"})
	if k.SelectorKey != overviewSelectorKey || k.Namespace != "" || k.ContextName != "ctx" {
		t.Fatalf("overview key = %+v", k)
	}
}
