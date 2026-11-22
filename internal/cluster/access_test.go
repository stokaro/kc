package cluster_test

import (
	"errors"
	"slices"
	"sync"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	kccluster "github.com/stokaro/kc/internal/cluster"
)

// reviewServer answers SelfSubjectAccessReviews from a set of allowed
// "verb group/resource" strings and records what it was asked.
type reviewServer struct {
	mu      sync.Mutex
	allowed map[string]bool
	fail    error
	asked   []string
}

func (s *reviewServer) client() *fake.Clientset {
	c := fake.NewClientset()
	c.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		review := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		a := review.Spec.ResourceAttributes
		key := a.Verb + " " + a.Group + "/" + a.Resource
		s.mu.Lock()
		defer s.mu.Unlock()
		s.asked = append(s.asked, key+" ns="+a.Namespace)
		if s.fail != nil {
			return true, nil, s.fail
		}
		review.Status.Allowed = s.allowed[key]
		return true, review, nil
	})
	return c
}

func (s *reviewServer) questions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.asked...)
}

var podsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

func TestListWatchAccessAllNamespaces(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		allowed []string
		want    bool
		asked   []string
	}{
		{
			name:    "admin is answered by the wildcard review",
			allowed: []string{"list */*", "watch */*"},
			want:    true,
			asked:   []string{"list */* ns=", "watch */* ns="},
		},
		{
			name:    "per type grant",
			allowed: []string{"list /pods", "watch /pods"},
			want:    true,
			asked:   []string{"list */* ns=", "list /pods ns=", "watch /pods ns="},
		},
		{
			name:    "list denied skips the watch review",
			allowed: []string{"watch /pods"},
			want:    false,
			asked:   []string{"list */* ns=", "list /pods ns="},
		},
		{
			name:    "list without watch is not enough for an informer",
			allowed: []string{"list /pods"},
			want:    false,
			asked:   []string{"list */* ns=", "list /pods ns=", "watch /pods ns="},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := &reviewServer{allowed: make(map[string]bool)}
			for _, a := range tc.allowed {
				srv.allowed[a] = true
			}
			access := kccluster.NewListWatchAccess(srv.client().AuthorizationV1())
			got, err := access.AllNamespaces(t.Context(), podsGVR)
			if err != nil {
				t.Fatalf("AllNamespaces: %v", err)
			}
			if got != tc.want {
				t.Fatalf("AllNamespaces = %v, want %v", got, tc.want)
			}
			if q := srv.questions(); !slices.Equal(q, tc.asked) {
				t.Fatalf("reviews = %q, want %q", q, tc.asked)
			}
			// The answer is kept: a second call and another version of the
			// same group and resource send no review.
			if again, err := access.AllNamespaces(t.Context(), schema.GroupVersionResource{Version: "v2", Resource: "pods"}); err != nil || again != tc.want {
				t.Fatalf("second call = %v, %v", again, err)
			}
			if q := srv.questions(); len(q) != len(tc.asked) {
				t.Fatalf("second call sent reviews: %q", q[len(tc.asked):])
			}
		})
	}
}

func TestListWatchAccessInNamespace(t *testing.T) {
	t.Parallel()
	srv := &reviewServer{allowed: map[string]bool{"list /pods": true, "watch /pods": true}}
	// The fake answers by verb and resource only, so the namespace in the
	// request is what tells a namespaced review from a cluster-wide one.
	access := kccluster.NewListWatchAccess(srv.client().AuthorizationV1())
	ok, err := access.InNamespace(t.Context(), podsGVR, "team-a")
	if err != nil || !ok {
		t.Fatalf("InNamespace = %v, %v", ok, err)
	}
	want := []string{"list */* ns=", "list /pods ns=team-a", "watch /pods ns=team-a"}
	if q := srv.questions(); !slices.Equal(q, want) {
		t.Fatalf("reviews = %q, want %q", q, want)
	}
	// Another namespace is another question.
	if _, err := access.InNamespace(t.Context(), podsGVR, "team-b"); err != nil {
		t.Fatal(err)
	}
	if q := srv.questions(); len(q) != len(want)+2 || q[len(want)] != "list /pods ns=team-b" {
		t.Fatalf("reviews = %q", q)
	}
}

func TestListWatchAccessErrorsAreNotKept(t *testing.T) {
	t.Parallel()
	srv := &reviewServer{allowed: make(map[string]bool), fail: errors.New("connection refused")}
	access := kccluster.NewListWatchAccess(srv.client().AuthorizationV1())
	if _, err := access.AllNamespaces(t.Context(), podsGVR); err == nil {
		t.Fatal("expected the review error")
	}
	srv.mu.Lock()
	srv.fail = nil
	srv.allowed["list /pods"], srv.allowed["watch /pods"] = true, true
	srv.mu.Unlock()
	got, err := access.AllNamespaces(t.Context(), podsGVR)
	if err != nil || !got {
		t.Fatalf("after the error cleared: %v, %v; want true", got, err)
	}
}

func TestListWatchAccessEvaluationErrorIsNoDenial(t *testing.T) {
	t.Parallel()
	c := fake.NewClientset()
	c.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		review := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		review.Status.EvaluationError = "webhook unavailable"
		return true, review, nil
	})
	access := kccluster.NewListWatchAccess(c.AuthorizationV1())
	if _, err := access.AllNamespaces(t.Context(), podsGVR); err == nil {
		t.Fatal("an evaluation error must surface as an error, not as a denial")
	}
}
