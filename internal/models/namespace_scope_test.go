package models

import (
	"errors"
	"testing"
)

func TestChooseListScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		namespace   string
		all         access
		inNamespace access
		want        listScope
	}{
		{name: "allowed everywhere uses the shared cache", namespace: "team-a", all: accessAllowed, want: scopeShared},
		{name: "allowed everywhere, all namespaces", namespace: AllNamespaces, all: accessAllowed, want: scopeShared},
		{name: "denied cluster-wide, allowed in the namespace", namespace: "team-a", all: accessDenied, inNamespace: accessAllowed, want: scopeNamespace},
		{name: "denied cluster-wide and in the namespace", namespace: "team-a", all: accessDenied, inNamespace: accessDenied, want: scopeDenied},
		{name: "denied with no namespace to narrow to", namespace: AllNamespaces, all: accessDenied, want: scopeDenied},
		{name: "unknown cluster-wide in a namespace takes the namespace cache", namespace: "team-a", all: accessUnknown, inNamespace: accessAllowed, want: scopeNamespace},
		{name: "unknown everywhere in a namespace takes the namespace cache", namespace: "team-a", all: accessUnknown, inNamespace: accessUnknown, want: scopeNamespace},
		{name: "denied cluster-wide, unknown in the namespace", namespace: "team-a", all: accessDenied, inNamespace: accessUnknown, want: scopeNamespace},
		{name: "unknown without a namespace leaves it to the API server", namespace: AllNamespaces, all: accessUnknown, want: scopeShared},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := chooseListScope(tc.namespace, tc.all, tc.inNamespace); got != tc.want {
				t.Fatalf("chooseListScope(%q, %d, %d) = %s, want %s", tc.namespace, tc.all, tc.inNamespace, got, tc.want)
			}
		})
	}
}

func TestAccessOf(t *testing.T) {
	t.Parallel()
	if got := accessOf(true, nil); got != accessAllowed {
		t.Fatalf("allowed = %d", got)
	}
	if got := accessOf(false, nil); got != accessDenied {
		t.Fatalf("denied = %d", got)
	}
	// A failed review is unknown even if the boolean says false.
	if got := accessOf(false, errors.New("timeout")); got != accessUnknown {
		t.Fatalf("error = %d", got)
	}
}
