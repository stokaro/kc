package models

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/helm"
	"github.com/stokaro/kc/internal/helm/helmtest"
	table "github.com/stokaro/kc/internal/table"
)

const helmTestManifest = `---
# Source: web/templates/cm.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: web-config
---
# Source: web/templates/deploy.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: other
  labels:
    app.kubernetes.io/managed-by: Helm
---
# Source: web/templates/role.yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: web-reader
  namespace: ignored-for-cluster-scope
---
# Source: web/templates/s1.yaml
apiVersion: v1
kind: Secret
metadata:
  name: s1
---
# Source: web/templates/s2.yaml
apiVersion: v1
kind: Secret
metadata:
  name: s2
---
# Source: web/templates/widget.yaml
apiVersion: example.com/v1
kind: Widget
metadata:
  name: w
---
# Source: web/templates/broken.yaml
metadata:
  name: no-kind
`

func helmFixture(ns, name string, rev int, status string) helmtest.Release {
	return helmtest.Release{
		Namespace: ns, Name: name, Revision: rev, Status: status,
		Chart: name, Version: "1.0." + strconv.Itoa(rev), AppVersion: "2.0",
		Description: "Install complete", Notes: "hello from " + name,
		Values:   map[string]any{"replicas": rev},
		Manifest: helmTestManifest,
	}
}

func helmRecord(t *testing.T, r helmtest.Release, corrupt bool) helm.Record {
	t.Helper()
	sec := r.Secret()
	if corrupt {
		sec = r.CorruptSecret()
	}
	rec, ok := helm.RecordFromSecret(sec)
	if !ok {
		t.Fatalf("fixture %s/%s v%d is not a record", r.Namespace, r.Name, r.Revision)
	}
	return rec
}

func helmTestDeps(t *testing.T) Deps {
	return Deps{Ctx: t.Context(), CtxName: "dev", KubectlContext: "dev"}
}

type helmRow struct {
	id    string
	cells []string
	row   table.Row
}

func helmRowsOf(t *testing.T, rows []table.Row) []helmRow {
	t.Helper()
	out := make([]helmRow, 0, len(rows))
	for _, r := range rows {
		id, cells, _, ok := r.Columns()
		if !ok {
			t.Fatalf("row without columns: %#v", r)
		}
		out = append(out, helmRow{id: id, cells: cells, row: r})
	}
	return out
}

func helmIDs(rows []helmRow) string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.id
	}
	return strings.Join(ids, ",")
}

func TestHelmReleaseRows(t *testing.T) {
	t.Parallel()
	recs := []helm.Record{
		helmRecord(t, helmFixture("a", "web", 1, "superseded"), false),
		helmRecord(t, helmFixture("a", "web", 2, "deployed"), false),
		helmRecord(t, helmFixture("a", "db", 1, "failed"), true),
		helmRecord(t, helmFixture("b", "api", 3, "pending-upgrade"), false),
	}
	cases := []struct {
		name    string
		records []helm.Record
		scope   string
		wantIDs string
		check   func(t *testing.T, rows []helmRow)
	}{
		{
			name:    "newest revision per release",
			records: recs,
			wantIDs: "a/db,a/web,b/api",
			check: func(t *testing.T, rows []helmRow) {
				web := rows[1].cells
				want := []string{"/web", "a", "2", "deployed", "web-1.0.2", "2.0", formatHelmTime(mustDecode(t, recs[1]).Info.LastDeployed)}
				if strings.Join(web, "|") != strings.Join(want, "|") {
					t.Errorf("web cells = %q, want %q", web, want)
				}
				_, _, _, _, _, err := rows[1].row.(Viewable).ViewContent()
				if err != nil {
					t.Fatal(err)
				}
				if cmd := rows[1].row.(interface{ ViewCommand() string }).ViewCommand(); cmd != "helm status web -n a" {
					t.Errorf("view command = %q", cmd)
				}
			},
		},
		{
			name:    "a corrupt newest revision keeps the row with the label status",
			records: recs,
			wantIDs: "a/db,a/web,b/api",
			check: func(t *testing.T, rows []helmRow) {
				db := rows[0]
				if db.cells[3] != "failed" || db.cells[4] != "?" {
					t.Errorf("corrupt row cells = %q", db.cells)
				}
				_, body, _, _, _, _ := db.row.(Viewable).ViewContent()
				if !strings.Contains(body, "cannot be decoded") {
					t.Errorf("corrupt summary = %q", body)
				}
				if _, ok := db.row.(Enterable); !ok {
					t.Error("a corrupt release must still open, for its history")
				}
			},
		},
		{
			name:    "no releases explains the storage driver",
			scope:   "empty",
			wantIDs: "__helm_none__",
			check: func(t *testing.T, rows []helmRow) {
				_, body, _, _, _, _ := rows[0].row.(Viewable).ViewContent()
				if !strings.Contains(body, "HELM_DRIVER=configmap") || !strings.Contains(body, "empty") {
					t.Errorf("empty notice = %q", body)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reader := newHelmReader(helmTestDeps(t))
			rows := helmRowsOf(t, helmReleaseRows(helmTestDeps(t), reader, helm.Group(tc.records), tc.scope, []string{"helm"}))
			if got := helmIDs(rows); got != tc.wantIDs {
				t.Fatalf("ids = %s, want %s", got, tc.wantIDs)
			}
			tc.check(t, rows)
		})
	}
}

func mustDecode(t *testing.T, rec helm.Record) *helm.Release {
	t.Helper()
	rel, err := rec.Decode()
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

func TestHelmReleaseFolderRows(t *testing.T) {
	t.Parallel()
	history := func(t *testing.T, corruptLatest bool) helm.History {
		recs := []helm.Record{
			helmRecord(t, helmFixture("a", "web", 1, "superseded"), false),
			helmRecord(t, helmFixture("a", "web", 2, "superseded"), false),
			helmRecord(t, helmFixture("a", "web", 3, "deployed"), corruptLatest),
		}
		return helm.Group(recs)[0]
	}
	cases := []struct {
		name     string
		corrupt  bool
		found    bool
		revision int
		wantIDs  string
		check    func(t *testing.T, rows []helmRow)
	}{
		{
			name: "newest revision", found: true,
			wantIDs: "values,manifest,notes,hooks,resources,history",
			check: func(t *testing.T, rows []helmRow) {
				title, body, lang, _, _, err := rows[0].row.(Viewable).ViewContent()
				if err != nil || body != "replicas: 3\n" || lang != "yaml" || !strings.Contains(title, "revision 3, stored") {
					t.Errorf("values view = %q %q %q %v", title, body, lang, err)
				}
				if cmd := rows[0].row.(interface{ ViewCommand() string }).ViewCommand(); cmd != "helm get values web -n a" {
					t.Errorf("values command = %q", cmd)
				}
				if rows[0].cells[1] != "user-supplied values, stored in revision 3" {
					t.Errorf("values description = %q", rows[0].cells[1])
				}
				if rows[3].cells[1] != "none" {
					t.Errorf("hooks description = %q, want none", rows[3].cells[1])
				}
				if rows[5].cells[1] != "3 stored revisions" {
					t.Errorf("history description = %q", rows[5].cells[1])
				}
			},
		},
		{
			name: "an older revision has no history entry", found: true, revision: 1,
			wantIDs: "values,manifest,notes,hooks,resources",
			check: func(t *testing.T, rows []helmRow) {
				_, body, _, _, _, _ := rows[0].row.(Viewable).ViewContent()
				if body != "replicas: 1\n" {
					t.Errorf("revision 1 values = %q", body)
				}
				if cmd := rows[1].row.(interface{ ViewCommand() string }).ViewCommand(); cmd != "helm get manifest web -n a --revision 1" {
					t.Errorf("manifest command = %q", cmd)
				}
			},
		},
		{name: "revision no longer stored", found: true, revision: 9, wantIDs: "__helm_gone__"},
		{name: "corrupt newest revision keeps the history", found: true, corrupt: true, wantIDs: "__helm_corrupt__,history"},
		{name: "corrupt revision opened from history", found: true, corrupt: true, revision: 3, wantIDs: "__helm_corrupt__"},
		{name: "release uninstalled", found: false, wantIDs: "__helm_gone__"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			deps := helmTestDeps(t)
			rows := helmRowsOf(t, helmReleaseFolderRows(deps, newHelmReader(deps), history(t, tc.corrupt), tc.found, tc.revision, []string{"helm", "web"}))
			if got := helmIDs(rows); got != tc.wantIDs {
				t.Fatalf("ids = %s, want %s", got, tc.wantIDs)
			}
			if tc.check != nil {
				tc.check(t, rows)
			}
		})
	}
}

func TestHelmHistoryRows(t *testing.T) {
	t.Parallel()
	deps := helmTestDeps(t)
	h := helm.Group([]helm.Record{
		helmRecord(t, helmFixture("a", "web", 1, "superseded"), false),
		helmRecord(t, helmFixture("a", "web", 2, "superseded"), true),
		helmRecord(t, helmFixture("a", "web", 3, "deployed"), false),
	})[0]
	rows := helmRowsOf(t, helmHistoryRows(deps, newHelmReader(deps), h, []string{"helm", "web", "history"}))
	if got := helmIDs(rows); got != "3,2,1" {
		t.Fatalf("ids = %s, want newest first", got)
	}
	if c := rows[0].cells; c[0] != "/3" || c[2] != "deployed" || c[3] != "web-1.0.3" || c[5] != "Install complete" {
		t.Errorf("revision 3 cells = %q", c)
	}
	if c := rows[1].cells; c[2] != "superseded" || c[3] != "?" || !strings.HasPrefix(c[5], "cannot decode") {
		t.Errorf("corrupt revision cells = %q", c)
	}
	for _, r := range rows {
		if _, ok := r.row.(Enterable); !ok {
			t.Errorf("revision %s is not enterable", r.id)
		}
	}
}

func helmTestMapper() meta.RESTMapper {
	m := meta.NewDefaultRESTMapper(nil)
	m.Add(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, meta.RESTScopeNamespace)
	m.Add(schema.GroupVersionKind{Version: "v1", Kind: "Secret"}, meta.RESTScopeNamespace)
	m.Add(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, meta.RESTScopeNamespace)
	m.Add(schema.GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"}, meta.RESTScopeRoot)
	return m
}

func TestHelmResourceRows(t *testing.T) {
	t.Parallel()
	rel := mustDecode(t, helmRecord(t, helmFixture("a", "web", 2, "deployed"), false))
	var gets []string
	get := func(_ context.Context, gvr schema.GroupVersionResource, ns, name string) error {
		gets = append(gets, gvr.Resource+":"+ns+"/"+name)
		gr := gvr.GroupResource()
		switch gvr.Resource {
		case "deployments":
			return apierrors.NewNotFound(gr, name)
		case "secrets":
			return apierrors.NewForbidden(gr, name, errors.New("no"))
		}
		return nil
	}
	rows := helmRowsOf(t, helmResourceRows(t.Context(), helmTestMapper(), get, rel, []string{"helm", "web", "resources"}))

	type want struct {
		id, live, ns string
		gvr          schema.GroupVersionResource
		missing      string
	}
	wants := []want{
		{id: "v1/ConfigMap/a/web-config", live: liveYes, ns: "a", gvr: schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}},
		{id: "apps/v1/Deployment/other/web", live: liveMissing, ns: "other", gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, missing: "not in the cluster in other"},
		{id: "rbac.authorization.k8s.io/v1/ClusterRole//web-reader", live: liveYes, ns: "", gvr: schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}},
		{id: "v1/Secret/a/s1", live: liveDenied, ns: "a", gvr: schema.GroupVersionResource{Version: "v1", Resource: "secrets"}},
		{id: "v1/Secret/a/s2", live: liveDenied, ns: "a", gvr: schema.GroupVersionResource{Version: "v1", Resource: "secrets"}},
		{id: "example.com/v1/Widget//w", live: liveNotServed, missing: "not served"},
	}
	if len(rows) != len(wants)+1 {
		t.Fatalf("rows = %s, want %d objects and one manifest error", helmIDs(rows), len(wants))
	}
	for i, w := range wants {
		r := rows[i]
		if r.id != w.id {
			t.Errorf("row %d id = %s, want %s", i, r.id, w.id)
			continue
		}
		if r.cells[3] != w.live || r.cells[1] != w.ns {
			t.Errorf("%s cells = %q, want live %q namespace %q", w.id, r.cells, w.live, w.ns)
		}
		link, ok := r.row.(ObjectLink)
		if !ok {
			t.Fatalf("%s is not a link", w.id)
		}
		ref, missing := link.LinkTarget()
		if ref.GVR != w.gvr || ref.Namespace != w.ns {
			t.Errorf("%s target = %+v", w.id, ref)
		}
		if (w.missing == "") != (missing == "") || !strings.Contains(missing, w.missing) {
			t.Errorf("%s missing = %q, want %q", w.id, missing, w.missing)
		}
		_, doc, _, _, _, _ := r.row.(Viewable).ViewContent()
		if !strings.Contains(doc, "# Source: web/templates/") {
			t.Errorf("%s view is not the stored document: %q", w.id, doc)
		}
	}
	if last := rows[len(rows)-1]; !strings.HasPrefix(last.id, "__helm_manifest_error_") {
		t.Errorf("last row = %s, want the skipped document", last.id)
	}
	// After the first Secret is denied the second is not read again.
	if got := strings.Join(gets, ","); got != "configmaps:a/web-config,deployments:other/web,clusterroles:/web-reader,secrets:a/s1" {
		t.Errorf("reads = %s", got)
	}
}

// Without a REST mapper the rows are still listed, with an unknown state.
func TestHelmResourceRowsWithoutMapper(t *testing.T) {
	t.Parallel()
	rel := &helm.Release{Name: "web", Namespace: "a", Manifest: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n"}
	rows := helmRowsOf(t, helmResourceRows(t.Context(), nil, nil, rel, nil))
	if len(rows) != 1 || rows[0].cells[3] != liveUnknown {
		t.Fatalf("rows = %+v", rows)
	}
}

// fakeHelmScopes stands in for resolveListScope and the direct list. Each
// decision returns Deps named after the scope, so a test sees which cache a
// read would use, and counts the namespace caches still held.
type fakeHelmScopes struct {
	mu       sync.Mutex
	scopes   map[string]listScope // missing: shared
	peekErrs map[string]error
	resolves int
	held     int
	peeked   []string
}

func (f *fakeHelmScopes) install(r *helmReader) {
	r.resolve = func(ns string) (Deps, listScope, func()) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.resolves++
		scope := f.scopes[ns]
		f.held++
		return Deps{CtxName: scope.String() + ":" + ns}, scope, sync.OnceFunc(func() {
			f.mu.Lock()
			f.held--
			f.mu.Unlock()
		})
	}
	r.peek = func(_ context.Context, d Deps, ns string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.peeked = append(f.peeked, d.CtxName)
		return f.peekErrs[ns]
	}
}

func (f *fakeHelmScopes) counts() (resolves, held int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resolves, f.held
}

func TestHelmScopeFor(t *testing.T) {
	t.Parallel()
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New("no"))
	cases := []struct {
		name      string
		namespace string
		scope     listScope
		peekErr   error
		wantDeps  string // CtxName of the Deps used for reads; "" when denied
		wantErr   string // "access", "other" or ""
		wantHeld  int    // caches held after the decision
		wantCache bool   // a second call decides nothing again
	}{
		{name: "cluster-wide access uses the shared cache", namespace: "a", scope: scopeShared, wantDeps: "shared:a", wantHeld: 1, wantCache: true},
		{name: "namespace-only access uses a namespace cache", namespace: "a", scope: scopeNamespace, wantDeps: "namespace:a", wantHeld: 1, wantCache: true},
		{name: "denied by the review", namespace: "a", scope: scopeDenied, peekErr: forbidden, wantErr: "access", wantCache: true},
		{name: "denied in all namespaces", namespace: AllNamespaces, scope: scopeDenied, peekErr: forbidden, wantErr: "access", wantCache: true},
		{name: "unknown review, the list is forbidden", namespace: AllNamespaces, scope: scopeShared, peekErr: forbidden, wantErr: "access", wantCache: true},
		{name: "other errors are retried", namespace: "a", scope: scopeNamespace, peekErr: errors.New("connection refused"), wantErr: "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newHelmReader(helmTestDeps(t))
			fake := &fakeHelmScopes{
				scopes:   map[string]listScope{tc.namespace: tc.scope},
				peekErrs: map[string]error{tc.namespace: tc.peekErr},
			}
			fake.install(r)
			deps, err := r.scopeFor(t.Context(), tc.namespace)
			ae, isAccess := isHelmAccessError(err)
			switch tc.wantErr {
			case "access":
				if !isAccess || !apierrors.IsForbidden(err) {
					t.Fatalf("err = %v, want an access error that unwraps to Forbidden", err)
				}
				if ae.namespace != tc.namespace {
					t.Errorf("access error namespace = %q", ae.namespace)
				}
			case "other":
				if err == nil || isAccess {
					t.Fatalf("err = %v, want a plain error", err)
				}
			default:
				if err != nil {
					t.Fatalf("err = %v", err)
				}
			}
			if deps.CtxName != tc.wantDeps {
				t.Errorf("reads use %q, want %q", deps.CtxName, tc.wantDeps)
			}
			if _, held := fake.counts(); held != tc.wantHeld {
				t.Errorf("caches held = %d, want %d", held, tc.wantHeld)
			}
			_, _ = r.scopeFor(t.Context(), tc.namespace)
			if resolves, _ := fake.counts(); (resolves == 1) != tc.wantCache {
				t.Errorf("decisions after two calls = %d, cached = %v", resolves, tc.wantCache)
			}
			r.Close()
			if _, held := fake.counts(); held != 0 {
				t.Errorf("caches held after Close = %d", held)
			}
		})
	}
}

// A denial in one namespace does not change the answer for another, and
// the watch of each namespace uses its own decision.
func TestHelmScopesAreIndependent(t *testing.T) {
	t.Parallel()
	r := newHelmReader(helmTestDeps(t))
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New("no"))
	fake := &fakeHelmScopes{
		scopes:   map[string]listScope{"a": scopeNamespace, "b": scopeDenied},
		peekErrs: map[string]error{"b": forbidden},
	}
	fake.install(r)
	if _, err := r.scopeFor(t.Context(), "b"); err == nil {
		t.Fatal("b: want a denial")
	}
	deps, err := r.scopeFor(t.Context(), "a")
	if err != nil || deps.CtxName != "namespace:a" {
		t.Fatalf("a after b was denied: deps %q, err %v", deps.CtxName, err)
	}
	if _, err := r.watchFactory("b", "")(func() {}, func() {}); !apierrors.IsForbidden(err) {
		t.Errorf("watch in b: err = %v, want Forbidden", err)
	}
	r.Close()
	if _, held := fake.counts(); held != 0 {
		t.Errorf("caches held after Close = %d", held)
	}
}

// Until a decision is made the watch factory starts nothing; once denied it
// reports Forbidden, which stops the row source from retrying.
func TestHelmWatchFactoryWaitsForAccess(t *testing.T) {
	t.Parallel()
	r := newHelmReader(helmTestDeps(t))
	cancel, err := r.watchFactory("a", "")(func() {}, func() {})
	if cancel != nil || err != nil {
		t.Fatalf("before the decision: cancel=%v err=%v", cancel != nil, err)
	}
	fake := &fakeHelmScopes{
		scopes:   map[string]listScope{"a": scopeDenied},
		peekErrs: map[string]error{"a": apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New("no"))},
	}
	fake.install(r)
	_, _ = r.scopeFor(t.Context(), "a")
	if _, err := r.watchFactory("a", "")(func() {}, func() {}); !apierrors.IsForbidden(err) {
		t.Fatalf("after a denial: err = %v, want Forbidden", err)
	}
}

// A load that finishes after the view closed gives its cache back at once.
func TestHelmScopeAfterClose(t *testing.T) {
	t.Parallel()
	r := newHelmReader(helmTestDeps(t))
	fake := &fakeHelmScopes{scopes: map[string]listScope{"a": scopeNamespace}}
	fake.install(r)
	r.Close()
	if _, err := r.scopeFor(t.Context(), "a"); !errors.Is(err, errHelmReaderClosed) {
		t.Fatalf("err = %v, want errHelmReaderClosed", err)
	}
	if _, held := fake.counts(); held != 0 {
		t.Errorf("caches held = %d, want 0", held)
	}
}

// A review that denies watch while the list works still explains itself
// as Forbidden.
func TestHelmDeniedCauseWithoutListError(t *testing.T) {
	t.Parallel()
	r := newHelmReader(helmTestDeps(t))
	fake := &fakeHelmScopes{scopes: map[string]listScope{"a": scopeDenied}}
	fake.install(r)
	_, err := r.scopeFor(t.Context(), "a")
	if !apierrors.IsForbidden(err) || !strings.Contains(err.(*helmAccessError).cause.Error(), "list or watch") {
		t.Fatalf("err = %v", err)
	}
}

func TestHelmAccessNotice(t *testing.T) {
	t.Parallel()
	deps := Deps{Ctx: t.Context(), CtxName: "prod", KubectlContext: "dev"}
	cases := []struct {
		name   string
		err    *helmAccessError
		ns     string
		wantIn []string
	}{
		{name: "namespace", ns: "a", err: &helmAccessError{namespace: "a"},
			wantIn: []string{"Secrets in a is forbidden", "list and watch on secrets in a", "helm --kube-context prod list -a -n a"}},
		{name: "all namespaces", ns: AllNamespaces, err: &helmAccessError{namespace: AllNamespaces, cause: errors.New(`secrets is forbidden: User "u"`)},
			wantIn: []string{"in all namespaces is forbidden", "Open /helm in a namespace", "helm.sh/release.v1", "helm --kube-context prod list -a -A", `User "u"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			item := helmAccessNotice(deps, tc.err, tc.ns, []string{"helm"}, 3)
			_, body, _, _, _, _ := item.ViewContent()
			for _, w := range tc.wantIn {
				if !strings.Contains(body, w) {
					t.Errorf("notice %q lacks %q", body, w)
				}
			}
			if _, ok := any(item).(Enterable); ok {
				t.Error("a notice must not be enterable")
			}
		})
	}
}

func TestHelmFolderCommands(t *testing.T) {
	t.Parallel()
	deps := Deps{Ctx: t.Context(), CtxName: "dev", KubectlContext: "dev"}
	other := Deps{Ctx: t.Context(), CtxName: "prod", KubectlContext: "dev"}
	reader := newHelmReader(deps)
	rel := &helm.Release{Name: "web", Namespace: "a", Version: 2}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"list in a namespace", (&HelmReleasesFolder{BaseFolder: NewBaseFolder(deps, nil, nil), namespace: "a"}).KubectlCommand(), "helm list -a -n a"},
		{"list everywhere, other context", (&HelmReleasesFolder{BaseFolder: NewBaseFolder(other, nil, nil)}).KubectlCommand(), "helm --kube-context prod list -a -A"},
		{"status", (&HelmReleaseFolder{BaseFolder: NewBaseFolder(deps, nil, nil), reader: reader, namespace: "a", release: "web"}).KubectlCommand(), "helm status web -n a"},
		{"status of a revision", (&HelmReleaseFolder{BaseFolder: NewBaseFolder(deps, nil, nil), namespace: "a", release: "web", revision: 2}).KubectlCommand(), "helm status web -n a --revision 2"},
		{"history", (&HelmHistoryFolder{BaseFolder: NewBaseFolder(deps, nil, nil), namespace: "a", release: "web"}).KubectlCommand(), "helm history web -n a"},
		{"resources", (&HelmResourcesFolder{BaseFolder: NewBaseFolder(other, nil, nil), rel: rel, revision: 2}).KubectlCommand(), "helm --kube-context prod get manifest web -n a --revision 2 | kubectl --context prod get -f -"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.got != tc.want {
				t.Fatalf("command = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestHelmResourcesEntryCountsObjects(t *testing.T) {
	t.Parallel()
	one := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n"
	tests := []struct {
		name     string
		manifest string
		want     string
	}{
		{name: "one", manifest: one, want: "1 object of the manifest"},
		{name: "two", manifest: one + "---\n" + strings.ReplaceAll(one, "name: c", "name: d"), want: "2 objects of the manifest"},
		{name: "none", manifest: "", want: "0 objects of the manifest"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rel := &helm.Release{Name: "web", Namespace: "a", Version: 1, Manifest: tc.manifest}
			_, cells, _, _ := newHelmResourcesEntry(Deps{}, rel, 1, []string{"helm", "web"}).Columns()
			if !strings.HasPrefix(cells[1], tc.want+",") {
				t.Fatalf("description = %q, want it to start with %q", cells[1], tc.want)
			}
		})
	}
}
