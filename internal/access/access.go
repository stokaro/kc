// Package access answers "may the current identity do this?" with Kubernetes
// SelfSubjectAccessReviews. Results are cached per identity and check for a
// short time so the UI can gray out actions without blocking on the API.
//
// The answers are hints for the UI. The API server still authorizes every
// real request, so an Unknown result never blocks an action.
package access

import (
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Decision is the outcome of one access review.
type Decision int

const (
	// Unknown means the review failed, was not supported, or has not run yet.
	Unknown Decision = iota
	// Allowed means the API server reported the request as allowed.
	Allowed
	// Denied means the API server reported the request as not allowed.
	Denied
)

func (d Decision) String() string {
	switch d {
	case Allowed:
		return "allowed"
	case Denied:
		return "denied"
	default:
		return "unknown"
	}
}

// Identity names who asks: the kubeconfig, the context and the impersonation
// settings. Reviews are cached per identity, so a context or impersonation
// change never reuses answers given to someone else.
type Identity struct {
	Kubeconfig string
	Context    string
	User       string
	UID        string
	Groups     []string
	KubectlCtx string // context plain kubectl uses; --context is omitted when equal
}

func (i Identity) key() string {
	groups := append([]string(nil), i.Groups...)
	sort.Strings(groups)
	return strings.Join([]string{i.Kubeconfig, i.Context, i.User, i.UID, strings.Join(groups, ",")}, "\x00")
}

// Impersonated reports whether requests run as another user or group.
func (i Identity) Impersonated() bool {
	return strings.TrimSpace(i.User) != "" || strings.TrimSpace(i.UID) != "" || len(i.Groups) > 0
}

// Check is one resource attribute set for a SelfSubjectAccessReview.
type Check struct {
	Resource    schema.GroupVersionResource
	Subresource string
	Verb        string
	Namespace   string
	Name        string
}

// TypeLevel returns the check without the object name: "any object of this
// type in this namespace".
func (c Check) TypeLevel() Check {
	c.Name = ""
	return c
}

// ResourceString names the resource the way kubectl and RBAC rules do:
// "pods", "pods/log", "deployments.apps/scale".
func (c Check) ResourceString() string {
	r := c.Resource.Resource
	if c.Resource.Group != "" {
		r += "." + c.Resource.Group
	}
	if c.Subresource != "" {
		r += "/" + c.Subresource
	}
	return r
}

// Mutating reports whether the check asks for a write. Exec, attach and
// port-forward count as writes: they are "create" on a pod subresource and
// let the caller change what runs in the cluster.
func (c Check) Mutating() bool {
	switch strings.ToLower(c.Verb) {
	case "get", "list", "watch":
		return false
	default:
		return true
	}
}

// Target is the object or resource type the access view inspects.
type Target struct {
	Resource  schema.GroupVersionResource
	Namespace string
	Name      string // empty for a resource type row
}

// Check builds a check for verb on the target itself.
func (t Target) Check(verb string) Check {
	return Check{Resource: t.Resource, Verb: verb, Namespace: t.Namespace, Name: t.Name}
}

// SubCheck builds a check for verb on a subresource of the target.
func (t Target) SubCheck(subresource, verb string) Check {
	c := t.Check(verb)
	c.Subresource = subresource
	return c
}

// StandardVerbs are the verbs the access view always lists.
var StandardVerbs = []string{"get", "list", "watch", "create", "update", "patch", "delete"}

var (
	podsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
)

// subresourceChecks lists the subresources kc acts on, per resource. The list
// is static on purpose: it names what kc offers (logs, exec, port-forward,
// scale), not everything discovery reports.
func subresourceChecks(t Target) []Check {
	gr := t.Resource.GroupResource()
	switch gr {
	case podsGVR.GroupResource():
		return []Check{
			t.SubCheck("log", "get"),
			t.SubCheck("exec", "create"),
			t.SubCheck("portforward", "create"),
			t.SubCheck("attach", "create"),
			t.SubCheck("eviction", "create"),
			t.SubCheck("ephemeralcontainers", "patch"),
		}
	case schema.GroupResource{Group: "apps", Resource: "deployments"},
		schema.GroupResource{Group: "apps", Resource: "statefulsets"},
		schema.GroupResource{Group: "apps", Resource: "replicasets"},
		schema.GroupResource{Resource: "replicationcontrollers"}:
		return []Check{
			t.SubCheck("scale", "get"),
			t.SubCheck("scale", "update"),
		}
	}
	return nil
}

// ViewChecks lists what the access view shows for a target: the standard
// verbs, then the subresources kc uses for that resource. Verbs that only make
// sense on a collection (list, watch, create) are checked without a name: a
// named create or list is never what kubectl sends.
func ViewChecks(t Target) []Check {
	checks := make([]Check, 0, len(StandardVerbs)+6)
	for _, verb := range StandardVerbs {
		c := t.Check(verb)
		if verb == "create" || verb == "list" {
			c.Name = ""
		}
		checks = append(checks, c)
	}
	return append(checks, subresourceChecks(t)...)
}

// EditChecks are what F4 needs: kubectl edit reads the object and sends a
// patch.
func EditChecks(t Target) []Check {
	return []Check{t.Check("get"), t.Check("patch")}
}

// DeleteChecks are what F8 needs.
func DeleteChecks(t Target) []Check {
	return []Check{t.Check("delete")}
}

// LogsChecks are what the logs viewer needs for a pod.
func LogsChecks(namespace, pod string) []Check {
	return []Check{{Resource: podsGVR, Subresource: "log", Verb: "get", Namespace: namespace, Name: pod}}
}

// ExecChecks are what a shell or a pod filesystem browser needs for a pod.
func ExecChecks(namespace, pod string) []Check {
	return []Check{{Resource: podsGVR, Subresource: "exec", Verb: "create", Namespace: namespace, Name: pod}}
}

// PortForwardChecks are what port forwarding to a pod needs.
func PortForwardChecks(namespace, pod string) []Check {
	return []Check{{Resource: podsGVR, Subresource: "portforward", Verb: "create", Namespace: namespace, Name: pod}}
}

// Policy is the user's own restriction on actions, independent of RBAC. The
// read-only mode of issue #42 sets ReadOnly; until then it is always false.
type Policy struct {
	ReadOnly bool
}

// Permits reports whether an action with the given checks stays enabled:
// read-only mode refuses every write, and a Denied result refuses the action.
// Unknown results permit it; the API server still has the last word.
func (p Policy) Permits(decide func(Check) Decision, checks ...Check) bool {
	for _, c := range checks {
		if p.ReadOnly && c.Mutating() {
			return false
		}
		if decide != nil && decide(c) == Denied {
			return false
		}
	}
	return true
}
