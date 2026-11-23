package models

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func TestLogsSpecOptionsAndCommandAgree(t *testing.T) {
	t.Parallel()
	deps := Deps{CtxName: "dev", KubectlContext: "dev"}
	base := LogsSpec{Namespace: "web", Pod: "p", Container: "app", Follow: true}
	ptr := func(v int64) *int64 { return &v }
	tests := []struct {
		name    string
		mutate  func(*LogsSpec)
		wantCmd string
		want    corev1.PodLogOptions
	}{
		{
			name:    "zero tail means the default",
			mutate:  func(*LogsSpec) {},
			wantCmd: "kubectl logs p -n web -c app --tail=200 -f",
			want:    corev1.PodLogOptions{Container: "app", Follow: true, TailLines: ptr(200)},
		},
		{
			name:    "negative tail fetches the whole log",
			mutate:  func(s *LogsSpec) { s.TailLines = -1 },
			wantCmd: "kubectl logs p -n web -c app -f",
			want:    corev1.PodLogOptions{Container: "app", Follow: true},
		},
		{
			name: "all options",
			mutate: func(s *LogsSpec) {
				s.TailLines = 50
				s.SinceSeconds = 3600
				s.Timestamps = true
				s.Previous = true
			},
			wantCmd: "kubectl logs p -n web -c app --tail=50 --since=1h --timestamps -p -f",
			want: corev1.PodLogOptions{
				Container: "app", Follow: true, TailLines: ptr(50),
				SinceSeconds: ptr(3600), Timestamps: true, Previous: true,
			},
		},
		{
			name:    "no follow",
			mutate:  func(s *LogsSpec) { s.Follow = false; s.SinceSeconds = 300 },
			wantCmd: "kubectl logs p -n web -c app --tail=200 --since=5m",
			want:    corev1.PodLogOptions{Container: "app", TailLines: ptr(200), SinceSeconds: ptr(300)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			spec := base
			tt.mutate(&spec)
			if got := logsCommand(deps, spec); got != tt.wantCmd {
				t.Errorf("command = %q, want %q", got, tt.wantCmd)
			}
			got := spec.PodLogOptions()
			if got.Container != tt.want.Container || got.Follow != tt.want.Follow ||
				got.Previous != tt.want.Previous || got.Timestamps != tt.want.Timestamps ||
				!equalPtr(got.TailLines, tt.want.TailLines) || !equalPtr(got.SinceSeconds, tt.want.SinceSeconds) {
				t.Errorf("PodLogOptions = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// An investigation window's start wins over the since window, in the
// request and in the printed command.
func TestLogsSpecSinceTime(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 23, 10, 0, 0, 0, time.FixedZone("CET", 3600))
	spec := LogsSpec{Namespace: "web", Pod: "p", Container: "app", TailLines: -1, SinceSeconds: 60, SinceTime: start}
	if got, want := logsCommand(Deps{}, spec), "kubectl logs p -n web -c app --since-time=2026-11-23T09:00:00Z"; got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
	opts := spec.PodLogOptions()
	if opts.SinceTime == nil || !opts.SinceTime.Time.Equal(start) || opts.SinceSeconds != nil {
		t.Fatalf("PodLogOptions = %+v", opts)
	}
}

func equalPtr(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func TestFormatLogsSince(t *testing.T) {
	t.Parallel()
	for in, want := range map[int64]string{0: "", -5: "", 60: "1m", 900: "15m", 3600: "1h", 7200: "2h", 90: "90s", 5400: "90m"} {
		if got := FormatLogsSince(in); got != want {
			t.Errorf("FormatLogsSince(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestContainerRestartCount(t *testing.T) {
	t.Parallel()
	pod := &corev1.Pod{Status: corev1.PodStatus{
		ContainerStatuses:          []corev1.ContainerStatus{{Name: "app", RestartCount: 3}, {Name: "side"}},
		InitContainerStatuses:      []corev1.ContainerStatus{{Name: "app", RestartCount: 7}},
		EphemeralContainerStatuses: []corev1.ContainerStatus{{Name: "dbg", RestartCount: 1}},
	}}
	tests := []struct {
		name      string
		container string
		kind      containerKind
		want      int32
	}{
		{"primary", "app", containerKindPrimary, 3},
		{"primary without restarts", "side", containerKindPrimary, 0},
		{"init with the same name", "app", containerKindInit, 7},
		{"ephemeral", "dbg", containerKindEphemeral, 1},
		{"unknown", "nope", containerKindPrimary, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := containerRestartCount(pod, tt.container, tt.kind); got != tt.want {
				t.Errorf("containerRestartCount = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestContainerLogItemCommandFollowsSpec(t *testing.T) {
	t.Parallel()
	deps := Deps{CtxName: "other", KubectlContext: "dev"}
	spec := LogsSpec{Namespace: "web", Pod: "p", Container: "app", Follow: true, TailLines: 200, Previous: true}
	item := NewContainerLogItem(deps, "logs_previous", []string{"logs (previous)"}, nil, spec)
	if got, want := item.ViewCommand(), "kubectl --context other logs p -n web -c app --tail=200 -p -f"; got != want {
		t.Fatalf("ViewCommand = %q, want %q", got, want)
	}
	spec.Previous = false
	spec.Timestamps = true
	if got, want := item.LogsCommand(spec), "kubectl --context other logs p -n web -c app --tail=200 --timestamps -f"; got != want {
		t.Fatalf("LogsCommand = %q, want %q", got, want)
	}
}
