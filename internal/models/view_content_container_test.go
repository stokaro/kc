package models

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// containerLookupPod is a typed pod with one entry of each container kind,
// including the names kc gives its own debug helpers. The lookup tests
// convert it to the unstructured shape the API server returns, so they
// cannot encode a wrong idea of where an entry keeps its name.
func containerLookupPod() *corev1.Pod {
	return &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "example.com/app:1"},
				{Name: "sidecar", Image: "example.com/sidecar:1"},
			},
			InitContainers: []corev1.Container{
				{Name: "migrate", Image: "example.com/migrate:1"},
			},
			EphemeralContainers: []corev1.EphemeralContainer{
				{
					EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debugger", Image: "busybox:1.36"},
					TargetContainerName:      "app",
				},
				{
					EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "kc-fs-helper-app-0", Image: "busybox:1.36"},
					TargetContainerName:      "app",
				},
				{
					EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "kc-fs-ptrace-app-1", Image: "busybox:1.36"},
					TargetContainerName:      "app",
				},
			},
		},
	}
}

func toUnstructuredPod(t *testing.T, pod *corev1.Pod) map[string]interface{} {
	t.Helper()
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pod)
	if err != nil {
		t.Fatalf("convert pod: %v", err)
	}
	return obj
}

func TestFindContainer(t *testing.T) {
	t.Parallel()

	obj := toUnstructuredPod(t, containerLookupPod())

	tests := []struct {
		name      string
		kind      containerKind
		container string
		wantImage string // empty means the lookup must find nothing
		wantTgt   string // targetContainerName, only for ephemeral entries
	}{
		{name: "regular", kind: containerKindPrimary, container: "app", wantImage: "example.com/app:1"},
		{name: "second regular", kind: containerKindPrimary, container: "sidecar", wantImage: "example.com/sidecar:1"},
		{name: "init", kind: containerKindInit, container: "migrate", wantImage: "example.com/migrate:1"},
		{name: "ephemeral", kind: containerKindEphemeral, container: "debugger", wantImage: "busybox:1.36", wantTgt: "app"},
		{name: "kc fs helper", kind: containerKindEphemeral, container: "kc-fs-helper-app-0", wantImage: "busybox:1.36", wantTgt: "app"},
		{name: "kc ptrace helper", kind: containerKindEphemeral, container: "kc-fs-ptrace-app-1", wantImage: "busybox:1.36", wantTgt: "app"},
		{name: "missing regular", kind: containerKindPrimary, container: "nope"},
		{name: "missing ephemeral", kind: containerKindEphemeral, container: "nope"},
		{name: "regular name under init", kind: containerKindInit, container: "app"},
		{name: "regular name under ephemeral", kind: containerKindEphemeral, container: "app"},
		{name: "ephemeral name under regular", kind: containerKindPrimary, container: "debugger"},
		{name: "init name under regular", kind: containerKindPrimary, container: "migrate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := findContainer(obj, tt.kind, tt.container)
			if tt.wantImage == "" {
				if got != nil {
					t.Fatalf("findContainer(%v, %q) = %v, want nil", tt.kind, tt.container, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("findContainer(%v, %q) = nil, want entry with image %q", tt.kind, tt.container, tt.wantImage)
			}
			if n, _ := got["name"].(string); n != tt.container {
				t.Fatalf("name = %q, want %q", n, tt.container)
			}
			if img, _ := got["image"].(string); img != tt.wantImage {
				t.Fatalf("image = %q, want %q", img, tt.wantImage)
			}
			if tgt, _ := got["targetContainerName"].(string); tgt != tt.wantTgt {
				t.Fatalf("targetContainerName = %q, want %q", tgt, tt.wantTgt)
			}
		})
	}
}

// TestFindContainerIgnoresMetadataName pins that the lookup reads the
// top-level name only. An entry that carries the name under metadata (the
// shape the old lookup expected) is not a container of that name.
func TestFindContainerIgnoresMetadataName(t *testing.T) {
	t.Parallel()

	obj := map[string]interface{}{
		"spec": map[string]interface{}{
			"ephemeralContainers": []interface{}{
				map[string]interface{}{
					"metadata": map[string]interface{}{"name": "ghost"},
					"image":    "busybox:1.36",
				},
			},
		},
	}
	if got := findContainer(obj, containerKindEphemeral, "ghost"); got != nil {
		t.Fatalf("findContainer matched metadata.name: %v", got)
	}
}

func TestFindContainerWithoutList(t *testing.T) {
	t.Parallel()

	obj := toUnstructuredPod(t, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "bare"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	})
	for _, kind := range []containerKind{containerKindInit, containerKindEphemeral} {
		if got := findContainer(obj, kind, "app"); got != nil {
			t.Fatalf("findContainer(%v, app) on a pod without that list = %v, want nil", kind, got)
		}
	}
}
