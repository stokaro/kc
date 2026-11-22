package portforward_test

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/stokaro/kc/internal/portforward"
)

func TestParsePair(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    portforward.Pair
		wantErr string
	}{
		{in: "8080:80", want: portforward.Pair{Local: 8080, Remote: "80"}},
		{in: " 8080:80 ", want: portforward.Pair{Local: 8080, Remote: "80"}},
		{in: "5432", want: portforward.Pair{Local: 5432, Remote: "5432"}},
		{in: ":80", want: portforward.Pair{Local: 0, Remote: "80"}},
		{in: "0:80", want: portforward.Pair{Local: 0, Remote: "80"}},
		{in: "8080:http", want: portforward.Pair{Local: 8080, Remote: "http"}},
		{in: "http", want: portforward.Pair{Remote: "http", SameLocal: true}},
		{in: "", wantErr: "enter a port"},
		{in: "8080:", wantErr: "remote port is empty"},
		{in: "8080:80 9090:90", wantErr: "one port pair"},
		{in: "8080:80,9090", wantErr: "one port pair"},
		{in: "1:2:3", wantErr: "invalid port pair"},
		{in: "8080:0", wantErr: "out of range"},
		{in: "8080:70000", wantErr: "out of range"},
		{in: "70000:80", wantErr: "out of range"},
		{in: "abc:80", wantErr: "not a number"},
		{in: "8080:bad_name", wantErr: "neither a number nor a port name"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := portforward.ParsePair(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParsePair(%q) error = %v, want containing %q", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePair(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("ParsePair(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func testPod() *corev1.Pod {
	return &corev1.Pod{
		Name: "web-0", Namespace: "ns",
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{
				Name:  "proxy",
				Ports: []corev1.ContainerPort{{Name: "admin", ContainerPort: 9901}},
			}},
			Containers: []corev1.Container{
				{Name: "app", Ports: []corev1.ContainerPort{
					{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP},
					{Name: "dns", ContainerPort: 53, Protocol: corev1.ProtocolUDP},
				}},
				{Name: "metrics", Ports: []corev1.ContainerPort{{Name: "metrics", ContainerPort: 9090}}},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func TestPodPorts(t *testing.T) {
	t.Parallel()
	pod := testPod()
	t.Run("all containers, TCP only, regular before init", func(t *testing.T) {
		t.Parallel()
		got := portforward.PodPorts(pod, "")
		want := []portforward.PortOption{
			{Name: "http", Port: 8080, Container: "app"},
			{Name: "metrics", Port: 9090, Container: "metrics"},
			{Name: "admin", Port: 9901, Container: "proxy"},
		}
		if len(got) != len(want) {
			t.Fatalf("PodPorts = %+v, want %+v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("PodPorts[%d] = %+v, want %+v", i, got[i], want[i])
			}
		}
		if d := portforward.DefaultPair(got); d != "8080:8080" {
			t.Fatalf("DefaultPair = %q, want 8080:8080", d)
		}
	})
	t.Run("one container", func(t *testing.T) {
		t.Parallel()
		got := portforward.PodPorts(pod, "metrics")
		if len(got) != 1 || got[0].Port != 9090 {
			t.Fatalf("PodPorts(metrics) = %+v", got)
		}
	})
	t.Run("no ports", func(t *testing.T) {
		t.Parallel()
		if got := portforward.PodPorts(&corev1.Pod{}, ""); len(got) != 0 {
			t.Fatalf("PodPorts(empty) = %+v", got)
		}
		if d := portforward.DefaultPair(nil); d != "" {
			t.Fatalf("DefaultPair(nil) = %q, want empty", d)
		}
	})
}

func TestPodPort(t *testing.T) {
	t.Parallel()
	pod := testPod()
	tests := []struct {
		remote  string
		want    int32
		wantErr bool
	}{
		{remote: "8080", want: 8080},
		{remote: "1234", want: 1234}, // undeclared numbers pass, like kubectl
		{remote: "http", want: 8080},
		{remote: "metrics", want: 9090},
		{remote: "nope", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.remote, func(t *testing.T) {
			t.Parallel()
			got, err := portforward.PodPort(pod, tt.remote)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("PodPort(%q) = %d, want error", tt.remote, got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("PodPort(%q) = %d, %v; want %d", tt.remote, got, err, tt.want)
			}
		})
	}
}

func testService() *corev1.Service {
	return &corev1.Service{
		Name: "web", Namespace: "ns",
		Spec: corev1.ServiceSpec{
			Selector:  map[string]string{"app": "web"},
			ClusterIP: "10.0.0.1",
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 80, TargetPort: intstr.FromString("http")},
				{Name: "metrics", Port: 9000, TargetPort: intstr.FromInt32(9090)},
				{Name: "plain", Port: 7000},
				{Name: "dns", Port: 53, Protocol: corev1.ProtocolUDP},
			},
		},
	}
}

func TestServicePorts(t *testing.T) {
	t.Parallel()
	got := portforward.ServicePorts(testService())
	if len(got) != 3 || got[0].Port != 80 || got[1].Port != 9000 || got[2].Port != 7000 {
		t.Fatalf("ServicePorts = %+v, want 80, 9000, 7000 (no UDP)", got)
	}
	if got[0].String() != "80/http" {
		t.Fatalf("String() = %q, want 80/http", got[0].String())
	}
}

func TestServicePort(t *testing.T) {
	t.Parallel()
	svc := testService()
	pod := testPod()
	headless := testService()
	headless.Spec.ClusterIP = corev1.ClusterIPNone
	tests := []struct {
		name        string
		svc         *corev1.Service
		remote      string
		wantSvc     int32
		wantPod     int32
		wantErrText string
	}{
		{name: "named target port", svc: svc, remote: "80", wantSvc: 80, wantPod: 8080},
		{name: "service port by name", svc: svc, remote: "http", wantSvc: 80, wantPod: 8080},
		{name: "numeric target port", svc: svc, remote: "9000", wantSvc: 9000, wantPod: 9090},
		{name: "omitted target port", svc: svc, remote: "7000", wantSvc: 7000, wantPod: 7000},
		{name: "headless ignores target port", svc: headless, remote: "9000", wantSvc: 9000, wantPod: 9000},
		{name: "unknown port", svc: svc, remote: "81", wantErrText: "does not have a service port 81"},
		{name: "unknown name", svc: svc, remote: "grpc", wantErrText: "does not have a named port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotSvc, gotPod, err := portforward.ServicePort(tt.svc, pod, tt.remote)
			if tt.wantErrText != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrText) {
					t.Fatalf("ServicePort(%q) error = %v, want %q", tt.remote, err, tt.wantErrText)
				}
				return
			}
			if err != nil || gotSvc != tt.wantSvc || gotPod != tt.wantPod {
				t.Fatalf("ServicePort(%q) = %d, %d, %v; want %d, %d", tt.remote, gotSvc, gotPod, err, tt.wantSvc, tt.wantPod)
			}
		})
	}
}

func pod(name string, labels map[string]string, phase corev1.PodPhase, ready bool, created time.Time) corev1.Pod {
	p := corev1.Pod{
		Name: name, Namespace: "ns", Labels: labels, CreationTimestamp: metav1.NewTime(created),
		Spec:   corev1.PodSpec{NodeName: "node"},
		Status: corev1.PodStatus{Phase: phase},
	}
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: status, LastTransitionTime: metav1.NewTime(created)}}
	return p
}

func TestSelectServicePod(t *testing.T) {
	t.Parallel()
	svc := testService()
	web := map[string]string{"app": "web"}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	deleting := pod("web-deleting", web, corev1.PodRunning, true, t0)
	now := metav1.NewTime(t0.Add(time.Hour))
	deleting.DeletionTimestamp = &now

	tests := []struct {
		name    string
		svc     *corev1.Service
		pods    []corev1.Pod
		want    string
		wantErr string
	}{
		{
			name: "ready pod wins over a not ready one",
			svc:  svc,
			pods: []corev1.Pod{
				pod("web-notready", web, corev1.PodRunning, false, t0),
				pod("web-ready", web, corev1.PodRunning, true, t0.Add(time.Minute)),
			},
			want: "web-ready",
		},
		{
			name: "selector refuses other pods",
			svc:  svc,
			pods: []corev1.Pod{
				pod("db", map[string]string{"app": "db"}, corev1.PodRunning, true, t0),
				pod("web-1", web, corev1.PodRunning, false, t0),
			},
			want: "web-1",
		},
		{
			name: "other namespace refused",
			svc:  svc,
			pods: func() []corev1.Pod {
				p := pod("web-other", web, corev1.PodRunning, true, t0)
				p.Namespace = "other"
				return []corev1.Pod{p}
			}(),
			wantErr: "no pods match",
		},
		{
			name:    "pending only",
			svc:     svc,
			pods:    []corev1.Pod{pod("web-pending", web, corev1.PodPending, false, t0)},
			wantErr: "no running pod",
		},
		{
			name:    "deleting pod is skipped",
			svc:     svc,
			pods:    []corev1.Pod{deleting},
			wantErr: "no running pod",
		},
		{
			name: "no selector",
			svc: func() *corev1.Service {
				s := testService()
				s.Spec.Selector = nil
				return s
			}(),
			pods:    []corev1.Pod{pod("web-1", web, corev1.PodRunning, true, t0)},
			wantErr: "has no selector",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := portforward.SelectServicePod(tt.svc, tt.pods)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("SelectServicePod error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SelectServicePod: %v", err)
			}
			if got.Name != tt.want {
				t.Fatalf("SelectServicePod = %s, want %s", got.Name, tt.want)
			}
		})
	}
}
