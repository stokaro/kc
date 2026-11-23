package models

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"

	table "github.com/stokaro/kc/internal/table"
)

func netpolPairObjects() map[schema.GroupVersionResource][]runtime.Object {
	port := intstr.FromString("http")
	return map[schema.GroupVersionResource][]runtime.Object{
		podGVR: {
			&corev1.Pod{Namespace: "shop", Name: "client", UID: "client-uid", Labels: map[string]string{"app": "client"},
				Status: corev1.PodStatus{PodIPs: []corev1.PodIP{{IP: "10.1.0.4"}}}},
			&corev1.Pod{Namespace: "shop", Name: "web", UID: "web-uid", Labels: map[string]string{"app": "web"},
				Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}}}},
				Status: corev1.PodStatus{PodIPs: []corev1.PodIP{{IP: "10.1.0.5"}}}},
		},
		networkPolicyGVR: {
			&networkingv1.NetworkPolicy{Namespace: "shop", Name: "deny-all", Spec: networkingv1.NetworkPolicySpec{
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			}},
			&networkingv1.NetworkPolicy{Namespace: "shop", Name: "allow-client", Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
				Ingress: []networkingv1.NetworkPolicyIngressRule{{
					From:  []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "client"}}}},
					Ports: []networkingv1.NetworkPolicyPort{{Port: &port}},
				}},
			}},
		},
		netpolNamespacesGVR: {&corev1.Namespace{Name: "shop"}},
	}
}

func cellsByID(rows []table.Row) map[string][]string {
	out := make(map[string][]string, len(rows))
	for _, r := range rows {
		id, cells, _, _ := r.Columns()
		out[id] = cells
	}
	return out
}

var (
	pairSrc = PodRef{Namespace: "shop", Name: "client", UID: "client-uid"}
	pairDst = PodRef{Namespace: "shop", Name: "web", UID: "web-uid"}
)

func TestNetpolPairRows(t *testing.T) {
	t.Parallel()
	r := newFakeReader(t, netpolPairObjects())
	in, problems := readNetpolPair(t.Context(), r, pairSrc, pairDst)
	if len(problems) != 0 {
		t.Fatalf("problems = %+v", problems)
	}
	read := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	rows := cellsByID(netpolPairRows(Deps{Ctx: t.Context()}, nil, []string{"network-policy"}, pairSrc, pairDst, in, problems, read))
	for id, want := range map[string]string{
		"summary/source":       "shop/client uid client-uid, 10.1.0.4",
		"summary/destination":  "shop/web uid web-uid, 10.1.0.5",
		"summary/read":         "2026-11-23T12:00:00Z",
		"port/TCP/8080/(http)": "TCP 8080 (http): allowed (egress allowed, ingress allowed)",
		"port/TCP/other/ports": "TCP other ports: denied (egress allowed, ingress denied)",
		"port/TCP/8080/(http)/ingress/0/networking.k8s.io/v1/networkpolicies/shop/allow-client": "allows, spec.ingress[0]",
		"port/TCP/other/ports/ingress/0/networking.k8s.io/v1/networkpolicies/shop/deny-all":     "isolates, spec.podSelector",
	} {
		got, ok := rows[id]
		if !ok || !strings.Contains(got[3], want) {
			t.Errorf("row %s = %q, want %q; rows: %v", id, got, want, keys(rows))
		}
	}
	if _, ok := rows["note"]; !ok {
		t.Error("no note row")
	}
}

// A pod replaced under the same name, or a source that cannot be read,
// stops the evaluation with an explicit problem instead of a verdict.
func TestNetpolPairProblems(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		src      PodRef
		denied   map[schema.GroupVersionResource]bool
		wantCode string
		fatal    bool
	}{
		{name: "replaced pod", src: PodRef{Namespace: "shop", Name: "client", UID: "old-uid"}, wantCode: "source-replaced", fatal: true},
		{name: "pods forbidden", src: pairSrc, denied: map[schema.GroupVersionResource]bool{podGVR: true}, wantCode: "source-unreadable", fatal: true},
		{name: "policies forbidden", src: pairSrc, denied: map[schema.GroupVersionResource]bool{networkPolicyGVR: true}, wantCode: "policies-unreadable/shop"},
		{name: "namespaces forbidden", src: pairSrc, denied: map[schema.GroupVersionResource]bool{netpolNamespacesGVR: true}, wantCode: "namespace-unreadable/shop"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &denyingReader{fakeReader: newFakeReader(t, netpolPairObjects()), denied: tc.denied}
			in, problems := readNetpolPair(t.Context(), r, tc.src, pairDst)
			found := false
			for _, p := range problems {
				if p.code == tc.wantCode {
					found = true
					if p.fatal != tc.fatal {
						t.Errorf("%s fatal = %v", p.code, p.fatal)
					}
				}
			}
			if !found {
				t.Fatalf("problems = %+v, want %s", problems, tc.wantCode)
			}
			rows := cellsByID(netpolPairRows(Deps{Ctx: t.Context()}, nil, []string{"network-policy"}, tc.src, pairDst, in, problems, time.Now()))
			if _, ok := rows["finding/"+tc.wantCode]; !ok {
				t.Errorf("no finding row for %s: %v", tc.wantCode, keys(rows))
			}
			_, anyVerdict := rows["port/TCP/8080/(http)"]
			if anyVerdict == tc.fatal {
				t.Errorf("verdict rows present = %v, fatal = %v", anyVerdict, tc.fatal)
			}
			if tc.wantCode == "policies-unreadable/shop" && !strings.Contains(rows["port/TCP/8080/(http)"][3], "unknown") {
				t.Errorf("unreadable policies must give unknown: %q", rows["port/TCP/8080/(http)"])
			}
		})
	}
}

func keys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
