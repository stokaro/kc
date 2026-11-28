package cluster

import (
	"context"
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
)

// Serves reports whether API discovery lists gvr. It reads the last
// discovery read, so the answer is at most one refresh old.
//
// An error means discovery could not tell: the server did not answer, or
// the group version of gvr failed its own discovery (an aggregated API that
// is down). A caller must not take it for "not served".
func (c *Cluster) Serves(ctx context.Context, gvr schema.GroupVersionResource) (bool, error) {
	st := c.discoveryState()
	if st == nil {
		return false, errors.New("cluster has no discovery")
	}
	return servesIn(ctx, discovery.ToCachedDiscoveryInterfaceWithContext(st.disco), gvr)
}

// groupVersionResources is the part of a discovery client that Serves reads.
type groupVersionResources interface {
	ServerResourcesForGroupVersionWithContext(ctx context.Context, groupVersion string) (*metav1.APIResourceList, error)
}

func servesIn(ctx context.Context, d groupVersionResources, gvr schema.GroupVersionResource) (bool, error) {
	list, err := d.ServerResourcesForGroupVersionWithContext(ctx, gvr.GroupVersion().String())
	switch {
	case errors.Is(err, memory.ErrCacheNotFound), apierrors.IsNotFound(err):
		// The server lists no such group version.
		return false, nil
	case err != nil:
		return false, err
	case list == nil:
		return false, nil
	}
	for _, r := range list.APIResources {
		if r.Name == gvr.Resource {
			return true, nil
		}
	}
	return false, nil
}
