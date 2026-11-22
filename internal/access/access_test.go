package access_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/stokaro/kc/internal/access"
)

var (
	podsGVR        = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	deploymentsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
)

// rbac is a tiny authorizer for the fake API server: a rule allows a verb on
// a resource/subresource in a namespace, optionally only for some names.
type rule struct {
	verb, resource, subresource, namespace string
	names                                  []string
}

func (r rule) matches(a *authorizationv1.ResourceAttributes) bool {
	if r.verb != a.Verb || r.resource != a.Resource || r.subresource != a.Subresource || r.namespace != a.Namespace {
		return false
	}
	if len(r.names) == 0 {
		return true
	}
	for _, n := range r.names {
		if n == a.Name && a.Name != "" {
			return true
		}
	}
	return false
}

type fakeServer struct {
	mu       sync.Mutex
	reviews  []authorizationv1.ResourceAttributes
	rules    []rule
	fail     error
	evalErr  string
	explicit bool // report Denied=true for denials
}

func (s *fakeServer) client() *fake.Clientset {
	cs := fake.NewClientset()
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		review := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		attrs := review.Spec.ResourceAttributes
		s.mu.Lock()
		defer s.mu.Unlock()
		s.reviews = append(s.reviews, *attrs)
		if s.fail != nil {
			return true, nil, s.fail
		}
		out := review.DeepCopy()
		out.Status.EvaluationError = s.evalErr
		for _, r := range s.rules {
			if r.matches(attrs) {
				out.Status.Allowed = true
				out.Status.Reason = "RBAC: allowed by RoleBinding \"test\""
				return true, out, nil
			}
		}
		out.Status.Denied = s.explicit
		return true, out, nil
	})
	return cs
}

func (s *fakeServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reviews)
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newClock() *clock { return &clock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)} }

func TestSSARReviewerDecisions(t *testing.T) {
	t.Parallel()
	check := access.Check{Resource: podsGVR, Verb: "delete", Namespace: "team-a", Name: "web-0"}
	tests := []struct {
		name       string
		server     *fakeServer
		want       access.Decision
		wantReason string
	}{
		{
			name:       "allowed",
			server:     &fakeServer{rules: []rule{{verb: "delete", resource: "pods", namespace: "team-a"}}},
			want:       access.Allowed,
			wantReason: `RBAC: allowed by RoleBinding "test"`,
		},
		{
			name:       "no opinion is denied",
			server:     &fakeServer{},
			want:       access.Denied,
			wantReason: "no RBAC rule or authorizer allows it",
		},
		{
			name:       "explicit denial",
			server:     &fakeServer{explicit: true},
			want:       access.Denied,
			wantReason: "denied by an authorizer",
		},
		{
			name:       "evaluation error is unknown",
			server:     &fakeServer{evalErr: "role.rbac.authorization.k8s.io \"gone\" not found"},
			want:       access.Unknown,
			wantReason: "evaluation error: role.rbac.authorization.k8s.io \"gone\" not found",
		},
		{
			name:       "request error is unknown",
			server:     &fakeServer{fail: errors.New("connection refused")},
			want:       access.Unknown,
			wantReason: "access review failed: connection refused",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := access.SSARReviewer{Client: tt.server.client().AuthorizationV1()}
			got := r.Review(t.Context(), check)
			if got.Decision != tt.want {
				t.Fatalf("decision = %v, want %v (reason %q)", got.Decision, tt.want, got.Reason)
			}
			if got.Reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", got.Reason, tt.wantReason)
			}
			if got.CheckedAt.IsZero() {
				t.Fatal("CheckedAt not set")
			}
			if tt.server.count() != 1 {
				t.Fatalf("reviews sent = %d, want 1", tt.server.count())
			}
		})
	}
}

func TestSSARReviewerSendsFullAttributes(t *testing.T) {
	t.Parallel()
	srv := &fakeServer{}
	r := access.SSARReviewer{Client: srv.client().AuthorizationV1()}
	r.Review(t.Context(), access.Check{Resource: deploymentsGVR, Subresource: "scale", Verb: "update", Namespace: "ns1", Name: "api"})
	want := authorizationv1.ResourceAttributes{Namespace: "ns1", Verb: "update", Group: "apps", Version: "v1", Resource: "deployments", Subresource: "scale", Name: "api"}
	if got := srv.reviews[0]; got != want {
		t.Fatalf("attributes = %+v, want %+v", got, want)
	}
}

func TestCacheCachesWithinTTLAndRefreshesAfter(t *testing.T) {
	t.Parallel()
	srv := &fakeServer{rules: []rule{{verb: "get", resource: "pods", namespace: "ns"}}}
	clk := newClock()
	cache := access.NewCache(30*time.Second, clk.Now)
	reviewer := access.SSARReviewer{Client: srv.client().AuthorizationV1(), Now: clk.Now}
	id := access.Identity{Context: "dev"}
	c := access.Check{Resource: podsGVR, Verb: "get", Namespace: "ns"}

	cache.Run(t.Context(), reviewer, id, cache.Claim(id, c))
	if got := cache.Claim(id, c); len(got) != 0 {
		t.Fatalf("fresh result claimed again: %v", got)
	}
	if srv.count() != 1 {
		t.Fatalf("reviews = %d, want 1", srv.count())
	}

	clk.Add(31 * time.Second)
	res, ok := cache.Lookup(id, c)
	if !ok || !res.Stale || res.Decision != access.Allowed {
		t.Fatalf("expired lookup = %+v ok=%v, want stale allowed", res, ok)
	}
	// A stale answer still gates actions until the refresh lands.
	if d := cache.Decide(id, c); d != access.Allowed {
		t.Fatalf("stale decide = %v, want allowed", d)
	}
	again := cache.Claim(id, c)
	if len(again) != 1 {
		t.Fatalf("stale result not claimed for refresh: %v", again)
	}
	srv.mu.Lock()
	srv.rules = nil
	srv.mu.Unlock()
	cache.Run(t.Context(), reviewer, id, again)
	if d := cache.Decide(id, c); d != access.Denied {
		t.Fatalf("refreshed decide = %v, want denied", d)
	}
	if srv.count() != 2 {
		t.Fatalf("reviews = %d, want 2", srv.count())
	}
}

func TestCacheClaimSkipsInFlight(t *testing.T) {
	t.Parallel()
	cache := access.NewCache(0, nil)
	id := access.Identity{Context: "dev"}
	c := access.Check{Resource: podsGVR, Verb: "list", Namespace: "ns"}
	if got := cache.Claim(id, c); len(got) != 1 {
		t.Fatalf("first claim = %v", got)
	}
	if got := cache.Claim(id, c); len(got) != 0 {
		t.Fatalf("in-flight check claimed twice: %v", got)
	}
	cache.Store(id, c, access.Result{Decision: access.Denied, CheckedAt: time.Now()})
	if got := cache.Claim(id, c); len(got) != 0 {
		t.Fatalf("stored check claimed again: %v", got)
	}
}

func TestCacheErrorsAreUnknownAndCached(t *testing.T) {
	t.Parallel()
	srv := &fakeServer{fail: errors.New("boom")}
	cache := access.NewCache(0, nil)
	id := access.Identity{Context: "dev"}
	c := access.Check{Resource: podsGVR, Verb: "delete", Namespace: "ns", Name: "p"}
	cache.Run(t.Context(), access.SSARReviewer{Client: srv.client().AuthorizationV1()}, id, cache.Claim(id, c))
	if d := cache.Decide(id, c); d != access.Unknown {
		t.Fatalf("decide = %v, want unknown", d)
	}
	if got := cache.Claim(id, c); len(got) != 0 {
		t.Fatalf("failed review retried within TTL: %v", got)
	}
}

func TestCacheRunCanceledContextStoresUnknown(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	reviewer := access.ReviewerFunc(func(context.Context, access.Check) access.Result {
		calls.Add(1)
		return access.Result{Decision: access.Allowed}
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cache := access.NewCache(0, nil)
	id := access.Identity{}
	c := access.Check{Resource: podsGVR, Verb: "get"}
	cache.Run(ctx, reviewer, id, cache.Claim(id, c))
	if calls.Load() != 0 {
		t.Fatalf("reviewer called %d times with a canceled context", calls.Load())
	}
	res, ok := cache.Lookup(id, c)
	if !ok || res.Decision != access.Unknown {
		t.Fatalf("lookup = %+v ok=%v, want stored unknown", res, ok)
	}
}

func TestCacheSeparatesIdentitiesAndNamespaces(t *testing.T) {
	t.Parallel()
	cache := access.NewCache(0, nil)
	admin := access.Identity{Kubeconfig: "/k", Context: "dev"}
	viewer := access.Identity{Kubeconfig: "/k", Context: "dev", User: "jane", Groups: []string{"viewers"}}
	other := access.Identity{Kubeconfig: "/k", Context: "prod"}
	inA := access.Check{Resource: podsGVR, Verb: "delete", Namespace: "a"}
	inB := access.Check{Resource: podsGVR, Verb: "delete", Namespace: "b"}

	cache.Store(admin, inA, access.Result{Decision: access.Allowed, CheckedAt: time.Now()})
	cache.Store(viewer, inA, access.Result{Decision: access.Denied, CheckedAt: time.Now()})

	if d := cache.Decide(admin, inA); d != access.Allowed {
		t.Fatalf("admin in a = %v", d)
	}
	if d := cache.Decide(viewer, inA); d != access.Denied {
		t.Fatalf("impersonated viewer in a = %v, want denied", d)
	}
	if d := cache.Decide(other, inA); d != access.Unknown {
		t.Fatalf("other context reused an answer: %v", d)
	}
	if d := cache.Decide(admin, inB); d != access.Unknown {
		t.Fatalf("namespace b reused namespace a's answer: %v", d)
	}
	// Group order does not change who asks.
	reordered := access.Identity{Kubeconfig: "/k", Context: "dev", User: "jane", Groups: []string{"viewers"}}
	if d := cache.Decide(reordered, inA); d != access.Denied {
		t.Fatalf("same identity = %v", d)
	}
}

func TestGateChecksResourceNames(t *testing.T) {
	t.Parallel()
	// The identity may delete only pod "web-0" in ns: a resourceNames rule.
	srv := &fakeServer{rules: []rule{{verb: "delete", resource: "pods", namespace: "ns", names: []string{"web-0"}}}}
	cache := access.NewCache(0, nil)
	reviewer := access.SSARReviewer{Client: srv.client().AuthorizationV1()}
	id := access.Identity{Context: "dev"}

	gate := func(name string) access.Decision {
		c := access.Check{Resource: podsGVR, Verb: "delete", Namespace: "ns", Name: name}
		for range 3 {
			need := cache.GateChecks(id, c)
			if len(need) == 0 {
				break
			}
			cache.Run(t.Context(), reviewer, id, cache.Claim(id, need...))
		}
		return cache.Decide(id, c)
	}
	if d := gate("web-0"); d != access.Allowed {
		t.Fatalf("web-0 = %v, want allowed by resourceNames", d)
	}
	if d := gate("web-1"); d != access.Denied {
		t.Fatalf("web-1 = %v, want denied", d)
	}
	// One type-level review plus one named review per pod.
	if srv.count() != 3 {
		t.Fatalf("reviews = %d, want 3", srv.count())
	}
}

func TestGateChecksTypeLevelAllowSettlesAllNames(t *testing.T) {
	t.Parallel()
	srv := &fakeServer{rules: []rule{{verb: "delete", resource: "pods", namespace: "ns"}}}
	cache := access.NewCache(0, nil)
	reviewer := access.SSARReviewer{Client: srv.client().AuthorizationV1()}
	id := access.Identity{Context: "dev"}
	for _, name := range []string{"a", "b", "c"} {
		c := access.Check{Resource: podsGVR, Verb: "delete", Namespace: "ns", Name: name}
		if need := cache.GateChecks(id, c); len(need) > 0 {
			cache.Run(t.Context(), reviewer, id, cache.Claim(id, need...))
		}
		if d := cache.Decide(id, c); d != access.Allowed {
			t.Fatalf("%s = %v", name, d)
		}
	}
	if srv.count() != 1 {
		t.Fatalf("reviews = %d, want 1 type-level review", srv.count())
	}
}

func TestPolicyPermits(t *testing.T) {
	t.Parallel()
	del := access.Check{Resource: podsGVR, Verb: "delete", Namespace: "ns", Name: "p"}
	get := access.Check{Resource: podsGVR, Verb: "get", Namespace: "ns", Name: "p"}
	exec := access.ExecChecks("ns", "p")[0]
	decide := func(d access.Decision) func(access.Check) access.Decision {
		return func(access.Check) access.Decision { return d }
	}
	tests := []struct {
		name   string
		policy access.Policy
		decide func(access.Check) access.Decision
		checks []access.Check
		want   bool
	}{
		{"unknown stays enabled", access.Policy{}, decide(access.Unknown), []access.Check{del}, true},
		{"allowed", access.Policy{}, decide(access.Allowed), []access.Check{del}, true},
		{"denied", access.Policy{}, decide(access.Denied), []access.Check{del}, false},
		{"one denied of two", access.Policy{}, func(c access.Check) access.Decision {
			if c.Verb == "patch" {
				return access.Denied
			}
			return access.Allowed
		}, access.EditChecks(access.Target{Resource: podsGVR, Namespace: "ns", Name: "p"}), false},
		{"read-only refuses writes", access.Policy{ReadOnly: true}, decide(access.Allowed), []access.Check{del}, false},
		{"read-only refuses exec", access.Policy{ReadOnly: true}, decide(access.Allowed), []access.Check{exec}, false},
		{"read-only keeps reads", access.Policy{ReadOnly: true}, decide(access.Allowed), []access.Check{get}, true},
		{"nil decide", access.Policy{}, nil, []access.Check{del}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.policy.Permits(tt.decide, tt.checks...); got != tt.want {
				t.Fatalf("Permits = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCanICommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		id   access.Identity
		c    access.Check
		want string
	}{
		{
			name: "core named in namespace",
			id:   access.Identity{Context: "dev", KubectlCtx: "dev"},
			c:    access.Check{Resource: podsGVR, Verb: "delete", Namespace: "ns", Name: "web-0"},
			want: "kubectl auth can-i delete pods/web-0 -n ns",
		},
		{
			name: "subresource with group and other context",
			id:   access.Identity{Context: "prod", KubectlCtx: "dev"},
			c:    access.Check{Resource: deploymentsGVR, Subresource: "scale", Verb: "update", Namespace: "ns", Name: "api"},
			want: "kubectl --context prod auth can-i update deployments.apps/api --subresource scale -n ns",
		},
		{
			name: "impersonation",
			id:   access.Identity{Context: "dev", KubectlCtx: "dev", User: "jane", UID: "42", Groups: []string{"team a", "viewers"}},
			c:    access.Check{Resource: podsGVR, Subresource: "exec", Verb: "create", Namespace: "ns", Name: "p"},
			want: "kubectl auth can-i create pods/p --subresource exec -n ns --as jane --as-uid 42 --as-group 'team a' --as-group viewers",
		},
		{
			name: "cluster-wide type",
			id:   access.Identity{},
			c:    access.Check{Resource: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, Verb: "list"},
			want: "kubectl auth can-i list nodes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := access.CanICommand(tt.id, tt.c); got != tt.want {
				t.Fatalf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestViewChecksSubresources(t *testing.T) {
	t.Parallel()
	has := func(checks []access.Check, sub, verb string) bool {
		for _, c := range checks {
			if c.Subresource == sub && c.Verb == verb {
				return true
			}
		}
		return false
	}
	pod := access.ViewChecks(access.Target{Resource: podsGVR, Namespace: "ns", Name: "p"})
	for _, want := range [][2]string{{"log", "get"}, {"exec", "create"}, {"portforward", "create"}} {
		if !has(pod, want[0], want[1]) {
			t.Fatalf("pod view lacks %s %s: %v", want[1], want[0], pod)
		}
	}
	for _, c := range pod {
		if (c.Verb == "create" || c.Verb == "list") && c.Subresource == "" && c.Name != "" {
			t.Fatalf("collection verb checked with a name: %v", c)
		}
	}
	deploy := access.ViewChecks(access.Target{Resource: deploymentsGVR, Namespace: "ns"})
	if !has(deploy, "scale", "update") || has(deploy, "exec", "create") {
		t.Fatalf("deployment view subresources wrong: %v", deploy)
	}
	cm := access.ViewChecks(access.Target{Resource: schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}})
	if len(cm) != len(access.StandardVerbs) {
		t.Fatalf("configmap view = %d checks, want only the %d standard verbs", len(cm), len(access.StandardVerbs))
	}
}
