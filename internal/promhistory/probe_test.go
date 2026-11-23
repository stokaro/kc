package promhistory_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stokaro/kc/internal/promhistory"
)

func TestScalarAndSelector(t *testing.T) {
	t.Parallel()
	b := &backend{t: t, path: "/api/v1/namespaces/monitoring/services/prometheus-k8s:9090/proxy/api/v1/", owners: vector("12")}
	cfg := promBackend
	cfg.Labels = map[string]string{"cluster": `prod"}`}
	c := newClient(t, b, cfg)
	sel := c.Selector(promhistory.Eq("namespace", "shop"))
	if sel != `{cluster="prod\"}",namespace="shop"}` {
		t.Errorf("Selector = %s, want the label quoted before the extra matcher", sel)
	}
	if got := promhistory.UnlabeledSelector(promhistory.Eq("namespace", "shop")); got != `{namespace="shop"}` {
		t.Errorf("UnlabeledSelector = %s", got)
	}
	v, _, err := c.Scalar(t.Context(), "count(kube_pod_owner"+sel+")", now)
	if err != nil || v != 12 {
		t.Fatalf("Scalar = %v, %v; want 12", v, err)
	}
}

func TestForbiddenMatchesErrForbidden(t *testing.T) {
	t.Parallel()
	for _, code := range []int{http.StatusForbidden, http.StatusUnauthorized} {
		b := &backend{t: t, status: code, body: `{"kind":"Status","message":"cannot get resource \"services/proxy\""}`}
		c := newClient(t, b, promBackend)
		_, _, err := c.Scalar(t.Context(), "count(kube_pod_owner{})", now)
		if !errors.Is(err, promhistory.ErrForbidden) {
			t.Errorf("HTTP %d: err = %v, want ErrForbidden", code, err)
		}
	}
	b := &backend{t: t, status: http.StatusServiceUnavailable, body: `{"kind":"Status","message":"no endpoints available for service"}`}
	_, _, err := newClient(t, b, promBackend).Scalar(t.Context(), "count(kube_pod_owner{})", now)
	if err == nil || errors.Is(err, promhistory.ErrForbidden) {
		t.Errorf("HTTP 503: err = %v, want a failure that is not ErrForbidden", err)
	}
}
