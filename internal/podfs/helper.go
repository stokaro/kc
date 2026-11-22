package podfs

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

type helperManager struct {
	client kubernetes.Interface
	cfg    helperConfig
}

type helperConfig struct {
	enabled          bool
	image            string
	command          []string
	namePrefix       string
	ptracePrefix     string // helpers with CAP_SYS_PTRACE
	readinessTimeout time.Duration
}

func newHelperManager(client kubernetes.Interface) *helperManager {
	cfg := helperConfig{
		enabled:          true,
		image:            getenvDefault("KC_PODFS_HELPER_IMAGE", "docker.io/library/busybox:1.36.1"),
		command:          []string{"/bin/sh", "-c", helperScript},
		namePrefix:       "kc-fs-helper",
		ptracePrefix:     "kc-fs-ptrace",
		readinessTimeout: 60 * time.Second,
	}
	if strings.EqualFold(os.Getenv("KC_PODFS_HELPER_DISABLE"), "1") {
		cfg.enabled = false
	}
	return &helperManager{client: client, cfg: cfg}
}

func (m *helperManager) available() bool {
	return m != nil && m.cfg.enabled && m.cfg.image != ""
}

func (m *helperManager) ensureHelper(ctx context.Context, spec SessionSpec) (SessionSpec, error) {
	if !m.available() {
		return spec, fmt.Errorf("pod filesystem helper disabled")
	}
	// Prefer a helper with CAP_SYS_PTRACE: it can read the filesystem of a
	// target running as another user through /proc/1/root. Pod security
	// admission (baseline and stricter) rejects that capability; a plain
	// helper still reaches targets that run as root.
	ptrace := true
	pods := m.client.CoreV1().Pods(spec.Namespace)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		prefix := m.cfg.namePrefix
		if ptrace {
			prefix = m.cfg.ptracePrefix
		}
		pod, err := pods.Get(ctx, spec.Pod, metav1.GetOptions{})
		if err != nil {
			return spec, err
		}
		// Reuse a helper that is still running; one that exited after
		// going idle cannot restart, so the next one gets a new name.
		helperName, running := pickHelper(pod, prefix, spec.Container)
		if !running && !spec.AllowHelper {
			// Reusing a running helper leaves the pod as it is; adding one
			// changes it for good and needs the user's consent.
			name, ok := runningHelper(pod, spec.Container, m.cfg.ptracePrefix, m.cfg.namePrefix)
			if !ok {
				return spec, ErrHelperNeeded
			}
			helperName, running = name, true
		}
		if !running {
			if err := m.createHelper(ctx, pods, pod, spec.Container, helperName, ptrace); err != nil {
				lastErr = err
				if ptrace && (apierrors.IsForbidden(err) || apierrors.IsInvalid(err)) {
					ptrace = false
					attempt = -1
				}
				continue
			}
		}
		if err := m.waitForHelper(ctx, spec.Namespace, spec.Pod, helperName); err != nil {
			lastErr = err
			if ctx.Err() != nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
			continue
		}
		return SessionSpec{Namespace: spec.Namespace, Pod: spec.Pod, Container: helperName}, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("failed to start debug helper after retries")
	}
	return spec, lastErr
}

func (m *helperManager) waitForHelper(ctx context.Context, namespace, podName, helperName string) error {
	timeout := m.cfg.readinessTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, timeout, true, func(ctx context.Context) (bool, error) {
		pod, err := m.client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, status := range pod.Status.EphemeralContainerStatuses {
			if status.Name != helperName {
				continue
			}
			if status.State.Running != nil {
				return true, nil
			}
			if status.State.Terminated != nil {
				reason := strings.TrimSpace(status.State.Terminated.Reason)
				message := strings.TrimSpace(status.State.Terminated.Message)
				detail := reason
				if detail == "" {
					detail = "unknown"
				}
				if message != "" {
					detail = fmt.Sprintf("%s: %s", detail, message)
				}
				return false, fmt.Errorf("debug helper terminated: %s", detail)
			}
		}
		return false, nil
	})
}

// helperIdleTimeout is how long a helper waits for kc's next command
// before it exits. Each command touches helperHeartbeat.
const (
	helperIdleTimeout = 60
	helperHeartbeat   = "/tmp/.kc-fs-alive"
)

// helperScript keeps the helper alive while kc uses it and exits once it
// has been idle for helperIdleTimeout seconds, so its extra rights do not
// outlive the browsing. The marker comment lets a session recognize the
// helper's own command line.
var helperScript = fmt.Sprintf(`touch %[1]s; while [ $(( $(date +%%s) - $(stat -c %%Y %[1]s 2>/dev/null || echo 0) )) -lt %[2]d ]; do sleep 5; done # %[3]s`,
	helperHeartbeat, helperIdleTimeout, helperSleepMarker)

// runningHelper finds a running (or starting) helper for container under
// any of the prefixes.
func runningHelper(pod *corev1.Pod, container string, prefixes ...string) (string, bool) {
	for _, prefix := range prefixes {
		if name, running := pickHelper(pod, prefix, container); running {
			return name, true
		}
	}
	return "", false
}

// pickHelper returns the helper to use for container: a running (or
// starting) one with prefix, or the next unused name.
func pickHelper(pod *corev1.Pod, prefix, container string) (string, bool) {
	// The sanitizer trims a trailing hyphen, so add it after.
	base := sanitizeHelperName(fmt.Sprintf("%s-%s", prefix, container)) + "-"
	state := map[string]corev1.ContainerState{}
	for _, st := range pod.Status.EphemeralContainerStatuses {
		state[st.Name] = st.State
	}
	next := 0
	for _, ec := range pod.Spec.EphemeralContainers {
		suffix, ok := strings.CutPrefix(ec.Name, base)
		if !ok {
			continue
		}
		n, err := strconv.Atoi(suffix)
		if err != nil {
			continue
		}
		next = max(next, n+1)
		if st, seen := state[ec.Name]; !seen || st.Terminated == nil {
			return ec.Name, true
		}
	}
	return base + strconv.Itoa(next), false
}

// helperSleepMarker tags the helper's own command line, so a session can
// tell the helper's PID 1 from the target's.
const helperSleepMarker = "kc-fs-helper"

var helperNameSanitizer = regexp.MustCompile(`[^a-z0-9-]+`)

func sanitizeHelperName(input string) string {
	s := strings.ToLower(input)
	s = helperNameSanitizer.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) == 0 {
		return "kc-fs-helper"
	}
	if len(s) <= 63 {
		return s
	}
	return s[:63]
}

func getenvDefault(key, def string) string {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		return val
	}
	return def
}

func (m *helperManager) createHelper(ctx context.Context, pods typedcorev1.PodInterface, pod *corev1.Pod, targetContainer, helperName string, ptrace bool) error {
	ec := corev1.EphemeralContainer{
		EphemeralContainerCommon: corev1.EphemeralContainerCommon{
			Name:            helperName,
			Image:           m.cfg.image,
			Command:         append([]string(nil), m.cfg.command...),
			ImagePullPolicy: corev1.PullIfNotPresent,
			Stdin:           true,
			TTY:             true,
		},
		TargetContainerName: targetContainer,
	}
	if ptrace {
		ec.SecurityContext = &corev1.SecurityContext{
			Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"SYS_PTRACE"}},
		}
	}
	pod.Spec.EphemeralContainers = append(pod.Spec.EphemeralContainers, ec)
	_, err := pods.UpdateEphemeralContainers(ctx, pod.Name, pod, metav1.UpdateOptions{})
	return err
}
