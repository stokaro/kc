package logs_test

import (
	"testing"

	models "github.com/sttts/kc/internal/models"
	logswidget "github.com/sttts/kc/internal/ui/panelcontent/logs"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	podsGVR        = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	deploymentsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
)

func podItem(ns, name string) models.Item {
	return models.NewObjectRow(name, []string{name}, []string{"namespaces", ns, "pods", name}, podsGVR, ns, name, nil)
}

func containerItem(ns, pod, container string) models.Item {
	item := models.NewContainerItem(container, []string{"/" + container}, []string{"namespaces", ns, "pods", pod, "containers", container}, nil, nil, nil)
	item.SetPod(ns, pod)
	return item
}

func TestTarget(t *testing.T) {
	t.Parallel()
	tail := models.DefaultLogsTailLines
	tests := []struct {
		name string
		item models.Item
		want models.LogsSpec
		ok   bool
	}{
		{name: "nil", item: nil},
		{
			name: "pod",
			item: podItem("ns1", "web"),
			want: models.LogsSpec{Namespace: "ns1", Pod: "web", Follow: true, TailLines: tail},
			ok:   true,
		},
		{
			name: "container row",
			item: containerItem("ns1", "web", "sidecar"),
			want: models.LogsSpec{Namespace: "ns1", Pod: "web", Container: "sidecar", Follow: true, TailLines: tail},
			ok:   true,
		},
		{
			name: "container log row",
			item: models.NewContainerLogItem(models.Deps{}, "logs_latest", []string{"logs"}, nil, models.LogsSpec{Namespace: "ns1", Pod: "web", Container: "app", TailLines: 50}),
			want: models.LogsSpec{Namespace: "ns1", Pod: "web", Container: "app", Follow: true, TailLines: 50},
			ok:   true,
		},
		{name: "container row without pod", item: models.NewContainerItem("app", []string{"/app"}, nil, nil, nil, nil)},
		{name: "deployment", item: models.NewObjectRow("d", []string{"d"}, nil, deploymentsGVR, "ns1", "d", nil)},
		{name: "pods of another group", item: models.NewObjectRow("p", []string{"p"}, nil, schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}, "ns1", "p", nil)},
		{name: "plain row", item: models.NewRowItem("x", []string{"x"}, nil, nil)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := logswidget.Target(tc.item)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("Target() = %+v, %v; want %+v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestDefaultContainer(t *testing.T) {
	t.Parallel()
	pod := func(annotation string, names ...string) *corev1.Pod {
		p := &corev1.Pod{}
		if annotation != "" {
			p.ObjectMeta = metav1.ObjectMeta{Annotations: map[string]string{logswidget.DefaultContainerAnnotation: annotation}}
		}
		for _, n := range names {
			p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: n})
		}
		return p
	}
	tests := []struct {
		name string
		pod  *corev1.Pod
		want string
	}{
		{name: "nil pod", pod: nil, want: ""},
		{name: "no containers", pod: pod(""), want: ""},
		{name: "first container", pod: pod("", "app", "sidecar"), want: "app"},
		{name: "annotation", pod: pod("sidecar", "app", "sidecar"), want: "sidecar"},
		{name: "annotation names a missing container", pod: pod("gone", "app", "sidecar"), want: "app"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := logswidget.DefaultContainer(tc.pod); got != tc.want {
				t.Fatalf("DefaultContainer() = %q, want %q", got, tc.want)
			}
		})
	}
}
