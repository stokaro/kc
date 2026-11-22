package explain_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sttts/kc/internal/explain"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/openapi"
)

// fakeServer serves /openapi/v3 from fixture files and counts requests.
type fakeServer struct {
	mu         sync.Mutex
	docs       map[string][]byte // index key -> document
	hashes     map[string]string
	indexErr   error
	schemaErr  error
	indexCalls int
	fetches    map[string]int
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	read := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	return &fakeServer{
		docs: map[string][]byte{
			"apis/apps/v1":           read("apis_apps_v1.json"),
			"apis/fixture.kc.dev/v1": read("apis_fixture.kc.dev_v1.json"),
		},
		hashes:  map[string]string{"apis/apps/v1": "a1", "apis/fixture.kc.dev/v1": "f1"},
		fetches: map[string]int{},
	}
}

func (s *fakeServer) PathsWithContext(context.Context) (map[string]openapi.GroupVersionWithContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.indexCalls++
	if s.indexErr != nil {
		return nil, s.indexErr
	}
	out := map[string]openapi.GroupVersionWithContext{}
	for key := range s.docs {
		out[key] = fakeGV{server: s, key: key, hash: s.hashes[key]}
	}
	return out, nil
}

func (s *fakeServer) setHash(key, hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hashes[key] = hash
}

func (s *fakeServer) fetchCount(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fetches[key]
}

type fakeGV struct {
	server *fakeServer
	key    string
	hash   string
}

func (g fakeGV) SchemaWithContext(_ context.Context, contentType string) ([]byte, error) {
	g.server.mu.Lock()
	defer g.server.mu.Unlock()
	if contentType != "application/json" {
		return nil, errors.New("unexpected content type " + contentType)
	}
	g.server.fetches[g.key]++
	if g.server.schemaErr != nil {
		return nil, g.server.schemaErr
	}
	return g.server.docs[g.key], nil
}

func (g fakeGV) ServerRelativeURL() string { return "/openapi/v3/" + g.key + "?hash=" + g.hash }

var (
	appsV1    = schema.GroupVersion{Group: "apps", Version: "v1"}
	fixtureV1 = schema.GroupVersion{Group: "fixture.kc.dev", Version: "v1"}
)

func TestLoaderCachesByClusterAndHash(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	srv := newFakeServer(t)
	l := explain.NewLoader(4)

	first, err := l.Load(ctx, "c1", srv, appsV1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := l.Load(ctx, "c1", srv, appsV1)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("second load did not reuse the cached document")
	}
	if got := srv.fetchCount("apis/apps/v1"); got != 1 {
		t.Errorf("schema fetches = %d, want 1", got)
	}
	if srv.indexCalls != 2 {
		t.Errorf("index reads = %d, want one per load", srv.indexCalls)
	}

	// Another cluster with the same hash is fetched on its own.
	if _, err := l.Load(ctx, "c2", srv, appsV1); err != nil {
		t.Fatal(err)
	}
	if got := srv.fetchCount("apis/apps/v1"); got != 2 {
		t.Errorf("schema fetches after a second cluster = %d, want 2", got)
	}

	// A schema change (a CRD update) changes the hash and refetches.
	srv.setHash("apis/apps/v1", "a2")
	third, err := l.Load(ctx, "c1", srv, appsV1)
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Error("changed hash served the stale document")
	}
	if got := srv.fetchCount("apis/apps/v1"); got != 3 {
		t.Errorf("schema fetches after a hash change = %d, want 3", got)
	}
}

func TestLoaderIsBounded(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	srv := newFakeServer(t)
	l := explain.NewLoader(2)
	for _, cluster := range []string{"a", "b", "c"} {
		if _, err := l.Load(ctx, cluster, srv, appsV1); err != nil {
			t.Fatal(err)
		}
	}
	if l.Len() != 2 {
		t.Fatalf("cached documents = %d, want 2", l.Len())
	}
	// "a" was evicted; "c" is still cached.
	if _, err := l.Load(ctx, "c", srv, appsV1); err != nil {
		t.Fatal(err)
	}
	if got := srv.fetchCount("apis/apps/v1"); got != 3 {
		t.Errorf("fetches = %d, want 3 (c cached)", got)
	}
	if _, err := l.Load(ctx, "a", srv, appsV1); err != nil {
		t.Fatal(err)
	}
	if got := srv.fetchCount("apis/apps/v1"); got != 4 {
		t.Errorf("fetches = %d, want 4 (a evicted)", got)
	}
}

func TestLoaderAbsentVersusFailure(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	forbidden := apierrors.NewForbidden(schema.GroupResource{}, "", errors.New("no access to /openapi/v3"))

	tests := []struct {
		name   string
		setup  func(*fakeServer)
		gv     schema.GroupVersion
		absent bool
	}{
		{name: "group-version not published", gv: schema.GroupVersion{Group: "metrics.k8s.io", Version: "v1beta1"}, absent: true},
		{name: "index forbidden", setup: func(s *fakeServer) { s.indexErr = forbidden }, gv: appsV1},
		{name: "schema request fails", setup: func(s *fakeServer) { s.schemaErr = errors.New("connection reset") }, gv: fixtureV1},
		{name: "schema is not JSON", setup: func(s *fakeServer) { s.docs["apis/apps/v1"] = []byte("<html>") }, gv: appsV1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := newFakeServer(t)
			if tt.setup != nil {
				tt.setup(srv)
			}
			_, err := explain.NewLoader(4).Load(ctx, "c", srv, tt.gv)
			if err == nil {
				t.Fatal("no error")
			}
			if got := explain.IsAbsent(err); got != tt.absent {
				t.Errorf("IsAbsent(%v) = %v, want %v", err, got, tt.absent)
			}
		})
	}

	// The forbidden error stays inspectable for callers.
	srv := newFakeServer(t)
	srv.indexErr = forbidden
	_, err := explain.NewLoader(4).Load(ctx, "c", srv, appsV1)
	if !apierrors.IsForbidden(err) {
		t.Errorf("err = %v, want a wrapped Forbidden", err)
	}
}

func TestLoaderCoreGroupPath(t *testing.T) {
	t.Parallel()
	srv := newFakeServer(t)
	srv.docs["api/v1"] = srv.docs["apis/apps/v1"]
	srv.hashes["api/v1"] = "core"
	if _, err := explain.NewLoader(4).Load(t.Context(), "c", srv, schema.GroupVersion{Version: "v1"}); err != nil {
		t.Fatalf("core group-version: %v", err)
	}
	if srv.fetchCount("api/v1") != 1 {
		t.Error("core group-version was not read from api/v1")
	}
}
