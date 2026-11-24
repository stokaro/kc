// Package execctx describes the cluster, namespace and identity that a
// subprocess started by kc runs against, and turns it into what each kind
// of subprocess needs: kubectl flags for commands kc builds itself, and a
// generated kubeconfig for commands it does not (the embedded shell and
// custom commands).
package execctx

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Impersonation is the identity kc acts as (kc --as, --as-uid, --as-group).
type Impersonation struct {
	User   string
	UID    string
	Groups []string
}

// Normalized trims the values and drops empty groups.
func (i Impersonation) Normalized() Impersonation {
	out := Impersonation{User: strings.TrimSpace(i.User), UID: strings.TrimSpace(i.UID)}
	for _, g := range i.Groups {
		if g = strings.TrimSpace(g); g != "" {
			out.Groups = append(out.Groups, g)
		}
	}
	return out
}

// IsZero reports whether no impersonation is requested.
func (i Impersonation) IsZero() bool {
	n := i.Normalized()
	return n.User == "" && n.UID == "" && len(n.Groups) == 0
}

// Context is what a subprocess runs against. It is built from the active
// panel: kc's selected kubeconfig context, the panel's namespace and the
// impersonation kc was started with.
type Context struct {
	// Kubeconfig is the file that defines Context.
	Kubeconfig string
	// Load is what kc loads for Context: Kubeconfig, or the files of
	// $KUBECONFIG when the context came from that list. Empty means
	// Kubeconfig.
	Load string
	// Config is Load as kc loaded it, several files merged the way
	// kubectl merges them. It is read, never changed.
	Config *clientcmdapi.Config
	// Context is the kubeconfig context name.
	Context string
	// Namespace is the namespace to use. Empty keeps the namespace the
	// kubeconfig context sets; kc uses empty for "all namespaces".
	Namespace string
	// Impersonation is applied on top of the context's user. The user's
	// own credentials (token, certificate, exec plugin) are kept.
	Impersonation Impersonation
}

// IsZero reports whether no context is selected.
func (c Context) IsZero() bool { return c.Context == "" }

// KubeconfigEnv is the KUBECONFIG value with which kubectl loads what kc
// loads for the context.
func (c Context) KubeconfigEnv() string {
	if c.Load != "" {
		return c.Load
	}
	return c.Kubeconfig
}

// isList reports whether path names several files.
func isList(path string) bool { return len(filepath.SplitList(path)) > 1 }

// KubectlArgs returns the kubectl flags that select this context. They
// come after the subcommand, for example "kubectl edit pod/x <args>".
// Flags change only the impersonation fields, so kubectl still
// authenticates with the context's own credentials.
func (c Context) KubectlArgs() []string {
	var args []string
	// --kubeconfig takes one file; a merged list goes in KUBECONFIG
	// (KubeconfigEnv) instead.
	if load := c.KubeconfigEnv(); load != "" && !isList(load) {
		args = append(args, "--kubeconfig", load)
	}
	if c.Context != "" {
		args = append(args, "--context", c.Context)
	}
	if ns := strings.TrimSpace(c.Namespace); ns != "" {
		args = append(args, "--namespace", ns)
	}
	imp := c.Impersonation.Normalized()
	if imp.User != "" {
		args = append(args, "--as", imp.User)
	}
	if imp.UID != "" {
		args = append(args, "--as-uid", imp.UID)
	}
	for _, g := range imp.Groups {
		args = append(args, "--as-group", g)
	}
	return args
}

// Env returns base with KUBECONFIG replaced by kubeconfig. An empty
// kubeconfig only removes the variable.
func Env(base []string, kubeconfig string) []string {
	const prefix = "KUBECONFIG="
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		out = append(out, kv)
	}
	if kubeconfig != "" {
		out = append(out, prefix+kubeconfig)
	}
	return out
}

// ProcessEnv is Env applied to the environment of kc itself.
func ProcessEnv(kubeconfig string) []string { return Env(os.Environ(), kubeconfig) }

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,-]+$`)

// ShellJoin joins argv into a command line a POSIX shell reads back as
// argv. kc shows it to the user and logs it.
func ShellJoin(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && shellSafe.MatchString(a) {
			parts[i] = a
			continue
		}
		parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(parts, " ")
}
