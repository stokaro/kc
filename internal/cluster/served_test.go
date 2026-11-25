package cluster

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery/cached/memory"
)

// stubResources answers ServerResourcesForGroupVersion from a map: a group
// version with an error fails with it, one that is missing is not found in
// the cache, as memory.memCacheClient answers.
type stubResources struct {
	lists map[string]*metav1.APIResourceList
	errs  map[string]error
}

func (s stubResources) ServerResourcesForGroupVersionWithContext(_ context.Context, gv string) (*metav1.APIResourceList, error) {
	if err, ok := s.errs[gv]; ok {
		return nil, err
	}
	if l, ok := s.lists[gv]; ok {
		return l, nil
	}
	return nil, memory.ErrCacheNotFound
}

func TestServesIn(t *testing.T) {
	t.Parallel()
	certs := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	down := errors.New("the server is currently unable to handle the request")
	tests := []struct {
		name    string
		d       stubResources
		served  bool
		wantErr bool
	}{
		{
			name:   "listed",
			d:      stubResources{lists: map[string]*metav1.APIResourceList{"cert-manager.io/v1": {APIResources: []metav1.APIResource{{Name: "issuers"}, {Name: "certificates"}}}}},
			served: true,
		},
		{
			name: "the group version is listed without the resource",
			d:    stubResources{lists: map[string]*metav1.APIResourceList{"cert-manager.io/v1": {APIResources: []metav1.APIResource{{Name: "issuers"}}}}},
		},
		{
			name: "only another version is listed",
			d:    stubResources{lists: map[string]*metav1.APIResourceList{"cert-manager.io/v1beta1": {APIResources: []metav1.APIResource{{Name: "certificates"}}}}},
		},
		{
			name: "the server says the group version is not found",
			d:    stubResources{errs: map[string]error{"cert-manager.io/v1": apierrors.NewNotFound(schema.GroupResource{}, "")}},
		},
		{
			name:    "the group version failed its discovery",
			d:       stubResources{errs: map[string]error{"cert-manager.io/v1": down}},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			served, err := servesIn(t.Context(), tt.d, certs)
			if served != tt.served || (err != nil) != tt.wantErr {
				t.Fatalf("servesIn = %v, %v; want %v, error %v", served, err, tt.served, tt.wantErr)
			}
		})
	}
}
