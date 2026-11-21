package podfs

import (
	"context"
	"io"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

type recordingSession struct {
	lists, reads []string
	files        map[string]string
	listErr      error
}

func (s *recordingSession) List(_ context.Context, p string) ([]FileEntry, error) {
	s.lists = append(s.lists, p)
	return nil, s.listErr
}

func (s *recordingSession) ReadFile(_ context.Context, p string, _ int64) (io.ReadCloser, error) {
	s.reads = append(s.reads, p)
	return io.NopCloser(strings.NewReader(s.files[p])), nil
}

func (s *recordingSession) Close() error { return nil }

func TestHelperSessionServesTargetFilesystem(t *testing.T) {
	inner := &recordingSession{}
	s := &helperAwareSession{ExecSession: inner, container: "kc-fs-ptrace-app-0"}
	_, _ = s.List(t.Context(), "/")
	_, _ = s.ReadFile(t.Context(), "/etc/passwd", 0)
	if inner.lists[0] != "/proc/1/root" || inner.reads[0] != "/proc/1/root/etc/passwd" {
		t.Fatalf("paths = %v %v", inner.lists, inner.reads)
	}
}

func TestHelperSessionCheckTarget(t *testing.T) {
	t.Run("shared namespace", func(t *testing.T) {
		inner := &recordingSession{files: map[string]string{"/proc/1/cmdline": "/coredns\x00-conf\x00/etc/coredns/Corefile"}}
		if err := (&helperAwareSession{ExecSession: inner}).checkTarget(t.Context()); err != nil {
			t.Fatalf("checkTarget: %v", err)
		}
	})
	t.Run("helper is PID 1", func(t *testing.T) {
		cmd := strings.Join(newHelperManager(fake.NewClientset()).cfg.command, "\x00")
		inner := &recordingSession{files: map[string]string{"/proc/1/cmdline": cmd}}
		if err := (&helperAwareSession{ExecSession: inner}).checkTarget(t.Context()); err == nil {
			t.Fatalf("a helper that sees only itself must be rejected")
		}
	})
	t.Run("root unreadable", func(t *testing.T) {
		inner := &recordingSession{files: map[string]string{"/proc/1/cmdline": "/app"}, listErr: io.ErrUnexpectedEOF}
		err := (&helperAwareSession{ExecSession: inner}).checkTarget(t.Context())
		if err == nil || !strings.Contains(err.Error(), "SYS_PTRACE") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCreateHelperAddsPtrace(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"}}
	client := fake.NewClientset(pod)
	m := newHelperManager(client)
	pods := client.CoreV1().Pods("ns")
	if err := m.createHelper(t.Context(), pods, pod.DeepCopy(), "app", "h1", true); err != nil {
		t.Fatal(err)
	}
	got, _ := pods.Get(t.Context(), "p", metav1.GetOptions{})
	ec := got.Spec.EphemeralContainers[0]
	if ec.SecurityContext == nil || ec.SecurityContext.Capabilities.Add[0] != "SYS_PTRACE" || ec.TargetContainerName != "app" {
		t.Fatalf("helper = %+v", ec)
	}
	if err := m.createHelper(t.Context(), pods, got, "app", "h2", false); err != nil {
		t.Fatal(err)
	}
	got, _ = pods.Get(t.Context(), "p", metav1.GetOptions{})
	if got.Spec.EphemeralContainers[1].SecurityContext != nil {
		t.Fatalf("plain helper must not ask for capabilities")
	}
}

func TestPickHelper(t *testing.T) {
	running := corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	done := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}}
	pod := func(states map[string]corev1.ContainerState, names ...string) *corev1.Pod {
		p := &corev1.Pod{}
		for _, n := range names {
			p.Spec.EphemeralContainers = append(p.Spec.EphemeralContainers, corev1.EphemeralContainer{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: n}})
			if st, ok := states[n]; ok {
				p.Status.EphemeralContainerStatuses = append(p.Status.EphemeralContainerStatuses, corev1.ContainerStatus{Name: n, State: st})
			}
		}
		return p
	}
	cases := []struct {
		name    string
		pod     *corev1.Pod
		want    string
		running bool
	}{
		{"none", pod(nil), "kc-fs-ptrace-app-0", false},
		{"running", pod(map[string]corev1.ContainerState{"kc-fs-ptrace-app-0": running}, "kc-fs-ptrace-app-0"), "kc-fs-ptrace-app-0", true},
		{"starting", pod(nil, "kc-fs-ptrace-app-0"), "kc-fs-ptrace-app-0", true},
		{"idle exit", pod(map[string]corev1.ContainerState{"kc-fs-ptrace-app-0": done, "kc-fs-ptrace-app-1": done}, "kc-fs-ptrace-app-0", "kc-fs-ptrace-app-1"), "kc-fs-ptrace-app-2", false},
		{"other container", pod(map[string]corev1.ContainerState{"kc-fs-ptrace-db-0": running}, "kc-fs-ptrace-db-0"), "kc-fs-ptrace-app-0", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, run := pickHelper(tc.pod, "kc-fs-ptrace", "app")
			if got != tc.want || run != tc.running {
				t.Fatalf("pickHelper = %q, %v; want %q, %v", got, run, tc.want, tc.running)
			}
		})
	}
}

func TestHeartbeatPrefixesCommands(t *testing.T) {
	var buf strings.Builder
	s := &nativeSession{spec: SessionSpec{Heartbeat: "/tmp/.kc-fs-alive"}, stdin: nopWriteCloser{&buf}}
	if err := s.writeCommand("__kc_ls '/'"); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "touch '/tmp/.kc-fs-alive' 2>/dev/null; __kc_ls '/'\n" {
		t.Fatalf("command = %q", got)
	}
}

func TestHelperScriptExitsWhenIdle(t *testing.T) {
	if !strings.Contains(helperScript, "-lt 60") || !strings.Contains(helperScript, helperHeartbeat) || !strings.Contains(helperScript, helperSleepMarker) {
		t.Fatalf("script = %s", helperScript)
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
