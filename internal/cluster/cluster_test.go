package cluster

import (
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
)

func TestClusterDiscoveryListenerNotified(t *testing.T) {
	t.Parallel()

	cfg := kcenvtest.Start(t)

	cl, err := New(cfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}

	var hits atomic.Int32
	cancel := cl.AddDiscoveryListener(func() {
		hits.Add(1)
	})

	cl.RefreshDiscovery()
	got := hits.Load()
	if got == 0 {
		t.Fatalf("expected discovery listener to fire at least once")
	}

	cancel()
	cl.RefreshDiscovery()
	if hits.Load() != got {
		t.Fatalf("listener fired after cancel: before=%d after=%d", got, hits.Load())
	}
}

func TestNamespaceScopedClusterListsOneNamespace(t *testing.T) {
	t.Parallel()

	cfg := kcenvtest.Start(t)

	cl, err := New(cfg, WithNamespaceScope("ns-a"))
	if err != nil {
		t.Fatalf("namespace-scoped cluster: %v", err)
	}
	if cl.Namespace() != "ns-a" {
		t.Fatalf("Namespace() = %q", cl.Namespace())
	}

	ctx := t.Context()
	kctesting.RunInBackground(t, "cluster", cl.Start)

	createCM := func(ns, name string) {
		t.Helper()
		cm := &corev1.ConfigMap{
			Name: name, Namespace: ns,
			Data: map[string]string{"k": "v"},
		}
		if err := cl.GetClient().Create(t.Context(), cm); err != nil {
			t.Fatalf("create configmap %s/%s: %v", ns, name, err)
		}
	}
	for _, ns := range []string{"ns-a", "ns-b"} {
		if err := cl.GetClient().Create(t.Context(), &corev1.Namespace{Name: ns}); err != nil && !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("create namespace %s: %v", ns, err)
		}
	}
	createCM("ns-a", "only-a")
	createCM("ns-b", "only-b")

	names := func(list *unstructured.UnstructuredList) []string {
		var out []string
		for _, item := range list.Items {
			out = append(out, item.GetNamespace()+"/"+item.GetName())
		}
		return out
	}
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	deadline := time.Now().Add(5 * time.Second)
	var got []string
	for time.Now().Before(deadline) {
		list, err := cl.ListByGVR(ctx, gvr, "")
		if err != nil {
			t.Fatalf("list configmaps: %v", err)
		}
		got = names(list)
		if slices.Contains(got, "ns-a/only-a") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !slices.Contains(got, "ns-a/only-a") || slices.Contains(got, "ns-b/only-b") {
		t.Fatalf("namespace-scoped list = %v, want ns-a/only-a and nothing from ns-b", got)
	}

	// A cluster-scoped type is not rewritten into the namespace: the
	// cache lists it cluster-wide.
	nsList, err := cl.ListByGVR(ctx, schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, "")
	if err != nil {
		t.Fatalf("list namespaces through a namespace-scoped cluster: %v", err)
	}
	if got := names(nsList); !slices.Contains(got, "/ns-a") || !slices.Contains(got, "/ns-b") {
		t.Fatalf("namespaces = %v, want ns-a and ns-b", got)
	}
}
