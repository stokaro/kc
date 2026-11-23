package ui

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/manifestdiff"
	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/setdiff"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/pkg/appconfig"
)

var configMapsGVR = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}

// twoContextKubeconfig writes one kubeconfig with a context per control
// plane, as kc reads them.
func twoContextKubeconfig(t *testing.T, cfgs map[string]*rest.Config) string {
	t.Helper()
	kc := clientcmdapi.NewConfig()
	for name, cfg := range cfgs {
		kc.Clusters[name] = &clientcmdapi.Cluster{Server: cfg.Host, CertificateAuthorityData: cfg.CAData}
		kc.AuthInfos[name] = &clientcmdapi.AuthInfo{ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData, Token: cfg.BearerToken}
		kc.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name}
		kc.CurrentContext = name
	}
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*kc, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func createConfigMaps(t *testing.T, cfg *rest.Config, ns string, data map[string]string) {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	cli, err := crclient.New(cfg, crclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := cli.Create(ctx, &corev1.Namespace{Name: ns}); err != nil {
		t.Fatal(err)
	}
	for name, v := range data {
		if err := cli.Create(ctx, &corev1.ConfigMap{Namespace: ns, Name: name, Data: map[string]string{"k": v}}); err != nil {
			t.Fatal(err)
		}
	}
}

// Two isolated control planes stand for staging and prod. The comparison
// reads each through the pool, as C-x c does, releases the lease it took,
// and drops a result that a newer comparison replaced.
func TestCompareListsAcrossContexts(t *testing.T) {
	kcenvtest.Require(t, testCfg)
	prodCfg := kcenvtest.Start(t)
	shop := kctesting.UniqueName("cmp-shop")
	createConfigMaps(t, testCfg, shop, map[string]string{"same": "1", "differs": "1", "staging-only": "1"})
	createConfigMaps(t, prodCfg, shop, map[string]string{"same": "1", "differs": "2", "prod-only": "1"})
	path := twoContextKubeconfig(t, map[string]*rest.Config{"staging": testCfg, "prod": prodCfg})

	pool := kccluster.NewPool(time.Minute, 0)
	pool.Start()
	t.Cleanup(pool.Stop)
	stagingKey := kccluster.Key{KubeconfigPath: path, ContextName: "staging"}
	lease, err := pool.Acquire(t.Context(), stagingKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lease.Release)
	deps := func(cl *kccluster.Cluster, key kccluster.Key) models.Deps {
		return models.Deps{Cl: cl, Ctx: t.Context(), CtxName: key.ContextName, AppConfig: appconfig.Default(), ClusterKey: key}
	}
	left := setSide{set: setdiff.Set{Context: "staging", GVR: configMapsGVR, Namespace: shop}, deps: deps(lease.Cluster(), stagingKey)}
	prodKey := kccluster.Key{KubeconfigPath: path, ContextName: "prod"}
	right := setSide{set: setdiff.Set{Context: "prod", GVR: configMapsGVR, Namespace: shop}, deps: deps(nil, prodKey)}
	var held atomic.Int32
	acquire := func(ctx context.Context) (*kccluster.Cluster, func(), error) {
		l, err := pool.Acquire(ctx, prodKey, nil)
		if err != nil {
			return nil, nil, err
		}
		held.Add(1)
		return l.Cluster(), func() { held.Add(-1); l.Release() }, nil
	}

	a := &App{ctx: t.Context()}
	first := a.runSetCompare(left, right, acquire)
	second := a.runSetCompare(left, right, acquire)
	msg, ok := second().(setCompareMsg)
	if !ok || msg.err != nil {
		t.Fatalf("compare: %#v", msg)
	}
	if held.Load() != 0 {
		t.Errorf("prod lease still held after the read: %d", held.Load())
	}
	got := make(map[string]string)
	for _, row := range msg.report.Rows {
		// The kube-root-ca.crt ConfigMap is in both namespaces and differs
		// by certificate.
		if row.Key != "kube-root-ca.crt" {
			got[row.Key] = row.State.String()
		}
	}
	want := map[string]string{"same": "equal", "differs": "changed", "staging-only": "only left", "prod-only": "only right"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("rows %v, want %v", got, want)
	}
	if msg.right.CtxName != "prod" || msg.left.CtxName != "staging" {
		t.Errorf("deps contexts %q / %q", msg.left.CtxName, msg.right.CtxName)
	}
	// The older comparison finishes last and is dropped.
	stale, _ := first().(setCompareMsg)
	if cmd := a.applySetCompare(stale); cmd != nil {
		t.Error("a replaced comparison was shown")
	}

	// A version the other side does not list is refused before any read.
	other := right
	other.set.GVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	if _, err := readAndCompare(t.Context(), left, other, manifestdiff.Options{}); err == nil || !strings.Contains(err.Error(), "different resources") {
		t.Errorf("incompatible sets: %v", err)
	}
}
