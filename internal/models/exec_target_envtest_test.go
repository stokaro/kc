package models

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/pkg/appconfig"
)

// A pod row reads the pod through the folder's cluster and opens the
// container the default-container annotation names.
func TestResolveExecTargetForPodRow(t *testing.T) {
	if testCfg == nil {
		t.Skip("envtest not available")
	}
	ctx := t.Context()
	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	go cl.Start(ctx)
	t.Cleanup(cl.Stop)

	const ns = "exec-target"
	createNamespace(t, cl, ns)
	pod := &corev1.Pod{
		Namespace:   ns,
		Name:        "web-0",
		Annotations: map[string]string{DefaultContainerAnnotation: "app"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "proxy", Image: "registry.k8s.io/pause:3.9"},
			{Name: "app", Image: "registry.k8s.io/pause:3.9", Stdin: true, TTY: true},
		}},
	}
	createCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := cl.GetClient().Create(createCtx, pod); err != nil {
		t.Fatalf("create pod: %v", err)
	}

	deps := Deps{
		Cl:         cl,
		Ctx:        ctx,
		CtxName:    "envtest",
		ClusterKey: kccluster.Key{ContextName: "envtest", ImpersonateUser: "alice"},
		AppConfig:  appconfig.Default(),
	}
	folder := NewBaseFolder(deps, nil, []string{"namespaces", ns, "pods"})
	row := NewObjectRow("web-0", []string{"web-0"}, nil, podGVR, ns, "web-0", nil)

	got, err := ResolveExecTarget(ctx, folder, row)
	if err != nil {
		t.Fatalf("ResolveExecTarget: %v", err)
	}
	if got.Container != "app" || !got.Stdin || !got.TTY || !got.Defaulted || got.Cluster.ImpersonateUser != "alice" {
		t.Fatalf("target = %+v, want the annotated container app with the panel's identity", got)
	}

	missing := NewObjectRow("gone", []string{"gone"}, nil, podGVR, ns, "gone", nil)
	if _, err := ResolveExecTarget(ctx, folder, missing); err == nil {
		t.Fatalf("missing pod: want an error")
	}
}
