package portforward_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/stokaro/kc/internal/portforward"
)

// TestClientRunnerRefusedBeforeReady checks that a refused upgrade (RBAC,
// missing pod) comes back from Run as an error before ready, so Start
// reports it in the dialog's place instead of listing a dead forward.
func TestClientRunnerRefusedBeforeReady(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	var path atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The pod watch of the forward lists pods too.
		if strings.HasSuffix(r.URL.Path, "/portforward") {
			requests.Add(1)
			path.Store(r.URL.Path)
		}
		http.Error(w, `pods "web-0" is forbidden`, http.StatusForbidden)
	}))
	defer srv.Close()

	reg := portforward.NewRegistry(t.Context())
	runner := portforward.ClientRunner{Config: &rest.Config{Host: srv.URL, Timeout: 5 * time.Second}}
	s := portforward.Spec{Namespace: "ns", Kind: "pod", Name: "web-0", Pod: "web-0", Address: "127.0.0.1", RemotePort: 80}
	_, err := reg.Start(t.Context(), s, runner)
	if err == nil {
		t.Fatal("Start succeeded against a server that refuses every request")
	}
	if requests.Load() == 0 {
		t.Fatal("the runner sent no forward request")
	}
	if got, _ := path.Load().(string); got != "/api/v1/namespaces/ns/pods/web-0/portforward" {
		t.Fatalf("request path = %q", got)
	}
	if !strings.Contains(err.Error(), "forbidden") && !strings.Contains(err.Error(), "403") {
		t.Fatalf("error %q does not carry the refusal", err)
	}
	if reg.Len() != 0 {
		t.Fatalf("Len = %d, want 0", reg.Len())
	}
}
