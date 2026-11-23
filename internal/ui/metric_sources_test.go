package ui

// White-box: the dialog and its helpers are unexported.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stokaro/kc/internal/metricsources"
	"github.com/stokaro/kc/pkg/appconfig"
)

func TestSetContextBackend(t *testing.T) {
	t.Parallel()
	glob := appconfig.MetricsBackendConfig{Contexts: []string{"*prod*"}, Namespace: "mon", Service: "prom"}
	own := appconfig.MetricsBackendConfig{Contexts: []string{"prod-eu"}, Namespace: "obs", Service: "thanos-query", Port: "10902"}
	t.Run("goes before a glob entry", func(t *testing.T) {
		t.Parallel()
		mh := appconfig.MetricsHistoryConfig{Backends: []appconfig.MetricsBackendConfig{glob}}
		setContextBackend(&mh, "prod-eu", own, true)
		if got, _ := mh.BackendFor("prod-eu"); got.Service != "thanos-query" {
			t.Errorf("BackendFor = %+v, want the new entry to win", got)
		}
		if got, _ := mh.BackendFor("prod-us"); got.Service != "prom" {
			t.Errorf("other context = %+v, want the glob entry", got)
		}
	})
	t.Run("replaces its own entry", func(t *testing.T) {
		t.Parallel()
		mh := appconfig.MetricsHistoryConfig{Backends: []appconfig.MetricsBackendConfig{glob, own}}
		changed := own
		changed.Port = "9090"
		setContextBackend(&mh, "prod-eu", changed, true)
		if len(mh.Backends) != 2 || mh.Backends[1].Port != "9090" {
			t.Errorf("backends = %+v, want the entry replaced in place", mh.Backends)
		}
	})
	t.Run("an empty service removes its entry only", func(t *testing.T) {
		t.Parallel()
		backends := []appconfig.MetricsBackendConfig{own, glob}
		mh := appconfig.MetricsHistoryConfig{Backends: backends}
		setContextBackend(&mh, "prod-eu", appconfig.MetricsBackendConfig{}, false)
		if len(mh.Backends) != 1 || mh.Backends[0].Service != "prom" {
			t.Errorf("backends = %+v, want only the glob entry", mh.Backends)
		}
		if backends[0].Service != "thanos-query" {
			t.Error("the caller's slice was changed")
		}
	})
}

func TestMetricSourcesDialogBackend(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, service, prefix, labels string
		https                         bool
		want                          appconfig.MetricsBackendConfig
		wantOK                        bool
		wantErr                       string
	}{
		{name: "empty means none"},
		{name: "with port, prefix, labels and https", service: " monitoring/prometheus-k8s:9090 ", prefix: "/prometheus", labels: `cluster=prod-eu, region="eu"`, https: true,
			want: appconfig.MetricsBackendConfig{Contexts: []string{"prod"}, Namespace: "monitoring", Service: "prometheus-k8s", Port: "9090", Scheme: "https", PathPrefix: "/prometheus",
				Labels: map[string]string{"cluster": "prod-eu", "region": "eu"}}, wantOK: true},
		{name: "no namespace", service: "prometheus", wantErr: "namespace/name"},
		{name: "bad label", service: "m/p", labels: "cluster", wantErr: "not name=value"},
		{name: "invalid label name", service: "m/p", labels: "a-b=c", wantErr: "not a valid Prometheus label name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newMetricSourcesDialog(nil, "prod", "shop", appconfig.MetricsBackendConfig{}, false)
			d.service.SetValue(tc.service)
			d.prefix.SetValue(tc.prefix)
			d.labels.SetValue(tc.labels)
			d.https = tc.https
			got, ok, err := d.backend()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || ok != tc.wantOK {
				t.Fatalf("backend = %+v, %v, %v", got, ok, err)
			}
			if ok && !reflect.DeepEqual(got, tc.want) {
				t.Errorf("backend = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestMetricSourcesDialogRoundTripsTheConfiguredBackend(t *testing.T) {
	t.Parallel()
	b := appconfig.MetricsBackendConfig{Contexts: []string{"prod"}, Namespace: "obs", Service: "mimir", PathPrefix: "/prometheus", Labels: map[string]string{"cluster": "a", "env": "b"}}
	d := newMetricSourcesDialog(nil, "prod", "", b, true)
	got, ok, err := d.backend()
	if err != nil || !ok || !reflect.DeepEqual(got, b) {
		t.Fatalf("backend = %+v, %v, %v; want %+v", got, ok, err, b)
	}
}

func TestMetricSourcesDialogDropsAnOldReport(t *testing.T) {
	t.Parallel()
	d := newMetricSourcesDialog(nil, "prod", "", appconfig.MetricsBackendConfig{}, false)
	d.seq = 2
	d.showReport(metricSourcesReportMsg{seq: 1, checks: []metricsources.Check{{Name: "old"}}})
	if len(d.lines) != 0 {
		t.Fatalf("lines = %v, want an old report dropped", d.lines)
	}
	d.showReport(metricSourcesReportMsg{seq: 2, checks: []metricsources.Check{
		{Name: "metrics API", State: metricsources.Available},
		{Name: "nodes", State: metricsources.Forbidden, Detail: "not permitted"},
	}})
	if len(d.lines) != 2 || !d.errLines[1] || d.errLines[0] {
		t.Fatalf("lines = %v err %v, want the forbidden line marked", d.lines, d.errLines)
	}
	if !strings.Contains(d.note, "1 of 2 checks ok") {
		t.Errorf("note = %q", d.note)
	}
}
