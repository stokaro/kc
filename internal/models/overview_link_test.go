package models

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	kccluster "github.com/stokaro/kc/internal/cluster"
	kctesting "github.com/stokaro/kc/internal/testing"
)

// TestOverviewEventLinksToTheInvolvedObject maps involved objects with the
// discovery of a real API server.
func TestOverviewEventLinksToTheInvolvedObject(t *testing.T) {
	if testCfg == nil {
		t.Skip("envtest not available")
	}
	ctx := t.Context()
	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatal(err)
	}
	kctesting.RunInBackground(t, "cluster", cl.Start)
	deps := Deps{Cl: cl, Ctx: ctx}
	tests := []struct {
		name        string
		ref         corev1.ObjectReference
		want        ObjectRef
		wantMissing string
	}{
		{
			name: "a pod in the event's namespace",
			ref:  corev1.ObjectReference{APIVersion: "v1", Kind: "Pod", Name: "web"},
			want: ObjectRef{GVR: ovPodsGVR, Namespaced: true, Namespace: "a", Name: "web"},
		},
		{
			name: "the reference keeps the involved object's UID",
			ref:  corev1.ObjectReference{APIVersion: "v1", Kind: "Pod", Name: "web", UID: "old-uid"},
			want: ObjectRef{GVR: ovPodsGVR, Namespaced: true, Namespace: "a", Name: "web", UID: "old-uid"},
		},
		{
			name: "a deployment in its own namespace",
			ref:  corev1.ObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "b", Name: "api"},
			want: ObjectRef{GVR: ovDeploymentsGVR, Namespaced: true, Namespace: "b", Name: "api"},
		},
		{
			name: "a node has no namespace",
			ref:  corev1.ObjectReference{APIVersion: "v1", Kind: "Node", Namespace: "a", Name: "n1"},
			want: ObjectRef{GVR: ovNodesGVR, Name: "n1"},
		},
		{
			name:        "a kind the cluster does not serve",
			ref:         corev1.ObjectReference{APIVersion: "example.com/v1", Kind: "Widget", Name: "w"},
			wantMissing: "example.com/v1 Widget is not served by the cluster",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, missing := involvedObjectRef(deps, tc.ref, "a")
			if got != tc.want || missing != tc.wantMissing {
				t.Fatalf("involvedObjectRef = %+v %q, want %+v %q", got, missing, tc.want, tc.wantMissing)
			}
		})
	}
}

// TestOverviewTargetRowsLinkEventsOnly checks that only event rows open
// another object on Enter, and that an event without a known mapping says
// why instead of opening nothing.
func TestOverviewTargetRowsLinkEventsOnly(t *testing.T) {
	t.Parallel()
	f := &OverviewTargetsFolder{BaseFolder: NewBaseFolder(Deps{}, nil, []string{"overview", "events"})}
	ref := corev1.ObjectReference{APIVersion: "v1", Kind: "Pod", Name: "web"}
	event := f.targetRow(overviewTarget{gvr: ovEventsGVR, namespace: "a", name: "web.1", involved: &ref, severity: severityWarning}, nil)
	link, ok := event.(ObjectLink)
	if !ok {
		t.Fatalf("event row = %T, want an ObjectLink", event)
	}
	if got, missing := link.LinkTarget(); !got.GVR.Empty() || !strings.Contains(missing, "not known yet") {
		t.Fatalf("LinkTarget without discovery = %+v %q", got, missing)
	}
	pod := f.targetRow(overviewTarget{gvr: ovPodsGVR, namespace: "a", name: "web", severity: severityWarning}, nil)
	if _, ok := pod.(ObjectLink); ok {
		t.Fatalf("pod row %T is a link; Enter must open the pod's folder", pod)
	}
}

// TestOverviewSeverityStyles checks that the overview uses the row health
// colors of object lists.
func TestOverviewSeverityStyles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		sev  overviewSeverity
		want RowHealth
	}{
		{severityOK, RowHealthOK},
		{severityWarning, RowHealthNotReady},
		{severityFailed, RowHealthFailed},
		{severityUnknown, RowHealthUnknown},
	}
	for _, tc := range tests {
		if got := tc.sev.RowHealth(); got != tc.want {
			t.Errorf("%v.RowHealth() = %v, want %v", tc.sev, got, tc.want)
		}
	}
	if overviewSeverityStyle(severityWarning) != NotReadyStyle() {
		t.Error("a warning does not use the not-ready row style")
	}
	if overviewSeverityStyle(severityFailed) != JobFailedStyle() {
		t.Error("a failure does not use the failed row style")
	}
	if overviewSeverityStyle(severityOK).GetForeground() != WhiteStyle().GetForeground() {
		t.Error("ok does not use the normal row style")
	}
	if got := overviewSeverityStyle(severityUnknown); got.GetFaint() != DimStyle().GetFaint() || !got.GetFaint() {
		t.Error("an unknown source is not dim")
	}
	if RowHealthStyle(RowHealthSucceeded, nil) != JobSuccessStyle() {
		t.Error("a succeeded row does not use the success style")
	}
}
