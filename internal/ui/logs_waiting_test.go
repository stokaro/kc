package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stokaro/kc/internal/logmux"
	"github.com/stokaro/kc/internal/logresume"
	"github.com/stokaro/kc/internal/models"
)

// reportLog is what the report container writes before it exits.
const reportLog = "loading config /etc/report/config.yaml\nerror: config file not found\n"

// crashLoopingPod is a pod whose container crashed and waits in back-off,
// as the kubelet reports it.
func crashLoopingPod(name string) *corev1.Pod {
	return &corev1.Pod{
		Namespace: "batch", Name: name, UID: types.UID("uid-" + name),
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "report"}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:                 "report",
			RestartCount:         4,
			State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, ContainerID: "containerd://report-4"}},
		}}},
	}
}

// previousOnlyOpener serves reportLog to a request for the previous
// instance only, so a test sees which instance kc asked for.
func previousOnlyOpener(_ context.Context, t logmux.Target, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	if !opts.Previous {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("test opener: %s asked for the current instance", t.Name()))
	}
	return io.NopCloser(strings.NewReader(reportLog)), nil
}

// followedViewer opens a merged viewer on pod report-1 in batch, wired as
// startWorkloadLogs wires it: a follower fed with pods publishes the
// candidates to the mux, and the initial list is complete. step reads the
// open result and then n lines.
func followedViewer(t *testing.T, open logmux.Opener, pods ...*corev1.Pod) (v *LogsViewer, step func(n int)) {
	t.Helper()
	opener := func(models.LogsSpec) (io.ReadCloser, context.CancelFunc, error) {
		var follower *logmux.Follower
		mux := logmux.New(t.Context(), open, logmux.Options{
			LogOptions: corev1.PodLogOptions{Follow: true},
			Check:      func(tg logmux.Target) (logresume.Instance, bool) { return follower.Instance(tg) },
			Scope:      "pod report-1 in batch",
		})
		follower = logmux.NewFollower(mux, logmux.NameMatcher("batch", "report-1"))
		for _, p := range pods {
			follower.OnAdd(p, true)
		}
		mux.Synced()
		src := &workloadLogsSource{
			Reader: logmux.NewReader(mux, func(l logmux.Line) string { return logmux.Format(l, true) }),
			mux:    mux,
			stop:   func() {},
		}
		return src, nil, nil
	}
	v = NewLogsViewer("logs:batch/report-1/all-containers", models.LogsSpec{Namespace: "batch", Pod: "report-1", Follow: true}, opener, "")
	v.SetStyledLines(true)
	v.SetDimensions(140, 20)
	v.inner.Resize(140, 18)
	cmd := v.Init()
	step = func(n int) {
		t.Helper()
		for i := range n {
			if cmd == nil {
				t.Fatal("the viewer stopped reading")
			}
			// A line that never comes fails the test, not the bubble: the
			// wait is fake time, and closing the viewer ends the read.
			msgs := make(chan tea.Msg, 1)
			go func(read tea.Cmd) { msgs <- read() }(cmd)
			select {
			case msg := <-msgs:
				_, cmd = v.Update(msg)
			case <-time.After(10 * time.Second):
				t.Fatalf("message %d of %d did not arrive; the viewer shows:\n%s", i+1, n, strings.Join(plainLines(v), "\n"))
			}
		}
		synctest.Wait()
	}
	step(1) // the open result
	return v, step
}

func requireLines(t *testing.T, v *LogsViewer, want ...string) {
	t.Helper()
	got := plainLines(v)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("viewer lines =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func dialogRows(t *testing.T, v *LogsViewer) string {
	t.Helper()
	d := newLogsSourcesDialog(v, &logSelection{limit: 50}, nil)
	d.Init()
	d.SetDimensions(140, 30)
	return viewString(d.View())
}

// TestMergedLogsShowThePreviousInstanceOfACrashLoopingPod (issue #379): the
// merged logs of a pod whose container waits in back-off show the lines of
// its last instance, read with previous, and a [kc] line that says so. The
// status line and M-s show the source as waiting (CrashLoopBackOff).
func TestMergedLogsShowThePreviousInstanceOfACrashLoopingPod(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		v, step := followedViewer(t, previousOnlyOpener, crashLoopingPod("report-1"))
		defer v.Close()
		step(4)
		requireLines(t, v,
			"[kc] + report-1/report: previous instance (container waiting: CrashLoopBackOff)",
			"[report-1/report] loading config /etc/report/config.yaml",
			"[report-1/report] error: config file not found",
			"[kc] - report-1/report (end of the previous instance)",
		)
		if got := v.statusText(); !strings.HasPrefix(got, "0 streaming, 1 waiting (CrashLoopBackOff); ") {
			t.Fatalf("statusText() = %q, want the waiting source first", got)
		}
		rows := dialogRows(t, v)
		if !strings.Contains(rows, "waiting (CrashLoopBackOff) report-1/report: previous instance shown") {
			t.Fatalf("M-s does not list the waiting source:\n%s", rows)
		}
		if strings.Contains(rows, "no sources yet") {
			t.Fatalf("M-s says there is no source:\n%s", rows)
		}
	})
}

// TestLogsSourcesListAForbiddenSourceAsFailed (issue #379): for a user who
// may not read pod logs, the source of a crash-looping pod is failed with
// the refusal in M-s, and the view says why it has no line.
func TestLogsSourcesListAForbiddenSourceAsFailed(t *testing.T) {
	t.Parallel()
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "pods/log"}, "report-1",
		errors.New(`User "jane" cannot get resource "pods/log" in the namespace "batch"`))
	refuse := func(context.Context, logmux.Target, *corev1.PodLogOptions) (io.ReadCloser, error) {
		return nil, forbidden
	}
	synctest.Test(t, func(t *testing.T) {
		v, step := followedViewer(t, refuse, crashLoopingPod("report-1"))
		defer v.Close()
		step(2)
		requireLines(t, v,
			"[kc] - report-1/report: "+forbidden.Error(),
			"[kc] no log lines yet: 1 failed",
		)
		if got := v.statusText(); !strings.HasPrefix(got, "incomplete: 0 streaming, 1 failed; ") {
			t.Fatalf("statusText() = %q, want the failed source first", got)
		}
		rows := sourceRows(v.Mux().Sources(), 4, 0)
		if want := "failed       report-1/report: " + forbidden.Error(); rows[0] != want {
			t.Fatalf("first source row = %q, want %q", rows[0], want)
		}
		if view := dialogRows(t, v); !strings.Contains(view, `failed       report-1/report: pods/log "report-1" is forbidden`) {
			t.Fatalf("M-s does not show the refusal:\n%s", view)
		}
		// On a narrow screen the refusal wraps instead of being cut.
		d := newLogsSourcesDialog(v, &logSelection{limit: 50}, nil)
		d.Init()
		w, _ := d.PreferredSize(76, 30)
		d.SetDimensions(w, 30)
		view := ansi.Strip(viewString(d.View()))
		for _, want := range []string{"failed       report-1/report: pods/log", `cannot get resource "pods/log"`, `             "batch"`} {
			if !strings.Contains(view, want) {
				t.Fatalf("a %d-column M-s lacks %q:\n%s", w, want, view)
			}
		}
	})
}

func TestSourceRowsWrapLongRows(t *testing.T) {
	t.Parallel()
	failed := logmux.Source{
		Target: muxTarget("api-1", "app"), State: logmux.SourceFailed,
		Err: errors.New(`pods/log "api-1" is forbidden: User "jane" cannot get resource "pods/log"`),
	}
	waiting := logmux.Source{Target: muxTarget("api-2", "app"), State: logmux.SourceContainerWaiting, Waiting: "CrashLoopBackOff", Previous: true}
	rows := sourceRows([]logmux.Source{waiting, failed}, 6, 60)
	want := []string{
		`failed                     api-1/app: pods/log "api-1" is`,
		`                           forbidden: User "jane" cannot get`,
		`                           resource "pods/log"`,
		`waiting (CrashLoopBackOff) api-2/app: previous instance`,
		`                           shown`,
		``,
	}
	if strings.Join(rows, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rows =\n%s\nwant\n%s", strings.Join(rows, "\n"), strings.Join(want, "\n"))
	}
	// A wrapped row that does not fit is summed up with the rest; the first
	// source shows the lines that fit.
	rows = sourceRows([]logmux.Source{waiting, failed}, 4, 60)
	if rows[3] != "... and 1 more" || rows[2] != want[2] {
		t.Fatalf("rows = %q, want the failed source and the other summed up", rows)
	}
	rows = sourceRows([]logmux.Source{waiting, failed}, 3, 60)
	if rows[0] != want[0] || rows[1] != want[1] || rows[2] != "... and 1 more" {
		t.Fatalf("rows = %q, want two lines of the failed source and the other summed up", rows)
	}

	// Wide characters take two cells each; a row wraps by cells, as the
	// dialog cuts by cells.
	wide := logmux.Source{
		Target: muxTarget("api-1", "app"), State: logmux.SourceFailed,
		Err: errors.New(`User "` + strings.Repeat("用户", 12) + `" cannot get resource "pods/log"`),
	}
	for _, row := range sourceRows([]logmux.Source{wide}, 4, 60) {
		if w := ansi.StringWidth(row); w > 60 {
			t.Fatalf("row %q is %d cells wide, want at most 60", row, w)
		}
	}
}

// TestSourceRowsSayWhyAnEndedSourceHasNoLog checks the row of a container
// whose pod ended before it had a log.
func TestSourceRowsSayWhyAnEndedSourceHasNoLog(t *testing.T) {
	t.Parallel()
	done := logmux.Source{Target: muxTarget("job-1", "app"), State: logmux.SourceEnded, Done: "pod Failed (OutOfcpu)"}
	if rows := sourceRows([]logmux.Source{done}, 1, 0); rows[0] != "ended        job-1/app: no log, pod Failed (OutOfcpu)" {
		t.Fatalf("row = %q", rows[0])
	}
}

// TestEmptyMergedViewerSaysWhy (issue #379): a merged viewer with nothing
// to show says why in a [kc] line once the pod list is complete.
func TestEmptyMergedViewerSaysWhy(t *testing.T) {
	t.Parallel()
	pulling := crashLoopingPod("report-1")
	pulling.Status.ContainerStatuses[0] = corev1.ContainerStatus{
		Name: "report", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
	}
	tests := []struct {
		name   string
		pods   []*corev1.Pod
		want   string
		status string
	}{
		{name: "no sources", want: "[kc] no pods match pod report-1 in batch yet; waiting for them", status: "0 streaming; "},
		{name: "all waiting", pods: []*corev1.Pod{pulling}, want: "[kc] no log lines yet: 1 waiting (ImagePullBackOff)", status: "0 streaming, 1 waiting (ImagePullBackOff); "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				var opened atomic.Bool
				open := func(context.Context, logmux.Target, *corev1.PodLogOptions) (io.ReadCloser, error) {
					opened.Store(true)
					return nil, errors.New("no stream expected")
				}
				v, step := followedViewer(t, open, tt.pods...)
				defer v.Close()
				step(1)
				requireLines(t, v, tt.want)
				if got := v.statusText(); !strings.HasPrefix(got, tt.status) {
					t.Fatalf("statusText() = %q, want prefix %q", got, tt.status)
				}
				if opened.Load() {
					t.Fatal("a log was requested for a container that has none")
				}
			})
		})
	}
}
