package cluster

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// writeKubeconfig writes cfg as a kubeconfig with one context named "envtest".
func writeKubeconfig(t *testing.T, cfg *rest.Config) string {
	t.Helper()
	kc := clientcmdapi.NewConfig()
	kc.Clusters["envtest"] = &clientcmdapi.Cluster{Server: cfg.Host, CertificateAuthorityData: cfg.CAData}
	kc.AuthInfos["envtest"] = &clientcmdapi.AuthInfo{ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData, Token: cfg.BearerToken}
	kc.Contexts["envtest"] = &clientcmdapi.Context{Cluster: "envtest", AuthInfo: "envtest"}
	kc.CurrentContext = "envtest"
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*kc, path); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}

// TestPoolHeldNodeScopeStaysLivePastTTL is the scenario of issue #62: a node's
// pod cache held by an open folder keeps delivering additions, updates and
// deletions after the TTL has passed, and an evicted scope comes back running.
func TestPoolHeldNodeScopeStaysLivePastTTL(t *testing.T) {
	t.Parallel()

	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		if strings.Contains(err.Error(), "operation not permitted") || strings.Contains(err.Error(), "no such file or directory") {
			t.Skipf("envtest unavailable: %v", err)
		}
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	admin, err := crclient.New(cfg, crclient.Options{})
	if err != nil {
		t.Fatalf("admin client: %v", err)
	}
	ctx := t.Context()

	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pool := NewPool(testTTL, time.Minute)
	pool.now = func() time.Time { return clock }
	t.Cleanup(pool.Stop)

	key := Key{KubeconfigPath: writeKubeconfig(t, cfg), ContextName: "envtest"}
	podsGVR := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	selectors := map[schema.GroupVersionResource]cache.ByObject{
		podsGVR: {Field: fields.OneTermEqualSelector("spec.nodeName", "node-a")},
	}

	createPod := func(name, node string) {
		t.Helper()
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: corev1.PodSpec{
				NodeName:   node,
				Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
			},
		}
		if err := admin.Create(ctx, pod); err != nil {
			t.Fatalf("create pod %s: %v", name, err)
		}
	}
	// waitPods waits until the cache of cl lists exactly want (by name) and
	// check accepts the listed pods.
	waitPods := func(cl *Cluster, what string, want []string, check func(names map[string]map[string]string) bool) {
		t.Helper()
		var seen []string
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			list, err := cl.ListByGVR(ctx, podsGVR, "default")
			if err == nil {
				seen = seen[:0]
				labels := map[string]map[string]string{}
				for _, u := range list.Items {
					seen = append(seen, u.GetName())
					labels[u.GetName()] = u.GetLabels()
				}
				slices.Sort(seen)
				if slices.Equal(seen, want) && (check == nil || check(labels)) {
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("%s: node-a pod cache lists %v, want %v", what, seen, want)
	}

	lease, err := pool.Acquire(ctx, key, selectors)
	if err != nil {
		t.Fatalf("acquire node scope: %v", err)
	}
	first := lease.Cluster()
	createPod("on-a-1", "node-a")
	createPod("on-b", "node-b")
	waitPods(first, "initial sync", []string{"on-a-1"}, nil)

	// The folder stays open far beyond the TTL.
	clock = clock.Add(10 * testTTL)
	pool.evictIdle()

	createPod("on-a-2", "node-a")
	waitPods(first, "addition after the TTL", []string{"on-a-1", "on-a-2"}, nil)

	var pod corev1.Pod
	if err := admin.Get(ctx, crclient.ObjectKey{Namespace: "default", Name: "on-a-2"}, &pod); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	pod.Labels = map[string]string{"updated": "yes"}
	if err := admin.Update(ctx, &pod); err != nil {
		t.Fatalf("update pod: %v", err)
	}
	waitPods(first, "update after the TTL", []string{"on-a-1", "on-a-2"}, func(l map[string]map[string]string) bool {
		return l["on-a-2"]["updated"] == "yes"
	})

	gone := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "on-a-1", Namespace: "default"}}
	if err := admin.Delete(ctx, gone, crclient.GracePeriodSeconds(0)); err != nil {
		t.Fatalf("delete pod: %v", err)
	}
	// A zero grace period removes the pod at once; envtest runs no kubelet.
	waitPods(first, "deletion after the TTL", []string{"on-a-2"}, nil)

	// The folder closes; after the TTL the scope is evicted.
	lease.Release()
	clock = clock.Add(testTTL + time.Second)
	pool.evictIdle()
	pool.mu.Lock()
	_, present := pool.items[lease.Key()]
	pool.mu.Unlock()
	if present {
		t.Fatalf("node scope still in the pool after its last release and the TTL")
	}

	// Reopening the node's pods obtains a new, running cache.
	again, err := pool.Acquire(ctx, key, selectors)
	if err != nil {
		t.Fatalf("reacquire node scope: %v", err)
	}
	defer again.Release()
	if again.Cluster() == first {
		t.Fatalf("reacquire after eviction returned the stopped cluster")
	}
	createPod("on-a-3", "node-a")
	waitPods(again.Cluster(), "reopened scope", []string{"on-a-2", "on-a-3"}, nil)
}
