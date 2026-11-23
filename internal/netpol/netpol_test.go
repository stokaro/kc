package netpol_test

import (
	"errors"
	"slices"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/stokaro/kc/internal/netpol"
)

func pod(ns, name string, lbls map[string]string, ports ...corev1.ContainerPort) netpol.Pod {
	return netpol.Pod{Namespace: ns, Name: name, UID: name + "-uid", Labels: lbls, IPs: []string{"10.1.0.5"}, Ports: ports}
}

var (
	client = pod("shop", "client", map[string]string{"app": "client"})
	web    = pod("shop", "web", map[string]string{"app": "web"}, corev1.ContainerPort{Name: "http", ContainerPort: 8080})
	other  = pod("ops", "probe", map[string]string{"app": "probe"})
)

func policy(ns, name string, mut func(*networkingv1.NetworkPolicySpec)) networkingv1.NetworkPolicy {
	p := networkingv1.NetworkPolicy{Namespace: ns, Name: name}
	if mut != nil {
		mut(&p.Spec)
	}
	return p
}

func sel(kv ...string) *metav1.LabelSelector {
	m := make(map[string]string)
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return &metav1.LabelSelector{MatchLabels: m}
}

var (
	// denyIngress isolates web for ingress with no rules.
	denyIngress = policy("shop", "deny-web", func(s *networkingv1.NetworkPolicySpec) {
		s.PodSelector = *sel("app", "web")
		s.PolicyTypes = []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}
	})
	// allowClient lets client reach web on the named port http.
	allowClient = policy("shop", "allow-client", func(s *networkingv1.NetworkPolicySpec) {
		s.PodSelector = *sel("app", "web")
		port := intstr.FromString("http")
		s.Ingress = []networkingv1.NetworkPolicyIngressRule{{
			From:  []networkingv1.NetworkPolicyPeer{{PodSelector: sel("app", "client")}},
			Ports: []networkingv1.NetworkPolicyPort{{Port: &port}},
		}}
	})
	// allowOps lets every pod of namespaces labeled team=ops reach web.
	allowOps = policy("shop", "allow-ops", func(s *networkingv1.NetworkPolicySpec) {
		s.PodSelector = *sel("app", "web")
		s.Ingress = []networkingv1.NetworkPolicyIngressRule{{
			From: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: sel("team", "ops")}},
		}}
	})
	// denyEgress isolates every pod of shop for egress.
	denyEgress = policy("shop", "deny-egress", func(s *networkingv1.NetworkPolicySpec) {
		s.PolicyTypes = []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}
	})
)

var tcp8080 = netpol.Port{Protocol: corev1.ProtocolTCP, Number: 8080, Name: "http"}

func TestEvaluate(t *testing.T) {
	t.Parallel()
	nsLabels := netpol.Namespaces{Labels: map[string]map[string]string{"shop": {"team": "shop"}, "ops": {"team": "ops"}}}
	cases := []struct {
		name         string
		src          netpol.Pod
		port         netpol.Port
		policies     map[string]netpol.Policies
		ns           *netpol.Namespaces
		want         netpol.Verdict
		wantEgress   netpol.Verdict
		wantIngress  netpol.Verdict
		wantAllowing []string // policy/rule of the ingress side
	}{
		{name: "no policies", src: client, port: tcp8080, policies: map[string]netpol.Policies{"shop": {}},
			want: netpol.Allowed, wantEgress: netpol.Allowed, wantIngress: netpol.Allowed},
		{name: "default deny", src: client, port: tcp8080, policies: map[string]netpol.Policies{"shop": {Items: []networkingv1.NetworkPolicy{denyIngress}}},
			want: netpol.Denied, wantEgress: netpol.Allowed, wantIngress: netpol.Denied},
		{name: "additive allowance with a named port", src: client, port: tcp8080,
			policies: map[string]netpol.Policies{"shop": {Items: []networkingv1.NetworkPolicy{denyIngress, allowClient}}},
			want:     netpol.Allowed, wantEgress: netpol.Allowed, wantIngress: netpol.Allowed, wantAllowing: []string{"allow-client/0"}},
		{name: "named port does not cover another port", src: client, port: netpol.Port{Protocol: corev1.ProtocolTCP, Number: 9090},
			policies: map[string]netpol.Policies{"shop": {Items: []networkingv1.NetworkPolicy{allowClient}}},
			want:     netpol.Denied, wantEgress: netpol.Allowed, wantIngress: netpol.Denied},
		{name: "other protocol is not covered", src: client, port: netpol.Port{Protocol: corev1.ProtocolUDP, Number: 8080},
			policies: map[string]netpol.Policies{"shop": {Items: []networkingv1.NetworkPolicy{allowClient}}},
			want:     netpol.Denied, wantEgress: netpol.Allowed, wantIngress: netpol.Denied},
		{name: "pod selector without namespace selector stays in the policy namespace", src: pod("ops", "client", map[string]string{"app": "client"}), port: tcp8080,
			policies: map[string]netpol.Policies{"shop": {Items: []networkingv1.NetworkPolicy{allowClient}}, "ops": {}},
			want:     netpol.Denied, wantEgress: netpol.Allowed, wantIngress: netpol.Denied},
		{name: "namespace selector matches", src: other, port: tcp8080,
			policies: map[string]netpol.Policies{"shop": {Items: []networkingv1.NetworkPolicy{allowOps}}, "ops": {}},
			want:     netpol.Allowed, wantEgress: netpol.Allowed, wantIngress: netpol.Allowed, wantAllowing: []string{"allow-ops/0"}},
		{name: "namespace labels unknown", src: other, port: tcp8080,
			policies: map[string]netpol.Policies{"shop": {Items: []networkingv1.NetworkPolicy{allowOps}}, "ops": {}},
			ns:       &netpol.Namespaces{Err: errors.New("namespaces is forbidden")},
			want:     netpol.Unknown, wantEgress: netpol.Allowed, wantIngress: netpol.Unknown},
		{name: "unknown labels do not hide a certain allowance", src: client, port: tcp8080,
			policies: map[string]netpol.Policies{"shop": {Items: []networkingv1.NetworkPolicy{allowOps, allowClient}}},
			ns:       &netpol.Namespaces{Err: errors.New("namespaces is forbidden")},
			want:     netpol.Allowed, wantEgress: netpol.Allowed, wantIngress: netpol.Allowed, wantAllowing: []string{"allow-client/0"}},
		{name: "egress isolation at the source", src: client, port: tcp8080,
			policies: map[string]netpol.Policies{"shop": {Items: []networkingv1.NetworkPolicy{denyEgress, allowClient}}},
			want:     netpol.Denied, wantEgress: netpol.Denied, wantIngress: netpol.Allowed},
		{name: "policies of the destination namespace forbidden", src: other, port: tcp8080,
			policies: map[string]netpol.Policies{"shop": {Err: errors.New("forbidden")}, "ops": {}},
			want:     netpol.Unknown, wantEgress: netpol.Allowed, wantIngress: netpol.Unknown},
		{name: "a denial on one side decides even when the other is unknown", src: client, port: tcp8080,
			policies: map[string]netpol.Policies{"shop": {Items: []networkingv1.NetworkPolicy{denyEgress, denyIngress}}},
			want:     netpol.Denied, wantEgress: netpol.Denied, wantIngress: netpol.Denied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ns := nsLabels
			if tc.ns != nil {
				ns = *tc.ns
			}
			res := netpol.Evaluate(netpol.Input{Src: tc.src, Dst: web, Port: tc.port, Namespaces: ns, ByNamespace: tc.policies})
			if res.Verdict != tc.want || res.Egress.Verdict != tc.wantEgress || res.Ingress.Verdict != tc.wantIngress {
				t.Fatalf("verdict %v (egress %v: %s; ingress %v: %s), want %v (egress %v, ingress %v)",
					res.Verdict, res.Egress.Verdict, res.Egress.Reason, res.Ingress.Verdict, res.Ingress.Reason, tc.want, tc.wantEgress, tc.wantIngress)
			}
			var allowing []string
			for _, r := range res.Ingress.Allowing {
				allowing = append(allowing, r.Policy+"/"+strconv.Itoa(r.Rule))
			}
			if tc.wantAllowing != nil && !slices.Equal(allowing, tc.wantAllowing) {
				t.Errorf("ingress allowing = %v, want %v", allowing, tc.wantAllowing)
			}
			if res.Ingress.Verdict == netpol.Denied && len(res.Ingress.Selecting) == 0 {
				t.Error("a denial must name the policies that isolate the pod")
			}
		})
	}
}

func TestIPBlockIsUncertainForPodIPs(t *testing.T) {
	t.Parallel()
	byIP := policy("shop", "by-ip", func(s *networkingv1.NetworkPolicySpec) {
		s.PodSelector = *sel("app", "web")
		s.Ingress = []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "10.1.0.0/16"}}}}}
	})
	excluded := policy("shop", "by-ip", func(s *networkingv1.NetworkPolicySpec) {
		s.PodSelector = *sel("app", "web")
		s.Ingress = []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "10.1.0.0/16", Except: []string{"10.1.0.0/24"}}}}}}
	})
	for _, tc := range []struct {
		pol  networkingv1.NetworkPolicy
		want netpol.Verdict
	}{{byIP, netpol.Unknown}, {excluded, netpol.Denied}} {
		res := netpol.Evaluate(netpol.Input{Src: client, Dst: web, Port: tcp8080, ByNamespace: map[string]netpol.Policies{"shop": {Items: []networkingv1.NetworkPolicy{tc.pol}}}})
		if res.Ingress.Verdict != tc.want {
			t.Errorf("%v: ingress %v (%s), want %v", tc.pol.Spec.Ingress, res.Ingress.Verdict, res.Ingress.Reason, tc.want)
		}
	}
}

func TestEgressWithoutPolicyTypes(t *testing.T) {
	t.Parallel()
	// Egress rules make a policy without policyTypes isolate for egress.
	toWeb := policy("shop", "client-to-web", func(s *networkingv1.NetworkPolicySpec) {
		s.PodSelector = *sel("app", "client")
		port := intstr.FromInt32(8080)
		s.Egress = []networkingv1.NetworkPolicyEgressRule{{
			To:    []networkingv1.NetworkPolicyPeer{{PodSelector: sel("app", "web")}},
			Ports: []networkingv1.NetworkPolicyPort{{Port: &port}},
		}}
	})
	in := netpol.Input{Src: client, Dst: web, Port: tcp8080, ByNamespace: map[string]netpol.Policies{"shop": {Items: []networkingv1.NetworkPolicy{toWeb}}}}
	res := netpol.Evaluate(in)
	if res.Egress.Verdict != netpol.Allowed || len(res.Egress.Allowing) != 1 {
		t.Fatalf("egress = %+v", res.Egress)
	}
	// Ingress isolation also applies (no policyTypes), but toWeb selects
	// the client, not web.
	if res.Ingress.Isolated {
		t.Errorf("web is isolated by a policy that selects client")
	}
	in.Port = netpol.Port{Protocol: corev1.ProtocolTCP, Number: 9090}
	if res := netpol.Evaluate(in); res.Egress.Verdict != netpol.Denied {
		t.Errorf("egress to 9090 = %v, want denied", res.Egress.Verdict)
	}
}

func TestPortRange(t *testing.T) {
	t.Parallel()
	rng := policy("shop", "range", func(s *networkingv1.NetworkPolicySpec) {
		s.PodSelector = *sel("app", "web")
		port := intstr.FromInt32(8000)
		end := int32(8100)
		s.Ingress = []networkingv1.NetworkPolicyIngressRule{{Ports: []networkingv1.NetworkPolicyPort{{Port: &port, EndPort: &end}}}}
	})
	for _, tc := range []struct {
		port int32
		want netpol.Verdict
	}{{8080, netpol.Allowed}, {8100, netpol.Allowed}, {8101, netpol.Denied}} {
		res := netpol.Evaluate(netpol.Input{Src: client, Dst: web, Port: netpol.Port{Protocol: corev1.ProtocolTCP, Number: tc.port},
			ByNamespace: map[string]netpol.Policies{"shop": {Items: []networkingv1.NetworkPolicy{rng}}}})
		if res.Ingress.Verdict != tc.want {
			t.Errorf("port %d: %v, want %v", tc.port, res.Ingress.Verdict, tc.want)
		}
	}
}

func TestHostNetworkIsUnknown(t *testing.T) {
	t.Parallel()
	dst := web
	dst.HostNetwork = true
	res := netpol.Evaluate(netpol.Input{Src: client, Dst: dst, Port: tcp8080, ByNamespace: map[string]netpol.Policies{"shop": {}}})
	if res.Ingress.Verdict != netpol.Unknown || res.Verdict != netpol.Unknown {
		t.Errorf("host network destination: %+v", res)
	}
}

func TestPorts(t *testing.T) {
	t.Parallel()
	port := intstr.FromInt32(9090)
	pol := policy("shop", "p", func(s *networkingv1.NetworkPolicySpec) {
		s.Ingress = []networkingv1.NetworkPolicyIngressRule{{Ports: []networkingv1.NetworkPolicyPort{{Port: &port}}}}
	})
	got := netpol.Ports(web, pol, allowClient)
	var names []string
	for _, p := range got {
		names = append(names, p.String())
	}
	if want := []string{"TCP 8080 (http)", "TCP 9090", "TCP other ports"}; !slices.Equal(names, want) {
		t.Errorf("Ports = %v, want %v", names, want)
	}
}
