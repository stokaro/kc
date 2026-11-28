package portforward

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	"github.com/stokaro/kc/internal/testing/kcenvtest"
)

// watchSignal is a pod client that says when a watch is open.
type watchSignal struct {
	corev1client.PodInterface
	once   sync.Once
	opened chan struct{}
}

func newWatchSignal(pods corev1client.PodInterface) *watchSignal {
	return &watchSignal{PodInterface: pods, opened: make(chan struct{})}
}

func (w *watchSignal) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	wi, err := w.PodInterface.Watch(ctx, opts)
	if err == nil {
		w.once.Do(func() { close(w.opened) })
	}
	return wi, err
}

func (w *watchSignal) waitOpen(t *testing.T) {
	t.Helper()
	select {
	case <-w.opened:
	case <-time.After(15 * time.Second):
		t.Fatal("the pod watch never opened")
	}
}

func createPod(t *testing.T, cs kubernetes.Interface, ns, name string) *corev1.Pod {
	t.Helper()
	pod, err := cs.CoreV1().Pods(ns).Create(t.Context(), &corev1.Pod{
		Name: name,
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: "web:1"}}},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create pod %s: %v", name, err)
	}
	return pod
}

func deletePod(t *testing.T, cs kubernetes.Interface, ns, name string) {
	t.Helper()
	// The pod has no node, so the API server removes it at once.
	if err := cs.CoreV1().Pods(ns).Delete(t.Context(), name, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete pod %s: %v", name, err)
	}
}

func envtestNamespace(t *testing.T, cfg *rest.Config) (kubernetes.Interface, string) {
	t.Helper()
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	const ns = "forwards"
	if _, err := cs.CoreV1().Namespaces().Create(t.Context(), &corev1.Namespace{Name: ns}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	return cs, ns
}

// A forward ends when its pod is deleted, names the pod, and leaves the
// list of forwards; while the pod exists it goes on.
func TestDeletedPodEndsTheForward(t *testing.T) {
	cfg := kcenvtest.Start(t)
	cs, ns := envtestNamespace(t, cfg)
	pod := createPod(t, cs, ns, "web-0")

	ended := make(chan error, 1)
	reg := NewRegistry(t.Context())
	reg.OnEnded(func(_ Forward, err error) { ended <- err })
	pods := newWatchSignal(cs.CoreV1().Pods(ns))
	// The runner is ClientRunner's composition with a forward that only
	// holds its port: envtest has no kubelet to carry traffic.
	runner := RunnerFunc(func(ctx context.Context, spec Spec, ready func(int32)) error {
		return endWhenPodGone(ctx, logr.Discard(), pods, spec, func(ctx context.Context) error {
			ready(4242)
			<-ctx.Done()
			return nil
		})
	})
	spec := Spec{Namespace: ns, Kind: "pod", Name: "web-0", Pod: "web-0", PodUID: pod.UID, Address: DefaultAddress, RemotePort: 80}
	if _, err := reg.Start(t.Context(), spec, runner); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pods.waitOpen(t)
	select {
	case err := <-ended:
		t.Fatalf("the forward ended while its pod exists: %v", err)
	case <-time.After(time.Second):
	}
	if reg.Len() != 1 {
		t.Fatalf("Len = %d while the pod exists, want 1", reg.Len())
	}

	deletePod(t, cs, ns, "web-0")
	select {
	case err := <-ended:
		gone, ok := errors.AsType[*PodGoneError](err)
		if !ok || gone.Pod != "web-0" || gone.Namespace != ns {
			t.Fatalf("ended with %v (%T), want the deleted pod web-0", err, err)
		}
		if got, want := err.Error(), "pod web-0 was deleted"; got != want {
			t.Errorf("error = %q, want %q", got, want)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the forward did not end after its pod was deleted")
	}
	if reg.Len() != 0 {
		t.Errorf("Len = %d after the pod was deleted, want 0", reg.Len())
	}
}

func TestWaitPodGone(t *testing.T) {
	cfg := kcenvtest.Start(t)
	cs, ns := envtestNamespace(t, cfg)
	nobody := rest.CopyConfig(cfg)
	nobody.Impersonate = rest.ImpersonationConfig{UserName: "nobody"}
	limited, err := kubernetes.NewForConfig(nobody)
	if err != nil {
		t.Fatal(err)
	}
	gone := func(name string) error { return &PodGoneError{Namespace: ns, Pod: name} }

	tests := []struct {
		name  string
		pods  corev1client.PodInterface
		pod   string
		uid   func(created types.UID) types.UID
		setup bool // create the pod first
		want  error
	}{
		{name: "a pod that is gone already", pods: cs.CoreV1().Pods(ns), pod: "never", want: gone("never")},
		{
			name: "another pod under the name", pods: cs.CoreV1().Pods(ns), pod: "replaced", setup: true,
			uid:  func(types.UID) types.UID { return "the-old-pod" },
			want: gone("replaced"),
		},
		// A user who may forward but not list pods keeps the forward
		// without the check instead of retrying forever.
		{name: "no permission to list pods", pods: limited.CoreV1().Pods(ns), pod: "hidden", setup: true, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var uid types.UID
			if tt.setup {
				uid = createPod(t, cs, ns, tt.pod).UID
			}
			if tt.uid != nil {
				uid = tt.uid(uid)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			err := waitPodGone(ctx, logr.Discard(), tt.pods, ns, tt.pod, uid)
			if ctx.Err() != nil {
				t.Fatalf("waitPodGone did not return before the deadline (err %v)", err)
			}
			if !samePodGone(err, tt.want) {
				t.Fatalf("waitPodGone = %v, want %v", err, tt.want)
			}
		})
	}

	// The control: an existing pod keeps the wait going until the
	// context ends, and the end is no deletion.
	t.Run("a pod that exists", func(t *testing.T) {
		uid := createPod(t, cs, ns, "alive").UID
		pods := newWatchSignal(cs.CoreV1().Pods(ns))
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- waitPodGone(ctx, logr.Discard(), pods, ns, "alive", uid) }()
		pods.waitOpen(t)
		select {
		case err := <-done:
			t.Fatalf("waitPodGone returned %v while the pod exists", err)
		case <-time.After(time.Second):
		}
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("waitPodGone = %v after the context ended, want nil", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("waitPodGone did not return after the context ended")
		}
	})
}

func samePodGone(got, want error) bool {
	if want == nil {
		return got == nil
	}
	g, ok := errors.AsType[*PodGoneError](got)
	w, _ := errors.AsType[*PodGoneError](want)
	return ok && *g == *w
}
