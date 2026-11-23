package models

import (
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stokaro/kc/pkg/appconfig"
)

// testEvent is an Event in ns about kind/name, last seen ago before
// overviewTestNow.
func testEvent(ns, name, apiVersion, kind, obj string, uid types.UID, typ, reason string, ago time.Duration) *corev1.Event {
	at := metav1.NewTime(overviewTestNow.Add(-ago))
	return &corev1.Event{
		Namespace: ns, Name: name, CreationTimestamp: at,
		InvolvedObject: corev1.ObjectReference{APIVersion: apiVersion, Kind: kind, Namespace: ns, Name: obj, UID: uid},
		Type:           typ, Reason: reason, Message: reason + " of " + obj,
		FirstTimestamp: at, LastTimestamp: at, Count: 1,
	}
}

func eventObjects() []crclient.Object {
	backoff := testEvent("a", "e-backoff", "v1", "Pod", "web-1", "pod-uid", corev1.EventTypeWarning, "BackOff", 2*time.Minute)
	backoff.FirstTimestamp = metav1.NewTime(overviewTestNow.Add(-40 * time.Minute))
	backoff.Count = 3
	backoff.Series = &corev1.EventSeries{Count: 12, LastObservedTime: metav1.NewMicroTime(overviewTestNow.Add(-time.Minute))}
	// The API server kept no first time: only the creation time.
	pulled := testEvent("a", "e-pulled", "v1", "Pod", "db-0", "", corev1.EventTypeNormal, "Pulled", 30*time.Minute)
	pulled.FirstTimestamp = metav1.Time{}
	// Node events live in default and name no namespace.
	node := testEvent("default", "e-node", "v1", "Node", "n2", "", corev1.EventTypeWarning, "NodeNotReady", 5*time.Minute)
	node.InvolvedObject.Namespace = ""
	return append(amountObjects(),
		testEvent("a", "e-scale", "apps/v1", "Deployment", "web", "dep-uid", corev1.EventTypeNormal, "ScalingReplicaSet", 20*time.Minute),
		testEvent("a", "e-create", "apps/v1", "ReplicaSet", "web-abc", "rs-uid", corev1.EventTypeNormal, "SuccessfulCreate", 19*time.Minute),
		// An earlier ReplicaSet of the same name, now replaced.
		testEvent("a", "e-old-rs", "apps/v1", "ReplicaSet", "web-abc", "old-rs-uid", corev1.EventTypeNormal, "SuccessfulDelete", 50*time.Minute),
		backoff, pulled,
		node,
		testEvent("b", "e-other", "v1", "Pod", "web-1", "", corev1.EventTypeWarning, "BackOff", time.Minute),
	)
}

func eventNames(evs UsageEvents) []string {
	var out []string
	for _, e := range evs.Items {
		out = append(out, e.Event.Name)
	}
	return out
}

func TestUsageAmountEvents(t *testing.T) {
	t.Parallel()
	all := measuredReport(t, AllNamespaces, workloadMeasured(), eventObjects()...)
	inA := measuredReport(t, "a", workloadMeasured(), eventObjects()...)
	tests := []struct {
		name   string
		report *overviewReport
		focus  UsageFocus
		want   []string
	}{
		{
			name: "a Deployment has its own, its ReplicaSet's and its pods' events, oldest first", report: all,
			focus: UsageFocus{Workload: &wlDeployment},
			want:  []string{"e-scale", "e-create", "e-backoff"},
		},
		{name: "a pod has its own", report: all, focus: UsageFocus{PodNamespace: "a", Pod: "web-1"}, want: []string{"e-backoff"}},
		{name: "a node has its own and its pods'", report: all, focus: UsageFocus{Node: "n2"}, want: []string{"e-pulled", "e-node"}},
		{
			name: "a namespace has every event in it and none of another", report: inA, focus: UsageFocus{},
			want: []string{"e-old-rs", "e-pulled", "e-scale", "e-create", "e-backoff"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := usageAmounts(tc.report, nil, tc.focus, 3).Events
			if names := eventNames(got); !slices.Equal(names, tc.want) {
				t.Errorf("events = %v, want %v", names, tc.want)
			}
		})
	}

	t.Run("an event keeps its times, count and objects", func(t *testing.T) {
		t.Parallel()
		got := usageAmounts(all, nil, UsageFocus{PodNamespace: "a", Pod: "web-1"}, 3).Events
		if len(got.Items) != 1 {
			t.Fatalf("events = %v", eventNames(got))
		}
		e := got.Items[0]
		if e.Count != 12 || !e.Last.Equal(overviewTestNow.Add(-time.Minute)) || !e.First.Equal(overviewTestNow.Add(-40*time.Minute)) || !e.FirstKnown {
			t.Errorf("event = %+v, want the series count and last time, and the first time", e)
		}
		if e.Object != (ObjectRef{GVR: ovPodsGVR, Namespaced: true, Namespace: "a", Name: "web-1", UID: "pod-uid"}) {
			t.Errorf("object = %+v", e.Object)
		}
		if e.Event.GVR != ovEventsGVR || e.Event.Name != "e-backoff" || e.Event.Namespace != "a" {
			t.Errorf("event ref = %+v", e.Event)
		}
		if !got.Oldest.Equal(overviewTestNow.Add(-50 * time.Minute)) {
			t.Errorf("oldest = %v, want the earliest event of the scope", got.Oldest)
		}
	})

	t.Run("a node is cluster-scoped; a first time that was not kept says so", func(t *testing.T) {
		t.Parallel()
		got := usageAmounts(all, nil, UsageFocus{Node: "n2"}, 3).Events
		if len(got.Items) != 2 {
			t.Fatalf("events = %v", eventNames(got))
		}
		if got.Items[0].FirstKnown {
			t.Errorf("pulled = %+v, want the first time unknown", got.Items[0])
		}
		if n := got.Items[1].Object; n.Namespaced || n.Namespace != "" || n.GVR != ovNodesGVR {
			t.Errorf("node = %+v", n)
		}
	})

	t.Run("without events the reason is kept", func(t *testing.T) {
		t.Parallel()
		in := loadOverviewInput(t.Context(), newOverviewFakeClient(eventObjects()...), AllNamespaces, func(gvr schema.GroupVersionResource) string {
			if gvr == ovEventsGVR {
				return "forbidden"
			}
			return ""
		})
		r := computeOverview(in, appconfig.DefaultOverview(), overviewTestNow, nil)
		got := usageAmounts(r, nil, UsageFocus{}, 3).Events
		if got.Missing == "" || len(got.Items) != 0 {
			t.Errorf("events = %+v, want the reason and no items", got)
		}
	})
}
