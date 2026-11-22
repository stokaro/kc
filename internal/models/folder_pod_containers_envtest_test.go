package models

import (
	"context"
	"strings"
	"testing"
	"time"

	kccluster "github.com/sttts/kc/internal/cluster"
	"github.com/sttts/kc/pkg/appconfig"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// TestContainerItemsViewTheirOwnSpec drives the rows of each container list
// of a pod with regular, init and ephemeral containers, as F3 and F5 do: both
// read the row's ViewContent. Ephemeral containers are added through the
// ephemeralcontainers subresource, as kc adds its debug helpers.
func TestContainerItemsViewTheirOwnSpec(t *testing.T) {
	if testCfg == nil {
		t.Skip("envtest not available")
	}
	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	ctx := t.Context()
	go cl.Start(ctx)
	t.Cleanup(cl.Stop)

	ns := "pod-container-view"
	createNamespace(t, cl, ns)
	createPodWithEphemeralContainers(t, cl, ns, "web")

	deps := Deps{Cl: cl, Ctx: ctx, AppConfig: appconfig.Default()}

	tests := []struct {
		name      string
		kind      containerKind
		container string
		wantImage string
		wantField string
	}{
		{name: "regular", kind: containerKindPrimary, container: "app", wantImage: "example.com/app:1", wantField: "containers"},
		{name: "init", kind: containerKindInit, container: "migrate", wantImage: "example.com/migrate:1", wantField: "initContainers"},
		{name: "ephemeral", kind: containerKindEphemeral, container: "debugger", wantImage: "busybox:1.36", wantField: "ephemeralContainers"},
		{name: "kc fs helper", kind: containerKindEphemeral, container: "kc-fs-helper-app-0", wantImage: "busybox:1.36", wantField: "ephemeralContainers"},
		{name: "kc ptrace helper", kind: containerKindEphemeral, container: "kc-fs-ptrace-app-0", wantImage: "busybox:1.36", wantField: "ephemeralContainers"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			folder := NewPodContainerListFolder(deps, []string{"namespaces", ns, "pods", "web", "containers", "x"}, ns, "web", tt.kind)
			item := waitContainerItem(t, folder, tt.container)

			title, body, lang, mime, filename, err := item.ViewContent()
			if err != nil {
				t.Fatalf("ViewContent(%s): %v", tt.container, err)
			}
			if title != "web/"+tt.container {
				t.Fatalf("title = %q, want %q", title, "web/"+tt.container)
			}
			if filename != tt.container+".yaml" || lang != "yaml" || mime != "application/yaml" {
				t.Fatalf("filename, lang, mime = %q, %q, %q", filename, lang, mime)
			}
			var got struct {
				Name  string `json:"name"`
				Image string `json:"image"`
			}
			if err := yaml.Unmarshal([]byte(body), &got); err != nil {
				t.Fatalf("decode body: %v\n%s", err, body)
			}
			if got.Name != tt.container || got.Image != tt.wantImage {
				t.Fatalf("body is %q with image %q, want %q with image %q:\n%s", got.Name, got.Image, tt.container, tt.wantImage, body)
			}

			wantSelector := `{.spec.` + tt.wantField + `[?(@.name=="` + tt.container + `")]}`
			if cmd := item.ViewCommand(); !strings.Contains(cmd, wantSelector) {
				t.Fatalf("view command %q does not select %s", cmd, wantSelector)
			}
		})
	}
}

// waitContainerItem waits for the folder to list a row for name and fails
// with the rows it saw last when it never does.
func waitContainerItem(t *testing.T, folder *PodContainerListFolder, name string) *ContainerItem {
	t.Helper()
	ctx := t.Context()
	var seen []string
	deadline := time.Now().Add(10 * time.Second)
	for {
		seen = seen[:0]
		for _, r := range folder.Lines(ctx, 0, folder.Len(ctx)) {
			item, ok := r.(*ContainerItem)
			if !ok {
				continue
			}
			id, _, _, _ := item.Columns()
			if id == name {
				return item
			}
			seen = append(seen, id)
		}
		if time.Now().After(deadline) {
			t.Fatalf("container row %q not listed within 10s; rows seen: %v", name, seen)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func createPodWithEphemeralContainers(t *testing.T, cl *kccluster.Cluster, namespace, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "app", Image: "example.com/app:1"}},
			InitContainers: []corev1.Container{{Name: "migrate", Image: "example.com/migrate:1"}},
		},
	}
	c := cl.GetClient()
	if err := c.Create(ctx, pod); err != nil {
		t.Fatalf("create pod %s/%s: %v", namespace, name, err)
	}
	t.Cleanup(func() {
		delCtx, cancelDel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelDel()
		_ = c.Delete(delCtx, pod)
	})
	for _, n := range []string{"debugger", "kc-fs-helper-app-0", "kc-fs-ptrace-app-0"} {
		pod.Spec.EphemeralContainers = append(pod.Spec.EphemeralContainers, corev1.EphemeralContainer{
			EphemeralContainerCommon: corev1.EphemeralContainerCommon{
				Name:                     n,
				Image:                    "busybox:1.36",
				TerminationMessagePolicy: corev1.TerminationMessageReadFile,
				ImagePullPolicy:          corev1.PullIfNotPresent,
			},
			TargetContainerName: "app",
		})
	}
	if err := c.SubResource("ephemeralcontainers").Update(ctx, pod); err != nil {
		t.Fatalf("add ephemeral containers to %s/%s: %v", namespace, name, err)
	}
}
