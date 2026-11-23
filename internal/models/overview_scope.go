package models

// Most overview rules filter what they read to the folder's namespace
// scope. These findings do not: they cover the whole cluster in every scope
// (ADR 0171), and are n/a when the user may not read that (ADR 0182). The
// value says what they cover, for F3.
var overviewClusterScopes = map[string]string{
	"capacity/cpu":           "cluster: every node and every pod on it, in all namespaces",
	"capacity/memory":        "cluster: every node and every pod on it, in all namespaces",
	"capacity/pods":          "cluster: every node and every pod on it, in all namespaces",
	"capacity/disk":          "cluster: every node",
	"health/nodes-not-ready": "cluster: every node",
	"health/nodes-pressure":  "cluster: every node",
	"health/nodes-cordoned":  "cluster: every node",
	"health/pvs":             "cluster: every PersistentVolume",
}

// namespaceScopeText names a namespace scope: "namespace shop" or "all
// namespaces".
func namespaceScopeText(namespace string) string {
	if namespace == AllNamespaces {
		return "all namespaces"
	}
	return "namespace " + namespace
}

// findingScope is what a finding covers in an overview of namespace, and
// whether that is the whole cluster.
func findingScope(id, namespace string) (scope string, cluster bool) {
	if s, ok := overviewClusterScopes[id]; ok {
		return s, true
	}
	return namespaceScopeText(namespace), false
}

// sectionScope is the scope label of a section header: "cluster" when every
// finding in it covers the cluster, else the namespace scope; its cluster
// findings then carry their own label (mixed).
func sectionScope(findings []overviewFinding, section, namespace string) (label string, mixed bool) {
	cluster, local := 0, 0
	for _, f := range findings {
		if f.section != section {
			continue
		}
		if _, ok := overviewClusterScopes[f.id]; ok {
			cluster++
		} else {
			local++
		}
	}
	if cluster > 0 && local == 0 {
		return "cluster", false
	}
	return namespaceScopeText(namespace), cluster > 0
}

// overviewSwitchHint says how to look at another namespace; F3 and the
// section headers show it.
const overviewSwitchHint = "Another namespace: click the namespace in the panel's bottom border or type cd -n <namespace>, then open /overview; M-i on a list opens the overview of that list's namespace."
