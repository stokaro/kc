package appconfig_test

import (
	"testing"
	"time"

	"github.com/sttts/kc/pkg/appconfig"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestOverviewNormalized(t *testing.T) {
	t.Parallel()
	def := appconfig.DefaultOverview()
	tests := []struct {
		name string
		in   appconfig.OverviewConfig
		want appconfig.OverviewConfig
	}{
		{name: "empty takes every default", in: appconfig.OverviewConfig{}, want: def},
		{
			name: "negative values take the default",
			in:   appconfig.OverviewConfig{RestartThreshold: -1, QuotaWarnPercent: -5, Window: metav1.Duration{Duration: -time.Minute}},
			want: def,
		},
		{
			name: "set values stay",
			in: appconfig.OverviewConfig{
				Window: metav1.Duration{Duration: 10 * time.Minute}, RestartThreshold: 1,
				RequestsWarnPercent: 50, RequestsFailPercent: 70, LimitsWarnPercent: 150,
				PodsWarnPercent: 60, QuotaWarnPercent: 90,
			},
			want: appconfig.OverviewConfig{
				Window: metav1.Duration{Duration: 10 * time.Minute}, RestartThreshold: 1,
				RequestsWarnPercent: 50, RequestsFailPercent: 70, LimitsWarnPercent: 150,
				PodsWarnPercent: 60, QuotaWarnPercent: 90,
			},
		},
		{
			name: "fail below warn is raised to warn",
			in:   appconfig.OverviewConfig{RequestsWarnPercent: 90, RequestsFailPercent: 85},
			want: func() appconfig.OverviewConfig {
				o := def
				o.RequestsWarnPercent = 90
				o.RequestsFailPercent = 90
				return o
			}(),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.in.Normalized(); got != tc.want {
				t.Fatalf("Normalized() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDefaultConfigCarriesOverviewDefaults(t *testing.T) {
	t.Parallel()
	if got := appconfig.Default().Overview; got != appconfig.DefaultOverview() {
		t.Fatalf("Default().Overview = %+v, want %+v", got, appconfig.DefaultOverview())
	}
}
