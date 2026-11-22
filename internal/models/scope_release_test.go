package models

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"

	kccluster "github.com/stokaro/kc/internal/cluster"
)

// TestNodePodsFolderHoldsScopeUntilClose checks that the node's pods folder
// owns the selector-scoped cluster it reads and gives it up exactly once, on
// Close (issue #62).
func TestNodePodsFolderHoldsScopeUntilClose(t *testing.T) {
	t.Parallel()
	scoped := &kccluster.Cluster{}
	acquired, released := 0, 0
	deps := Deps{
		Ctx:        t.Context(),
		ClusterKey: kccluster.Key{KubeconfigPath: "/tmp/kubeconfig", ContextName: "ctx"},
		NamespaceFactory: func(context.Context, kccluster.Key, map[schema.GroupVersionResource]crcache.ByObject) (*kccluster.Cluster, func(), error) {
			acquired++
			return scoped, func() { released++ }, nil
		},
	}

	folder := NewNodePodsFolder(deps, []string{"nodes", "node-a", "pods"}, "node-a")
	if acquired != 1 || folder.Deps.Cl != scoped {
		t.Fatalf("folder acquired %d scopes and reads %p; want 1 and the scoped cluster %p", acquired, folder.Deps.Cl, scoped)
	}
	if released != 0 {
		t.Fatalf("scope released %d times before Close", released)
	}
	folder.Close()
	folder.Close()
	if released != 1 {
		t.Fatalf("scope released %d times after two Close calls, want 1", released)
	}
}

func TestDepsScopeReleaseIsNeverNil(t *testing.T) {
	t.Parallel()
	scope := map[schema.GroupVersionResource]crcache.ByObject{
		{Version: "v1", Resource: "pods"}: {Field: fields.OneTermEqualSelector("spec.nodeName", "node-a")},
	}
	failing := func(context.Context, kccluster.Key, map[schema.GroupVersionResource]crcache.ByObject) (*kccluster.Cluster, func(), error) {
		return nil, nil, context.Canceled
	}
	selector := func(sel map[schema.GroupVersionResource]crcache.ByObject) func(Deps) func() {
		return func(d Deps) func() { _, r := d.ForSelectorScope(sel); return r }
	}
	namespace := func(d Deps) func() { _, r := d.ForNamespace("ns"); return r }
	cases := []struct {
		name string
		deps Deps
		call func(Deps) func()
	}{
		{"selector without factory", Deps{Ctx: t.Context()}, selector(scope)},
		{"selector with failing factory", Deps{Ctx: t.Context(), NamespaceFactory: failing}, selector(scope)},
		{"empty selector", Deps{Ctx: t.Context(), NamespaceFactory: failing}, selector(nil)},
		{"namespace without factory", Deps{Ctx: t.Context()}, namespace},
		{"namespace with failing factory", Deps{Ctx: t.Context(), NamespaceFactory: failing}, namespace},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			release := tc.call(tc.deps)
			if release == nil {
				t.Fatalf("release is nil")
			}
			release()
		})
	}
}
