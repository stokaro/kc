// Package nodeshell starts and removes the pod behind a node shell: a
// privileged pod pinned to one node whose shell enters the host's
// namespaces with nsenter, like `kubectl debug node/<name> --profile=sysadmin`.
package nodeshell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/podexec"
	"github.com/stokaro/kc/pkg/appconfig"
)

const (
	// ContainerName is the container the shell runs in.
	ContainerName = "shell"
	// LabelComponent marks node shell pods, so leftovers are easy to find:
	// kubectl get pods -A -l app.kubernetes.io/component=kc-node-shell
	LabelComponent = "app.kubernetes.io/component"
	// ComponentValue is the value of LabelComponent on node shell pods.
	ComponentValue = "kc-node-shell"
	// LabelManagedBy names kc as the creator.
	LabelManagedBy = "app.kubernetes.io/managed-by"
	// AnnotationNode records the node the shell is for.
	AnnotationNode = "kc.sttts.github.io/node"

	namePrefix = "kc-node-shell-"
)

// IdleTimeout is how long the pod waits without kc before it exits. While
// a shell is open, kc keeps a second exec stream (Keepalive) that touches
// heartbeatFile every time kc writes a line, every heartbeatEvery.
const (
	IdleTimeout    = 60 * time.Second
	heartbeatEvery = 10 * time.Second
	heartbeatFile  = "/tmp/.kc-node-shell-alive"
)

// NsenterArgs enters every namespace of the host's PID 1.
var NsenterArgs = []string{"nsenter", "-t", "1", "-m", "-u", "-i", "-n", "-p", "--"}

// hostShell picks bash on the host when it has one.
const hostShell = `if command -v bash >/dev/null 2>&1; then exec bash -l; else exec sh -l; fi`

// Options shape the pod.
type Options struct {
	Image       string
	Namespace   string
	MaxDuration time.Duration
	// ReadyTimeout bounds the wait for the pod to run (image pull included).
	ReadyTimeout time.Duration
	// PollInterval is how often the wait reads the pod.
	PollInterval time.Duration
}

// OptionsFrom reads the options from the configuration.
func OptionsFrom(cfg appconfig.NodeShellConfig) Options {
	o := Options{
		Image:       strings.TrimSpace(cfg.Image),
		Namespace:   strings.TrimSpace(cfg.Namespace),
		MaxDuration: cfg.MaxDuration.Duration,
	}
	return o.withDefaults()
}

func (o Options) withDefaults() Options {
	if o.Image == "" {
		o.Image = appconfig.DefaultNodeShellImage
	}
	if o.Namespace == "" {
		o.Namespace = appconfig.DefaultNodeShellNamespace
	}
	if o.MaxDuration <= 0 {
		o.MaxDuration = appconfig.DefaultNodeShellMaxDuration
	}
	if o.ReadyTimeout <= 0 {
		o.ReadyTimeout = 2 * time.Minute
	}
	if o.PollInterval <= 0 {
		o.PollInterval = time.Second
	}
	return o
}

var nameSanitizer = regexp.MustCompile(`[^a-z0-9-]+`)

// PodName returns a fresh pod name for node: the prefix, the node name cut
// to fit 63 characters, and a random suffix.
func PodName(node string) string {
	base := nameSanitizer.ReplaceAllString(strings.ToLower(node), "-")
	const suffixLen = 5
	if maxBase := 63 - len(namePrefix) - 1 - suffixLen; len(base) > maxBase {
		base = base[:maxBase]
	}
	base = strings.Trim(base, "-")
	if base == "" {
		base = "node"
	}
	return namePrefix + base + "-" + utilrand.String(suffixLen)
}

// idleScript keeps the container alive while a shell touches the heartbeat
// file and exits after IdleTimeout without one. The first touch gives kc
// IdleTimeout to start the shell.
var idleScript = fmt.Sprintf(`touch %[1]s; while [ $(( $(date +%%s) - $(stat -c %%Y %[1]s 2>/dev/null || echo 0) )) -lt %[2]d ]; do sleep 5; done`,
	heartbeatFile, int(IdleTimeout/time.Second))

// ShellCommand is what kc execs in the pod: a shell in the host's
// namespaces.
func ShellCommand() []string {
	return append(append([]string(nil), NsenterArgs...), "sh", "-c", hostShell)
}

// keepaliveTimeout is how long the keepalive waits for kc's next line.
const keepaliveTimeout = 3 * heartbeatEvery

// KeepaliveCommand touches the heartbeat file for every line kc writes and
// ends when kc stops writing. A shell does not notice that kc is gone (the
// runtime keeps an exec process whose client disconnected), so the
// heartbeat must come from kc itself, not from the shell.
func KeepaliveCommand() []string {
	script := fmt.Sprintf(`touch %[1]s; while read -t %[2]d _; do touch %[1]s; done`,
		heartbeatFile, int(keepaliveTimeout/time.Second))
	return []string{"sh", "-c", script}
}

// Keepalive runs KeepaliveCommand and writes it a line every
// heartbeatEvery until ctx ends. It blocks.
func Keepalive(ctx context.Context, cfg *rest.Config, client kubernetes.Interface, pod *corev1.Pod) error {
	stdinR, stdinW := io.Pipe()
	defer stdinW.Close()
	go func() {
		t := time.NewTicker(heartbeatEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = stdinW.Close()
				return
			case <-t.C:
				if _, err := io.WriteString(stdinW, "\n"); err != nil {
					return
				}
			}
		}
	}()
	target := podexec.Target{Namespace: pod.Namespace, Pod: pod.Name, Container: ContainerName, Command: KeepaliveCommand()}
	err := podexec.Stream(ctx, cfg, client, target, stdinR, io.Discard, nil)
	_ = stdinR.Close()
	return err
}

// NewPod returns the pod for a shell on node.
func NewPod(node, name string, opts Options) *corev1.Pod {
	opts = opts.withDefaults()
	deadline := int64(opts.MaxDuration / time.Second)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: opts.Namespace,
			Labels: map[string]string{
				LabelComponent: ComponentValue,
				LabelManagedBy: "kc",
			},
			Annotations: map[string]string{AnnotationNode: node},
		},
		Spec: corev1.PodSpec{
			// nodeName skips the scheduler: the pod lands on the node even
			// when it is cordoned or full.
			NodeName:      node,
			RestartPolicy: corev1.RestartPolicyNever,
			HostPID:       true,
			HostNetwork:   true,
			HostIPC:       true,
			// Tolerate every taint, NoExecute included, so the pod is not
			// evicted from a tainted or unreachable node.
			Tolerations:                   []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
			ActiveDeadlineSeconds:         &deadline,
			TerminationGracePeriodSeconds: ptr.To[int64](0),
			AutomountServiceAccountToken:  ptr.To(false),
			EnableServiceLinks:            ptr.To(false),
			DNSPolicy:                     corev1.DNSClusterFirstWithHostNet,
			Containers: []corev1.Container{{
				Name:            ContainerName,
				Image:           opts.Image,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{"sh", "-c", idleScript},
				SecurityContext: &corev1.SecurityContext{
					Privileged: ptr.To(true),
					RunAsUser:  ptr.To[int64](0),
				},
			}},
		},
	}
}

// KubectlDebugArgs are the kubectl arguments that start a similar shell.
// kubectl leaves its pod behind; kc deletes its own.
func KubectlDebugArgs(node string, opts Options) []string {
	opts = opts.withDefaults()
	args := []string{"debug", "node/" + node, "-n", opts.Namespace, "-it", "--profile=sysadmin", "--image=" + opts.Image, "--"}
	return append(append(args, NsenterArgs...), "sh")
}

// Start creates the pod and waits until its container runs. When the pod
// does not come up, Start deletes it again.
func Start(ctx context.Context, client kubernetes.Interface, node, name string, opts Options) (*corev1.Pod, error) {
	opts = opts.withDefaults()
	log := ctrllog.FromContext(ctx).WithName("nodeShell")
	pod := NewPod(node, name, opts)
	log.Info("create node shell pod", "gvr", "v1/pods", "namespace", pod.Namespace, "name", pod.Name, "node", node, "image", opts.Image)
	created, err := client.CoreV1().Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}
	running, err := waitRunning(ctx, client, created, opts)
	if err != nil {
		// The caller's context may be over; the cleanup must still run.
		delCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if delErr := Delete(delCtx, client, created); delErr != nil {
			log.Error(delErr, "delete node shell pod after failed start", "namespace", created.Namespace, "name", created.Name)
		}
		return nil, err
	}
	return running, nil
}

// errNotStarting reports a pod that will not run without a change.
var errNotStarting = errors.New("node shell pod cannot start")

func waitRunning(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod, opts Options) (*corev1.Pod, error) {
	log := ctrllog.FromContext(ctx).WithName("nodeShell")
	var last *corev1.Pod
	err := wait.PollUntilContextTimeout(ctx, opts.PollInterval, opts.ReadyTimeout, true, func(ctx context.Context) (bool, error) {
		log.Info("get node shell pod", "gvr", "v1/pods", "namespace", pod.Namespace, "name", pod.Name)
		got, err := client.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		last = got
		return podRunning(got)
	})
	if err == nil {
		return last, nil
	}
	if errors.Is(err, errNotStarting) {
		return nil, err
	}
	if last != nil && wait.Interrupted(err) {
		return nil, fmt.Errorf("pod %s/%s did not start in %s (phase %s): %w", pod.Namespace, pod.Name, opts.ReadyTimeout, last.Status.Phase, err)
	}
	return nil, fmt.Errorf("wait for pod %s/%s: %w", pod.Namespace, pod.Name, err)
}

// podRunning reports whether the shell container runs, and fails for
// states it will not leave on its own.
func podRunning(pod *corev1.Pod) (bool, error) {
	switch pod.Status.Phase {
	case corev1.PodSucceeded, corev1.PodFailed:
		return false, fmt.Errorf("%w: pod %s/%s is %s: %s", errNotStarting, pod.Namespace, pod.Name, pod.Status.Phase, pod.Status.Message)
	}
	for _, st := range pod.Status.ContainerStatuses {
		if st.Name != ContainerName {
			continue
		}
		if st.State.Running != nil {
			return true, nil
		}
		if w := st.State.Waiting; w != nil {
			switch w.Reason {
			case "ErrImagePull", "ImagePullBackOff", "InvalidImageName", "CreateContainerConfigError", "CreateContainerError":
				return false, fmt.Errorf("%w: container %s: %s: %s", errNotStarting, ContainerName, w.Reason, w.Message)
			}
		}
		if t := st.State.Terminated; t != nil {
			return false, fmt.Errorf("%w: container %s exited: %s", errNotStarting, ContainerName, t.Reason)
		}
	}
	return false, nil
}

// Delete removes the pod at once. A pod that is already gone is fine; the
// UID precondition keeps a later pod of the same name safe.
func Delete(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod) error {
	log := ctrllog.FromContext(ctx).WithName("nodeShell")
	log.Info("delete node shell pod", "gvr", "v1/pods", "namespace", pod.Namespace, "name", pod.Name)
	opts := metav1.DeleteOptions{GracePeriodSeconds: ptr.To[int64](0)}
	if pod.UID != "" {
		opts.Preconditions = &metav1.Preconditions{UID: &pod.UID}
	}
	err := client.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, opts)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}
	return nil
}
