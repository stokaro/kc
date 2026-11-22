package models

import (
	"context"
	"fmt"
	"strings"

	kccluster "github.com/sttts/kc/internal/cluster"
	"github.com/sttts/kc/internal/podfs"
	"github.com/sttts/kc/pkg/appconfig"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	crlog "sigs.k8s.io/controller-runtime/pkg/log"
)

// NamespaceClusterFactory provides namespace- or selector-scoped clusters on
// demand. The cluster keeps running until release is called, so the caller
// owns release and must call it exactly when it stops using the cluster
// (calling it more than once is safe). See docs/adr/0062-cache-ownership.md.
type NamespaceClusterFactory func(ctx context.Context, key kccluster.Key, selectors map[schema.GroupVersionResource]crcache.ByObject) (cl *kccluster.Cluster, release func(), err error)

// noRelease is the release of a Deps that holds no scoped cluster.
func noRelease() {}

// Deps contains the inputs required by navigation folders.
// Invariants:
//   - Cl is non-nil and already started.
//   - Ctx is non-nil and used for informer/list operations.
//   - CtxName is the human-facing context label (may be empty for cluster-scoped views).
//   - KubeConfig always contains the discovered contexts (never nil maps).
//   - AppConfig is non-nil and already validated by appconfig loading.
//
// Callers must uphold these invariants; downstream code does not nil-check these fields.
type Deps struct {
	Cl      *kccluster.Cluster
	Ctx     context.Context
	CtxName string
	// KubectlContext is the context a plain kubectl call uses (the current
	// context of the default kubeconfig). Commands shown to the user add
	// --context when CtxName differs from it.
	KubectlContext   string
	KubeConfig       clientcmdapi.Config
	AppConfig        *appconfig.Config
	ClusterKey       kccluster.Key
	NamespaceFactory NamespaceClusterFactory
	PodFSFactory     podfs.Factory
	// ReportError shows a read failure to the user (a red status message).
	// It may be nil in tests and is safe to call from any goroutine.
	ReportError func(error)
	// Activity pauses background count refreshes; nil never pauses.
	Activity *Activity
	// Usage adds CPU and memory columns to pod and node lists; nil adds none.
	Usage *UsageMetrics
}

// reportReadError reports that reading what failed.
func (d Deps) reportReadError(what string, err error) {
	if d.ReportError == nil || err == nil {
		return
	}
	d.ReportError(fmt.Errorf("could not read %s: %w", what, err))
}

// ForNamespace derives a copy of Deps using a namespace-scoped cluster when
// available. The returned release gives up that cluster; the caller owns it
// and calls it when the folder built from the Deps is closed. release is never
// nil.
func (d Deps) ForNamespace(namespace string) (Deps, func()) {
	ns := strings.TrimSpace(namespace)
	if ns == "" || d.NamespaceFactory == nil {
		d.ClusterKey.Namespace = ns
		return d, noRelease
	}
	ctx := d.Ctx
	key := d.ClusterKey
	key.Namespace = ns
	cl, release, err := d.NamespaceFactory(ctx, key, nil)
	if err != nil {
		crlog.FromContext(ctx).Error(err, "namespace cluster acquisition failed", "namespace", ns)
		return d, noRelease
	}
	d.Cl = cl
	d.ClusterKey = key
	return d, release
}

// ForSelectorScope derives a copy of Deps using a selector-scoped cluster when
// available. The returned release gives up that cluster; the caller owns it
// and calls it when the folder built from the Deps is closed. release is never
// nil.
func (d Deps) ForSelectorScope(selectors map[schema.GroupVersionResource]crcache.ByObject) (Deps, func()) {
	if len(selectors) == 0 {
		return d, noRelease
	}
	key := d.ClusterKey
	key.SelectorKey = kccluster.SelectorKeyForScopes(selectors)
	if d.NamespaceFactory == nil {
		d.ClusterKey = key
		return d, noRelease
	}
	cl, release, err := d.NamespaceFactory(d.Ctx, key, selectors)
	if err != nil {
		crlog.FromContext(d.Ctx).Error(err, "selector-scoped cluster acquisition failed", "selectorKey", key.SelectorKey)
		d.ClusterKey = key
		return d, noRelease
	}
	d.Cl = cl
	d.ClusterKey = key
	return d, release
}
