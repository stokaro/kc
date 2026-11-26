package models

import (
	"context"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/pkg/appconfig"
)

// These tests install and delete the Certificate CRD, a cluster-wide
// change, so each starts a control plane of its own.

// waitCertificateRow reads the certificate row through get until ok accepts
// it.
func waitCertificateRow(t *testing.T, what string, get func() (overviewFinding, bool), ok func(overviewFinding) bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last overviewFinding
	for time.Now().Before(deadline) {
		if f, found := get(); found {
			last = f
			if ok(f) {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("waited 30s for %s: last certificate row value=%q n/a=%q unavailable=%q targets=%v", what, last.value, last.notApplicable, last.unavailable, targetKeys(last))
}

func engineRow(e *overviewEngine) func() (overviewFinding, bool) {
	return func() (overviewFinding, bool) {
		r, _ := e.snapshot()
		return r.finding("risks/certificates")
	}
}

func notServedRow(f overviewFinding) bool {
	return f.notApplicable == certManagerNotInstalled && f.value == "none" && f.unavailable == "" && len(f.targets) == 0
}

// listsOnly accepts an applicable row whose targets are exactly keys.
func listsOnly(keys ...string) func(overviewFinding) bool {
	return func(f overviewFinding) bool {
		return f.notApplicable == "" && f.unavailable == "" && slices.Equal(targetKeys(f), keys)
	}
}

func deleteCertificateCRD(t *testing.T, cfg *rest.Config) {
	t.Helper()
	ctx := t.Context()
	apiext, err := apiextclient.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	const name = "certificates.cert-manager.io"
	if err := apiext.ApiextensionsV1().CustomResourceDefinitions().Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete CRD: %v", err)
	}
	kctesting.Eventually(t, 30*time.Second, 100*time.Millisecond, func() bool {
		_, err := apiext.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, name, metav1.GetOptions{})
		return apierrors.IsNotFound(err)
	}, "the CRD was not deleted")
}

// TestOverviewFollowsAnAddOnInstalledAndRemoved runs /overview and the Info
// panel's engine on the overview's pool caches, as kc does, while
// cert-manager's CRD is installed, deleted and installed again. Each
// change shows after a discovery refresh, without a new engine.
func TestOverviewFollowsAnAddOnInstalledAndRemoved(t *testing.T) {
	t.Parallel()
	cfg := kcenvtest.Start(t)
	ctx := t.Context()
	// The pool's clusters refresh discovery every 300ms, as they do every
	// kubernetes.discovery.refresh (30s) in kc.
	pool := kccluster.NewPool(time.Minute, 300*time.Millisecond)
	pool.Start()
	t.Cleanup(pool.Stop)
	key := kccluster.Key{KubeconfigPath: writeEnvtestKubeconfig(t, cfg), ContextName: "envtest"}
	lease, err := pool.Acquire(ctx, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lease.Release)
	deps := Deps{
		Cl: lease.Cluster(), Ctx: ctx, CtxName: "envtest", AppConfig: appconfig.Default(), ClusterKey: key,
		NamespaceFactory: func(ctx context.Context, key kccluster.Key, selectors map[schema.GroupVersionResource]crcache.ByObject) (*kccluster.Cluster, func(), error) {
			l, err := pool.Acquire(ctx, key, selectors)
			if err != nil {
				return nil, nil, err
			}
			return l.Cluster(), l.Release, nil
		},
	}
	folder := NewOverviewFolder(deps, AllNamespaces, []string{"overview"})
	t.Cleanup(folder.Close)
	info := NewOverviewInfo(deps, AllNamespaces)
	t.Cleanup(info.Close)
	// previewRow is the row as the Info panel previews it beside /overview.
	previewRow := func() (overviewFinding, bool) {
		f, ok := folder.currentFinding("risks/certificates")
		if !ok {
			return f, false
		}
		p := folder.findingPreview(f)
		if p.NotApplicable != f.notApplicable || p.TargetCount != len(f.targets) {
			t.Fatalf("preview n/a %q, %d targets; row n/a %q, %d targets", p.NotApplicable, p.TargetCount, f.notApplicable, len(f.targets))
		}
		return f, true
	}
	rows := map[string]func() (overviewFinding, bool){"/overview": previewRow, "Info": engineRow(info.engine)}
	waitAll := func(what string, ok func(overviewFinding) bool) {
		t.Helper()
		for name, get := range rows {
			waitCertificateRow(t, name+": "+what, get, ok)
		}
	}

	waitAll("cert-manager not installed", notServedRow)

	installCertificateCRD(t, cfg)
	cli, err := crclient.New(cfg, crclient.Options{Scheme: overviewTestScheme})
	if err != nil {
		t.Fatal(err)
	}
	mustCreate(t, cli, &corev1.Namespace{Name: "certs"})
	mustCreate(t, cli, certificate("certs", "pending", "False", "Issuing", ""))
	waitAll("the new Certificate that is not ready", listsOnly("certificates:certs/pending"))

	deleteCertificateCRD(t, cfg)
	waitAll("cert-manager not installed after its CRD was deleted", notServedRow)

	installCertificateCRD(t, cfg)
	mustCreate(t, cli, certificate("certs", "again", "False", "Issuing", ""))
	waitAll("the Certificate of the new install, and not the old one", listsOnly("certificates:certs/again"))
}

// TestOverviewRereadRetriesANotServedKind checks C-r: with no periodic
// discovery refresh due, the row changes only when the overview is read
// again.
func TestOverviewRereadRetriesANotServedKind(t *testing.T) {
	t.Parallel()
	cfg := kcenvtest.Start(t)
	ctx := t.Context()
	cl, err := kccluster.New(cfg, kccluster.WithRefreshInterval(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	kctesting.RunInBackground(t, "cluster", cl.Start)
	t.Cleanup(cl.Stop)
	e := newOverviewEngine(Deps{Cl: cl, Ctx: ctx}, AllNamespaces)
	t.Cleanup(e.close)
	row := engineRow(e)
	waitCertificateRow(t, "cert-manager not installed", row, notServedRow)

	installCertificateCRD(t, cfg)
	cli, err := crclient.New(cfg, crclient.Options{Scheme: overviewTestScheme})
	if err != nil {
		t.Fatal(err)
	}
	mustCreate(t, cli, &corev1.Namespace{Name: "certs"})
	mustCreate(t, cli, certificate("certs", "pending", "False", "Issuing", ""))
	// Recomputes alone do not read discovery again.
	time.Sleep(3 * overviewInterval)
	if f, _ := row(); !notServedRow(f) {
		t.Fatalf("before C-r: value %q, n/a %q, targets %v; want the row unchanged", f.value, f.notApplicable, targetKeys(f))
	}
	e.reread()
	waitCertificateRow(t, "the Certificate after C-r", row, listsOnly("certificates:certs/pending"))

	deleteCertificateCRD(t, cfg)
	e.reread()
	waitCertificateRow(t, "cert-manager not installed after C-r", row, notServedRow)
}
