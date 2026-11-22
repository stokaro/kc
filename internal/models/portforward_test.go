package models

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/stokaro/kc/internal/portforward"
	table "github.com/stokaro/kc/internal/table"
)

func TestPortForwardTargetFor(t *testing.T) {
	t.Parallel()
	podRow := NewObjectRow("p", []string{"web-0"}, nil, gvrPods, "ns", "web-0", nil)
	svcRow := NewObjectRow("s", []string{"web"}, nil, gvrServices, "ns", "web", nil)
	deployRow := NewObjectRow("d", []string{"web"}, nil, schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, "ns", "web", nil)
	// Same resource name in another group is not a core pod.
	fakePods := NewObjectRow("f", []string{"x"}, nil, schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}, "ns", "x", nil)
	container := NewContainerItem("app", []string{"app"}, nil, nil, nil, nil)
	container.SetPod("ns", "web-0")
	orphan := NewContainerItem("app", []string{"app"}, nil, nil, nil, nil)

	tests := []struct {
		name string
		item Item
		want PortForwardTarget
		ok   bool
	}{
		{name: "pod", item: podRow, want: PortForwardTarget{Namespace: "ns", Kind: "pod", Name: "web-0"}, ok: true},
		{name: "service", item: svcRow, want: PortForwardTarget{Namespace: "ns", Kind: "service", Name: "web"}, ok: true},
		{name: "container", item: container, want: PortForwardTarget{Namespace: "ns", Kind: "pod", Name: "web-0", Container: "app"}, ok: true},
		{name: "deployment refused", item: deployRow},
		{name: "pods of another group refused", item: fakePods},
		{name: "container without pod refused", item: orphan},
		{name: "back row refused", item: BackItem{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := PortForwardTargetFor(tt.item)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("PortForwardTargetFor = %+v, %v; want %+v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func runningPod(name string, labels map[string]string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", Labels: labels},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name:  "app",
			Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
}

func TestResolvePortForward(t *testing.T) {
	t.Parallel()
	deps := Deps{CtxName: "prod", KubectlContext: "dev"}
	pod := runningPod("web-0", map[string]string{"app": "web"})
	pending := runningPod("web-1", map[string]string{"app": "web"})
	pending.Status.Phase = corev1.PodPending
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "ns"},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "web"},
			Ports:    []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromString("http")}},
		},
	}
	podTarget := PortForwardTarget{Namespace: "ns", Kind: "pod", Name: "web-0", Container: "app"}
	svcTarget := PortForwardTarget{Namespace: "ns", Kind: "service", Name: "web"}

	tests := []struct {
		name    string
		target  PortForwardTarget
		value   string
		address string
		pod     *corev1.Pod
		svc     *corev1.Service
		pods    []corev1.Pod
		want    portforward.Spec
		wantErr string
	}{
		{
			name: "pod with numbers", target: podTarget, value: "9000:8080", address: "127.0.0.1", pod: &pod,
			want: portforward.Spec{Context: "prod", Namespace: "ns", Kind: "pod", Name: "web-0", Pod: "web-0", Container: "app", Address: "127.0.0.1", LocalPort: 9000, RemotePort: 8080,
				Command: "kubectl --context prod port-forward -n ns pod/web-0 9000:8080 --address 127.0.0.1"},
		},
		{
			name: "pod with a port name keeps the port locally", target: podTarget, value: "http", address: "localhost", pod: &pod,
			want: portforward.Spec{Context: "prod", Namespace: "ns", Kind: "pod", Name: "web-0", Pod: "web-0", Container: "app", Address: "localhost", LocalPort: 8080, RemotePort: 8080,
				Command: "kubectl --context prod port-forward -n ns pod/web-0 http"},
		},
		{
			name: "pending pod refused", target: podTarget, value: "8080", address: "127.0.0.1", pod: &pending,
			wantErr: "not running",
		},
		{
			name: "service maps to the target port of a running pod", target: svcTarget, value: "8000:80", address: "127.0.0.1", svc: svc,
			pods: []corev1.Pod{pending, pod},
			want: portforward.Spec{Context: "prod", Namespace: "ns", Kind: "service", Name: "web", Pod: "web-0", Address: "127.0.0.1", LocalPort: 8000, RemotePort: 8080, ServicePort: 80,
				Command: "kubectl --context prod port-forward -n ns service/web 8000:80 --address 127.0.0.1"},
		},
		{
			name: "service port by name", target: svcTarget, value: ":http", address: "127.0.0.1", svc: svc,
			pods: []corev1.Pod{pod},
			want: portforward.Spec{Context: "prod", Namespace: "ns", Kind: "service", Name: "web", Pod: "web-0", Address: "127.0.0.1", LocalPort: 0, RemotePort: 8080, ServicePort: 80,
				Command: "kubectl --context prod port-forward -n ns service/web :http --address 127.0.0.1"},
		},
		{
			name: "service without running pods", target: svcTarget, value: "80", address: "127.0.0.1", svc: svc,
			pods:    []corev1.Pod{pending},
			wantErr: "no running pod",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pair, err := portforward.ParsePair(tt.value)
			if err != nil {
				t.Fatalf("ParsePair(%q): %v", tt.value, err)
			}
			got, err := resolvePortForward(deps, tt.target, tt.address, tt.value, pair, tt.svc, tt.pod, tt.pods)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolvePortForward: %v", err)
			}
			if got != tt.want {
				t.Fatalf("spec =\n %+v\nwant\n %+v", got, tt.want)
			}
		})
	}
}

func readyRunner(port int32) portforward.Runner {
	return portforward.RunnerFunc(func(ctx context.Context, _ portforward.Spec, ready func(int32)) error {
		ready(port)
		<-ctx.Done()
		return nil
	})
}

func TestPortForwardsFolderAndRootRow(t *testing.T) {
	t.Parallel()
	reg := portforward.NewRegistry(t.Context())
	defer reg.StopAll(time.Second)
	t0 := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	reg.SetClock(func() time.Time { return t0 })
	deps := Deps{CtxName: "dev", PortForwards: reg}

	if _, ok := portForwardsRootRow(deps, nil); ok {
		t.Fatal("root shows /port-forwards with no forwards")
	}
	if _, ok := portForwardsRootRow(Deps{}, nil); ok {
		t.Fatal("root shows /port-forwards without a registry")
	}

	spec := portforward.Spec{Context: "dev", Namespace: "ns", Kind: "service", Name: "web", Pod: "web-0", Address: "127.0.0.1", RemotePort: 8080, ServicePort: 80, Command: "kubectl port-forward -n ns service/web 8000:80"}
	fwd, err := reg.Start(t.Context(), spec, readyRunner(8000))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	row, ok := portForwardsRootRow(deps, nil)
	if !ok {
		t.Fatal("root hides /port-forwards with one forward")
	}
	_, cells, _, _ := row.Columns()
	if cells[0] != "/port-forwards" || cells[2] != "1" {
		t.Fatalf("root row cells = %q", cells)
	}
	folder, err := row.(Enterable).Enter()
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	pf := folder.(*PortForwardsFolder)
	pf.now = func() time.Time { return t0.Add(90 * time.Second) }
	dirty := make(chan struct{}, 4)
	cancel := pf.RegisterDirtyListener(func() { dirty <- struct{}{} })
	defer cancel()

	ctx := t.Context()
	// Row 0 is "..": the folder sits below the root.
	rows := pf.Lines(ctx, 0, 10)
	if len(rows) != 2 {
		t.Fatalf("folder has %d rows, want back + 1", len(rows))
	}
	id, cells, _, _ := rows[1].Columns()
	want := []string{"127.0.0.1:8000", "service/web:80", "web-0:8080", "ns", "dev", "90s"}
	if id != fwd.ID || strings.Join(cells, "|") != strings.Join(want, "|") {
		t.Fatalf("row %s = %q, want %q", id, cells, want)
	}
	item, ok := pf.ItemByID(ctx, fwd.ID)
	if !ok {
		t.Fatal("ItemByID did not find the forward")
	}
	if vc := item.(interface{ ViewCommand() string }).ViewCommand(); vc != spec.Command {
		t.Fatalf("ViewCommand = %q", vc)
	}
	_, body, _, _, _, err := item.(Viewable).ViewContent()
	if err != nil || !strings.Contains(body, "target: service/web:80") || !strings.Contains(body, "pod: web-0") {
		t.Fatalf("ViewContent = %q, %v", body, err)
	}

	stop, ok := item.(Stoppable)
	if !ok {
		t.Fatal("forward row is not stoppable")
	}
	action := stop.StopAction()
	if c := action.Confirmation(); c.Action != "Stop" || len(c.Details) == 0 {
		t.Fatalf("confirmation = %+v", c)
	}
	if err := action.Confirm(); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	select {
	case <-dirty:
	case <-time.After(5 * time.Second):
		t.Fatal("folder not marked dirty after the forward stopped")
	}
	if rows := pf.Lines(ctx, 0, 10); len(rows) != 1 {
		t.Fatalf("folder has %d rows after stop, want only ..", len(rows))
	}
	if err := action.Confirm(); err == nil {
		t.Fatal("stopping a stopped forward reported success")
	}
	if _, ok := portForwardsRootRow(deps, nil); ok {
		t.Fatal("root still shows /port-forwards after the last forward stopped")
	}
	var _ table.Row = row
}
