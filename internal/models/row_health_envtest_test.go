package models

import (
	"maps"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	kccluster "github.com/stokaro/kc/internal/cluster"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/pkg/appconfig"
)

// A server table row carries metadata only, so the health of a pod must
// come from its full object. Pods whose containers wait for an image or
// crash are faults: C-z keeps them and they render as not ready, while a
// running, ready pod is neither kept nor colored.
func TestPodRowHealthFromServerTable(t *testing.T) {
	t.Parallel()
	kcenvtest.Require(t, testCfg)
	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("kccluster: %v", err)
	}
	ctx := t.Context()
	kctesting.RunInBackground(t, "cluster", cl.Start)
	t.Cleanup(cl.Stop)
	ns := &corev1.Namespace{Name: kctesting.UniqueName("row-health")}
	mustCreate(t, cl.GetClient(), ns)

	waiting := func(phase corev1.PodPhase, reason string) corev1.PodStatus {
		return corev1.PodStatus{Phase: phase, ContainerStatuses: []corev1.ContainerStatus{{
			Name: "c", Image: "example.invalid/app:1",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}},
		}}}
	}
	statuses := map[string]corev1.PodStatus{
		"pull-backoff":  waiting(corev1.PodPending, "ImagePullBackOff"),
		"pull-error":    waiting(corev1.PodPending, "ErrImagePull"),
		"crash-loop":    waiting(corev1.PodRunning, "CrashLoopBackOff"),
		"config-error":  waiting(corev1.PodPending, "CreateContainerConfigError"),
		"running-ready": {Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}, ContainerStatuses: []corev1.ContainerStatus{{Name: "c", Image: "example.invalid/app:1", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}},
	}
	for name, st := range statuses {
		p := &corev1.Pod{Name: name, Namespace: ns.Name, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "example.invalid/app:1"}}}}
		mustCreate(t, cl.GetClient(), p)
		p.Status = st
		if err := cl.GetClient().Status().Update(ctx, p); err != nil {
			t.Fatalf("update status of %s: %v", name, err)
		}
	}

	deps := Deps{
		Cl: cl, Ctx: ctx, CtxName: "envtest",
		KubeConfig: clientcmdapi.Config{CurrentContext: "envtest", Contexts: map[string]*clientcmdapi.Context{"envtest": {Namespace: ns.Name}}},
		AppConfig:  appconfig.Default(),
	}
	folder := NewNamespacedObjectsFolder(deps, podGVR, ns.Name, []string{"namespaces", ns.Name, "pods"}, nil)
	t.Cleanup(folder.Close)

	faulty := []string{"config-error", "crash-loop", "pull-backoff", "pull-error"}
	type seen struct {
		health  RowHealth
		colored bool
	}
	read := func() map[string]seen {
		out := make(map[string]seen)
		for _, r := range folder.Lines(ctx, 0, folder.Len(ctx)) {
			c, ok := r.(rowFactsCarrier)
			if !ok {
				continue
			}
			facts, ok := c.rowFacts()
			if !ok {
				continue
			}
			_, _, styles, _ := r.Columns()
			out[facts.Name] = seen{health: facts.Health, colored: len(styles) > 0 && styles[0] == NotReadyStyle()}
		}
		return out
	}
	var got map[string]seen
	kctesting.Eventually(t, 10*time.Second, 50*time.Millisecond, func() bool {
		got = read()
		if len(got) != len(statuses) {
			return false
		}
		for _, name := range faulty {
			if !got[name].health.Faulty() {
				return false
			}
		}
		return true
	})
	for _, name := range faulty {
		if s := got[name]; !s.health.Faulty() || !s.colored {
			t.Errorf("pod %s: health %v, colored %v; want a fault rendered as not ready", name, s.health, s.colored)
		}
	}
	if s := got["running-ready"]; s.health != RowHealthOK || s.colored {
		t.Errorf("running-ready pod: health %v, colored %v; want ok and uncolored", s.health, s.colored)
	}

	folder.SetRowFilter(RowFilter{FaultsOnly: true})
	kept := slices.Sorted(maps.Keys(read()))
	if !slices.Equal(kept, faulty) {
		t.Errorf("faults only kept %v, want %v", kept, faulty)
	}
}
