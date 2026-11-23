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

	src := infoHistory(t.Context(), cfgFn, key, mh, deploy)
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
	if src := infoHistory(t.Context(), cfgFn, kccluster.Key{ContextName: "prod"}, mh, deploy); src != nil {
		t.Error("source for a context without a backend")
	}
	revisions := deploy
	revisions.GVR.Resource = "controllerrevisions"
	if src := infoHistory(t.Context(), cfgFn, key, mh, revisions); src != nil {
		t.Error("source for controllerrevisions")
	}
	// The impersonated user is part of the identity.
	other := key
	other.ImpersonateUser = "dev"
	if infoHistory(t.Context(), cfgFn, other, mh, deploy).Key == src.Key {
		t.Error("impersonation does not change the key")
	}
}
