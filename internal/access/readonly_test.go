package access_test

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/access"
)

var deploy = access.Target{
	Resource:  schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
	Namespace: "ns",
	Name:      "web",
}

func TestPolicyPermitsReadOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		checks []access.Check
		// want is the answer in read-only mode; a writable policy permits all.
		want bool
	}{
		{name: "get", checks: []access.Check{deploy.Check("get")}, want: true},
		{name: "list and watch", checks: []access.Check{deploy.Check("list"), deploy.Check("WATCH")}, want: true},
		{name: "edit needs a patch", checks: access.EditChecks(deploy), want: false},
		{name: "delete", checks: access.DeleteChecks(deploy), want: false},
		{name: "create namespace", checks: access.CreateNamespaceChecks(), want: false},
		{name: "debug helper", checks: access.DebugHelperChecks("ns", "pod"), want: false},
		{name: "exec", checks: access.ExecChecks("ns", "pod"), want: false},
		{name: "port-forward", checks: access.PortForwardChecks("ns", "pod"), want: false},
		{name: "no checks", checks: nil, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := (access.Policy{ReadOnly: true}).Permits(nil, tt.checks...); got != tt.want {
				t.Fatalf("read-only Permits = %v, want %v", got, tt.want)
			}
			if !(access.Policy{}).Permits(nil, tt.checks...) {
				t.Fatal("a writable policy without answers refused")
			}
		})
	}
}

func TestPolicyPermitsDecide(t *testing.T) {
	t.Parallel()
	denyDelete := func(c access.Check) access.Decision {
		if c.Verb == "delete" {
			return access.Denied
		}
		return access.Unknown
	}
	if (access.Policy{}).Permits(denyDelete, access.DeleteChecks(deploy)...) {
		t.Fatal("a Denied answer must refuse the action")
	}
	if !(access.Policy{}).Permits(denyDelete, access.EditChecks(deploy)...) {
		t.Fatal("Unknown answers must permit the action")
	}
}

func TestPolicyPermitsCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		readOnly, dangerous, want bool
	}{
		{readOnly: false, dangerous: false, want: true},
		{readOnly: false, dangerous: true, want: true},
		{readOnly: true, dangerous: false, want: true},
		{readOnly: true, dangerous: true, want: false},
	}
	for _, tt := range tests {
		if got := (access.Policy{ReadOnly: tt.readOnly}).PermitsCommand(tt.dangerous); got != tt.want {
			t.Errorf("ReadOnly=%v PermitsCommand(%v) = %v, want %v", tt.readOnly, tt.dangerous, got, tt.want)
		}
	}
}
