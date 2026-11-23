package models

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	table "github.com/stokaro/kc/internal/table"
	"github.com/stokaro/kc/pkg/appconfig"
)

// enteredRowIDs enters an object of gvr in namespace ns the way an object
// list does and returns the IDs of the rows of the folder it opens. It
// fails the test when Enter would open the YAML instead.
func enteredRowIDs(t *testing.T, gvr schema.GroupVersionResource, ns string) []string {
	t.Helper()
	ctor, ok := ChildFor(gvr)
	if !ok {
		t.Fatalf("%s has no folder: Enter opens its YAML", gvr)
	}
	deps := Deps{AppConfig: appconfig.Default()}
	f := ctor(deps, ns, "obj", []string{gvr.Resource, "obj"})
	types, ok := f.(*ChildResourceTypesFolder)
	if !ok {
		// A dedicated folder (Pod containers, ConfigMap keys) adds its
		// rows itself; its own tests cover them.
		return nil
	}
	rows, err := types.populate(t.Context())
	if err != nil {
		t.Fatalf("%s: populate: %v", gvr, err)
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		id, _, _, _ := r.Columns()
		ids = append(ids, id)
	}
	return ids
}

func hasSegment(ids []string, segment string) bool {
	for _, id := range ids {
		if strings.HasSuffix(id, "/"+segment) {
			return true
		}
	}
	return false
}

// TestKindsWithDiagnosisCanBeEntered fails when a kind gets a /diagnosis
// row but its objects cannot be entered, so the row is unreachable from a
// list (#323).
func TestKindsWithDiagnosisCanBeEntered(t *testing.T) {
	kinds := []schema.GroupVersionResource{
		serviceGVR,
		hpaGVR,
		pvcGVR,
		ingressGVR,
		{Group: gatewayGroup, Version: "v1", Resource: "httproutes"},
		{Group: gatewayGroup, Version: "v1beta1", Resource: "httproutes"},
		{Group: fluxKustomizationGK.Group, Version: "v1", Resource: "kustomizations"},
		{Group: fluxHelmReleaseGK.Group, Version: "v2", Resource: "helmreleases"},
		{Group: argoApplicationGK.Group, Version: "v1alpha1", Resource: "applications"},
	}
	kinds = append(kinds, workloadDiagnosisGVRs...)
	for _, gvr := range kinds {
		t.Run(gvr.String(), func(t *testing.T) {
			if !hasDiagnosis(gvr) {
				t.Fatalf("%s is listed here but has no /diagnosis; update the list", gvr)
			}
			ids := enteredRowIDs(t, gvr, "ns")
			if ids != nil && !hasSegment(ids, DiagnosisSegment) {
				t.Fatalf("%s folder rows = %v, want a /%s row", gvr, ids, DiagnosisSegment)
			}
		})
	}
}

// TestDeploymentFolderListsOnlyReplicaSets checks that a Deployment folder
// offers the ReplicaSets it owns and no ReplicationControllers, which a
// Deployment never owns (#323).
func TestDeploymentFolderListsOnlyReplicaSets(t *testing.T) {
	ids := enteredRowIDs(t, relDeploymentsGVR, "ns")
	if !hasSegment(ids, "replicasets") {
		t.Fatalf("Deployment folder rows = %v, want /replicasets", ids)
	}
	if hasSegment(ids, "replicationcontrollers") {
		t.Fatalf("Deployment folder rows = %v, want no /replicationcontrollers", ids)
	}
}

// TestKindsWithoutRowsOpenYAML shows the rule refuses: a kind with no
// diagnosis and no registered rows still opens its YAML on Enter.
func TestKindsWithoutRowsOpenYAML(t *testing.T) {
	for _, gvr := range []schema.GroupVersionResource{
		{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"},
		{Version: "v1", Resource: "serviceaccounts"},
		{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"},
	} {
		if _, ok := ChildFor(gvr); ok {
			t.Errorf("%s has a folder, want Enter to open its YAML", gvr)
		}
	}
}

// TestConfiguredHealthKindCanBeEntered covers a custom resource that gets
// its /diagnosis from the health: configuration. It changes the global
// adapters, so it does not run in parallel.
func TestConfiguredHealthKindCanBeEntered(t *testing.T) {
	widgets := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	if _, ok := ChildFor(widgets); ok {
		t.Fatalf("an unconfigured custom resource has a folder")
	}
	installHealth(t, map[string][]appconfig.HealthRule{"example.com/v1/widgets": fluxRules})
	if ids := enteredRowIDs(t, widgets, "ns"); !hasSegment(ids, DiagnosisSegment) {
		t.Fatalf("configured widget folder rows = %v, want a /%s row", ids, DiagnosisSegment)
	}
}

// TestRegisteredObjectRowsCanBeEntered covers rows another package adds
// with RegisterObjectRows to a kind without a folder of its own.
func TestRegisteredObjectRowsCanBeEntered(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "registry-test.example.com", Version: "v1", Resource: "gizmos"}
	if _, ok := ChildFor(gvr); ok {
		t.Fatalf("%s has a folder before any rows are registered", gvr)
	}
	RegisterObjectRows(gvr, func(_ Deps, namespace, name string, _ []string) []table.Row {
		return []table.Row{NewChildResourceItem(namespace+"/"+name+"/action", []string{"action"}, nil, nil)}
	})
	if ids := enteredRowIDs(t, gvr, "ns"); !hasSegment(ids, "action") {
		t.Fatalf("folder rows = %v, want the registered action row", ids)
	}
}
