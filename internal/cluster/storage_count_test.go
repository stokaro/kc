package cluster_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"

	kccluster "github.com/stokaro/kc/internal/cluster"
)

// TestStorageCountHoldsOffAfterForbidden: a user without get on /metrics
// gets one 403, not one per resource count. Other errors wait maxAge.
func TestStorageCountHoldsOffAfterForbidden(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		maxAge time.Duration
		want   int32
	}{
		{name: "forbidden", status: http.StatusForbidden, maxAge: time.Nanosecond, want: 1},
		{name: "unauthorized", status: http.StatusUnauthorized, maxAge: time.Nanosecond, want: 1},
		{name: "server error within maxAge", status: http.StatusInternalServerError, maxAge: time.Minute, want: 1},
		{name: "server error after maxAge", status: http.StatusInternalServerError, maxAge: time.Nanosecond, want: 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var metrics atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/metrics" {
					metrics.Add(1)
				}
				w.WriteHeader(tc.status)
			}))
			t.Cleanup(srv.Close)
			cl, err := kccluster.New(&rest.Config{Host: srv.URL})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
			for range 5 {
				if _, ok := cl.StorageCount(t.Context(), gvr, tc.maxAge); ok {
					t.Fatal("StorageCount ok without metrics")
				}
			}
			if got := metrics.Load(); got != tc.want {
				t.Fatalf("/metrics requests = %d, want %d", got, tc.want)
			}
		})
	}
}
