package models

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stokaro/kc/pkg/appconfig"
)

// amountObjects is workloadObjects on two nodes, with a pending pod of the
// Deployment and creation times on the Deployment and one pod.
func amountObjects() []crclient.Object {
	objs := []crclient.Object{readyNode("n1", "4", "8Gi", 110), readyNode("n2", "2", "4Gi", 110)}
	for _, o := range workloadObjects() {
		if p, ok := o.(*corev1.Pod); ok {
			node := "n1"
			if p.Name == "db-0" {
				node = "n2"
			}
			o = onNode(p, node)
		}
		if o.GetName() == "web" || o.GetName() == "web-1" {
			o.SetCreationTimestamp(metav1.NewTime(overviewTestNow.Add(-time.Hour)))
		}
		if o.GetName() == "web-1" {
			o.SetUID("pod-uid")
		}
		objs = append(objs, o)
	}
	pending := ownedPod("a", "web-3", controllerRef("apps/v1", "ReplicaSet", "web-abc", "rs-uid"), resources("100m", "64Mi", "200m", "128Mi"))
	pending.Status.Phase = corev1.PodPending
	return append(objs, pending)
}

func TestUsageAmounts(t *testing.T) {
	t.Parallel()
	const mi = 1 << 20
	all := measuredReport(t, AllNamespaces, workloadMeasured(), amountObjects()...)
	tests := []struct {
		name     string
		report   *overviewReport
		focus    UsageFocus
		want     UsageAmounts
		wantTop  []string
		wantNote string
	}{
		{
			name: "a Deployment sums its pods; the pending one asks but is not expected to be measured", report: all,
			focus: UsageFocus{Workload: &wlDeployment},
			want: UsageAmounts{
				CPU:      ResourceAmount{Used: 0.2, UsedKnown: true, Requests: 0.3, Limits: 0.6},
				Memory:   ResourceAmount{Used: 140 * mi, UsedKnown: true, Requests: 192 * mi, Limits: 384 * mi},
				Measured: 2, Running: 2, Pods: 3, Found: true, UID: "dep-uid", Created: overviewTestNow.Add(-time.Hour),
			},
			wantTop: []string{"web-1", "web-2"},
		},
		{
			name: "a pod without limits makes the limit sum partial", report: all,
			focus: UsageFocus{Workload: &wlStatefulSet},
			want: UsageAmounts{
				CPU:      ResourceAmount{Used: 0.3, UsedKnown: true, Requests: 0.1, LimitsPartial: true},
				Memory:   ResourceAmount{Used: 30 * mi, UsedKnown: true, Requests: 64 * mi, LimitsPartial: true},
				Measured: 1, Running: 1, Pods: 1, Found: true, UID: "sts-uid",
			},
			wantTop: []string{"db-0"},
		},
		{
			name: "one pod carries its identity", report: all,
			focus: UsageFocus{PodNamespace: "a", Pod: "web-1"},
			want: UsageAmounts{
				CPU:      ResourceAmount{Used: 0.15, UsedKnown: true, Requests: 0.1, Limits: 0.2},
				Memory:   ResourceAmount{Used: 120 * mi, UsedKnown: true, Requests: 64 * mi, Limits: 128 * mi},
				Measured: 1, Running: 1, Pods: 1, Found: true, UID: "pod-uid", Created: overviewTestNow.Add(-time.Hour),
			},
			wantTop: []string{"web-1"},
		},
		{
			name: "a pod that is gone is not found and has no usage", report: all,
			focus:    UsageFocus{PodNamespace: "a", Pod: "web-9"},
			wantNote: "no pods that have not terminated",
		},
		{
			name: "a node has the allocatable as capacity", report: all,
			focus: UsageFocus{Node: "n2"},
			want: UsageAmounts{
				CPU:      ResourceAmount{Used: 0.3, UsedKnown: true, Requests: 0.1, LimitsPartial: true, Capacity: 2},
				Memory:   ResourceAmount{Used: 30 * mi, UsedKnown: true, Requests: 64 * mi, LimitsPartial: true, Capacity: 4096 * mi},
				Measured: 1, Running: 1, Pods: 1, Found: true,
			},
			wantTop: []string{"db-0"},
		},
		{
			name: "a namespace has no capacity of its own", report: measuredReport(t, "a", workloadMeasured(), amountObjects()...),
			focus: UsageFocus{},
			want: UsageAmounts{
				CPU:      ResourceAmount{Used: 0.511, UsedKnown: true, Requests: 0.8, Limits: 1.4, LimitsPartial: true},
				Memory:   ResourceAmount{Used: 181 * mi, UsedKnown: true, Requests: 512 * mi, Limits: 896 * mi, LimitsPartial: true},
				Measured: 5, Running: 7, Pods: 8, Found: true,
			},
			wantTop: []string{"web-1", "db-0", "web-2"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := usageAmounts(tc.report, nil, tc.focus, 3)
			var top []string
			for _, c := range got.Top {
				top = append(top, c.Name)
			}
			if !slices.Equal(top, tc.wantTop) {
				t.Errorf("top = %v, want %v", top, tc.wantTop)
			}
			if got.Note != tc.wantNote {
				t.Errorf("note = %q, want %q", got.Note, tc.wantNote)
			}
			got.Top, got.Note, got.At, got.ReadAt = nil, "", time.Time{}, time.Time{}
			if !got.Created.Equal(tc.want.Created) {
				t.Errorf("created = %v, want %v", got.Created, tc.want.Created)
			}
			got.Created = tc.want.Created
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("amounts = %+v\nwant      %+v", got, tc.want)
			}
		})
	}
}

func TestUsageAmountsBeforeAndWithoutPods(t *testing.T) {
	t.Parallel()
	if got := usageAmounts(nil, nil, UsageFocus{}, 3); !got.Loading {
		t.Errorf("no report = %+v, want loading", got)
	}
	in := loadOverviewInput(t.Context(), newOverviewFakeClient(), AllNamespaces, func(gvr schema.GroupVersionResource) string {
		if gvr == ovPodsGVR {
			return "forbidden"
		}
		return ""
	})
	r := computeOverview(in, appconfig.DefaultOverview(), overviewTestNow, nil)
	got := usageAmounts(r, nil, UsageFocus{Workload: &wlDeployment}, 3)
	if !strings.HasPrefix(got.Note, "pods cannot be read: ") || got.Found || got.UID != types.UID("") {
		t.Errorf("without pods = %+v", got)
	}
}
