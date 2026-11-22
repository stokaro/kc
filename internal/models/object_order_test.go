package models

import (
	"slices"
	"testing"
	"time"

	"github.com/sttts/kc/internal/tablecache"
	appconfig "github.com/sttts/kc/pkg/appconfig"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestOrderRowIndicesGroupsByNamespace(t *testing.T) {
	row := func(ns, name string, age int) tablecache.Row {
		var r tablecache.Row
		r.Namespace, r.Name = ns, name
		r.CreationTimestamp = metav1.NewTime(time.Unix(int64(age), 0))
		return r
	}
	items := []tablecache.Row{
		row("tailscale", "operator", 3),
		row("auth", "authelia", 2),
		row("kube-system", "coredns", 1),
		row("auth", "auth-db-1", 4),
		row("tailscale", "nameserver", 5),
	}
	names := func(idxs []int) []string {
		out := make([]string, len(idxs))
		for i, ii := range idxs {
			out[i] = items[ii].Namespace + "/" + items[ii].Name
		}
		return out
	}
	cases := []struct {
		name        string
		order       string
		byNamespace bool
		want        []string
	}{
		{"one namespace", appconfig.ObjectsOrderName, false,
			[]string{"auth/auth-db-1", "auth/authelia", "kube-system/coredns", "tailscale/nameserver", "tailscale/operator"}},
		{"by namespace, then name", appconfig.ObjectsOrderName, true,
			[]string{"auth/auth-db-1", "auth/authelia", "kube-system/coredns", "tailscale/nameserver", "tailscale/operator"}},
		{"by namespace, newest first", appconfig.ObjectsOrderCreationDesc, true,
			[]string{"auth/auth-db-1", "auth/authelia", "kube-system/coredns", "tailscale/nameserver", "tailscale/operator"}},
		{"by namespace, name descending", appconfig.ObjectsOrderNameDesc, true,
			[]string{"auth/authelia", "auth/auth-db-1", "kube-system/coredns", "tailscale/operator", "tailscale/nameserver"}},
		{"creation without grouping", appconfig.ObjectsOrderCreation, false,
			[]string{"kube-system/coredns", "auth/authelia", "tailscale/operator", "auth/auth-db-1", "tailscale/nameserver"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := names(orderRowIndices(items, tc.order, tc.byNamespace)); !slices.Equal(got, tc.want) {
				t.Fatalf("order = %v\nwant    %v", got, tc.want)
			}
		})
	}
}
