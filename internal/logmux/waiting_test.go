package logmux_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stokaro/kc/internal/logmux"
	"github.com/stokaro/kc/internal/logresume"
)

// reportPod is the pod of a Deployment whose one container has the given
// status.
func reportPod(name string, st corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		Namespace: "batch", Name: name, UID: types.UID("uid-" + name), Labels: map[string]string{"app": "report"},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: st.Name}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{st}},
	}
}

func waitingFor(name, reason string) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}}}
}

// newFollowedMux returns a mux fed by a follower of the pods named in
// pods, as startWorkloadLogs builds it, with a stream tail of 200.
func newFollowedMux(t *testing.T, op *fakeOpener, pods ...string) (*logmux.Mux, *logmux.Follower) {
	t.Helper()
	tail := int64(200)
	var follower *logmux.Follower
	m := logmux.New(t.Context(), op.open, logmux.Options{
		LogOptions: corev1.PodLogOptions{Follow: true, TailLines: &tail},
		Check:      func(tg logmux.Target) (logresume.Instance, bool) { return follower.Instance(tg) },
		Scope:      "selector app=report in batch",
	})
	follower = logmux.NewFollower(m, func(o metav1.Object) bool {
		return o.GetNamespace() == "batch" && slices.Contains(pods, o.GetName())
	})
	return m, follower
}

func expectLine(t *testing.T, lines <-chan logmux.Line, want string) logmux.Line {
	t.Helper()
	got := readLine(t, lines)
	if text := logmux.Format(got, false); text != want {
		t.Fatalf("line = %q, want %q", text, want)
	}
	return got
}

func onlySource(t *testing.T, m *logmux.Mux) logmux.Source {
	t.Helper()
	sources := m.Sources()
	if len(sources) != 1 {
		t.Fatalf("sources = %+v, want one", sources)
	}
	return sources[0]
}

// TestMuxReadsThePreviousInstanceOfAContainerInBackOff (issue #379): a
// container that crashed and waits in back-off is a source. Its last
// instance is read once with previous, a notice says so, and the source
// shows as waiting (CrashLoopBackOff). When the container starts again the
// new instance streams live; when it crashes again, it is not read twice.
func TestMuxReadsThePreviousInstanceOfAContainerInBackOff(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		op := newFakeOpener()
		m, follower := newFollowedMux(t, op, "report-1")
		defer m.Close()
		lines := m.Lines()

		follower.OnAdd(reportPod("report-1", crashLooping("report", 5)), true)
		m.Synced()
		synctest.Wait()
		expectLine(t, lines, "[kc] + report-1/report: previous instance (container waiting: CrashLoopBackOff)")
		opts := op.optsFor("report-1/report")
		if !opts.Previous || opts.Follow || opts.TailLines == nil || *opts.TailLines != 200 || opts.Container != "report" {
			t.Fatalf("opened with %+v, want the previous instance, not followed, tail 200", opts)
		}
		prev := op.stream(t, "report-1/report")
		prev.lines <- "loading config /etc/report/config.yaml"
		expectLine(t, lines, "[report-1/report] loading config /etc/report/config.yaml")
		prev.lines <- "error: config file not found"
		expectLine(t, lines, "[report-1/report] error: config file not found")
		close(prev.lines)
		expectLine(t, lines, "[kc] - report-1/report (end of the previous instance)")
		synctest.Wait()

		src := onlySource(t, m)
		if src.State != logmux.SourceContainerWaiting || src.Status() != "waiting (CrashLoopBackOff)" || !src.Previous || src.Target.Restarts != 5 {
			t.Fatalf("source = %+v (%s), want waiting (CrashLoopBackOff) with the previous instance shown", src, src.Status())
		}
		if got, want := m.Stats().String(), "0 streaming, 1 waiting (CrashLoopBackOff)"; got != want {
			t.Fatalf("Stats().String() = %q, want %q", got, want)
		}

		// The container starts again: the new instance streams live, and
		// the source shows it.
		started := corev1.ContainerStatus{
			Name: "report", RestartCount: 6, State: running(),
			LastTerminationState: crashLooping("report", 5).LastTerminationState,
		}
		follower.OnUpdate(nil, reportPod("report-1", started))
		synctest.Wait()
		expectLine(t, lines, "[kc] + report-1/report")
		if opts := op.optsFor("report-1/report"); opts.Previous || !opts.Follow {
			t.Fatalf("the restarted container opened with %+v, want a followed current stream", opts)
		}
		live := op.stream(t, "report-1/report")
		live.lines <- "loading config /etc/report/config.yaml"
		expectLine(t, lines, "[report-1/report] loading config /etc/report/config.yaml")
		if src := onlySource(t, m); src.State != logmux.SourceStreaming || src.Target.Restarts != 6 {
			t.Fatalf("source = %+v, want the new instance streaming", src)
		}

		// It crashes again. The kubelet first reports it terminated, for up
		// to a minute: the stream ends and the source is ended.
		follower.OnUpdate(nil, reportPod("report-1", corev1.ContainerStatus{
			Name: "report", RestartCount: 6,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, ContainerID: "containerd://def"}},
		}))
		close(live.lines)
		expectLine(t, lines, "[kc] - report-1/report")
		synctest.Wait()
		if src := onlySource(t, m); src.State != logmux.SourceEnded || src.Target.Restarts != 6 {
			t.Fatalf("source = %+v, want the terminated instance ended", src)
		}
		// Then the back-off: the source waits, and the instance that was
		// streamed is not read again with previous.
		follower.OnUpdate(nil, reportPod("report-1", crashLooping("report", 6)))
		synctest.Wait()
		if got := op.opened(); len(got) != 2 {
			t.Fatalf("opened %v, want the previous instance and the live one only", got)
		}
		if src := onlySource(t, m); src.Status() != "waiting (CrashLoopBackOff)" || src.Previous {
			t.Fatalf("source = %+v, want waiting without a previous read", src)
		}
		if got := readAll(m); len(got) != 0 {
			t.Fatalf("more lines after the back-off: %q", got)
		}
	})
}

// TestMuxReadsAnInstanceTheInformerNeverSawRun checks decision 4 of ADR
// 0379: an instance that started and crashed between two pod updates
// (restart count 5, then 6 in back-off) is read once with previous under
// its own target.
func TestMuxReadsAnInstanceTheInformerNeverSawRun(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		op := newFakeOpener()
		m, follower := newFollowedMux(t, op, "report-1")
		defer m.Close()
		lines := m.Lines()

		follower.OnAdd(reportPod("report-1", crashLooping("report", 5)), true)
		synctest.Wait()
		expectLine(t, lines, "[kc] + report-1/report: previous instance (container waiting: CrashLoopBackOff)")
		close(op.stream(t, "report-1/report").lines)
		expectLine(t, lines, "[kc] - report-1/report (end of the previous instance)")

		next := crashLooping("report", 6)
		next.LastTerminationState.Terminated.ContainerID = "containerd://def"
		follower.OnUpdate(nil, reportPod("report-1", next))
		synctest.Wait()
		expectLine(t, lines, "[kc] + report-1/report: previous instance (container waiting: CrashLoopBackOff)")
		if opts := op.optsFor("report-1/report"); !opts.Previous || opts.Follow {
			t.Fatalf("opened with %+v, want the previous instance", opts)
		}
		close(op.stream(t, "report-1/report").lines)
		expectLine(t, lines, "[kc] - report-1/report (end of the previous instance)")
		synctest.Wait()
		if got := op.opened(); len(got) != 2 {
			t.Fatalf("opened %v, want each instance once", got)
		}
		if src := onlySource(t, m); src.Target.Restarts != 6 || !src.Previous {
			t.Fatalf("source = %+v, want the instance counted 6", src)
		}
	})
}

// TestMuxEndsASourceWithoutALogInAPodThatIsOver checks that a container
// that has no log in a pod that failed (rejected by the kubelet, or failed
// before its init containers finished) gets no request and counts as
// ended, not as waiting for good.
func TestMuxEndsASourceWithoutALogInAPodThatIsOver(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		op := newFakeOpener()
		m, follower := newFollowedMux(t, op, "report-1", "report-2")
		defer m.Close()

		rejected := reportPod("report-1", corev1.ContainerStatus{Name: "report"})
		rejected.Status = corev1.PodStatus{Phase: corev1.PodFailed, Reason: "OutOfcpu"}
		initFailed := reportPod("report-2", waitingFor("report", "PodInitializing"))
		initFailed.Status.Phase = corev1.PodFailed
		follower.OnAdd(rejected, true)
		follower.OnAdd(initFailed, true)
		m.Synced()
		synctest.Wait()
		if got := op.opened(); len(got) != 0 {
			t.Fatalf("opened %v for containers without a log", got)
		}
		sources := m.Sources()
		if len(sources) != 2 || sources[0].State != logmux.SourceEnded || sources[0].Done != "pod Failed (OutOfcpu)" ||
			sources[1].State != logmux.SourceEnded || sources[1].Done != "pod Failed" {
			t.Fatalf("sources = %+v, want both ended with how the pod ended", sources)
		}
		if got, want := m.Stats().String(), "0 streaming, 2 ended"; got != want {
			t.Fatalf("Stats().String() = %q, want %q", got, want)
		}
		if got, want := readAll(m), []string{"[kc] no log lines yet: 2 ended"}; !slices.Equal(got, want) {
			t.Fatalf("lines %q, want %q", got, want)
		}
	})
}

// TestMuxRetryAfterACrashResumesTheReadInstance checks Retry of a followed
// source that failed and whose container then crashed: the instance it was
// reading is now the last one, and Retry resumes after the last line shown
// instead of reading the whole previous log again. A failed source whose
// container waits without a log gets no request.
func TestMuxRetryAfterACrashResumesTheReadInstance(t *testing.T) {
	t.Parallel()
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "pods/log"}, "report-1", errors.New("denied"))
	fail := func(t *testing.T, op *fakeOpener, m *logmux.Mux, follower *logmux.Follower) {
		t.Helper()
		follower.OnAdd(reportPod("report-1", corev1.ContainerStatus{Name: "report", RestartCount: 5, State: running()}), true)
		synctest.Wait()
		op.stream(t, "report-1/report").lines <- "2026-11-28T10:00:01.5Z one"
		synctest.Wait()
		op.mu.Lock()
		op.fail["report-1/report"] = forbidden
		op.mu.Unlock()
		_ = op.stream(t, "report-1/report").Close() // the connection drops; the reconnect is refused
		synctest.Wait()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if src := onlySource(t, m); src.State != logmux.SourceFailed {
			t.Fatalf("source = %+v, want failed", src)
		}
		op.mu.Lock()
		delete(op.fail, "report-1/report")
		op.mu.Unlock()
	}

	t.Run("the last instance has a log", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			op := newFakeOpener()
			m, follower := newFollowedMux(t, op, "report-1")
			defer m.Close()
			fail(t, op, m, follower)
			follower.OnUpdate(nil, reportPod("report-1", crashLooping("report", 5)))
			synctest.Wait()
			_ = readAll(m)

			if n := m.Retry(); n != 1 {
				t.Fatalf("Retry() = %d, want 1", n)
			}
			synctest.Wait()
			opts := op.optsFor("report-1/report")
			if opts.Previous || opts.SinceTime == nil || opts.SinceTime.UTC().Format("15:04:05") != "10:00:01" {
				t.Fatalf("retry opened with %+v, want the instance it read, after its last line", opts)
			}
			s := op.stream(t, "report-1/report")
			s.lines <- "2026-11-28T10:00:01.5Z one"
			s.lines <- "2026-11-28T10:00:02Z two"
			close(s.lines)
			synctest.Wait()
			got := readAll(m)
			if want := []string{"[kc] + report-1/report", "[report-1/report] two", "[kc] - report-1/report"}; !slices.Equal(got, want) {
				t.Fatalf("lines %q, want %q", got, want)
			}
		})
	})
	t.Run("the container waits without a log", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			op := newFakeOpener()
			m, follower := newFollowedMux(t, op, "report-1")
			defer m.Close()
			fail(t, op, m, follower)
			lost := crashLooping("report", 5)
			lost.LastTerminationState.Terminated.ContainerID = "" // ContainerStatusUnknown
			follower.OnUpdate(nil, reportPod("report-1", lost))
			synctest.Wait()
			before := len(op.opened())

			if n := m.Retry(); n != 1 {
				t.Fatalf("Retry() = %d, want 1", n)
			}
			synctest.Wait()
			if got := op.opened(); len(got) != before {
				t.Fatalf("opened %v, want no request for a container without a log", got)
			}
			if src := onlySource(t, m); src.Status() != "waiting (CrashLoopBackOff)" {
				t.Fatalf("source = %+v, want waiting", src)
			}
		})
	})
}

// TestMuxSelectedAgainDoesNotReadAFinishedInstance checks that a source
// selected again after the filter excluded it streams from that moment on:
// the previous instance of a container in back-off has nothing new, so no
// request is made.
func TestMuxSelectedAgainDoesNotReadAFinishedInstance(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		op := newFakeOpener()
		m, follower := newFollowedMux(t, op, "report-1")
		defer m.Close()
		lines := m.Lines()
		follower.OnAdd(reportPod("report-1", crashLooping("report", 5)), true)
		synctest.Wait()
		expectLine(t, lines, "[kc] + report-1/report: previous instance (container waiting: CrashLoopBackOff)")
		close(op.stream(t, "report-1/report").lines)
		expectLine(t, lines, "[kc] - report-1/report (end of the previous instance)")

		m.SetFilter(mustFilter(t, "-report-1"))
		synctest.Wait()
		m.SetFilter(logmux.Filter{})
		synctest.Wait()
		if got := op.opened(); len(got) != 1 {
			t.Fatalf("opened %v, want the previous instance read once", got)
		}
		if src := onlySource(t, m); src.Status() != "waiting (CrashLoopBackOff)" || src.Previous {
			t.Fatalf("source = %+v, want waiting", src)
		}
		for _, l := range readAll(m) {
			if strings.Contains(l, "previous instance") {
				t.Fatalf("selecting the source again printed %q", l)
			}
		}
	})
}

// TestMuxStartsAWaitingContainerWhenItRuns checks that a container that
// waits to start gets no request, and streams once the pod status says it
// runs; the target stays the same.
func TestMuxStartsAWaitingContainerWhenItRuns(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		op := newFakeOpener()
		m, follower := newFollowedMux(t, op, "report-1")
		defer m.Close()
		drain(m)

		follower.OnAdd(reportPod("report-1", waitingFor("report", "ContainerCreating")), true)
		synctest.Wait()
		if got := op.opened(); len(got) != 0 {
			t.Fatalf("opened %v for a container that waits to start", got)
		}
		if src := onlySource(t, m); src.Status() != "waiting (ContainerCreating)" || src.Previous {
			t.Fatalf("source = %+v", src)
		}
		follower.OnUpdate(nil, reportPod("report-1", corev1.ContainerStatus{Name: "report", State: running()}))
		synctest.Wait()
		if got := op.opened(); !slices.Equal(got, []string{"report-1/report"}) {
			t.Fatalf("opened %v, want the container once it runs", got)
		}
		if opts := op.optsFor("report-1/report"); opts.Previous || !opts.Follow {
			t.Fatalf("opened with %+v, want a followed current stream", opts)
		}
		if src := onlySource(t, m); src.State != logmux.SourceStreaming {
			t.Fatalf("source = %+v, want streaming", src)
		}
	})
}

// TestMuxFailedPreviousReadIsAFailedSource (issue #379): a source whose
// request is refused, here the previous instance of a crash-looping
// container for a user who may not read pod logs, is failed with the
// refusal, and the view says why it has no line.
func TestMuxFailedPreviousReadIsAFailedSource(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		op := newFakeOpener()
		forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "pods/log"}, "report-1", errors.New("denied"))
		op.fail["report-1/report"] = forbidden
		m, follower := newFollowedMux(t, op, "report-1")
		defer m.Close()
		lines := m.Lines()

		follower.OnAdd(reportPod("report-1", crashLooping("report", 2)), true)
		m.Synced()
		expectLine(t, lines, "[kc] - report-1/report: "+forbidden.Error())
		expectLine(t, lines, "[kc] no log lines yet: 1 failed")
		synctest.Wait()
		if opts := op.optsFor("report-1/report"); !opts.Previous || opts.Follow {
			t.Fatalf("refused request had %+v, want the previous instance", opts)
		}
		src := onlySource(t, m)
		if src.State != logmux.SourceFailed || !apierrors.IsForbidden(src.Err) {
			t.Fatalf("source = %+v, want failed with the refusal", src)
		}
		if got, want := m.Stats().String(), "incomplete: 0 streaming, 1 failed"; got != want {
			t.Fatalf("Stats().String() = %q, want %q", got, want)
		}
	})
}

// TestMuxExplainsAnEmptyView (issue #379): once the pod list is complete,
// a view with no log line says why, and says it again only when the reason
// changes; a source that streams, even silently, needs no explanation.
func TestMuxExplainsAnEmptyView(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		filter string
		pods   []*corev1.Pod
		fail   string
		want   []string
	}{
		{
			name: "no pods",
			want: []string{"[kc] no pods match selector app=report in batch yet; waiting for them"},
		},
		{
			name: "every container waits to start",
			pods: []*corev1.Pod{
				reportPod("report-1", waitingFor("report", "ImagePullBackOff")),
				reportPod("report-2", waitingFor("report", "ImagePullBackOff")),
			},
			want: []string{"[kc] no log lines yet: 2 waiting (ImagePullBackOff)"},
		},
		{
			name: "every source failed",
			pods: []*corev1.Pod{reportPod("report-1", corev1.ContainerStatus{Name: "report", State: running()})},
			fail: "report-1/report",
			want: []string{"[kc] - report-1/report: not found", "[kc] no log lines yet: 1 failed"},
		},
		{
			name:   "the filter excludes every source",
			filter: "-report-*",
			pods: []*corev1.Pod{
				reportPod("report-1", corev1.ContainerStatus{Name: "report", State: running()}),
				reportPod("report-2", waitingFor("report", "ContainerCreating")),
			},
			want: []string{"[kc] source filter: -report-*", "[kc] no sources selected: the filter excludes all 2"},
		},
		{
			name: "a source streams without a line",
			pods: []*corev1.Pod{
				reportPod("report-1", corev1.ContainerStatus{Name: "report", State: running()}),
				reportPod("report-2", waitingFor("report", "ContainerCreating")),
			},
			want: []string{"[kc] + report-1/report"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				op := newFakeOpener()
				if tt.fail != "" {
					op.fail[tt.fail] = errors.New("not found")
				}
				names := []string{"report-1", "report-2"}
				m, follower := newFollowedMux(t, op, names...)
				defer m.Close()
				if tt.filter != "" {
					m.SetFilter(mustFilter(t, tt.filter))
				}
				// Lines before Synced explain nothing: the list is still
				// arriving.
				for _, p := range tt.pods {
					follower.OnAdd(p, true)
				}
				m.Synced()
				synctest.Wait()
				got := readAll(m)
				if !slices.Equal(got, tt.want) {
					t.Fatalf("lines =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
				}
			})
		})
	}
}

// TestMuxExplainsOnlyAChangedReason checks that the explanation is given
// again when the reason changes, not on every pod update, and never after
// a stream opened, even one that wrote nothing.
func TestMuxExplainsOnlyAChangedReason(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		op := newFakeOpener()
		m, follower := newFollowedMux(t, op, "report-1")
		defer m.Close()
		follower.OnAdd(reportPod("report-1", waitingFor("report", "ContainerCreating")), true)
		m.Synced()
		synctest.Wait()
		if got, want := readAll(m), []string{"[kc] no log lines yet: 1 waiting (ContainerCreating)"}; !slices.Equal(got, want) {
			t.Fatalf("lines %q, want %q", got, want)
		}
		// The same reason again says nothing; a new reason is explained.
		follower.OnUpdate(nil, reportPod("report-1", waitingFor("report", "ContainerCreating")))
		follower.OnUpdate(nil, reportPod("report-1", waitingFor("report", "CreateContainerConfigError")))
		synctest.Wait()
		if got, want := readAll(m), []string{"[kc] no log lines yet: 1 waiting (CreateContainerConfigError)"}; !slices.Equal(got, want) {
			t.Fatalf("lines %q, want %q", got, want)
		}
		// Once a stream opened, its `+` line is in the view: a later idle
		// state is not explained. A crash loop that writes nothing would
		// otherwise add two lines per restart (ended, then back-off).
		follower.OnUpdate(nil, reportPod("report-1", corev1.ContainerStatus{Name: "report", State: running()}))
		synctest.Wait()
		follower.OnUpdate(nil, reportPod("report-1", corev1.ContainerStatus{
			Name: "report", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, ContainerID: "containerd://abc"}},
		}))
		close(op.stream(t, "report-1/report").lines)
		synctest.Wait()
		follower.OnUpdate(nil, reportPod("report-1", crashLooping("report", 0)))
		synctest.Wait()
		if got, want := readAll(m), []string{"[kc] + report-1/report", "[kc] - report-1/report"}; !slices.Equal(got, want) {
			t.Fatalf("lines %q, want %q", got, want)
		}
	})
}

// readAll returns the lines the mux has buffered, formatted, without
// blocking. Call it after synctest.Wait.
func readAll(m *logmux.Mux) []string {
	var out []string
	for {
		select {
		case l := <-m.Lines():
			out = append(out, logmux.Format(l, false))
		case <-time.After(time.Millisecond):
			return out
		}
	}
}
