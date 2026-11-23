package appconfig_test

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stokaro/kc/pkg/appconfig"
)

func TestMetricsHistoryEffectiveValues(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		window, want time.Duration
	}{
		{0, time.Hour},
		{-time.Minute, time.Hour},
		{time.Minute, 5 * time.Minute},
		{6 * time.Hour, 6 * time.Hour},
		{30 * 24 * time.Hour, 7 * 24 * time.Hour},
	} {
		c := appconfig.MetricsHistoryConfig{Window: metav1.Duration{Duration: tc.window}}
		if got := c.EffectiveWindow(); got != tc.want {
			t.Errorf("EffectiveWindow(%v) = %v, want %v", tc.window, got, tc.want)
		}
	}
	if got := (appconfig.MetricsHistoryConfig{}).EffectiveTimeout(); got != appconfig.DefaultMetricsHistoryTimeout {
		t.Errorf("EffectiveTimeout = %v", got)
	}
}

func TestMetricsHistoryBackendFor(t *testing.T) {
	t.Parallel()
	c := appconfig.MetricsHistoryConfig{Backends: []appconfig.MetricsBackendConfig{
		{Contexts: []string{"*prod*"}, Service: "first"},
		{Contexts: []string{"arn:aws:eks:eu-west-1:1:cluster/prod", "kind-*"}, Service: "second"},
	}}
	for _, tc := range []struct {
		context, want string
	}{
		{"arn:aws:eks:eu-west-1:1:cluster/prod", "first"}, // the first match wins
		{"kind-dev", "second"},
		{"staging", ""},
		{"", ""},
	} {
		b, ok := c.BackendFor(tc.context)
		if ok != (tc.want != "") || b.Service != tc.want {
			t.Errorf("BackendFor(%q) = %q %v, want %q", tc.context, b.Service, ok, tc.want)
		}
	}
	if _, ok := appconfig.DefaultMetricsHistory().BackendFor("kind-dev"); ok {
		t.Error("the default has a backend")
	}
}
