package models

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	kccluster "github.com/stokaro/kc/internal/cluster"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/pkg/appconfig"
)

// apiServer is an API server stand-in: answer serves every request and
// discovery counts the discovery requests.
type apiServer struct {
	srv       *httptest.Server
	discovery atomic.Int64
}

func newAPIServer(t *testing.T, answer func(w http.ResponseWriter, r *http.Request)) *apiServer {
	t.Helper()
	s := &apiServer{}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if kctesting.IsDiscovery(r.URL.Path) {
			s.discovery.Add(1)
		}
		answer(w, r)
	}))
	t.Cleanup(func() {
		s.srv.CloseClientConnections()
		s.srv.Close()
	})
	return s
}

func (s *apiServer) cluster(t *testing.T) *kccluster.Cluster {
	t.Helper()
	cfg := &rest.Config{Host: s.srv.URL, Insecure: true}
	cl, err := kccluster.New(cfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	t.Cleanup(cl.Stop)
	return cl
}

func stallDeps(t *testing.T, cl *kccluster.Cluster) Deps {
	return Deps{
		Cl:         cl,
		Ctx:        t.Context(),
		CtxName:    "stalled",
		KubeConfig: clientcmdapi.Config{CurrentContext: "stalled", Contexts: map[string]*clientcmdapi.Context{"stalled": {}}},
		AppConfig:  appconfig.Default(),
	}
}

// A root whose populate fails, because discovery fails, is read on every
// repaint. Each read used to run discovery again, on the UI goroutine;
// now the failure is recorded and retried later, and the cluster's own
// retry is the only one that asks the server.
func TestFailingRootPopulateDoesNotReadDiscoveryOnEveryLen(t *testing.T) {
	t.Parallel()
	api := newAPIServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})
	root := NewRootFolder(stallDeps(t, api.cluster(t)), "default")

	const reads = 50
	start := api.discovery.Load()
	began := time.Now()
	for range reads {
		ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
		root.Len(ctx)
		cancel()
	}
	took := time.Since(began)
	sent := api.discovery.Load() - start
	t.Logf("%d reads of the root took %v and sent %d discovery requests", reads, took, sent)
	if sent >= reads {
		t.Fatalf("%d reads of a root whose populate fails sent %d discovery requests; want the failure recorded instead of discovery on every read", reads, sent)
	}
	if took > 5*time.Second {
		t.Fatalf("%d reads of the root took %v; want them answered from the recorded failure", reads, took)
	}
	if err := root.rowSource().(*sliceRowSource).LoadError(); err == nil {
		t.Fatal("the root recorded no load error while discovery fails")
	}
}

// The command line is drawn on the UI goroutine. It reads the scope the
// folder was built with and never the RESTMapper: before, a list's command
// asked the mapper, which waited for a server that does not answer.
func TestObjectsFolderCommandReadsStoredScope(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	api := newAPIServer(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	deps := stallDeps(t, api.cluster(t))

	tests := []struct {
		name   string
		folder func() *ObjectsFolder
		want   string
		absent string
	}{
		{
			name: "cluster-scoped type",
			folder: func() *ObjectsFolder {
				return NewClusterObjectsFolder(deps, nodesGVR, []string{"nodes"}, nil).ObjectsFolder
			},
			want:   "get nodes",
			absent: "-A",
		},
		{
			name: "namespaced type in all namespaces",
			folder: func() *ObjectsFolder {
				return NewNamespacedObjectsFolder(deps, podGVR, AllNamespaces, []string{"pods"}, nil).ObjectsFolder
			},
			want: "get pods -A",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			folder := tt.folder()
			t.Cleanup(folder.Close)
			done := make(chan string, 1)
			go func() { done <- folder.KubectlCommand() }()
			select {
			case got := <-done:
				if !strings.Contains(got, tt.want) || (tt.absent != "" && strings.Contains(got, tt.absent)) {
					t.Fatalf("KubectlCommand() = %q, want %q without %q", got, tt.want, tt.absent)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("KubectlCommand waited for the API server")
			}
		})
	}
}
