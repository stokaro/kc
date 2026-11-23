package models

import (
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	kctesting "github.com/stokaro/kc/internal/testing"
)

// WatchObject reports the instances under one name from the shared
// informer: the current one first, a deletion, and the new UID of a
// recreation. Other names in the namespace are not reported.
func TestWatchObjectEnvtest(t *testing.T) {
	t.Parallel()
	if testCfg == nil {
		t.Skip("envtest unavailable")
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	cli, err := crclient.New(testCfg, crclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx := t.Context()
	ns := kctesting.UniqueName("objectwatch")
	mustCreate(t, cli, &corev1.Namespace{Name: ns})
	first := &corev1.ConfigMap{Namespace: ns, Name: "pinned"}
	mustCreate(t, cli, first)
	mustCreate(t, cli, &corev1.ConfigMap{Namespace: ns, Name: "other"})

	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	kctesting.RunInBackground(t, "cluster", cl.Start)

	var mu sync.Mutex
	var got []ObjectSighting
	stop, err := WatchObject(Deps{Cl: cl, Ctx: ctx}, schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, ns, "pinned", func(s ObjectSighting) {
		mu.Lock()
		got = append(got, s)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer stop()
	waitFor := func(what string, ok func([]ObjectSighting) bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			mu.Lock()
			seen := append([]ObjectSighting(nil), got...)
			mu.Unlock()
			if ok(seen) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("waiting for %s, got %+v", what, seen)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	last := func(want ObjectSighting) func([]ObjectSighting) bool {
		return func(s []ObjectSighting) bool { return len(s) > 0 && s[len(s)-1] == want }
	}
	waitFor("the current instance", last(ObjectSighting{UID: first.UID}))

	if err := cli.Delete(ctx, first); err != nil {
		t.Fatalf("delete: %v", err)
	}
	waitFor("the deletion", last(ObjectSighting{UID: first.UID, Gone: true}))
	second := &corev1.ConfigMap{Namespace: ns, Name: "pinned"}
	mustCreate(t, cli, second)
	waitFor("the new instance", last(ObjectSighting{UID: second.UID}))
	if second.UID == first.UID {
		t.Fatal("the recreated object kept its UID")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, s := range got {
		if s.UID != first.UID && s.UID != second.UID {
			t.Fatalf("a sighting of another object: %+v", s)
		}
	}
}
