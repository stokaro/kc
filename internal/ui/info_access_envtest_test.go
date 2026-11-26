package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/models"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

// TestInfoWithNamespaceOnlyAccess starts the Info mode as a user who may
// read everything in one namespace and nothing across the cluster. In the
// namespace scope the engine starts with namespace caches only: the
// workload under the cursor shows its pods, and the capacity bars say what
// they cannot read instead of the whole panel being refused
// (docs/adr/0257-metric-sources.md).
func TestInfoWithNamespaceOnlyAccess(t *testing.T) {
	if testCfg == nil {
		t.Skip("envtest not available")
	}
	ns, user := kctesting.UniqueName("info-team-a"), kctesting.UniqueName("info-team-user")
	ctx := t.Context()
	admin, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("admin cluster: %v", err)
	}
	kctesting.RunInBackground(t, "admin", admin.Start)
	t.Cleanup(admin.Stop)
	adminCreate(t, admin, &corev1.Namespace{Name: ns})
	adminCreate(t, admin, &rbacv1.Role{
		Name: "reader", Namespace: ns,
		Rules: []rbacv1.PolicyRule{{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"get", "list", "watch"}}},
	})
	adminCreate(t, admin, &rbacv1.RoleBinding{
		Name: "reader", Namespace: ns,
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "reader"},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: user}},
	})
	kctesting.WaitForAccess(t, admin.GetClient(), user, ns, "", "pods", "list", "watch")
	kctesting.WaitForAccess(t, admin.GetClient(), user, ns, "apps", "statefulsets", "list", "watch")

	labels := map[string]string{"app": "db"}
	sts := &appsv1.StatefulSet{
		Name: "db", Namespace: ns,
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{Labels: labels, Spec: testPod(ns, "x").Spec},
		},
	}
	adminCreate(t, admin, sts)
	pod := testPod(ns, "db-0")
	pod.Labels = labels
	pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: sts.Name, UID: sts.UID, Controller: new(true)}}
	adminCreate(t, admin, pod)

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
	usePoolContext(t, app, &kubeconfig.Context{Name: "envtest", Namespace: ns, Kubeconfig: &kubeconfig.Kubeconfig{Path: path, Context: "envtest"}})
	app.goToNamespace(app.initialNamespace())

	tgt := app.infoTarget(ctx, app.rightPanel)
	if tgt.Open == nil {
		t.Fatalf("target = %+v, want an Info source", tgt)
	}
	src := tgt.Open()
	t.Cleanup(src.Close)
	var s models.InfoSummary
	kctesting.Eventually(t, 15*time.Second, 100*time.Millisecond, func() bool {
		s = src.Summary(models.InfoFocus{})
		return !s.Loading
	}, "the Info source never answered")
	if s.Err != "" {
		t.Fatalf("summary = %+v, want the engine to run in the namespace", s)
	}
	if len(s.Bars) == 0 || s.Bars[0].Known {
		t.Fatalf("capacity bars = %+v, want them unknown with a reason", s.Bars)
	}
	t.Logf("capacity bar: %s", s.Bars[0].Detail)

	wl := models.InfoWorkload{GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, Namespace: ns, Name: sts.Name}
	kctesting.Eventually(t, 15*time.Second, 100*time.Millisecond, func() bool {
		s = src.Summary(models.InfoFocus{Workload: &wl})
		return len(s.Bars) == 3 && strings.Contains(s.Bars[2].Detail, "1 pods")
	}, "the workload never showed its pod")
	if s.Err != "" {
		t.Fatalf("workload summary = %+v", s)
	}
}
