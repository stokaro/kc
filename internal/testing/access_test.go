package kctesting_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kctesting "github.com/stokaro/kc/internal/testing"
)

// TestWaitForAccessPollsUntilEveryVerbIsAllowed stands in for an authorizer
// that denies watch for a few reviews, as one that has not yet seen a new
// binding does, and checks that the wait asks for every verb with the
// given attributes and returns only once all are allowed.
func TestWaitForAccessPollsUntilEveryVerbIsAllowed(t *testing.T) {
	t.Parallel()
	const deniedWatches = 3
	var (
		mu      sync.Mutex
		asked   []authorizationv1.ResourceAttributes
		watches int
	)
	c := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Create: func(_ context.Context, _ crclient.WithWatch, obj crclient.Object, _ ...crclient.CreateOption) error {
			sar := obj.(*authorizationv1.SubjectAccessReview)
			mu.Lock()
			defer mu.Unlock()
			if sar.Spec.User != "alice" {
				t.Errorf("review for user %q, want alice", sar.Spec.User)
			}
			attrs := *sar.Spec.ResourceAttributes
			asked = append(asked, attrs)
			sar.Status.Allowed = true
			if attrs.Verb == "watch" {
				watches++
				sar.Status.Allowed = watches > deniedWatches
			}
			return nil
		},
	}).Build()

	kctesting.WaitForAccess(t, c, "alice", "team-a", "apps", "deployments", "list", "watch")

	mu.Lock()
	defer mu.Unlock()
	if watches != deniedWatches+1 {
		t.Fatalf("watch asked %d times, want %d", watches, deniedWatches+1)
	}
	want := authorizationv1.ResourceAttributes{Namespace: "team-a", Group: "apps", Resource: "deployments"}
	var verbs []string
	for _, a := range asked {
		verbs = append(verbs, a.Verb)
		a.Verb = ""
		if a != want {
			t.Fatalf("asked %+v, want %+v", a, want)
		}
	}
	if !slices.Contains(verbs, "list") {
		t.Fatalf("verbs asked = %v, want list among them", verbs)
	}
}
