package navigation

import "slices"

// Location is a place a panel can return to: the kubeconfig context, the
// panel's namespace scope and the row IDs entered from the panel root. The
// first step of a resource list is the GVR-keyed resource row ID, so a
// location never depends on a bare resource name.
type Location struct {
	// Context is the kubeconfig context name.
	Context string `json:"context"`
	// Namespace is the panel's namespace scope; models.AllNamespaces for
	// all namespaces.
	Namespace string `json:"namespace"`
	// Steps are the row IDs entered from the root, as Navigator.Steps
	// returns them.
	Steps []string `json:"steps,omitempty"`
	// Path is the breadcrumb shown for the location. It is a label only and
	// takes no part in Equal.
	Path string `json:"path,omitempty"`
}

// Equal reports whether l and o name the same place. Path is ignored.
func (l Location) Equal(o Location) bool {
	return l.Context == o.Context && l.Namespace == o.Namespace && slices.Equal(l.Steps, o.Steps)
}

// CommonSteps returns how many leading steps l and o share.
func (l Location) CommonSteps(o Location) int {
	n := 0
	for n < len(l.Steps) && n < len(o.Steps) && l.Steps[n] == o.Steps[n] {
		n++
	}
	return n
}
