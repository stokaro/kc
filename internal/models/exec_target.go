package models

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	kccluster "github.com/stokaro/kc/internal/cluster"
)

// ExecMode selects what an exec session does in a container.
type ExecMode int

const (
	// ExecShell runs an interactive shell: bash when the image has it, else sh.
	ExecShell ExecMode = iota
	// ExecAttach attaches to the container's main process.
	ExecAttach
)

func (m ExecMode) String() string {
	if m == ExecAttach {
		return "attach"
	}
	return "shell"
}

// DefaultContainerAnnotation names the container kubectl picks when -c is
// not given.
const DefaultContainerAnnotation = "kubectl.kubernetes.io/default-container"

// shellScript starts bash when the image has it and falls back to sh, as
// k9s does. An image without sh fails with "executable file not found".
const shellScript = "command -v bash >/dev/null 2>&1 && exec bash || exec sh"

// ExecTarget is one container a shell or attach session opens in, together
// with the cluster identity of the panel that shows it.
type ExecTarget struct {
	// Cluster carries the kubeconfig, context and impersonation of the panel.
	Cluster kccluster.Key
	// KubectlContext is the context plain kubectl uses; the shown command
	// adds --context only when the panel's context differs.
	KubectlContext string
	Namespace      string
	Pod            string
	Container      string
	Ephemeral      bool
	Init           bool
	// Stdin and TTY mirror the container spec; attach only asks for what
	// the container offers.
	Stdin bool
	TTY   bool
	// Defaulted is true when the container was chosen for a pod row.
	Defaulted bool
	// Containers lists the pod's regular containers when Defaulted is set.
	Containers []string
}

// ExecTargetProvider is implemented by rows that name one container.
type ExecTargetProvider interface {
	ExecTarget(ctx context.Context) (ExecTarget, error)
}

// ExecLauncher is implemented by rows that open a session on Enter.
type ExecLauncher interface {
	ExecTargetProvider
	ExecMode() ExecMode
}

// ExecRequest asks the UI to open a session for a row.
type ExecRequest struct {
	Item Item
	Mode ExecMode
}

// ErrNoExecTarget means the row is neither a pod nor a container.
var ErrNoExecTarget = errors.New("not a pod or a container")

// SupportsExec reports whether ResolveExecTarget can work on item.
func SupportsExec(item Item) bool {
	if _, ok := item.(ExecTargetProvider); ok {
		return true
	}
	obj, ok := item.(ObjectItem)
	return ok && obj.GVR() == podGVR
}

// depsHolder is implemented by every folder that embeds BaseFolder.
type depsHolder interface {
	folderDeps() Deps
}

func (b *BaseFolder) folderDeps() Deps { return b.Deps }

// ResolveExecTarget names the container a session opens in. A container row
// names its own container. A pod row gets the default container: the one
// named by the kubectl.kubernetes.io/default-container annotation, else the
// first container, as kubectl does. The pod is read through the folder's
// cluster, so call this off the UI goroutine.
func ResolveExecTarget(ctx context.Context, folder Folder, item Item) (ExecTarget, error) {
	if p, ok := item.(ExecTargetProvider); ok {
		return p.ExecTarget(ctx)
	}
	obj, ok := item.(ObjectItem)
	if !ok || obj.GVR() != podGVR {
		return ExecTarget{}, ErrNoExecTarget
	}
	holder, ok := folder.(depsHolder)
	if !ok {
		return ExecTarget{}, fmt.Errorf("pod %s/%s: folder has no cluster", obj.Namespace(), obj.Name())
	}
	deps := holder.folderDeps()
	u, err := deps.Cl.GetByGVR(ctx, podGVR, obj.Namespace(), obj.Name())
	if err != nil {
		return ExecTarget{}, fmt.Errorf("reading pod %s/%s: %w", obj.Namespace(), obj.Name(), err)
	}
	if u == nil {
		return ExecTarget{}, fmt.Errorf("pod %s/%s not found", obj.Namespace(), obj.Name())
	}
	var pod corev1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &pod); err != nil {
		return ExecTarget{}, fmt.Errorf("decoding pod %s/%s: %w", obj.Namespace(), obj.Name(), err)
	}
	return PodExecTarget(deps, &pod)
}

// PodExecTarget picks the default container of pod.
func PodExecTarget(deps Deps, pod *corev1.Pod) (ExecTarget, error) {
	if len(pod.Spec.Containers) == 0 {
		return ExecTarget{}, fmt.Errorf("pod %s/%s has no containers", pod.Namespace, pod.Name)
	}
	names := make([]string, 0, len(pod.Spec.Containers))
	for _, c := range pod.Spec.Containers {
		names = append(names, c.Name)
	}
	target := containerExecTarget(deps, pod.Namespace, pod.Name, pod.Spec.Containers[0].Name, containerKindPrimary, pod.Spec.Containers[0].Stdin, pod.Spec.Containers[0].TTY)
	if want := strings.TrimSpace(pod.Annotations[DefaultContainerAnnotation]); want != "" {
		if t, ok := findContainerTarget(deps, pod, want); ok {
			target = t
		}
	}
	target.Defaulted = true
	target.Containers = names
	return target, nil
}

// findContainerTarget looks up name in all container lists, like kubectl's
// default-container lookup.
func findContainerTarget(deps Deps, pod *corev1.Pod, name string) (ExecTarget, bool) {
	for _, c := range pod.Spec.Containers {
		if c.Name == name {
			return containerExecTarget(deps, pod.Namespace, pod.Name, c.Name, containerKindPrimary, c.Stdin, c.TTY), true
		}
	}
	for _, c := range pod.Spec.InitContainers {
		if c.Name == name {
			return containerExecTarget(deps, pod.Namespace, pod.Name, c.Name, containerKindInit, c.Stdin, c.TTY), true
		}
	}
	for _, c := range pod.Spec.EphemeralContainers {
		if c.Name == name {
			return containerExecTarget(deps, pod.Namespace, pod.Name, c.Name, containerKindEphemeral, c.Stdin, c.TTY), true
		}
	}
	return ExecTarget{}, false
}

func containerExecTarget(deps Deps, namespace, pod, container string, kind containerKind, stdin, tty bool) ExecTarget {
	key := deps.ClusterKey
	if key.ContextName == "" {
		key.ContextName = deps.CtxName
	}
	return ExecTarget{
		Cluster:        key,
		KubectlContext: deps.KubectlContext,
		Namespace:      namespace,
		Pod:            pod,
		Container:      container,
		Ephemeral:      kind == containerKindEphemeral,
		Init:           kind == containerKindInit,
		Stdin:          stdin,
		TTY:            tty,
	}
}

// impersonationArgs repeats the panel's impersonation for kubectl.
func (t ExecTarget) impersonationArgs() []string {
	var args []string
	if u := strings.TrimSpace(t.Cluster.ImpersonateUser); u != "" {
		args = append(args, "--as", u)
	}
	if g := strings.TrimSpace(t.Cluster.ImpersonateGroupsKey); g != "" {
		for _, group := range strings.Split(g, ",") {
			if group = strings.TrimSpace(group); group != "" {
				args = append(args, "--as-group", group)
			}
		}
	}
	if uid := strings.TrimSpace(t.Cluster.ImpersonateUID); uid != "" {
		args = append(args, "--as-uid", uid)
	}
	return args
}

// verbArgs is the part after the global flags.
func (t ExecTarget) verbArgs(mode ExecMode) []string {
	switch mode {
	case ExecAttach:
		args := []string{"attach"}
		switch {
		case t.Stdin && t.TTY:
			args = append(args, "-it")
		case t.Stdin:
			args = append(args, "-i")
		case t.TTY:
			args = append(args, "-t")
		}
		return append(args, t.Pod, "-n", t.Namespace, "-c", t.Container)
	default:
		return []string{"exec", "-it", t.Pod, "-n", t.Namespace, "-c", t.Container, "--", "sh", "-c", shellScript}
	}
}

// KubectlArgs returns the arguments kc runs kubectl with. They name the
// kubeconfig, context and impersonation of the panel explicitly, so the
// session talks to the cluster the panel shows whatever kubectl's own
// current context is.
func (t ExecTarget) KubectlArgs(mode ExecMode) []string {
	var args []string
	if p := strings.TrimSpace(t.Cluster.KubeconfigPath); p != "" {
		args = append(args, "--kubeconfig", p)
	}
	if c := strings.TrimSpace(t.Cluster.ContextName); c != "" {
		args = append(args, "--context", c)
	}
	args = append(args, t.impersonationArgs()...)
	return append(args, t.verbArgs(mode)...)
}

// Command is the kubectl command line shown to the user. Like the other
// shown commands it adds --context only when plain kubectl would use
// another context. Impersonation stays, since it decides what is allowed.
func (t ExecTarget) Command(mode ExecMode) string {
	parts := []string{"kubectl"}
	if c := strings.TrimSpace(t.Cluster.ContextName); c != "" && c != t.KubectlContext {
		parts = append(parts, "--context", shellQuote(c))
	}
	for _, a := range t.impersonationArgs() {
		parts = append(parts, shellQuote(a))
	}
	for _, a := range t.verbArgs(mode) {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// Label names the target for messages: pod/container.
func (t ExecTarget) Label() string {
	return t.Pod + "/" + t.Container
}
