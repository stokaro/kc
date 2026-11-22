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
