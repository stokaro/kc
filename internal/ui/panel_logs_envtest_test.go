package ui

import (
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/models"
	kctesting "github.com/stokaro/kc/internal/testing"
)

// TestPanelAllContainersStream opens the Logs panel's merged stream of one
// pod against envtest: the pod and its containers come from the shared
// cache, every started container is streamed, and the line prefix carries
// no ANSI color for the plain panel viewer.
func TestPanelAllContainersStream(t *testing.T) {
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
	ns := generatedNamespace(t, cli, "panel-logs-all-")
	runningPod(t, cli, ns, "web-1", nil)

	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatal(err)
	}
	kctesting.RunInBackground(t, "cluster", cl.Start)

	spec := models.LogsSpec{Namespace: ns, Pod: "web-1", Container: "app", Follow: true, TailLines: 20, Timestamps: true}
	pod, err := panelLogsPod(ctx, cl, spec)
	if err != nil {
		t.Fatalf("reading the pod from the cache: %v", err)
	}
	op := &recordingOpener{}
	stream, err := openPanelAllContainers(ctx, cl, spec, 10, op.open, []string{"app", "sidecar"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		stream.Cancel()
		_ = stream.Body.Close()
	})
	if got := podContainerNames(pod); !slices.Equal(got, []string{"app", "sidecar"}) {
		t.Fatalf("pod containers = %v", got)
	}
	if stream.Spec.Container != "" || !stream.Spec.Timestamps {
		t.Fatalf("stream spec = %+v, want no container and the options kept", stream.Spec)
	}

	// readUntil strips ANSI codes; look at the raw lines first.
	var colored atomic.Bool
	raw, checked := readLines(stream.Body), make(chan string, 64)
	go func() {
		defer close(checked)
		for l := range raw {
			if strings.Contains(l, "\x1b") {
				colored.Store(true)
			}
			checked <- l
		}
	}()
	readUntil(t, checked, "[web-1/app] hello from web-1/app tail=20")
	if colored.Load() {
		t.Fatal("panel lines carry ANSI codes")
	}
	// The sidecar still waits and has no log yet.
	if op.has("web-1/sidecar") {
		t.Fatalf("a waiting container was opened: %v", op.snapshot())
	}
}

func podContainerNames(pod *corev1.Pod) []string {
	var names []string
	for _, c := range pod.Spec.Containers {
		names = append(names, c.Name)
	}
	return names
}
