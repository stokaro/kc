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

// Serves reports whether API discovery lists gvr. It reads the cached
// discovery that RefreshDiscovery invalidates, so the answer is at most one
// refresh old; after a refresh the first call reads discovery from the
// server again.
//
// An error means discovery could not tell: the server did not answer, or
// the group version of gvr failed its own discovery (an aggregated API that
// is down). A caller must not take it for "not served".
func (c *Cluster) Serves(ctx context.Context, gvr schema.GroupVersionResource) (bool, error) {
	if err := c.ensureDiscovery(); err != nil {
		return false, err
	}
	return servesIn(ctx, discovery.ToCachedDiscoveryInterfaceWithContext(c.disco), gvr)
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
