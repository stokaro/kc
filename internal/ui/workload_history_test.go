package ui

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	models "github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/promhistory"
)

func TestHistoryTargetOf(t *testing.T) {
	t.Parallel()
	rollouts := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	rs := schema.GroupKind{Group: "apps", Kind: "ReplicaSet"}
	job := schema.GroupKind{Group: "batch", Kind: "Job"}
	dep := schema.GroupKind{Group: "apps", Kind: "Deployment"}
	wl := models.InfoWorkload{GVR: rollouts, Namespace: "shop", Name: "web"}
	chain := func(kind string, vias ...[]schema.GroupKind) models.WorkloadChain {
		return models.WorkloadChain{Kind: kind, Vias: vias}
	}
	tests := []struct {
		name    string
		wl      models.InfoWorkload
		chain   models.WorkloadChain
		seen    bool
		want    promhistory.Target
		wantErr string
	}{
		{
			name: "built-in kind needs no chain",
			wl:   models.InfoWorkload{GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, Namespace: "shop", Name: "web"},
			want: promhistory.Target{Kind: "Deployment", Namespace: "shop", Name: "web"},
		},
		{name: "owns its pods", wl: wl, chain: chain("Rollout", nil), seen: true, want: promhistory.Target{Kind: "Rollout", Custom: true, Namespace: "shop", Name: "web"}},
		{name: "through a ReplicaSet", wl: wl, chain: chain("Rollout", []schema.GroupKind{rs}), seen: true, want: promhistory.Target{Kind: "Rollout", Custom: true, Via: "ReplicaSet", Namespace: "shop", Name: "web"}},
		{name: "through a Job", wl: wl, chain: chain("Rollout", []schema.GroupKind{job}), seen: true, want: promhistory.Target{Kind: "Rollout", Custom: true, Via: "Job", Namespace: "shop", Name: "web"}},
		{name: "no pod seen", wl: wl, wantErr: "no pod of rollouts.argoproj.io seen yet"},
		{name: "named like a built-in kind", wl: wl, chain: chain("StatefulSet", nil), seen: true, wantErr: "rollouts.argoproj.io looks like the built-in StatefulSet"},
		{name: "two ways", wl: wl, chain: chain("Rollout", nil, []schema.GroupKind{rs}), seen: true, wantErr: "pods of rollouts.argoproj.io are owned in 2 ways (directly; ReplicaSet.apps)"},
		{name: "too deep", wl: wl, chain: chain("Rollout", []schema.GroupKind{rs, dep}), seen: true, wantErr: "owned through ReplicaSet.apps, Deployment.apps; the owner series join one ReplicaSet or Job at most"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := historyTargetOf(tt.wl, tt.chain, tt.seen)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("target = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}
