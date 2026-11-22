package models

import (
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// KubectlCommander is implemented by folders that can show the kubectl command
// producing roughly the same listing. The UI shows it above the command line.
type KubectlCommander interface {
	KubectlCommand() string
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,-]+$`)

func shellQuote(s string) string {
	if s != "" && shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// kubectlCommand joins kubectl arguments, quoting where needed, and adds
// --context when the folder's context is not the one plain kubectl uses.
func kubectlCommand(deps Deps, args ...string) string {
	parts := []string{"kubectl"}
	if deps.CtxName != "" && deps.CtxName != deps.KubectlContext {
		parts = append(parts, "--context", shellQuote(deps.CtxName))
	}
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// kubectlResource names a resource the way kubectl accepts it: "pods",
// "deployments.apps". The group keeps the name unique across API groups.
func kubectlResource(gvr schema.GroupVersionResource) string {
	if gvr.Group == "" {
		return gvr.Resource
	}
	return gvr.Resource + "." + gvr.Group
}

func namespaceArgs(namespace string, namespaced bool) []string {
	switch {
	case !namespaced:
		return nil
	case namespace == "":
		return []string{"-A"}
	default:
		return []string{"-n", namespace}
	}
}

// KubectlCommand lists the types the root shows: namespaced and
// cluster-scoped ones alike.
func (f *RootFolder) KubectlCommand() string {
	return kubectlCommand(f.Deps, "api-resources")
}

func (f *ContextRootFolder) KubectlCommand() string {
	return kubectlCommand(f.Deps, "api-resources", "--namespaced=false")
}

func (f *ClusterResourcesFolder) KubectlCommand() string {
	return kubectlCommand(f.Deps, "api-resources", "--namespaced=false")
}

func (f *NamespacedResourcesFolder) KubectlCommand() string {
	return kubectlCommand(f.Deps, "api-resources", "--namespaced=true")
}

func (o *ObjectsFolder) KubectlCommand() string {
	args := []string{"get", kubectlResource(o.gvr)}
	args = append(args, namespaceArgs(o.namespace, o.resourceNamespaced())...)
	args = append(args, o.kubectlArgs...)
	cmd := kubectlCommand(o.Deps, args...)
	if o.kubectlNote != "" {
		cmd += "  # " + o.kubectlNote
	}
	return cmd
}

func (f *NodeChildFolder) KubectlCommand() string {
	return kubectlCommand(f.Deps, "get", "pods", "-A", "--field-selector", "spec.nodeName="+f.nodeName)
}

func (f *ChildResourceTypesFolder) KubectlCommand() string {
	args := []string{"get", kubectlResource(f.parentGVR), f.parentName}
	if f.parentNamespace != "" {
		args = append(args, "-n", f.parentNamespace)
	}
	return kubectlCommand(f.Deps, args...)
}

func (f *PodContainersFolder) KubectlCommand() string {
	return kubectlCommand(f.Deps, "get", "pod", f.Pod, "-n", f.Namespace, "-o",
		`jsonpath={range .spec.containers[*]}{.name}{"\n"}{end}`)
}

func containerField(kind containerKind) string {
	switch kind {
	case containerKindInit:
		return "initContainers"
	case containerKindEphemeral:
		return "ephemeralContainers"
	default:
		return "containers"
	}
}

func (f *PodContainerListFolder) KubectlCommand() string {
	return kubectlCommand(f.Deps, "get", "pod", f.Pod, "-n", f.Namespace, "-o",
		`jsonpath={range .spec.`+containerField(f.Kind)+`[*]}{.name}{"\n"}{end}`)
}

func (f *PodContainerDetailFolder) KubectlCommand() string {
	return kubectlCommand(f.Deps, "logs", f.Pod, "-n", f.Namespace, "-c", f.Container)
}

func (f *PodContainerFSFolder) KubectlCommand() string {
	if f.session == nil {
		return ""
	}
	spec := f.session.spec
	container, dir := f.session.execTarget(f.dir)
	return kubectlCommand(f.Deps, "exec", spec.Pod, "-n", spec.Namespace, "-c", container, "--", "ls", "-la", dir)
}

func (f *ConfigMapKeysFolder) KubectlCommand() string {
	return kubectlCommand(f.Deps, "get", "configmap", f.Name, "-n", f.Namespace, "-o", "jsonpath={.data}")
}

func (f *SecretKeysFolder) KubectlCommand() string {
	return kubectlCommand(f.Deps, "get", "secret", f.Name, "-n", f.Namespace, "-o", "jsonpath={.data}")
}

// jsonpathKey escapes a map key for a kubectl jsonpath expression.
func jsonpathKey(key string) string {
	return strings.ReplaceAll(key, ".", `\.`)
}

// keyViewCommand prints one ConfigMap or Secret key; secrets are decoded.
func keyViewCommand(deps Deps, kind, namespace, name, key string, secret bool) string {
	cmd := kubectlCommand(deps, "get", kind, name, "-n", namespace, "-o", "jsonpath={.data."+jsonpathKey(key)+"}")
	if secret {
		cmd += " | base64 -d"
	}
	return cmd
}

// logsCommand matches the logs viewer: recent lines, then follow.
func logsCommand(deps Deps, spec LogsSpec) string {
	args := []string{"logs", spec.Pod, "-n", spec.Namespace}
	if spec.Container != "" {
		args = append(args, "-c", spec.Container)
	}
	if spec.TailLines > 0 {
		args = append(args, fmt.Sprintf("--tail=%d", spec.TailLines))
	}
	if spec.Follow {
		args = append(args, "-f")
	}
	return kubectlCommand(deps, args...)
}

// helmCommand joins helm arguments like kubectlCommand; helm names the
// context flag --kube-context.
func helmCommand(deps Deps, args ...string) string {
	parts := []string{"helm"}
	if deps.CtxName != "" && deps.CtxName != deps.KubectlContext {
		parts = append(parts, "--kube-context", shellQuote(deps.CtxName))
	}
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// helmListCommand lists every release in the scope, whatever its status,
// as the Helm folder does.
func helmListCommand(deps Deps, namespace string) string {
	if namespace == AllNamespaces {
		return helmCommand(deps, "list", "-a", "-A")
	}
	return helmCommand(deps, "list", "-a", "-n", namespace)
}

func (f *HelmReleasesFolder) KubectlCommand() string {
	return helmListCommand(f.Deps, f.namespace)
}

func (f *HelmReleaseFolder) KubectlCommand() string {
	args := append([]string{"status", f.release, "-n", f.namespace}, revisionArgs(f.revision)...)
	return helmCommand(f.Deps, args...)
}

func (f *HelmHistoryFolder) KubectlCommand() string {
	return helmCommand(f.Deps, "history", f.release, "-n", f.namespace)
}

// KubectlCommand compares the stored manifest with the live objects.
func (f *HelmResourcesFolder) KubectlCommand() string {
	args := append([]string{"get", "manifest", f.rel.Name, "-n", f.rel.Namespace}, revisionArgs(f.revision)...)
	return helmCommand(f.Deps, args...) + " | " + kubectlCommand(f.Deps, "get", "-f", "-")
}
