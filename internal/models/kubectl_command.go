package models

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// KubectlCommander is implemented by folders that can show the kubectl command
// producing roughly the same listing. The UI shows it above the command line.
type KubectlCommander interface {
	KubectlCommand() string
}

// CommandLine is what the command line shows for a selected row, and what
// M-c copies.
type CommandLine struct {
	// Label precedes the command: "Command" for an equivalent command,
	// "Inputs (5)" for the queries behind a result kc computes.
	Label string
	// Show is the command on the line.
	Show string
	// Copy is what M-c copies; Show when empty.
	Copy string
	// What names the copied text in the confirmation; Show when empty.
	What string
}

// SelectionCommander is a folder whose command line follows the selected
// row. ok is false when the row has no command of its own; the folder's
// KubectlCommand is shown then.
type SelectionCommander interface {
	CommandLineFor(item Item) (line CommandLine, ok bool)
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
	// The identity kc impersonates (kc --as) reads the same objects only
	// when kubectl impersonates it too.
	if u := strings.TrimSpace(deps.ClusterKey.ImpersonateUser); u != "" {
		parts = append(parts, "--as", shellQuote(u))
	}
	if uid := strings.TrimSpace(deps.ClusterKey.ImpersonateUID); uid != "" {
		parts = append(parts, "--as-uid", shellQuote(uid))
	}
	if groups := strings.TrimSpace(deps.ClusterKey.ImpersonateGroupsKey); groups != "" {
		for g := range strings.SplitSeq(groups, ",") {
			parts = append(parts, "--as-group", shellQuote(g))
		}
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
	namespaced := o.resourceNamespaced()
	filter := o.RowFilter()
	filterNS, filterOther := filter.kubectlParts()
	switch {
	case filterNS != "" && namespaced && o.namespace == "":
		// "-n web" in a list of all namespaces is kubectl's own -n.
		args = append(args, namespaceArgs(filterNS, namespaced)...)
	case filterNS != "":
		args = append(args, namespaceArgs(o.namespace, namespaced)...)
		filterOther = append([]string{"-n " + filterNS}, filterOther...)
	default:
		args = append(args, namespaceArgs(o.namespace, namespaced)...)
	}
	args = append(args, o.kubectlArgs...)
	if sel := filter.Selector(); sel != "" {
		args = append(args, "-l", sel)
	}
	var notes []string
	if o.kubectlNote != "" {
		notes = append(notes, o.kubectlNote)
	}
	if len(filterOther) > 0 {
		// kubectl has no flag for these; say so rather than print a
		// command that lists more than the panel shows.
		notes = append(notes, "kc also filters: "+strings.Join(filterOther, ", "))
	}
	if o.Deps.AppConfig != nil {
		cc := planCustomColumns(o.Deps, o.gvr, o.requestedColumnsMode())
		ccArgs, ccNote := cc.kubectlArgs(namespaced && o.namespace == "")
		args = append(args, ccArgs...)
		if ccNote != "" {
			notes = append(notes, ccNote)
		}
	}
	cmd := kubectlCommand(o.Deps, args...)
	if len(notes) > 0 {
		cmd += "  # " + strings.Join(notes, "; ")
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
	// The folder lists both fields, so the command prints both.
	return kubectlCommand(f.Deps, "get", "configmap", f.Name, "-n", f.Namespace, "-o", `jsonpath={.data}{"\n"}{.binaryData}`)
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

// configMapKeyViewCommand prints one ConfigMap key from the field it lives
// in; binaryData values are base64 in the API and are decoded.
func configMapKeyViewCommand(deps Deps, namespace, name, key string, binary bool) string {
	if !binary {
		return keyViewCommand(deps, "configmap", namespace, name, key, false)
	}
	return kubectlCommand(deps, "get", "configmap", name, "-n", namespace, "-o", "jsonpath={.binaryData."+jsonpathKey(key)+"}") + " | base64 -d"
}

// logsCommand prints the kubectl command for the options LogsSpec.PodLogOptions
// sends: recent lines, the since window, timestamps, the previous instance,
// then follow.
func logsCommand(deps Deps, spec LogsSpec) string {
	args := []string{"logs", spec.Pod, "-n", spec.Namespace}
	if spec.Container != "" {
		args = append(args, "-c", spec.Container)
	}
	if tail := spec.EffectiveTailLines(); tail > 0 {
		args = append(args, fmt.Sprintf("--tail=%d", tail))
	}
	if !spec.SinceTime.IsZero() {
		args = append(args, "--since-time="+spec.SinceTime.UTC().Format(time.RFC3339))
	} else if since := FormatLogsSince(spec.SinceSeconds); since != "" {
		args = append(args, "--since="+since)
	}
	if spec.Timestamps {
		args = append(args, "--timestamps")
	}
	if spec.Previous {
		args = append(args, "-p")
	}
	if spec.Follow {
		args = append(args, "-f")
	}
	return kubectlCommand(deps, args...)
}

// PanelLogsCommand prints the kubectl command for what a Logs panel
// streams: spec, or every container of spec's pod with allContainers.
func PanelLogsCommand(deps Deps, spec LogsSpec, allContainers bool) string {
	if !allContainers {
		return logsCommand(deps, spec)
	}
	spec.Container = ""
	return logsCommand(deps, spec) + " --all-containers --prefix"
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
