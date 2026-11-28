package ui

import (
	"bufio"
	"context"
	"io"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/logmux"
	"github.com/stokaro/kc/internal/models"
	kctesting "github.com/stokaro/kc/internal/testing"
)

// recordingOpener serves one line per stream and then holds the stream open
// until it is canceled, like a followed container log.
type recordingOpener struct {
	mu     sync.Mutex
	opened []string
	ctxs   map[string]context.Context
}

func (r *recordingOpener) open(ctx context.Context, t logmux.Target, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	r.mu.Lock()
	r.opened = append(r.opened, t.Name())
	if r.ctxs == nil {
		r.ctxs = make(map[string]context.Context)
	}
	r.ctxs[t.Name()] = ctx
	r.mu.Unlock()
	pr, pw := io.Pipe()
	go func() {
		_, _ = io.WriteString(pw, "hello from "+t.Name()+" tail="+itoa(opts.TailLines)+"\n")
		<-ctx.Done()
		_ = pw.CloseWithError(ctx.Err())
	}()
	return pr, nil
}

func (r *recordingOpener) has(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Contains(r.opened, name)
}

func (r *recordingOpener) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.opened...)
}

func (r *recordingOpener) ctx(name string) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ctxs[name]
}

func itoa(p *int64) string {
	if p == nil {
		return "nil"
	}
	return strconv.FormatInt(*p, 10)
}

func runningPod(t *testing.T, cli crclient.Client, ns, name string, lbls map[string]string) {
	t.Helper()
	ctx := t.Context()
	pod := &corev1.Pod{
		Namespace: ns, Name: name, Labels: lbls,
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "app", Image: "busybox"},
			{Name: "sidecar", Image: "busybox"},
		}},
	}
	if err := cli.Create(ctx, pod); err != nil {
		t.Fatalf("create pod %s: %v", name, err)
	}
	// No kubelet runs in envtest; write the status it would report: app
	// runs, the sidecar still waits.
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()}}},
		{Name: "sidecar", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}},
	}
	if err := cli.Status().Update(ctx, pod); err != nil {
		t.Fatalf("update pod %s status: %v", name, err)
	}
}

// readUntil reads merged lines until want arrived, failing loudly with what
// it saw after the deadline.
func readUntil(t *testing.T, lines <-chan string, want ...string) {
	t.Helper()
	missing := make(map[string]bool)
	for _, w := range want {
		missing[w] = true
	}
	var seen []string
	deadline := time.After(30 * time.Second)
	for len(missing) > 0 {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("merged stream ended; still missing %v; saw %q", keys(missing), seen)
			}
			plain := ansi.Strip(l)
			seen = append(seen, plain)
			delete(missing, plain)
		case <-deadline:
			t.Fatalf("after 30s still missing %v; saw %q", keys(missing), seen)
		}
	}
}

// generatedNamespace creates a namespace with a generated name, so repeated
// runs against one envtest do not collide.
func generatedNamespace(t *testing.T, cli crclient.Client, prefix string) string {
	t.Helper()
	ns := &corev1.Namespace{GenerateName: prefix}
	if err := cli.Create(t.Context(), ns); err != nil {
		t.Fatal(err)
	}
	return ns.Name
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func readLines(r io.Reader) <-chan string {
	out := make(chan string, 64)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			out <- sc.Text()
		}
	}()
	return out
}

func TestWorkloadLogsFollowsPodsThroughTheSharedCache(t *testing.T) {
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
	ns := generatedNamespace(t, cli, "workload-logs-")
	api := map[string]string{"app": "api"}
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
	runningPod(t, cli, ns, "api-1", api)
	runningPod(t, cli, ns, "api-2", api)
	runningPod(t, cli, ns, "db-1", map[string]string{"app": "db"})

	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatal(err)
	}
	kctesting.RunInBackground(t, "cluster", cl.Start)

	op := &recordingOpener{}
	spec := models.WorkloadLogsSpec{
		Namespace:    ns,
		Workload:     schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
		WorkloadName: "api",
		Follow:       true,
	}
	src, err := startWorkloadLogs(ctx, cl, spec, spec.LogsSpec(), 10, op.open, true, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = src.Close() }()
	if src.selector != "app=api" {
		t.Fatalf("selector = %q, want app=api", src.selector)
	}
	lines := readLines(src)

	// The pods of the workload stream; the waiting sidecar and the pod of
	// another app do not. The tail defaults to 200 per container.
	readUntil(t, lines,
		"[kc] + api-1/app", "[api-1/app] hello from api-1/app tail=200",
		"[kc] + api-2/app", "[api-2/app] hello from api-2/app tail=200",
	)
	for _, name := range []string{"api-1/sidecar", "db-1/app"} {
		if op.has(name) {
			t.Fatalf("opened %s; opened: %v", name, op.snapshot())
		}
	}

	// A new pod is followed.
	runningPod(t, cli, ns, "api-3", api)
	readUntil(t, lines, "[kc] + api-3/app", "[api-3/app] hello from api-3/app tail=200")

	// Close stops the streams and the follower: a later pod is not opened.
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	src.mux.Wait()
	for _, name := range []string{"api-1/app", "api-2/app", "api-3/app"} {
		if op.ctx(name).Err() == nil {
			t.Fatalf("%s still streams after Close", name)
		}
	}
	runningPod(t, cli, ns, "api-4", api)
	time.Sleep(500 * time.Millisecond)
	if op.has("api-4/app") {
		t.Fatalf("a pod created after Close was opened: %v", op.snapshot())
	}
}

// TestWorkloadLogsReadThePreviousInstanceOfACrashLoopingPod (issue #379)
// runs the merged logs of a Deployment on the shared pod informer: the
// container of its pod waits in back-off, so its last instance is read
// with previous, and the coverage counts it as waiting.
func TestWorkloadLogsReadThePreviousInstanceOfACrashLoopingPod(t *testing.T) {
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
	ns := generatedNamespace(t, cli, "workload-logs-backoff-")
	lbls := map[string]string{"app": "report"}
	deploy := &appsv1.Deployment{
		Namespace: ns, Name: "report",
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: lbls},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: lbls},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "report", Image: "busybox"}}},
			},
		},
	}
	if err := cli.Create(ctx, deploy); err != nil {
		t.Fatal(err)
	}
	pod := crashLoopingPod("report-1")
	pod.Namespace, pod.UID, pod.Labels = ns, "", lbls
	pod.Spec.Containers[0].Image = "busybox"
	status := pod.Status
	pod.Status = corev1.PodStatus{}
	if err := cli.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	// No kubelet runs in envtest; write the status it reports for a
	// container in back-off.
	pod.Status = status
	if err := cli.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}

	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatal(err)
	}
	kctesting.RunInBackground(t, "cluster", cl.Start)

	spec := models.WorkloadLogsSpec{
		Namespace:    ns,
		Workload:     schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
		WorkloadName: "report",
		Follow:       true,
	}
	src, err := startWorkloadLogs(ctx, cl, spec, spec.LogsSpec(), 10, previousOnlyOpener, false, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = src.Close() }()
	readUntil(t, readLines(src),
		"[kc] + report-1/report: previous instance (container waiting: CrashLoopBackOff)",
		"[report-1/report] loading config /etc/report/config.yaml",
		"[report-1/report] error: config file not found",
		"[kc] - report-1/report (end of the previous instance)",
	)
	if got, want := src.Coverage(), "0 streaming, 1 waiting (CrashLoopBackOff)"; got != want {
		t.Fatalf("Coverage() = %q, want %q", got, want)
	}
}

func TestWorkloadLogsSinglePodAndNoMatch(t *testing.T) {
	if testCfg == nil {
		t.Skip("envtest not available")
	}
	ctx := t.Context()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	cli, err := crclient.New(testCfg, crclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ns := generatedNamespace(t, cli, "workload-logs-pod-")
	runningPod(t, cli, ns, "web-1", nil)
	runningPod(t, cli, ns, "web-2", nil)

	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatal(err)
	}
	kctesting.RunInBackground(t, "cluster", cl.Start)

	op := &recordingOpener{}
	podSpec := models.WorkloadLogsSpec{Namespace: ns, Pod: "web-1", Follow: true, TailLines: 20}
	src, err := startWorkloadLogs(ctx, cl, podSpec, podSpec.LogsSpec(), 10, op.open, true, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = src.Close() }()
	readUntil(t, readLines(src), "[kc] + web-1/app", "[web-1/app] hello from web-1/app tail=20")
	if op.has("web-2/app") {
		t.Fatalf("opened another pod: %v", op.snapshot())
	}

	goneSpec := models.WorkloadLogsSpec{Namespace: ns, Pod: "gone", Follow: true}
	missing, err := startWorkloadLogs(ctx, cl, goneSpec, goneSpec.LogsSpec(), 10, op.open, true, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = missing.Close() }()
	readUntil(t, readLines(missing), "[kc] no pods match pod gone in "+ns+" yet; waiting for them")
}
