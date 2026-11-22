// Package access decides whether kc may run an action. Today the only input
// is the read-only mode (issue #42): Policy.ReadOnly refuses every mutating
// check. The access review work of issue #69 adds RBAC answers through the
// decide function of Policy.Permits; the types and names here match it.
//
// The answers are hints for the UI and a guard against accidental writes.
// The API server still authorizes every real request.
package access

import (
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

// Check is one resource attribute set for a SelfSubjectAccessReview.
type Check struct {
	Resource    schema.GroupVersionResource
	Subresource string
	Verb        string
	Namespace   string
	Name        string
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

// Target is the object or resource type an action works on.
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

var (
	podsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
)

// EditChecks are what F4 needs: kubectl edit reads the object and sends a
// patch.
func EditChecks(t Target) []Check {
	return []Check{t.Check("get"), t.Check("patch")}
}

// DeleteChecks are what F8 needs.
func DeleteChecks(t Target) []Check {
	return []Check{t.Check("delete")}
}

// ExecChecks are what a shell or a pod filesystem browser needs for a pod.
func ExecChecks(namespace, pod string) []Check {
	return []Check{{Resource: podsGVR, Subresource: "exec", Verb: "create", Namespace: namespace, Name: pod}}
}

// PortForwardChecks are what port forwarding to a pod needs.
func PortForwardChecks(namespace, pod string) []Check {
	return []Check{{Resource: podsGVR, Subresource: "portforward", Verb: "create", Namespace: namespace, Name: pod}}
}

// Policy is the user's own restriction on actions, independent of RBAC.
// ReadOnly comes from the --readonly flag and the readOnly config.
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
