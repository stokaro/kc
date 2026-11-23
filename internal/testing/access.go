package kctesting

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// WaitForAccess waits until the API server's authorizer allows user every
// verb on group/resource in namespace (empty namespace: cluster-wide).
//
// The RBAC authorizer reads Roles and bindings from its own informers, so a
// grant is not in force the moment its create returns. kc caches access
// answers per cluster, so a denial read in that gap sticks for the whole
// test. Call this after creating a grant and before kc reads access. It asks
// with SubjectAccessReviews through c, an admin client, so the user's own
// cached answers stay untouched.
func WaitForAccess(t testing.TB, c crclient.Client, user, namespace, group, resource string, verbs ...string) {
	t.Helper()
	if len(verbs) == 0 {
		t.Fatalf("WaitForAccess: no verbs for %s", resource)
	}
	where := "cluster-wide"
	if namespace != "" {
		where = "in namespace " + namespace
	}
	what := fmt.Sprintf("%s %s.%s %s", strings.Join(verbs, ","), resource, group, where)
	var last string
	done := false
	defer func() {
		// Eventually fails with t.Fatalf, which runs deferred calls.
		if !done {
			t.Logf("last answer for %s: %s", user, last)
		}
	}()
	Eventually(t, 30*time.Second, 50*time.Millisecond, func() bool {
		for _, verb := range verbs {
			ok, answer := allowed(t, c, user, namespace, group, resource, verb)
			if !ok {
				last = answer
				return false
			}
		}
		return true
	}, fmt.Sprintf("user %s was never allowed %s", user, what))
	done = true
}

func allowed(t testing.TB, c crclient.Client, user, namespace, group, resource, verb string) (bool, string) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	sar := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
		User: user,
		ResourceAttributes: &authorizationv1.ResourceAttributes{
			Namespace: namespace, Verb: verb, Group: group, Resource: resource,
		},
	}}
	if err := c.Create(ctx, sar); err != nil {
		return false, fmt.Sprintf("%s: create SubjectAccessReview: %v", verb, err)
	}
	if !sar.Status.Allowed {
		return false, fmt.Sprintf("%s: denied (reason %q, evaluation error %q)", verb, sar.Status.Reason, sar.Status.EvaluationError)
	}
	return true, ""
}
