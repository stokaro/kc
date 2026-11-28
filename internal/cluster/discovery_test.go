package cluster

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
)

var podsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

// A refresh reads discovery into a new state and swaps it in when the read
// is complete. Readers answer from the previous read meanwhile, so a server
// that stalls during a refresh stalls none of them. Before, a refresh
// emptied the cache readers used, and the next KindFor (from a render, in
// the UI's case) waited for the server.
func TestDiscoveryRefreshDoesNotBlockReaders(t *testing.T) {
	t.Parallel()
	proxy := kctesting.NewStallProxy(t, kcenvtest.Start(t))
	cl, err := New(proxy.Config())
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	t.Cleanup(cl.Stop)
	if _, _, err := cl.DiscoveredResources(t.Context()); err != nil {
		t.Fatalf("first discovery read: %v", err)
	}

	proxy.Stall(kctesting.DiscoveryRequest)
	refreshed := make(chan struct{})
	go func() {
		cl.RefreshDiscovery()
		close(refreshed)
	}()
	kctesting.Eventually(t, 10*time.Second, 10*time.Millisecond, func() bool {
		select {
		case <-refreshed:
			return true
		default:
			return proxy.Held() > 0
		}
	}, "the refresh neither sent a discovery request nor returned")

	readers := []struct {
		name string
		read func() error
	}{
		{"RESTMapper.KindFor", func() error { _, err := cl.RESTMapper().KindFor(podsGVR); return err }},
		{"RESTMapper.RESTMapping", func() error {
			_, err := cl.RESTMapper().RESTMapping(schema.GroupKind{Kind: "Pod"}, "v1")
			return err
		}},
		{"GetResourceInfos", func() error { _, err := cl.GetResourceInfos(); return err }},
		{"DiscoveredResources", func() error { _, _, err := cl.DiscoveredResources(t.Context()); return err }},
		{"DiscoveryClient", func() error { _, err := cl.DiscoveryClient().ServerResourcesForGroupVersion("v1"); return err }},
		{"Serves", func() error { _, err := cl.Serves(t.Context(), podsGVR); return err }},
	}
	for _, r := range readers {
		done := make(chan error, 1)
		go func() { done <- r.read() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("%s during a stalled refresh: %v", r.name, err)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("%s waited for the stalled discovery refresh", r.name)
		}
	}
	select {
	case <-refreshed:
		t.Fatal("the refresh returned while the proxy held its discovery request; the readers above did not run during it")
	default:
	}
	proxy.Resume()
	select {
	case <-refreshed:
	case <-time.After(30 * time.Second):
		t.Fatal("the refresh did not end after the proxy let its requests through")
	}
}

// While the first read is held, DiscoveredResources waits only as long as
// its context allows. A first read that fails is recorded: later calls
// return its error at once and send nothing, the cluster retries on its
// own, and listeners run when a read succeeds.
func TestDiscoveredResourcesAroundTheFirstRead(t *testing.T) {
	t.Parallel()
	proxy := kctesting.NewStallProxy(t, kcenvtest.Start(t))
	proxy.Stall(kctesting.DiscoveryRequest)
	cl, err := New(proxy.Config())
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	t.Cleanup(cl.Stop)
	notified := make(chan struct{}, 1)
	cl.AddDiscoveryListener(func() {
		select {
		case notified <- struct{}{}:
		default:
		}
	})

	short, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	start := time.Now()
	_, _, err = cl.DiscoveredResources(short)
	cancel()
	if !errors.Is(err, ErrDiscoveryPending) {
		t.Fatalf("DiscoveredResources while the first read is held = %v, want ErrDiscoveryPending", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("DiscoveredResources with a 100ms deadline took %v", took)
	}

	// Refuse discovery, then release the held read so that it fails.
	proxy.Refuse(kctesting.DiscoveryRequest)
	proxy.Stall(func(*http.Request) bool { return false })
	if _, _, err := cl.DiscoveredResources(t.Context()); err == nil || errors.Is(err, ErrDiscoveryPending) {
		t.Fatalf("DiscoveredResources after a failed first read = %v, want the read's error", err)
	}
	before := proxy.Requests(kctesting.DiscoveryURI)
	for range 50 {
		if _, _, err := cl.DiscoveredResources(t.Context()); err == nil {
			t.Fatal("DiscoveredResources succeeded while discovery is refused")
		}
	}
	// The cluster's own retry may fall into the loop; one read per call
	// may not.
	if sent := proxy.Requests(kctesting.DiscoveryURI) - before; sent > 10 {
		t.Fatalf("50 calls after a failed read sent %d discovery requests; want them to answer from the recorded failure", sent)
	}

	proxy.Resume()
	select {
	case <-notified:
	case <-time.After(30 * time.Second):
		t.Fatal("no discovery listener ran after discovery came back")
	}
	if _, _, err := cl.DiscoveredResources(t.Context()); err != nil {
		t.Fatalf("DiscoveredResources after discovery came back: %v", err)
	}
}

// The RESTMapper answers from the groups and resources of the last read,
// shortcuts included, and sends nothing: not for a type the read does not
// list, and not while the server refuses discovery.
func TestRESTMapperSendsNoRequests(t *testing.T) {
	t.Parallel()
	proxy := kctesting.NewStallProxy(t, kcenvtest.Start(t))
	cl, err := New(proxy.Config())
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	t.Cleanup(cl.Stop)
	mapper := cl.RESTMapper()
	if _, err := mapper.KindFor(podsGVR); err != nil {
		t.Fatalf("KindFor(pods) after the first read: %v", err)
	}
	proxy.Refuse(kctesting.DiscoveryRequest)
	before := proxy.Requests(kctesting.DiscoveryURI)

	if gvr, err := mapper.ResourceFor(schema.GroupVersionResource{Resource: "po"}); err != nil || gvr != podsGVR {
		t.Errorf("ResourceFor(po) = %v, %v; want %v", gvr, err, podsGVR)
	}
	if gvk, err := mapper.KindFor(schema.GroupVersionResource{Group: "apps", Resource: "deploy"}); err != nil || gvk.Kind != "Deployment" {
		t.Errorf("KindFor(deploy.apps) = %v, %v; want Deployment", gvk, err)
	}
	if _, err := mapper.KindFor(schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "gadgets"}); err == nil {
		t.Error("KindFor of a type discovery does not list succeeded")
	}
	if m, err := mapper.RESTMapping(schema.GroupKind{Kind: "Namespace"}); err != nil || m.Scope.Name() != "root" {
		t.Errorf("RESTMapping(Namespace) = %v, %v; want a cluster-scoped mapping", m, err)
	}
	if sent := proxy.Requests(kctesting.DiscoveryURI) - before; sent != 0 {
		t.Fatalf("RESTMapper calls sent %d discovery requests, want none", sent)
	}
}

func TestDiscoveryRetryDelay(t *testing.T) {
	t.Parallel()
	tests := []struct {
		failures int
		limit    time.Duration
		want     time.Duration
	}{
		{1, 30 * time.Second, time.Second},
		{2, 30 * time.Second, 2 * time.Second},
		{5, 30 * time.Second, 16 * time.Second},
		{6, 30 * time.Second, 30 * time.Second},
		{40, 30 * time.Second, 30 * time.Second},
		{3, 500 * time.Millisecond, time.Second},
	}
	for _, tt := range tests {
		if got := discoveryRetryDelay(tt.failures, tt.limit); got != tt.want {
			t.Errorf("discoveryRetryDelay(%d, %v) = %v, want %v", tt.failures, tt.limit, got, tt.want)
		}
	}
}
