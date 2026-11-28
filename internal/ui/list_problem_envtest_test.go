package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/models"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/pkg/appconfig"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

// problemApp is an App whose panels are on envtest in namespace ns, as kc
// would start there, impersonating user when it is set. It returns the
// left panel's root.
func problemApp(t *testing.T, ns, user string) (*App, *models.RootFolder) {
	t.Helper()
	ctx := t.Context()
	// A kubeconfig for the pool, which builds clusters from files. Never
	// let the pool fall back to the default kubeconfig.
	kc := clientcmdapi.NewConfig()
	kc.Clusters["envtest"] = &clientcmdapi.Cluster{Server: testCfg.Host, CertificateAuthorityData: testCfg.CAData}
	kc.AuthInfos["envtest"] = &clientcmdapi.AuthInfo{ClientCertificateData: testCfg.CertData, ClientKeyData: testCfg.KeyData, Token: testCfg.BearerToken}
	kc.Contexts["envtest"] = &clientcmdapi.Context{Cluster: "envtest", AuthInfo: "envtest", Namespace: ns}
	kc.CurrentContext = "envtest"
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*kc, path); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	app := NewApp()
	app.cancel()
	app.ctx, app.cancel = context.WithCancel(ctx)
	t.Cleanup(app.cancel)
	app.clPool = kccluster.NewPool(time.Minute, 0)
	app.clPool.Start()
	t.Cleanup(app.clPool.Stop)
	app.impersonateUser = user
	// The configuration kc starts with: the root lists the types that
	// have objects.
	app.cfg = appconfig.Default()
	app.leftConfig, app.rightConfig = nil, nil
	app.leftPanel.SetResourceViewOptions(true, string(appconfig.OrderAlpha))
	kctx := &kubeconfig.Context{Name: "envtest", Namespace: ns, Kubeconfig: &kubeconfig.Kubeconfig{Path: path, Context: "envtest"}}
	usePoolContext(t, app, kctx)
	app.goToNamespace(app.initialNamespace())
	root, ok := app.leftNav.Current().(*models.RootFolder)
	if !ok || root.NamespaceScope() != ns {
		t.Fatalf("left root = %T, want a root scoped to %s", app.leftNav.Current(), ns)
	}
	return app, root
}

// panelText draws folder in a panel of width x height and returns the
// panel without styles, its lines joined by single spaces so that a
// wrapped notice reads as one line.
func panelText(ctx context.Context, folder models.Folder, width, height int) string {
	p := NewPanel("")
	p.UseFolder(ctx, true)
	p.SetFolder(ctx, folder, true)
	var words []string
	for line := range strings.SplitSeq(ansi.Strip(p.Render(ctx, width, height, true)), "\n") {
		words = append(words, strings.Fields(strings.Trim(line, "│┌┐└┘─├┤"))...)
	}
	return strings.Join(words, " ")
}

// A list the user may not read says so, naming the verb, the resource,
// the namespace and the user, where it was an empty list. Before, the
// folder showed only its back row, and the table list's answer could not
// even be decoded: it read "unknown (get configmaps)".
func TestForbiddenListSaysWhoMayNotListIt(t *testing.T) {
	kcenvtest.Require(t, testCfg)
	ns, user := kctesting.UniqueName("forbid"), kctesting.UniqueName("limited")
	ctx := t.Context()
	admin, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("admin cluster: %v", err)
	}
	kctesting.RunInBackground(t, "admin", admin.Start)
	t.Cleanup(admin.Stop)
	adminCreate(t, admin, &corev1.Namespace{Name: ns})
	adminCreate(t, admin, &rbacv1.Role{
		Name: "pod-reader", Namespace: ns,
		Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch"}}},
	})
	adminCreate(t, admin, &rbacv1.RoleBinding{
		Name: "pod-reader", Namespace: ns,
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "pod-reader"},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: user}},
	})
	kctesting.WaitForAccess(t, admin.GetClient(), user, ns, "", "pods", "list", "watch")
	adminCreate(t, admin, &corev1.ConfigMap{Name: "settings", Namespace: ns})
	_, root := problemApp(t, ns, user)

	configMaps := models.NewNamespacedObjectsFolder(root.Deps, configMapsGVR, ns, []string{"namespaces", ns, "configmaps"}, nil)
	t.Cleanup(configMaps.Close)
	kctesting.Eventually(t, 15*time.Second, 50*time.Millisecond, func() bool {
		configMaps.Len(ctx)
		return configMaps.ListProblem() != nil
	}, "the config map list as "+user+" never reported a problem")
	if err := configMaps.ListProblem(); !apierrors.IsForbidden(err) {
		t.Fatalf("ListProblem() = %v, want Forbidden", err)
	}
	want := "Not allowed to list configmaps in " + ns + " (user " + user + ") C-r reads again"
	if got := panelText(ctx, configMaps, 120, 12); !strings.Contains(got, want) {
		t.Errorf("the panel reads %q, want %q", got, want)
	}

	// The control: a list the user may read draws no notice once it is
	// empty again.
	pods := models.NewNamespacedObjectsFolder(root.Deps, podsGVR, ns, []string{"namespaces", ns, "pods"}, nil)
	t.Cleanup(pods.Close)
	emptiedAfterAPod(t, admin, pods, ns)
	if err := pods.ListProblem(); err != nil {
		t.Errorf("the empty pod list the user may read has the problem %v", err)
	}
	if got := panelText(ctx, pods, 120, 12); strings.Contains(got, "Not allowed") || strings.Contains(got, "C-r") {
		t.Errorf("the empty pod list the user may read shows a notice: %q", got)
	}
}

// emptiedAfterAPod creates a pod in ns, waits until folder lists it,
// deletes it and waits until folder lists it no more. folder has then
// loaded an empty list at least once: a pod that was never scheduled is
// deleted at once.
func emptiedAfterAPod(t *testing.T, admin *kccluster.Cluster, folder models.Folder, ns string) {
	t.Helper()
	name := kctesting.UniqueName("probe")
	listed := func() bool {
		for _, r := range folder.Lines(t.Context(), 0, folder.Len(t.Context())) {
			if id, _, _, ok := r.Columns(); ok && (id == name || strings.HasSuffix(id, "/"+name)) {
				return true
			}
		}
		return false
	}
	pod := testPod(ns, name)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := admin.GetClient().Create(ctx, pod); err != nil {
		t.Fatalf("create pod %s: %v", name, err)
	}
	kctesting.Eventually(t, 15*time.Second, 50*time.Millisecond, listed, "the list of "+ns+" never showed pod "+name)
	if err := admin.GetClient().Delete(ctx, pod); err != nil {
		t.Fatalf("delete pod %s: %v", name, err)
	}
	kctesting.Eventually(t, 15*time.Second, 50*time.Millisecond, func() bool { return !listed() }, "the list of "+ns+" still shows the deleted pod "+name)
}

// A namespace that does not exist is named, in the root and in a list of
// it. Before, kc -n with a mistyped namespace showed the types and empty
// lists, as for a namespace with nothing in it.
func TestMissingNamespaceIsNamed(t *testing.T) {
	kcenvtest.Require(t, testCfg)
	ns := kctesting.UniqueName("missing")
	ctx := t.Context()
	_, root := problemApp(t, ns, "")

	want := "Namespace " + ns + " does not exist Namespace... (Left/Right menu) picks another"
	kctesting.Eventually(t, 15*time.Second, 50*time.Millisecond, func() bool {
		root.Len(ctx)
		return apierrors.IsNotFound(root.ListProblem())
	}, "the root of "+ns+" never named the missing namespace")
	// The root hides the namespaced types once their peeks find them
	// empty, which leaves room for the notice below the rows.
	got := panelText(ctx, root, 120, 30)
	for deadline := time.Now().Add(15 * time.Second); !strings.Contains(got, want); got = panelText(ctx, root, 120, 30) {
		if time.Now().After(deadline) {
			t.Fatalf("the root panel never said %q; it reads %q", want, got)
		}
		time.Sleep(50 * time.Millisecond)
		// As the UI does on the folder's dirty message.
		root.Refresh()
	}
	if strings.Contains(got, "/pods") {
		t.Errorf("the root of a missing namespace lists pods: %q", got)
	}

	pods := models.NewNamespacedObjectsFolder(root.Deps, podsGVR, ns, []string{"namespaces", ns, "pods"}, nil)
	t.Cleanup(pods.Close)
	kctesting.Eventually(t, 15*time.Second, 50*time.Millisecond, func() bool {
		pods.Len(ctx)
		return apierrors.IsNotFound(pods.ListProblem())
	}, "the pod list of "+ns+" never named the missing namespace")
	if got := panelText(ctx, pods, 120, 12); !strings.Contains(got, want) {
		t.Errorf("the pod list reads %q, want %q", got, want)
	}
}

// The control of TestMissingNamespaceIsNamed: the list of an empty
// namespace that exists does not name it.
func TestEmptyNamespaceIsNotNamed(t *testing.T) {
	kcenvtest.Require(t, testCfg)
	ns := kctesting.UniqueName("empty")
	ctx := t.Context()
	admin, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("admin cluster: %v", err)
	}
	kctesting.RunInBackground(t, "admin", admin.Start)
	t.Cleanup(admin.Stop)
	adminCreate(t, admin, &corev1.Namespace{Name: ns})
	_, root := problemApp(t, ns, "")

	pods := models.NewNamespacedObjectsFolder(root.Deps, podsGVR, ns, []string{"namespaces", ns, "pods"}, nil)
	t.Cleanup(pods.Close)
	emptiedAfterAPod(t, admin, pods, ns)
	if err := pods.ListProblem(); err != nil {
		t.Errorf("the pod list of the empty namespace %s has the problem %v", ns, err)
	}
	if got := panelText(ctx, pods, 120, 12); strings.Contains(got, "does not exist") {
		t.Errorf("the pod list of the empty namespace reads %q", got)
	}
}
