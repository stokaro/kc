package models

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/helm/helmtest"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/pkg/appconfig"
)

func startTestCluster(t *testing.T, cfg *rest.Config) *kccluster.Cluster {
	t.Helper()
	cl, err := kccluster.New(cfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	kctesting.RunInBackground(t, "cluster", cl.Start)
	t.Cleanup(cl.Stop)
	return cl
}

func createHelmSecret(t *testing.T, cli crclient.Client, r helmtest.Release, corrupt bool) {
	t.Helper()
	sec := r.Secret()
	if corrupt {
		sec = r.CorruptSecret()
	}
	sec.SetUID("")
	sec.SetResourceVersion("")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := cli.Create(ctx, sec); err != nil {
		t.Fatalf("create %s: %v", sec.GetName(), err)
	}
}

func eventuallyRows(t *testing.T, folder Folder, what string, ok func(map[string][]string) bool) map[string][]string {
	t.Helper()
	var last map[string][]string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		last = rowsByID(t, folder, t.Context())
		if ok(last) {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s: rows never matched; last rows: %v", what, last)
	return nil
}

func TestHelmFoldersEnvtest(t *testing.T) {
	if testCfg == nil {
		t.Skip("envtest not available")
	}
	ctx := t.Context()
	cl := startTestCluster(t, testCfg)
	cli := cl.GetClient()
	ns := kctesting.UniqueName("helm-releases")
	createNamespace(t, cl, ns)

	web := func(rev int, status string) helmtest.Release {
		r := helmFixture(ns, "web", rev, status)
		r.Manifest = "---\n# Source: web/templates/cm.yaml\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: web-config\n" +
			"---\n# Source: web/templates/deploy.yaml\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n"
		return r
	}
	createHelmSecret(t, cli, web(1, "superseded"), false)
	createHelmSecret(t, cli, web(2, "deployed"), false)
	createHelmSecret(t, cli, helmFixture(ns, "db", 1, "failed"), true)
	createSecret(t, cl, ns, "not-helm")
	mustCreate(t, cli, &corev1.ConfigMap{Namespace: ns, Name: "web-config"})

	deps := Deps{Cl: cl, Ctx: ctx, CtxName: "envtest", AppConfig: appconfig.Default()}

	root := NewRootFolder(deps, ns)
	assertRowPresent(t, root, helmEntryID)

	item, ok := root.ItemByID(ctx, helmEntryID)
	if !ok {
		t.Fatal("no /helm row in the root")
	}
	releasesFolder, err := item.(Enterable).Enter()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(releasesFolder.(*HelmReleasesFolder).Close)
	rows := eventuallyRows(t, releasesFolder, "release list", func(r map[string][]string) bool {
		return len(r[ns+"/web"]) > 2 && r[ns+"/web"][2] == "2" && len(r[ns+"/db"]) > 0
	})
	if len(rows) != 3 { // back row plus two releases; the plain Secret is not a release
		t.Fatalf("rows = %v", rows)
	}
	if got := rows[ns+"/db"]; got[3] != "failed" || got[4] != "?" {
		t.Errorf("corrupt release row = %q", got)
	}

	// A new revision shows through the shared Secret informer.
	createHelmSecret(t, cli, web(3, "deployed"), false)
	eventuallyRows(t, releasesFolder, "release list after upgrade", func(r map[string][]string) bool {
		return len(r[ns+"/web"]) > 2 && r[ns+"/web"][2] == "3"
	})

	webItem, ok := releasesFolder.ItemByID(ctx, ns+"/web")
	if !ok {
		t.Fatal("web release row missing")
	}
	relFolder, err := webItem.(Enterable).Enter()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(relFolder.(*HelmReleaseFolder).Close)
	eventuallyRows(t, relFolder, "release folder", func(r map[string][]string) bool {
		return r["values"] != nil && r["resources"] != nil && r["history"] != nil
	})

	histItem, _ := relFolder.ItemByID(ctx, "history")
	histFolder, _ := histItem.(Enterable).Enter()
	t.Cleanup(histFolder.(*HelmHistoryFolder).Close)
	eventuallyRows(t, histFolder, "history", func(r map[string][]string) bool {
		return r["1"] != nil && r["2"] != nil && r["3"] != nil
	})

	resItem, _ := relFolder.ItemByID(ctx, "resources")
	resFolder, _ := resItem.(Enterable).Enter()
	t.Cleanup(resFolder.(*HelmResourcesFolder).Close)
	res := eventuallyRows(t, resFolder, "resources", func(r map[string][]string) bool {
		return len(r) == 3
	})
	if got := res["v1/ConfigMap/"+ns+"/web-config"]; len(got) < 4 || got[3] != liveYes {
		t.Errorf("ConfigMap row = %q, want live", got)
	}
	if got := res["apps/v1/Deployment/"+ns+"/web"]; len(got) < 4 || got[3] != liveMissing {
		t.Errorf("Deployment row = %q, want missing", got)
	}
}

// grantSecretsInNamespace lets user list and watch Secrets in namespace.
func grantSecretsInNamespace(t *testing.T, admin *kccluster.Cluster, namespace, user string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	role := &rbacv1.Role{
		Namespace: namespace, Name: "secret-reader",
		Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get", "list", "watch"}}},
	}
	if err := admin.GetClient().Create(ctx, role); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	binding := &rbacv1.RoleBinding{
		Namespace: namespace, Name: "secret-reader",
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: user}},
	}
	if err := admin.GetClient().Create(ctx, binding); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
}

// A user who may read Secrets in one namespace only browses that
// namespace's releases through a namespace cache, sees new revisions, gets
// an explicit denial elsewhere, and gives the cache back on Close.
func TestHelmFoldersNamespaceOnlyEnvtest(t *testing.T) {
	user := kctesting.UniqueName("helm-ns-user")
	granted, other := kctesting.UniqueName("helm-granted"), kctesting.UniqueName("helm-other")
	e := newNamespaceUserEnv(t, user, granted, other)
	grantSecretsInNamespace(t, e.admin, granted, user)
	createHelmSecret(t, e.admin.GetClient(), helmFixture(granted, "web", 1, "deployed"), false)
	createHelmSecret(t, e.admin.GetClient(), helmFixture(other, "db", 1, "deployed"), false)
	if ok, err := e.shared.CanListWatchAllNamespaces(t.Context(), secretsGVR); err != nil || ok {
		t.Fatalf("user may list Secrets cluster-wide (%v, %v); the test needs namespace-only access", ok, err)
	}

	f := NewHelmReleasesFolder(e.deps, granted, []string{"helm"})
	eventuallyRows(t, f, "namespace-only release list", func(r map[string][]string) bool {
		return len(r[granted+"/web"]) > 2 && r[granted+"/web"][2] == "1"
	})
	if got := e.openHolds(); len(got) != 1 {
		t.Fatalf("open holds = %v, want the namespace cache of %s", got, granted)
	}

	// New revisions arrive through the namespace watch.
	createHelmSecret(t, e.admin.GetClient(), helmFixture(granted, "web", 2, "deployed"), false)
	eventuallyRows(t, f, "release list after upgrade", func(r map[string][]string) bool {
		return len(r[granted+"/web"]) > 2 && r[granted+"/web"][2] == "2"
	})

	// The release folder below shares the reader and its cache.
	item, _ := f.ItemByID(t.Context(), granted+"/web")
	rel, err := item.(Enterable).Enter()
	if err != nil {
		t.Fatal(err)
	}
	eventuallyRows(t, rel, "release folder", func(r map[string][]string) bool {
		return r["values"] != nil && r["history"] != nil
	})
	if got := e.openHolds(); len(got) != 1 {
		t.Fatalf("open holds with the release folder open = %v", got)
	}
	rel.(*HelmReleaseFolder).Close()
	f.Close()
	if got := e.openHolds(); len(got) != 0 {
		t.Fatalf("holds left after Close: %v", got)
	}

	cases := []struct {
		name string
		ns   string
		want string
	}{
		{name: "namespace without Secret access", ns: other, want: "Secrets in " + other + " is forbidden"},
		{name: "all namespaces", ns: AllNamespaces, want: "in all namespaces is forbidden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := NewHelmReleasesFolder(e.deps, tc.ns, []string{"helm"})
			t.Cleanup(f.Close)
			rows := eventuallyRows(t, f, "denied notice", func(r map[string][]string) bool {
				return r["__helm_denied__"] != nil
			})
			if len(rows) != 2 { // back row and the notice; never an empty list
				t.Errorf("rows = %v", rows)
			}
			item, _ := f.ItemByID(t.Context(), "__helm_denied__")
			_, body, _, _, _, _ := item.(Viewable).ViewContent()
			if !strings.Contains(body, tc.want) || !strings.Contains(body, "API server:") {
				t.Errorf("notice = %q, want %q and the API server's answer", body, tc.want)
			}
		})
	}
	if got := e.openHolds(); len(got) != 0 {
		t.Fatalf("a denied scope holds a cache: %v", got)
	}
}

// A user without any Secret access gets the denial, not an empty list.
func TestHelmFoldersNoAccessEnvtest(t *testing.T) {
	if testCfg == nil {
		t.Skip("envtest not available")
	}
	admin := startTestCluster(t, testCfg)
	ns := kctesting.UniqueName("helm-denied")
	createNamespace(t, admin, ns)
	createHelmSecret(t, admin.GetClient(), helmFixture(ns, "web", 1, "deployed"), false)

	cfg := rest.CopyConfig(testCfg)
	cfg.Impersonate.UserName = kctesting.UniqueName("helm-no-secrets")
	cl := startTestCluster(t, cfg)
	deps := Deps{Cl: cl, Ctx: t.Context(), CtxName: "envtest", AppConfig: appconfig.Default()}
	f := NewHelmReleasesFolder(deps, ns, []string{"helm"})
	t.Cleanup(f.Close)
	eventuallyRows(t, f, "denied notice", func(r map[string][]string) bool {
		return r["__helm_denied__"] != nil
	})
	item, _ := f.ItemByID(t.Context(), "__helm_denied__")
	_, body, _, _, _, _ := item.(Viewable).ViewContent()
	if !strings.Contains(body, "Secrets in "+ns+" is forbidden") || !strings.Contains(body, "list -a -n "+ns) {
		t.Errorf("notice = %q, want the denial and the helm command", body)
	}
}
