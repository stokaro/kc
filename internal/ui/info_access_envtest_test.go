package ui

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/models"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

// TestInfoWithNamespaceOnlyAccess starts the Info mode as a user who may
// read everything in one namespace and nothing across the cluster: the
// panel says the user is not permitted instead of starting cluster-wide
// informers that never sync.
func TestInfoWithNamespaceOnlyAccess(t *testing.T) {
	if testCfg == nil {
		t.Skip("envtest not available")
	}
	const (
		ns   = "info-team-a"
		user = "info-team-user"
	)
	ctx := t.Context()
	admin, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("admin cluster: %v", err)
	}
	go func() { _ = admin.Start(ctx) }()
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
	app.currentCtx = &kubeconfig.Context{Name: "envtest", Namespace: ns, Kubeconfig: &kubeconfig.Kubeconfig{Path: path, Context: "envtest"}}
	app.impersonateUser = user
	app.clLease, err = app.clPool.Acquire(ctx, app.currentClusterKey(), nil)
	if err != nil {
		t.Fatalf("user cluster: %v", err)
	}
	t.Cleanup(app.clLease.Release)
	app.cl = app.clLease.Cluster()
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
	if s.Err != infoNotPermitted {
		t.Fatalf("summary = %+v, want %q", s, infoNotPermitted)
	}
}
