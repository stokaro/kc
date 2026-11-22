package podfs

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// execFunc runs command in container of the session's pod with streams,
// in its own exec stream next to the session's shell. It returns when the
// command exits or ctx ends. Tests replace it with a fake stream.
type execFunc func(ctx context.Context, container string, command []string, streams remotecommand.StreamOptions) error

// execAvailable reports whether the session can start more exec streams.
func (s *nativeSession) execAvailable() bool {
	return s.exec != nil || (s.cfg != nil && s.client != nil)
}

// runExec starts command in container through the session's exec hook, or
// through a SPDY exec on the session's pod.
func (s *nativeSession) runExec(ctx context.Context, container string, command []string, streams remotecommand.StreamOptions) error {
	if s.exec != nil {
		return s.exec(ctx, container, command, streams)
	}
	if s.cfg == nil || s.client == nil {
		return fmt.Errorf("podfs: exec unavailable")
	}
	req := s.client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(s.spec.Pod).
		Namespace(s.spec.Namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdin:     streams.Stdin != nil,
			Stdout:    streams.Stdout != nil,
			Stderr:    streams.Stderr != nil,
		}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(s.cfg, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("podfs: executor: %w", err)
	}
	return exec.StreamWithContext(ctx, streams)
}
