package kctesting

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"

	"k8s.io/client-go/rest"
)

// StallProxy forwards requests to an API server and can hold or refuse
// them, the way an API server that accepts connections but stops answering
// does (kill -STOP, a hung load balancer), or one that fails. Tests put it
// between kc and an envtest control plane: kc connects to Config, and the
// proxy authenticates to the control plane with the upstream credentials.
type StallProxy struct {
	srv *httptest.Server

	mu       sync.Mutex
	hold     func(*http.Request) bool
	refuse   func(*http.Request) bool
	release  chan struct{}
	held     int
	requests []string // "METHOD path?query" of every request, in order
}

// NewStallProxy starts a proxy to upstream. Cleanup releases held
// requests, drops open connections (watches) and stops the proxy.
func NewStallProxy(t testing.TB, upstream *rest.Config) *StallProxy {
	t.Helper()
	transport, err := rest.TransportFor(upstream)
	if err != nil {
		t.Fatalf("stall proxy: upstream transport: %v", err)
	}
	target, err := url.Parse(upstream.Host)
	if err != nil {
		t.Fatalf("stall proxy: upstream host %q: %v", upstream.Host, err)
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
		},
		Transport: transport,
		// Watches stream; pass each event on at once.
		FlushInterval: -1,
	}
	p := &StallProxy{}
	p.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refuse, hold := p.admit(r)
		if refuse {
			http.Error(w, "refused by the test proxy", http.StatusServiceUnavailable)
			return
		}
		if hold != nil {
			select {
			case <-hold:
			case <-r.Context().Done():
			}
			p.unhold()
			if r.Context().Err() != nil {
				return
			}
			// A request released after Refuse fails like a new one.
			if p.refuses(r) {
				http.Error(w, "refused by the test proxy", http.StatusServiceUnavailable)
				return
			}
		}
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		p.Resume()
		p.srv.CloseClientConnections()
		p.srv.Close()
	})
	return p
}

// Config is a client configuration for the proxy. It trusts the proxy's
// certificate and carries no credentials of its own.
func (p *StallProxy) Config() *rest.Config {
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.srv.Certificate().Raw})
	return &rest.Config{Host: p.srv.URL, CAData: ca}
}

// Stall holds every later request that match accepts until Resume. A
// held request also ends when its client gives up.
func (p *StallProxy) Stall(match func(*http.Request) bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.release != nil {
		close(p.release)
	}
	p.hold = match
	p.release = make(chan struct{})
}

// Refuse answers every later request that match accepts with 503 Service
// Unavailable until Resume.
func (p *StallProxy) Refuse(match func(*http.Request) bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refuse = match
}

// Resume lets held requests through and stops holding or refusing new
// ones.
func (p *StallProxy) Resume() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.release != nil {
		close(p.release)
	}
	p.hold, p.refuse, p.release = nil, nil, nil
}

// Held is the number of requests the proxy holds now.
func (p *StallProxy) Held() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.held
}

// Requests is the number of requests so far whose method and request URI
// (path and query) match accepts.
func (p *StallProxy) Requests(match func(method, uri string) bool) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, r := range p.requests {
		method, uri, _ := strings.Cut(r, " ")
		if match(method, uri) {
			n++
		}
	}
	return n
}

func (p *StallProxy) admit(r *http.Request) (refuse bool, hold <-chan struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, r.Method+" "+r.URL.RequestURI())
	if p.refuse != nil && p.refuse(r) {
		return true, nil
	}
	if p.hold == nil || !p.hold(r) {
		return false, nil
	}
	p.held++
	return false, p.release
}

func (p *StallProxy) refuses(r *http.Request) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.refuse != nil && p.refuse(r)
}

func (p *StallProxy) unhold() {
	p.mu.Lock()
	p.held--
	p.mu.Unlock()
}

// IsDiscovery reports whether uri, a path with or without its query, is
// an API discovery document: /api, /api/v1, /apis, /apis/<group> or
// /apis/<group>/<version>.
func IsDiscovery(uri string) bool {
	path, _, _ := strings.Cut(uri, "?")
	segs := strings.Split(strings.Trim(path, "/"), "/")
	switch segs[0] {
	case "api":
		return len(segs) <= 2
	case "apis":
		return len(segs) <= 3
	}
	return false
}

// DiscoveryRequest is IsDiscovery for a request, for Stall and Refuse.
func DiscoveryRequest(r *http.Request) bool { return IsDiscovery(r.URL.Path) }

// DiscoveryURI is IsDiscovery for any method, for Requests.
func DiscoveryURI(_, uri string) bool { return IsDiscovery(uri) }
