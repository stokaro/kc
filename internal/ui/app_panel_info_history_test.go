package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/investigation"
	models "github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/pkg/appconfig"
)

func TestInfoHistorySource(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/query_range") {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[1,"1"]]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"3"]}]}}`))
	}))
	t.Cleanup(srv.Close)
	cfgFn := func() *rest.Config { return &rest.Config{Host: srv.URL} }
	mh := appconfig.MetricsHistoryConfig{
		Window: metav1.Duration{Duration: 30 * time.Minute},
		Backends: []appconfig.MetricsBackendConfig{
			{Contexts: []string{"kind-*"}, Namespace: "monitoring", Service: "prometheus", Port: "9090"},
		},
	}
	deploy := models.InfoWorkload{GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, Namespace: "shop", Name: "web"}
	key := kccluster.Key{ContextName: "kind-dev"}

	src := infoHistory(t.Context(), cfgFn, key, mh, deploy, investigation.Window{})
	if src == nil {
		t.Fatal("no source for a matching context and a Deployment")
	}
	if src.Window != 30*time.Minute || !strings.Contains(src.Key, "apps/v1, Resource=deployments") || !strings.Contains(src.Key, "shop/web") {
		t.Errorf("source window %v key %q", src.Window, src.Key)
	}
	read, cancel := src.Start()
	h, err := read()
	cancel()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if h.Target.Kind != "Deployment" || h.Source != "monitoring/prometheus:9090" || h.Pods != 3 {
		t.Errorf("history %+v", h)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) == 0 || !strings.HasPrefix(paths[0], "/api/v1/namespaces/monitoring/services/prometheus:9090/proxy/api/v1/") {
		t.Errorf("paths %v", paths)
	}

	// Another context, or a resource that is no workload, has no source.
	if src := infoHistory(t.Context(), cfgFn, kccluster.Key{ContextName: "prod"}, mh, deploy, investigation.Window{}); src != nil {
		t.Error("source for a context without a backend")
	}
	revisions := deploy
	revisions.GVR.Resource = "controllerrevisions"
	if src := infoHistory(t.Context(), cfgFn, key, mh, revisions, investigation.Window{}); src != nil {
		t.Error("source for controllerrevisions")
	}
	// The impersonated user is part of the identity.
	other := key
	other.ImpersonateUser = "dev"
	if infoHistory(t.Context(), cfgFn, other, mh, deploy, investigation.Window{}).Key == src.Key {
		t.Error("impersonation does not change the key")
	}
}

// TestInfoHistoryReadsTheInvestigationWindow: a fixed window is read with
// its own end and length and named in the heading; a live one ends now.
func TestInfoHistoryReadsTheInvestigationWindow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	start, end := now.Add(-3*time.Hour), now.Add(-2*time.Hour)
	fixed := historyOptions(30*time.Minute, investigation.Window{Start: start, End: end}, now)
	if fixed.Window != time.Hour || !fixed.Now.Equal(end) {
		t.Errorf("fixed = %+v, want 1h ending at %v", fixed, end)
	}
	live := historyOptions(30*time.Minute, investigation.Window{Last: 2 * time.Hour}, now)
	if live.Window != 2*time.Hour || !live.Now.Equal(now) {
		t.Errorf("live = %+v, want 2h ending now", live)
	}
	open := historyOptions(30*time.Minute, investigation.Window{Start: start}, now)
	if open.Window != 3*time.Hour || !open.Now.Equal(now) {
		t.Errorf("open-ended = %+v, want 3h ending now", open)
	}
	if off := historyOptions(30*time.Minute, investigation.Window{}, now); off.Window != 30*time.Minute || !off.Now.IsZero() {
		t.Errorf("off = %+v, want the configured window", off)
	}

	mh := appconfig.MetricsHistoryConfig{Backends: []appconfig.MetricsBackendConfig{{Contexts: []string{"kind-*"}, Namespace: "monitoring", Service: "prometheus"}}}
	deploy := models.InfoWorkload{GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, Namespace: "shop", Name: "web"}
	key := kccluster.Key{ContextName: "kind-dev"}
	cfgFn := func() *rest.Config { return &rest.Config{Host: "http://127.0.0.1:1"} }
	plain := infoHistory(t.Context(), cfgFn, key, mh, deploy, investigation.Window{})
	win := infoHistory(t.Context(), cfgFn, key, mh, deploy, investigation.Window{Start: start, End: end})
	if plain.Key == win.Key || win.Label == "" || plain.Label != "" {
		t.Errorf("keys %q / %q, labels %q / %q: the window must change the read and name itself", plain.Key, win.Key, plain.Label, win.Label)
	}
}
