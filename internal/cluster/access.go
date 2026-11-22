package cluster

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	authorizationv1client "k8s.io/client-go/kubernetes/typed/authorization/v1"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// accessReviewTimeout bounds one SelfSubjectAccessReview.
const accessReviewTimeout = 5 * time.Second

// ListWatchAccess answers whether the client's user may list and watch a
// resource, in all namespaces or in one, which an informer needs. It asks
// the API server with SelfSubjectAccessReviews and keeps every answer for the
// life of the cluster: RBAC changes during a session are rare, and a wrong
// answer only means an informer fails as it did before.
//
// Errors are not kept, so the next call asks again.
type ListWatchAccess struct {
	client authorizationv1client.SelfSubjectAccessReviewsGetter

	mu      sync.Mutex
	all     *bool // list and watch of every resource everywhere; nil until known
	results map[accessKey]bool
	flight  singleflight.Group
}

type accessKey struct {
	gr        schema.GroupResource
	namespace string
}

// NewListWatchAccess returns a checker that sends its reviews through client.
func NewListWatchAccess(client authorizationv1client.SelfSubjectAccessReviewsGetter) *ListWatchAccess {
	return &ListWatchAccess{client: client, results: map[accessKey]bool{}}
}

// AllNamespaces reports whether list and watch of gvr across all namespaces
// (or of a cluster-scoped gvr at all) are allowed.
func (a *ListWatchAccess) AllNamespaces(ctx context.Context, gvr schema.GroupVersionResource) (bool, error) {
	return a.allowed(ctx, accessKey{gr: gvr.GroupResource()})
}

// InNamespace reports whether list and watch of gvr in namespace are allowed.
func (a *ListWatchAccess) InNamespace(ctx context.Context, gvr schema.GroupVersionResource, namespace string) (bool, error) {
	return a.allowed(ctx, accessKey{gr: gvr.GroupResource(), namespace: namespace})
}

// allowed answers for one key. RBAC rules name groups and resources, not
// versions, so answers are kept per group and resource.
func (a *ListWatchAccess) allowed(ctx context.Context, k accessKey) (bool, error) {
	a.mu.Lock()
	if ok, known := a.results[k]; known {
		a.mu.Unlock()
		return ok, nil
	}
	a.mu.Unlock()
	v, err, _ := a.flight.Do(k.namespace+"\x00"+k.gr.String(), func() (any, error) {
		every, err := a.everything(ctx)
		if err == nil && every {
			return true, nil
		}
		ok, err := a.listAndWatch(ctx, k.gr.Group, k.gr.Resource, k.namespace)
		if err != nil {
			return false, err
		}
		a.mu.Lock()
		a.results[k] = ok
		a.mu.Unlock()
		return ok, nil
	})
	if err != nil {
		return false, err
	}
	return v.(bool), nil
}

// everything asks once whether the user may list and watch every resource
// in every group and namespace, as cluster admins may. Then one pair of
// reviews answers all types instead of one pair per type.
func (a *ListWatchAccess) everything(ctx context.Context) (bool, error) {
	a.mu.Lock()
	if a.all != nil {
		v := *a.all
		a.mu.Unlock()
		return v, nil
	}
	a.mu.Unlock()
	v, err, _ := a.flight.Do("*", func() (any, error) {
		ok, err := a.listAndWatch(ctx, "*", "*", "")
		if err != nil {
			return false, err
		}
		a.mu.Lock()
		a.all = &ok
		a.mu.Unlock()
		return ok, nil
	})
	if err != nil {
		return false, err
	}
	return v.(bool), nil
}

// listAndWatch reviews list, and watch only when list is allowed.
func (a *ListWatchAccess) listAndWatch(ctx context.Context, group, resource, namespace string) (bool, error) {
	for _, verb := range []string{"list", "watch"} {
		ok, err := a.review(ctx, verb, group, resource, namespace)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func (a *ListWatchAccess) review(ctx context.Context, verb, group, resource, namespace string) (bool, error) {
	if a.client == nil {
		return false, fmt.Errorf("no access review client")
	}
	ctx, cancel := context.WithTimeout(ctx, accessReviewTimeout)
	defer cancel()
	gr := schema.GroupResource{Group: group, Resource: resource}
	log := ctrllog.FromContext(ctx).WithName("access")
	log.Info("creating SelfSubjectAccessReview", "verb", verb, "group", group, "resource", resource, "namespace", namespace)
	review := &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace: namespace,
				Verb:      verb,
				Group:     group,
				Resource:  resource,
			},
		},
	}
	resp, err := a.client.SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("access review %s %s: %w", verb, gr, err)
	}
	if !resp.Status.Allowed && resp.Status.EvaluationError != "" {
		// Some rules could not be read; the answer is not a denial.
		return false, fmt.Errorf("access review %s %s: %s", verb, gr, resp.Status.EvaluationError)
	}
	return resp.Status.Allowed, nil
}
