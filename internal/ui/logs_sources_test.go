package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/logmux"
	"github.com/stokaro/kc/internal/models"
	kctesting "github.com/stokaro/kc/internal/testing"
)

// heldOpener opens container streams that stay open until canceled.
func heldOpener(ctx context.Context, _ logmux.Target, _ *corev1.PodLogOptions) (io.ReadCloser, error) {
	pr, pw := io.Pipe()
	context.AfterFunc(ctx, func() { _ = pw.CloseWithError(ctx.Err()) })
	return pr, nil
}

func muxTarget(pod, container string) logmux.Target {
	return logmux.Target{Namespace: "web", Pod: pod, Container: container, PodUID: types.UID("uid-" + pod)}
}

func typeInto(t *testing.T, d *logsSourcesDialog, text string) {
	t.Helper()
	for _, r := range text {
		d.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func pressDialogKey(d *logsSourcesDialog, code rune) {
	d.Update(tea.KeyPressMsg{Code: code})
}

// TestLogsSourcesDialogExcludesWithoutResettingTheView drives a merged
// viewer: the footer shows the coverage, the dialog excludes a sidecar,
// and the lines the viewer kept stay.
func TestLogsSourcesDialogExcludesWithoutResettingTheView(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		sel := &logSelection{limit: 10}
		open := func(models.LogsSpec) (io.ReadCloser, context.CancelFunc, error) {
			f, limit := sel.get()
			mux := logmux.New(t.Context(), heldOpener, logmux.Options{MaxStreams: limit, LogOptions: corev1.PodLogOptions{Follow: true}})
			mux.SetFilter(f)
			mux.SetTargets([]logmux.Target{
				muxTarget("a", "app"), muxTarget("a", "proxy"), muxTarget("b", "app"), muxTarget("b", "proxy"),
			})
			src := &workloadLogsSource{
				Reader: logmux.NewReader(mux, func(l logmux.Line) string { return logmux.Format(l, false) }),
				mux:    mux,
				stop:   func() {},
			}
			return src, nil, nil
		}
		v := NewLogsViewer("logs:web/deployments/api", models.LogsSpec{Namespace: "web", Follow: true}, open, "")
		v.SetStyledLines(true)
		v.SetDimensions(100, 20)
		v.inner.Resize(100, 18)
		opened := false
		v.SetOnSources(func() tea.Cmd { opened = true; return nil })
		defer v.Close()

		cmd := v.Init()
		step := func() {
			t.Helper()
			if cmd == nil {
				t.Fatal("the stream stopped reading")
			}
			_, cmd = v.Update(cmd())
		}
		step() // opened
		for range 4 {
			step() // + notices
		}
		synctest.Wait()
		if got := v.statusText(); !strings.HasPrefix(got, "4 streaming; ") {
			t.Fatalf("statusText() = %q, want the coverage first", got)
		}
		if _, handled := v.handleLogViewKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModAlt}); !handled || !opened {
			t.Fatal("M-s did not open the sources dialog")
		}

		closed := false
		d := newLogsSourcesDialog(v, sel, func() { closed = true })
		d.Init()
		d.SetDimensions(76, 30)
		typeInto(t, d, "a/b/c")
		pressDialogKey(d, tea.KeyEnter)
		if closed || !strings.Contains(d.note, "pod/container") || !d.isErr {
			t.Fatalf("a bad filter was accepted: closed %v, note %q", closed, d.note)
		}
		d.filter.SetValue("-/proxy")
		pressDialogKey(d, tea.KeyEnter)
		if !closed {
			t.Fatalf("Apply did not close the dialog; note %q", d.note)
		}
		if f, _ := sel.get(); f.String() != "-/proxy" {
			t.Fatalf("selection filter = %q", f.String())
		}
		for range 3 {
			step() // source filter notice, two "- (excluded)" notices
		}
		synctest.Wait()
		if got := v.statusText(); !strings.HasPrefix(got, "2 streaming, 2 excluded; ") {
			t.Fatalf("statusText() = %q after excluding the sidecars", got)
		}
		if v.log.Len() != 7 {
			t.Fatalf("the viewer keeps %d records, want the 4 earlier ones and 3 notices", v.log.Len())
		}
		got := plainLines(v)
		for _, want := range []string{"[kc] + a/proxy", "[kc] source filter: -/proxy", "[kc] - a/proxy (excluded)"} {
			if !strings.Contains(strings.Join(got, "\n"), want) {
				t.Fatalf("display %q lacks %q", got, want)
			}
		}
		view := viewString(d.View())
		if !strings.Contains(view, "excluded     a/proxy") || !strings.Contains(view, "streaming    a/app") {
			t.Fatalf("dialog rows:\n%s", view)
		}

		// A limit out of range is refused and changes nothing.
		closed = false
		d.limit.SetValue("0")
		pressDialogKey(d, tea.KeyEnter)
		if closed || !d.isErr {
			t.Fatalf("limit 0 was accepted; note %q", d.note)
		}
		if _, limit := sel.get(); limit != 10 {
			t.Fatalf("limit = %d after a refused change", limit)
		}
	})
}

func TestSourceRowsPutProblemsFirst(t *testing.T) {
	t.Parallel()
	var sources []logmux.Source
	for i := range 5 {
		sources = append(sources, logmux.Source{Target: muxTarget(fmt.Sprintf("p-%d", i), "app")})
	}
	sources = append(sources,
		logmux.Source{Target: muxTarget("q", "proxy"), State: logmux.SourceExcluded},
		logmux.Source{Target: muxTarget("w", "app"), State: logmux.SourceWaiting},
		logmux.Source{Target: muxTarget("f", "app"), State: logmux.SourceFailed, Err: errors.New("forbidden")},
	)
	rows := sourceRows(sources, 4, 0)
	want := []string{
		"failed       f/app: forbidden",
		"waiting      w/app",
		"excluded     q/proxy",
		"... and 5 more",
	}
	if strings.Join(rows, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rows =\n%s\nwant\n%s", strings.Join(rows, "\n"), strings.Join(want, "\n"))
	}
	if rows := sourceRows(sources[:2], 4, 0); len(rows) != 4 || rows[2] != "" {
		t.Fatalf("a short list is not padded: %q", rows)
	}
	if rows := sourceRows(sources[:4], 4, 0); rows[3] != "streaming    p-3/app" {
		t.Fatalf("a list that fits is cut: %q", rows)
	}
}

// TestWorkloadLogsStartWithASelection checks that a merged stream started
// with a selection, as after an F2 change, filters the pods of the shared
// cache, including a pod created later.
func TestWorkloadLogsStartWithASelection(t *testing.T) {
	if testCfg == nil {
		t.Skip("envtest not available")
	}
	ctx := t.Context()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	cli, err := crclient.New(testCfg, crclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ns := generatedNamespace(t, cli, "workload-logs-sel-")
	api := map[string]string{"app": "api"}
	runningPod(t, cli, ns, "api-1", api)
	runningPod(t, cli, ns, "api-2", api)
	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatal(err)
	}
	kctesting.RunInBackground(t, "cluster", cl.Start)

	filter, err := logmux.ParseFilter("-api-2")
	if err != nil {
		t.Fatal(err)
	}
	sel := &logSelection{filter: filter, limit: 1}
	op := &recordingOpener{}
	spec := models.WorkloadLogsSpec{Namespace: ns, Pod: "", Workload: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, WorkloadName: "api", Follow: true}
	deploy := &appsv1.Deployment{
		Namespace: ns, Name: "api",
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: api},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: api},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox"}}},
			},
		},
	}
	if err := cli.Create(ctx, deploy); err != nil {
		t.Fatal(err)
	}
	src, err := startWorkloadLogs(ctx, cl, spec, spec.LogsSpec(), 10, op.open, false, sel)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = src.Close() }()
	lines := readLines(src)
	readUntil(t, lines, "[kc] source filter: -api-2", "[kc] + api-1/app")

	// A later pod matches the filter and waits for the one slot.
	runningPod(t, cli, ns, "api-3", api)
	readUntil(t, lines, "[kc] 1 stream waits for a free slot: at most 1 run at once")
	deadline := time.Now().Add(30 * time.Second)
	// The sidecars wait to start (runningPod) and count as sources; api-2's
	// two containers are excluded.
	for src.Coverage() != "incomplete: 1 streaming, 1 waiting (limit 1), 2 waiting (ContainerCreating), 2 excluded" {
		if time.Now().After(deadline) {
			t.Fatalf("Coverage() = %q", src.Coverage())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if op.has("api-2/app") {
		t.Fatalf("opened the excluded pod: %v", op.snapshot())
	}
}

// Through the app: M-s opens the sources dialog over a merged viewer, and
// Esc returns to the viewer.
func TestAppLogsSourcesDialogOverViewer(t *testing.T) {
	v := newTestLogsViewer(t, "")
	mux := logmux.New(t.Context(), heldOpener, logmux.Options{LogOptions: corev1.PodLogOptions{Follow: true}})
	defer mux.Close()
	go func() {
		for range mux.Lines() {
		}
	}()
	mux.SetTargets([]logmux.Target{muxTarget("a", "app")})
	v.reader = &workloadLogsSource{mux: mux, stop: func() {}}
	sel := &logSelection{limit: 50}
	mm := NewModalManager(nil)
	app := &App{ctx: t.Context(), modalManager: mm, width: 100, height: 30}
	v.SetOnSources(func() tea.Cmd { return app.showLogsSourcesModal(v, sel) })
	logs := NewModal("logs", v)
	logs.SetMode(ModalModeFullscreen)
	logs.SetDimensions(100, 30)
	mm.Register("logs_viewer", logs)
	app.showModal("logs_viewer")

	app.Update(altKey('s', false))
	if top := mm.GetActiveModal(); top == nil || top != mm.modals[logsSourcesModalName] {
		t.Fatal("M-s must open the sources dialog on top of the viewer")
	}
	if got := viewString(mm.View()); !strings.Contains(got, "Streams at once") || !strings.Contains(got, "a/app") {
		t.Fatalf("the dialog is not drawn:\n%s", got)
	}
	app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if top := mm.GetActiveModal(); top != logs {
		t.Fatal("Esc must return to the viewer")
	}
}
