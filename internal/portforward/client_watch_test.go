package portforward_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/stokaro/kc/internal/portforward"
)

// TestClientRunnerWatchesThePod checks that a client forward reads its pod
// by name, the start of the watch that ends the forward when the pod is
// deleted. The server answers the forward only after that read, so the
// order of the two requests does not decide the result.
func TestClientRunnerWatchesThePod(t *testing.T) {
	t.Parallel()
	listed := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var selector string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces/ns/pods":
			mu.Lock()
			selector = r.URL.Query().Get("fieldSelector")
			mu.Unlock()
			once.Do(func() { close(listed) })
		case strings.HasSuffix(r.URL.Path, "/portforward"):
			select {
			case <-listed:
			case <-time.After(5 * time.Second):
			}
		}
		http.Error(w, `pods "web-0" is forbidden`, http.StatusForbidden)
	}))
	defer srv.Close()

	reg := portforward.NewRegistry(t.Context())
	runner := portforward.ClientRunner{Config: &rest.Config{Host: srv.URL, Timeout: 5 * time.Second}}
	s := portforward.Spec{Namespace: "ns", Kind: "pod", Name: "web-0", Pod: "web-0", Address: "127.0.0.1", RemotePort: 80}
	if _, err := reg.Start(t.Context(), s, runner); err == nil {
		t.Fatal("Start succeeded against a server that refuses every request")
	}
	select {
	case <-listed:
	default:
		t.Fatal("the forward never read its pod")
	}
	mu.Lock()
	defer mu.Unlock()
	if want := "metadata.name=web-0"; selector != want {
		t.Errorf("pod read with field selector %q, want %q", selector, want)
	}
}
