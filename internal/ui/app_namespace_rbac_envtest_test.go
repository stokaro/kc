package ui

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/models"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

// TestAppBrowsesPodsWithNamespaceOnlyAccess runs the app as kc -n would
// start it, as a user whose only grant is a Role on pods in one namespace.
func TestAppBrowsesPodsWithNamespaceOnlyAccess(t *testing.T) {
	if testCfg == nil {
		t.Skip("envtest not available")
	}
	ns, user := kctesting.UniqueName("ui-team-a"), kctesting.UniqueName("ui-team-user")
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
	adminCreate(t, admin, testPod(ns, "pod-1"))

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
	kctx := &kubeconfig.Context{Name: "envtest", Namespace: ns, Kubeconfig: &kubeconfig.Kubeconfig{Path: path, Context: "envtest"}}
	if key := app.clusterKeyFor(kctx); key.KubeconfigPath != path {
		t.Fatalf("cluster key %+v does not use the test kubeconfig", key)
	}
	usePoolContext(t, app, kctx)

	// The context's namespace scopes both panels.
	app.goToNamespace(app.initialNamespace())
	root, ok := app.leftNav.Current().(*models.RootFolder)
	if !ok || root.NamespaceScope() != ns {
		t.Fatalf("left root = %T, want a root scoped to %s", app.leftNav.Current(), ns)
	}

	podsGVR := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	podsID := resourceRowID(ns, podsGVR, true)
	var podsItem models.Enterable
	kctesting.Eventually(t, 15*time.Second, 100*time.Millisecond, func() bool {
		root.Len(ctx)
		item, ok := root.ItemByID(ctx, podsID)
		if !ok {
			return false
		}
		podsItem, ok = item.(models.Enterable)
		return ok
	}, "the root never listed pods in "+ns)
	next, err := podsItem.Enter()
	if err != nil {
		t.Fatalf("enter pods: %v", err)
	}
	pods := next.(*models.NamespacedObjectsFolder)
	t.Cleanup(pods.Close)
	names := func() []string {
		var out []string
		for _, row := range pods.Lines(ctx, 0, pods.Len(ctx)) {
			if id, _, _, ok := row.Columns(); ok && id != "__back__" {
				out = append(out, id)
			}
		}
		slices.Sort(out)
		return out
	}
	kctesting.Eventually(t, 15*time.Second, 100*time.Millisecond, func() bool { return slices.Equal(names(), []string{"pod-1"}) },
		"pods folder never showed pod-1")
	adminCreate(t, admin, testPod(ns, "pod-2"))
	kctesting.Eventually(t, 15*time.Second, 100*time.Millisecond, func() bool { return slices.Equal(names(), []string{"pod-1", "pod-2"}) },
		"pods folder did not follow a new pod")

	// The picker knows listing namespaces is forbidden and takes a name.
	msg, ok := app.loadNamespacesForPicker(0)().(namespacesLoadedMsg)
	if !ok || !errors.Is(msg.err, errNamespaceListForbidden) {
		t.Fatalf("picker load = %+v, want the forbidden fallback", msg)
	}
}

func adminCreate(t *testing.T, cl *kccluster.Cluster, obj crclient.Object) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := cl.GetClient().Create(ctx, obj); err != nil {
		t.Fatalf("create %s: %v", obj.GetName(), err)
	}
	t.Cleanup(func() {
		// t.Context() is canceled before cleanup functions run.
		delCtx, cancelDel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancelDel()
		_ = cl.GetClient().Delete(delCtx, obj)
	})
}

func testPod(namespace, name string) *corev1.Pod {
	return &corev1.Pod{
		Name: name, Namespace: namespace,
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "pause", Image: "registry.k8s.io/pause:3.9"}}},
	}
}
