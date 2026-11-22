package podfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// NewFactory returns a native exec-backed factory with optional helper support.
func NewFactory(cfg *rest.Config) (Factory, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil kubeconfig")
	}
	base := rest.CopyConfig(cfg)
	base = rest.AddUserAgent(base, "kc-podfs")
	if base.QPS == 0 {
		base.QPS = 50
	}
	if base.Burst == 0 {
		base.Burst = 100
	}
	clientset, err := kubernetes.NewForConfig(base)
	if err != nil {
		return nil, fmt.Errorf("podfs: kubernetes client: %w", err)
	}
	return &nativeFactory{
		cfg:    base,
		client: clientset,
		helper: newHelperManager(clientset),
	}, nil
}

type nativeFactory struct {
	cfg    *rest.Config
	client kubernetes.Interface
	helper *helperManager
}

func (f *nativeFactory) NewSession(ctx context.Context, spec SessionSpec) (ExecSession, error) {
	if spec.Namespace == "" || spec.Pod == "" || spec.Container == "" {
		return nil, fmt.Errorf("podfs: namespace, pod, and container are required")
	}
	sess, err := newNativeSession(ctx, f.cfg, f.client, spec)
	if err == nil {
		return sess, nil
	}
	if !shouldFallback(err) || f.helper == nil || !f.helper.available() {
		return nil, err
	}
	helperSpec, helperErr := f.helper.ensureHelper(ctx, spec)
	if errors.Is(helperErr, ErrHelperNeeded) {
		return nil, fmt.Errorf("%w: %w", helperErr, err)
	}
	if helperErr != nil {
		return nil, helperErr
	}
	helperSpec.Heartbeat = helperHeartbeat
	helperSession, err := newNativeSession(ctx, f.cfg, f.client, helperSpec)
	if err != nil {
		return nil, err
	}
	hs := &helperAwareSession{ExecSession: helperSession, container: helperSpec.Container}
	if err := hs.checkTarget(ctx); err != nil {
		_ = helperSession.Close()
		return nil, err
	}
	return hs, nil
}

// HelperRoot is where the target container's filesystem appears inside the
// debug helper: the root of the target's main process, which is PID 1 in
// the process namespace the helper shares with it.
const HelperRoot = "/proc/1/root"

// HelperPath maps a path in the target container to the helper.
func HelperPath(p string) string { return path.Join(HelperRoot, p) }

// helperAwareSession runs in the debug helper and serves the target
// container's filesystem, not the helper's own.
type helperAwareSession struct {
	ExecSession
	container string
}

func (h *helperAwareSession) HelperUsed() bool { return true }

// HelperContainer names the ephemeral helper container.
func (h *helperAwareSession) HelperContainer() string { return h.container }

func (h *helperAwareSession) List(ctx context.Context, p string) ([]FileEntry, error) {
	return h.ExecSession.List(ctx, HelperPath(p))
}

func (h *helperAwareSession) ReadFile(ctx context.Context, p string, limit int64) (io.ReadCloser, error) {
	return h.ExecSession.ReadFile(ctx, HelperPath(p), limit)
}

// checkTarget makes sure the helper sees the target's filesystem. Without
// a shared process namespace PID 1 is the helper itself, and kc would show
// the helper's files as the container's.
func (h *helperAwareSession) checkTarget(ctx context.Context) error {
	rc, err := h.ExecSession.ReadFile(ctx, "/proc/1/cmdline", 4096)
	if err != nil {
		return fmt.Errorf("debug helper cannot see the container's processes: %w", err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if strings.Contains(string(data), helperSleepMarker) {
		return fmt.Errorf("debug helper does not share the container's process namespace; the runtime may not support targeting containers")
	}
	if _, err := h.ExecSession.List(ctx, HelperRoot); err != nil {
		return fmt.Errorf("debug helper cannot read the container's filesystem (it may run as another user and the namespace may forbid SYS_PTRACE): %w", err)
	}
	return nil
}

func shouldFallback(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrShellMissing) {
		return true
	}
	var missing MissingCommandError
	return errors.As(err, &missing)
}
