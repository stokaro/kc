package models

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// White-box: the folder's type map is filled by populate, which needs a
// cluster; the test sets it directly to check that hidden types are found.
func TestResourcesFolderTypeItem(t *testing.T) {
	f := NewResourcesFolder(NewBaseFolder(Deps{}, nil, nil))
	pods := &ResourceGroupItem{gvr: schema.GroupVersionResource{Version: "v1", Resource: "pods"}}
	deployV1 := &ResourceGroupItem{gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}}
	deployBeta := &ResourceGroupItem{gvr: schema.GroupVersionResource{Group: "apps", Version: "v1beta1", Resource: "deployments"}}

	if _, _, ok := f.TypeItem(schema.GroupResource{Resource: "pods"}); ok {
		t.Fatalf("TypeItem found a type before population")
	}
	f.setItems(map[string]*ResourceGroupItem{
		"default//v1/pods":                 pods,
		"default/apps/v1beta1/deployments": deployBeta,
		"default/apps/v1/deployments":      deployV1,
	})

	tests := []struct {
		name   string
		gr     schema.GroupResource
		wantID string
		want   *ResourceGroupItem
	}{
		{name: "core type", gr: schema.GroupResource{Resource: "pods"}, wantID: "default//v1/pods", want: pods},
		{name: "lowest id among versions", gr: schema.GroupResource{Group: "apps", Resource: "deployments"}, wantID: "default/apps/v1/deployments", want: deployV1},
		{name: "group must match", gr: schema.GroupResource{Resource: "deployments"}},
		{name: "unknown", gr: schema.GroupResource{Resource: "services"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			item, id, ok := f.TypeItem(tc.gr)
			if tc.want == nil {
				if ok {
					t.Fatalf("TypeItem(%v) = %q, want not found", tc.gr, id)
				}
				return
			}
			if !ok || id != tc.wantID || item != Item(tc.want) {
				t.Fatalf("TypeItem(%v) = %q, %v; want %q", tc.gr, id, ok, tc.wantID)
			}
		})
	}
}

// White-box for the same reason: ExplicitItemByID reads the type map that
// populate fills.
func TestResourcesFolderExplicitItemByID(t *testing.T) {
	t.Parallel()
	f := NewResourcesFolder(NewBaseFolder(Deps{}, nil, nil))
	jobs := &ResourceGroupItem{gvr: schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}}
	betaJobs := &ResourceGroupItem{gvr: schema.GroupVersionResource{Group: "batch", Version: "v1beta1", Resource: "jobs"}}
	otherJobs := &ResourceGroupItem{gvr: schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "jobs"}}
	f.setItems(map[string]*ResourceGroupItem{
		"default/batch/v1/jobs":       jobs,
		"default/batch/v1beta1/jobs":  betaJobs,
		"default/example.com/v1/jobs": otherJobs,
	})
	tests := []struct {
		id   string
		want *ResourceGroupItem
	}{
		{id: "default/batch/v1/jobs", want: jobs},
		{id: "default/batch/v1beta1/jobs", want: betaJobs},
		{id: "default/example.com/v1/jobs", want: otherJobs},
		{id: "default/batch/v2/jobs"},
		{id: "other/batch/v1/jobs"},
		{id: "jobs"},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			item, ok := f.ExplicitItemByID(t.Context(), tc.id)
			if tc.want == nil {
				if ok {
					t.Fatalf("ExplicitItemByID(%q) found %v, want not found", tc.id, item)
				}
				return
			}
			if !ok || item != Item(tc.want) {
				t.Fatalf("ExplicitItemByID(%q) = %v, %v; want %v", tc.id, item, ok, tc.want.gvr)
			}
		})
	}
}
