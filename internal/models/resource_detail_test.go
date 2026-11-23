package models

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func resourceEntryFor(group, version, resource, kind string) resourceEntry {
	return resourceEntry{
		info: ResourceInfo{GVK: schema.GroupVersionKind{Group: group, Version: version, Kind: kind}, Resource: resource},
		gvr:  schema.GroupVersionResource{Group: group, Version: version, Resource: resource},
	}
}

// TestResourceDetailTellsTwinRowsApart checks the status line of a type
// row whose name another group serves too, such as the two /events rows
// (#323), and that a unique name keeps the plain text.
func TestResourceDetailTellsTwinRowsApart(t *testing.T) {
	t.Parallel()
	coreEvents := resourceEntryFor("", "v1", "events", "Event")
	newEvents := resourceEntryFor("events.k8s.io", "v1", "events", "Event")
	pods := resourceEntryFor("", "v1", "pods", "Pod")
	argoApps := resourceEntryFor("argoproj.io", "v1alpha1", "applications", "Application")
	sigApps := resourceEntryFor("app.k8s.io", "v1beta1", "applications", "Application")
	all := []resourceEntry{coreEvents, newEvents, pods, argoApps, sigApps}

	tests := []struct {
		name  string
		entry resourceEntry
		want  []string
	}{
		{name: "core events", entry: coreEvents, want: []string{"events (v1)", "kubectl get events", "same objects"}},
		{name: "events.k8s.io events", entry: newEvents, want: []string{"events (events.k8s.io/v1)", "same Event objects as the v1 row"}},
		{name: "unique name", entry: pods, want: []string{"pods (v1)"}},
		{name: "other twin", entry: argoApps, want: []string{"applications (argoproj.io/v1alpha1)", "/applications (app.k8s.io/v1beta1) is a different type"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := resourceDetail(tc.entry, all)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("resourceDetail() = %q, want it to contain %q", got, w)
				}
			}
		})
	}
	if got := resourceDetail(pods, all); got != "pods (v1)" {
		t.Errorf("unique name detail = %q, want exactly %q", got, "pods (v1)")
	}
	// Without its twin in the list, a type keeps the plain text.
	if got := resourceDetail(coreEvents, []resourceEntry{coreEvents, pods}); got != "events (v1)" {
		t.Errorf("events without a twin = %q, want %q", got, "events (v1)")
	}
}
