package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	metamapper "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"

	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
)

var gadgetsGVR = schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "gadgets"}

// legacyDiscoveryServer serves legacy (not aggregated) discovery: the core
// group with pods and namespaces, and example.com/v1 with gadgets. The
// gadgets document fails with 503 Service Unavailable while fail says so,
// the way an aggregated API that is down does.
type legacyDiscoveryServer struct {
	srv         *httptest.Server
	gadgetReads atomic.Int64
	// fail decides, from the number of the gadgets request (1 for the
	// first), whether it fails.
	fail func(n int64) bool
}

func newLegacyDiscoveryServer(t *testing.T, fail func(n int64) bool) *legacyDiscoveryServer {
	t.Helper()
	s := &legacyDiscoveryServer{fail: fail}
	docs := map[string]any{
		"/api": metav1.APIVersions{
			TypeMeta: metav1.TypeMeta{Kind: "APIVersions"},
			Versions: []string{"v1"},
		},
		"/api/v1": metav1.APIResourceList{
			TypeMeta:     metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"},
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{Name: "pods", Namespaced: true, Kind: "Pod", Verbs: metav1.Verbs{"get", "list"}},
				{Name: "namespaces", Kind: "Namespace", Verbs: metav1.Verbs{"get", "list"}},
			},
		},
		"/apis": metav1.APIGroupList{
			TypeMeta: metav1.TypeMeta{Kind: "APIGroupList", APIVersion: "v1"},
			Groups: []metav1.APIGroup{{
				Name:             "example.com",
				Versions:         []metav1.GroupVersionForDiscovery{{GroupVersion: "example.com/v1", Version: "v1"}},
				PreferredVersion: metav1.GroupVersionForDiscovery{GroupVersion: "example.com/v1", Version: "v1"},
			}},
		},
		"/apis/example.com/v1": metav1.APIResourceList{
			TypeMeta:     metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"},
			GroupVersion: "example.com/v1",
			APIResources: []metav1.APIResource{
				{Name: "gadgets", Namespaced: true, Kind: "Gadget", Verbs: metav1.Verbs{"get", "list"}},
			},
		},
	}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/apis/example.com/v1" && s.fail(s.gadgetReads.Add(1)) {
			http.Error(w, "the aggregated API is down", http.StatusServiceUnavailable)
			return
		}
		doc, ok := docs[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(func() {
		s.srv.CloseClientConnections()
		s.srv.Close()
	})
	return s
}

func (s *legacyDiscoveryServer) cluster(t *testing.T) *Cluster {
	t.Helper()
	cl, err := New(&rest.Config{Host: s.srv.URL, Insecure: true})
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	t.Cleanup(cl.Stop)
	return cl
}

func hasResource(infos []ResourceInfo, gvr schema.GroupVersionResource) bool {
	return slices.ContainsFunc(infos, func(i ResourceInfo) bool {
		return i.GVK.Group == gvr.Group && i.GVK.Version == gvr.Version && i.Resource == gvr.Resource
	})
}

// The resource list and the RESTMapper of a read list the same types. With
// legacy discovery client-go's memory cache asks again for a group version
// that failed with a transient error. The read used to build the mapper
// from one call and the list from a second one, which asked again: the
// list could name a type the mapper did not know, and the read counted as
// complete.
//
// client-go asks for a failed group version twice in the first call (once
// filling the cache, once in ServerGroupsAndResources), so failing the
// first two gadgets requests makes the first call miss example.com and a
// second call find it.
func TestDiscoveryReadListsOnlyTypesTheMapperKnows(t *testing.T) {
	t.Parallel()
	api := newLegacyDiscoveryServer(t, func(n int64) bool { return n <= 2 })
	cl := api.cluster(t)

	infos, failed, err := cl.DiscoveredResources(t.Context())
	if err != nil {
		t.Fatalf("first discovery read: %v", err)
	}
	t.Logf("first read: %d types, failed groups %v, %d gadgets requests", len(infos), failed, api.gadgetReads.Load())
	if !hasResource(infos, schema.GroupVersionResource{Version: "v1", Resource: "pods"}) {
		t.Fatalf("the read lists no pods: %v", infos)
	}
	mapper := cl.RESTMapper()
	for _, info := range infos {
		gvr := schema.GroupVersionResource{Group: info.GVK.Group, Version: info.GVK.Version, Resource: info.Resource}
		if _, err := mapper.KindFor(gvr); err != nil {
			t.Errorf("the read lists %v, which its RESTMapper does not know: %v", gvr, err)
		}
	}
	// A retry may have filled the gap already; until then the read names
	// the group it missed.
	if !hasResource(infos, gadgetsGVR) && !slices.Contains(failed, "example.com") {
		t.Errorf("the read misses gadgets and names failed groups %v, want example.com among them", failed)
	}
}

// A read that missed some group versions counts, but the cluster reads
// again sooner, with the backoff of a failed read, instead of after the
// refresh interval.
func TestPartialDiscoveryReadRetriesSoon(t *testing.T) {
	t.Parallel()
	var down atomic.Bool
	down.Store(true)
	api := newLegacyDiscoveryServer(t, func(int64) bool { return down.Load() })
	cl := api.cluster(t) // the refresh interval is 30s
	notified := make(chan struct{}, 8)
	cl.AddDiscoveryListener(func() {
		select {
		case notified <- struct{}{}:
		default:
		}
	})

	infos, failed, err := cl.DiscoveredResources(t.Context())
	if err != nil {
		t.Fatalf("first discovery read: %v", err)
	}
	if hasResource(infos, gadgetsGVR) || !slices.Equal(failed, []string{"example.com"}) {
		t.Fatalf("first read lists gadgets %v and failed groups %v, want no gadgets and [example.com]", hasResource(infos, gadgetsGVR), failed)
	}

	down.Store(false)
	start := time.Now()
	kctesting.Eventually(t, 10*time.Second, 50*time.Millisecond, func() bool {
		infos, failed, err := cl.DiscoveredResources(t.Context())
		return err == nil && hasResource(infos, gadgetsGVR) && len(failed) == 0
	}, "no discovery read within 10s found the group the first read missed; want a retry before the 30s refresh interval")
	t.Logf("the missed group was read %v after it came back", time.Since(start).Round(100*time.Millisecond))
	if _, err := cl.RESTMapper().KindFor(gadgetsGVR); err != nil {
		t.Errorf("KindFor(gadgets) after the retry: %v", err)
	}
	select {
	case <-notified:
	default:
		t.Error("no discovery listener ran")
	}
}

// preferred is the group, version and resource names of preferred lists.
func preferred(lists []*metav1.APIResourceList) []string {
	var out []string
	for _, l := range lists {
		for _, r := range l.APIResources {
			out = append(out, l.GroupVersion+" "+r.Name)
		}
	}
	return out
}

// The resource list of a read picks the preferred version of each resource
// from that read, as client-go's ServerPreferredResources does from the
// server.
func TestReadResourcesPreferredResources(t *testing.T) {
	t.Parallel()
	group := &metav1.APIGroup{
		Name: "apps",
		Versions: []metav1.GroupVersionForDiscovery{
			{GroupVersion: "apps/v1", Version: "v1"},
			{GroupVersion: "apps/v2", Version: "v2"},
		},
		PreferredVersion: metav1.GroupVersionForDiscovery{GroupVersion: "apps/v2", Version: "v2"},
	}
	v1 := &metav1.APIResourceList{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{
		{Name: "deployments", Kind: "Deployment", Namespaced: true},
		{Name: "oldsets", Kind: "OldSet", Namespaced: true},
	}}
	v2 := &metav1.APIResourceList{GroupVersion: "apps/v2", APIResources: []metav1.APIResource{
		{Name: "deployments", Kind: "Deployment", Namespaced: true},
		{Name: "deployments/scale", Kind: "Scale", Namespaced: true},
		{Name: "clusterthings", Kind: "ClusterThing"},
	}}
	tests := []struct {
		name  string
		lists []*metav1.APIResourceList
		want  []string
	}{
		{
			name:  "preferred version wins",
			lists: []*metav1.APIResourceList{v2, v1},
			want:  []string{"apps/v1 oldsets", "apps/v2 deployments", "apps/v2 clusterthings"},
		},
		{
			name:  "preferred version missing from the read",
			lists: []*metav1.APIResourceList{v1},
			want:  []string{"apps/v1 deployments", "apps/v1 oldsets"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			read := readResources{groups: []*metav1.APIGroup{group}, lists: tt.lists}
			lists, err := read.ServerPreferredResources()
			if err != nil {
				t.Fatal(err)
			}
			if got := preferred(lists); !slices.Equal(got, tt.want) {
				t.Errorf("ServerPreferredResources = %q, want %q", got, tt.want)
			}
			namespaced, err := read.ServerPreferredNamespacedResources()
			if err != nil {
				t.Fatal(err)
			}
			if got := preferred(namespaced); slices.Contains(got, "apps/v2 clusterthings") {
				t.Errorf("ServerPreferredNamespacedResources = %q, lists a cluster-scoped type", got)
			}
		})
	}
}

// A discovery read answers from memory what it holds and refuses the rest
// with an error. It used to embed a nil discovery interface, so a call the
// RESTMapper does not make today, after a client-go update, would have
// panicked inside a mapper call on the UI goroutine.
func TestReadResourcesRefusesWhatItCannotAnswer(t *testing.T) {
	t.Parallel()
	read := readResources{
		groups: []*metav1.APIGroup{{Name: "", Versions: []metav1.GroupVersionForDiscovery{{GroupVersion: "v1", Version: "v1"}}}},
		lists:  []*metav1.APIResourceList{{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "pods", Kind: "Pod", Namespaced: true}}}},
	}
	refused := []struct {
		name string
		call func() error
	}{
		{"ServerVersion", func() error { _, err := read.ServerVersion(); return err }},
		{"OpenAPISchema", func() error { _, err := read.OpenAPISchema(); return err }},
		{"OpenAPIV3 paths", func() error { _, err := read.OpenAPIV3().Paths(); return err }},
		{"RESTClient request", func() error { return read.RESTClient().Get().AbsPath("/api").Do(t.Context()).Error() }},
		{"WithLegacy ServerVersion", func() error { _, err := read.WithLegacy().ServerVersion(); return err }},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			if err := c.call(); !errors.Is(err, errReadOnlyDiscovery) {
				t.Errorf("%s = %v, want errReadOnlyDiscovery", c.name, err)
			}
		})
	}

	if groups, err := read.ServerGroups(); err != nil || len(groups.Groups) != 1 {
		t.Errorf("ServerGroups = %v, %v; want the read's group", groups, err)
	}
	if list, err := read.ServerResourcesForGroupVersion("v1"); err != nil || len(list.APIResources) != 1 {
		t.Errorf("ServerResourcesForGroupVersion(v1) = %v, %v; want the read's list", list, err)
	}
	if _, err := read.ServerResourcesForGroupVersion("apps/v1"); err == nil {
		t.Error("ServerResourcesForGroupVersion of a group version the read lacks succeeded")
	}
}

// Before the cluster's first discovery read ended, a mapping made with a
// context waits for that read only until the context ends, so a background
// load that gives up does not wait for a server that holds the read. Kept
// RESTMapper calls without a context wait for the read, which
// discoveryReadTimeout bounds.
func TestMapperWithContextStopsWaitingForTheFirstRead(t *testing.T) {
	t.Parallel()
	proxy := kctesting.NewStallProxy(t, kcenvtest.Start(t))
	proxy.Stall(kctesting.DiscoveryRequest)
	cl, err := New(proxy.Config())
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	t.Cleanup(cl.Stop)
	kctesting.Eventually(t, 10*time.Second, 10*time.Millisecond, func() bool { return proxy.Held() > 0 },
		"the first discovery read never reached the proxy")

	calls := []struct {
		name string
		call func(context.Context) error
	}{
		{"KindForWithContext", func(ctx context.Context) error {
			_, err := metamapper.ToRESTMapperWithContext(cl.RESTMapper()).KindForWithContext(ctx, podsGVR)
			return err
		}},
		{"RESTMappingWithContext", func(ctx context.Context) error {
			_, err := metamapper.ToRESTMapperWithContext(cl.RESTMapper()).RESTMappingWithContext(ctx, schema.GroupKind{Kind: "Pod"})
			return err
		}},
		{"GetByGVR", func(ctx context.Context) error {
			_, err := cl.GetByGVR(ctx, podsGVR, "default", "web")
			return err
		}},
	}
	for _, c := range calls {
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		done := make(chan error, 1)
		go func() { done <- c.call(ctx) }()
		select {
		case err := <-done:
			if !errors.Is(err, ErrDiscoveryPending) {
				t.Errorf("%s while the first read is held = %v, want ErrDiscoveryPending", c.name, err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("%s with a 200ms context waited for the held first discovery read", c.name)
		}
		cancel()
	}
	if proxy.Held() == 0 {
		t.Fatal("the proxy no longer holds the first read; the calls above did not run during it")
	}
}
