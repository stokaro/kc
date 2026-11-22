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
				UsageWarnPercent: 70, UsageFailPercent: 85, UnderRequestedFactor: 1.5,
				UnderRequestedMinPercent: 10, MemoryLimitWarnPercent: 95, CPULimitWarnPercent: 99,
				DiskWarnPercent: 70, DiskFailPercent: 75, VolumeWarnPercent: 60, StatsMaxNodes: 5,
			},
			want: appconfig.OverviewConfig{
				Window: metav1.Duration{Duration: 10 * time.Minute}, RestartThreshold: 1,
				RequestsWarnPercent: 50, RequestsFailPercent: 70, LimitsWarnPercent: 150,
				PodsWarnPercent: 60, QuotaWarnPercent: 90,
				UsageWarnPercent: 70, UsageFailPercent: 85, UnderRequestedFactor: 1.5,
				UnderRequestedMinPercent: 10, MemoryLimitWarnPercent: 95, CPULimitWarnPercent: 99,
				DiskWarnPercent: 70, DiskFailPercent: 75, VolumeWarnPercent: 60, StatsMaxNodes: 5,
			},
		},
		{
			name: "under-requested factor of 1 or less takes the default",
			in:   appconfig.OverviewConfig{UnderRequestedFactor: 1},
			want: def,
		},
		{
			name: "usage and disk fail below warn are raised to warn",
			in:   appconfig.OverviewConfig{UsageWarnPercent: 95, UsageFailPercent: 50, DiskWarnPercent: 85, DiskFailPercent: 10},
			want: func() appconfig.OverviewConfig {
				o := def
				o.UsageWarnPercent, o.UsageFailPercent = 95, 95
				o.DiskWarnPercent, o.DiskFailPercent = 85, 85
				return o
			}(),
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
