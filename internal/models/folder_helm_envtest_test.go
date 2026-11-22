package models

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/helm/helmtest"
	"github.com/stokaro/kc/pkg/appconfig"
)

func startTestCluster(t *testing.T, cfg *rest.Config) *kccluster.Cluster {
	t.Helper()
	cl, err := kccluster.New(cfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	go func() { _ = cl.Start(t.Context()) }()
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
	const ns = "helm-releases"
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
	mustCreate(t, cli, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "web-config"}})

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

func TestHelmFoldersPartialAccessEnvtest(t *testing.T) {
	if testCfg == nil {
		t.Skip("envtest not available")
	}
	admin := startTestCluster(t, testCfg)
	const ns = "helm-partial"
	createNamespace(t, admin, ns)
	createHelmSecret(t, admin.GetClient(), helmFixture(ns, "web", 1, "deployed"), false)

	// One user may read Secrets only in ns; the other may not read them.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "secret-reader"},
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get", "list", "watch"}}},
	}
	if err := admin.GetClient().Create(ctx, role); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "secret-reader"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "helm-ns-user"}},
	}
	if err := admin.GetClient().Create(ctx, binding); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		user string
		want string
	}{
		{name: "Secrets only in the namespace", user: "helm-ns-user", want: "in " + ns + " but not in all namespaces"},
		{name: "no Secrets at all", user: "helm-no-secrets", want: "listing Secrets is forbidden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := rest.CopyConfig(testCfg)
			cfg.Impersonate.UserName = tc.user
			cl := startTestCluster(t, cfg)
			deps := Deps{Cl: cl, Ctx: t.Context(), CtxName: "envtest", AppConfig: appconfig.Default()}
			f := NewHelmReleasesFolder(deps, ns, []string{"helm"})
			t.Cleanup(f.Close)
			eventuallyRows(t, f, "denied notice", func(r map[string][]string) bool {
				return r["__helm_denied__"] != nil
			})
			item, _ := f.ItemByID(t.Context(), "__helm_denied__")
			_, body, _, _, _, _ := item.(Viewable).ViewContent()
			if !strings.Contains(body, tc.want) || !strings.Contains(body, "list -a -n "+ns) {
				t.Errorf("notice = %q, want %q and the helm command", body, tc.want)
			}
		})
	}
}
