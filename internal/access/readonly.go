package access

import "k8s.io/apimachinery/pkg/runtime/schema"

var namespacesGVR = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}

// CreateNamespaceChecks are what F7 needs.
func CreateNamespaceChecks() []Check {
	return []Check{{Resource: namespacesGVR, Verb: "create"}}
}

// DebugHelperChecks are what starting the pod filesystem debug helper needs:
// kc adds an ephemeral container (an update of pods/ephemeralcontainers, as
// client-go's UpdateEphemeralContainers sends) and then execs into it.
func DebugHelperChecks(namespace, pod string) []Check {
	return append([]Check{{
		Resource:    podsGVR,
		Subresource: "ephemeralcontainers",
		Verb:        "update",
		Namespace:   namespace,
		Name:        pod,
	}}, ExecChecks(namespace, pod)...)
}

// PermitsCommand reports whether a custom command may run. kc cannot know
// what a shell command does, so a command declares it: dangerous commands
// are refused in read-only mode, every other command runs.
func (p Policy) PermitsCommand(dangerous bool) bool {
	return !p.ReadOnly || !dangerous
}

// ReadOnlyRefusal is the message shown when read-only mode refuses action.
func ReadOnlyRefusal(action string) string {
	return "Read-only mode: " + action + " is disabled"
}
