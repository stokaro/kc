package ui

import (
	"context"
	"io"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/logresume"
	"github.com/stokaro/kc/internal/models"
)

// openResumingLogs opens the log of one container. A followed log that
// fails or ends while the container still runs reconnects where it stopped
// (docs/adr/0180-resume-live-logs.md); the reader then carries "[kc] ..."
// notices about the connection. The first open's error is returned. The
// container state comes from the shared cache, so a reconnect adds no
// request besides the log stream itself. name names the logger.
func openResumingLogs(ctx context.Context, cl *kccluster.Cluster, clientset kubernetes.Interface, spec models.LogsSpec, name string) (io.ReadCloser, error) {
	cfg := logresume.Config{
		Options: *spec.PodLogOptions(),
		Open: func(ctx context.Context, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
			var since string
			if opts.SinceTime != nil {
				since = opts.SinceTime.UTC().Format("2006-01-02T15:04:05Z")
			}
			ctrllog.FromContext(ctx).WithName(name).Info("client stream logs",
				"gvr", "v1/pods", "namespace", spec.Namespace, "pod", spec.Pod, "container", spec.Container,
				"tailLines", spec.EffectiveTailLines(), "sinceSeconds", spec.SinceSeconds, "sinceTime", since,
				"timestamps", opts.Timestamps, "previous", opts.Previous, "follow", opts.Follow)
			return clientset.CoreV1().Pods(spec.Namespace).GetLogs(spec.Pod, opts).Stream(ctx)
		},
	}
	if cl != nil {
		cfg.Check = func(ctx context.Context) (logresume.Instance, bool) {
			return containerInstance(ctx, cl, spec)
		}
	}
	return logresume.Open(ctx, cfg)
}

// isReconnecting reports a resumed stream that waits to reconnect.
func isReconnecting(r io.Reader) bool {
	rr, ok := r.(interface{ Reconnecting() bool })
	return ok && rr.Reconnecting()
}

// containerInstance reads the state of spec's container from the shared
// cache. A pod the cache does not have is gone; any other error leaves the
// state unknown.
func containerInstance(ctx context.Context, cl *kccluster.Cluster, spec models.LogsSpec) (logresume.Instance, bool) {
	pod, err := panelLogsPod(ctx, cl, spec)
	if apierrors.IsNotFound(err) {
		return logresume.Instance{}, true
	}
	if err != nil {
		return logresume.Instance{}, false
	}
	return logresume.PodInstances(pod)[spec.Container], true
}
