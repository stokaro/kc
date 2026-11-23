// Package netpol evaluates standard Kubernetes NetworkPolicies
// (networking.k8s.io/v1) for one pair of pods and one port, and explains
// the decision. It models the API semantics only: what a conforming
// network plugin enforces. It knows nothing about plugin-specific policy
// types, and it never probes a connection.
package netpol

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// Pod is one end of a connection, as the policies see it.
type Pod struct {
	Namespace string
	Name      string
	UID       string
	Labels    map[string]string
	IPs       []string
	// HostNetwork pods are outside NetworkPolicy for most plugins.
	HostNetwork bool
	// Ports are the declared container ports, for named ports.
	Ports []corev1.ContainerPort
}

// PodFrom copies what the evaluator needs from a pod.
func PodFrom(p *corev1.Pod) Pod {
	out := Pod{Namespace: p.Namespace, Name: p.Name, UID: string(p.UID), Labels: p.Labels, HostNetwork: p.Spec.HostNetwork}
	for _, ip := range p.Status.PodIPs {
		out.IPs = append(out.IPs, ip.IP)
	}
	if len(out.IPs) == 0 && p.Status.PodIP != "" {
		out.IPs = []string{p.Status.PodIP}
	}
	for _, c := range p.Spec.Containers {
		out.Ports = append(out.Ports, c.Ports...)
	}
	return out
}

// Port is the destination port of a connection. Name is the container
// port name when the port is a declared one.
type Port struct {
	Protocol corev1.Protocol
	Number   int32
	Name     string
}

// OtherPort is the number of a port that no rule names.
const OtherPort int32 = 0

func (p Port) String() string {
	if p.Number == OtherPort {
		return string(p.Protocol) + " other ports"
	}
	s := string(p.Protocol) + " " + strconv.Itoa(int(p.Number))
	if p.Name != "" {
		s += " (" + p.Name + ")"
	}
	return s
}

// Namespaces gives namespace labels. A namespace that is missing from
// Labels has unknown labels; Err says why when they could not be read.
type Namespaces struct {
	Labels map[string]map[string]string
	Err    error
}

// Policies are the NetworkPolicies of one namespace, or why they could
// not be read.
type Policies struct {
	Items []networkingv1.NetworkPolicy
	Err   error
}

// Input is what one evaluation reads.
type Input struct {
	Src, Dst   Pod
	Port       Port
	Namespaces Namespaces
	// ByNamespace holds the policies of the source and destination
	// namespaces.
	ByNamespace map[string]Policies
}

// Verdict is the outcome of one direction or of the connection.
type Verdict int

const (
	// Unknown: kc could not decide (missing data, plugin-dependent rule).
	Unknown Verdict = iota
	Allowed
	Denied
)

func (v Verdict) String() string {
	switch v {
	case Allowed:
		return "allowed"
	case Denied:
		return "denied"
	default:
		return "unknown"
	}
}

// RuleRef names the policy rule a decision rests on.
type RuleRef struct {
	Namespace, Policy string
	// Rule is the index in spec.ingress or spec.egress; -1 when the
	// policy only selects the pod (isolation without an allowing rule).
	Rule int
	// Why explains the match or the reason it is uncertain.
	Why string
}

func (r RuleRef) Field(dir Direction) string {
	if r.Rule < 0 {
		return "spec.podSelector"
	}
	return fmt.Sprintf("spec.%s[%d]", dir.field(), r.Rule)
}

// Direction is ingress (at the destination) or egress (at the source).
type Direction int

const (
	Egress Direction = iota
	Ingress
)

func (d Direction) String() string {
	if d == Ingress {
		return "ingress"
	}
	return "egress"
}

func (d Direction) field() string {
	if d == Ingress {
		return "ingress"
	}
	return "egress"
}

// Side is the decision of one direction.
type Side struct {
	Direction Direction
	Verdict   Verdict
	// Isolated is set when a policy selects the pod for this direction.
	Isolated bool
	// Selecting are the policies that isolate the pod (Rule -1).
	Selecting []RuleRef
	// Allowing are the rules that allow the connection.
	Allowing []RuleRef
	// Uncertain are the rules that may allow it, with why kc cannot tell.
	Uncertain []RuleRef
	// Reason explains the verdict in one sentence.
	Reason string
}

// Result is the evaluation of one port.
type Result struct {
	Port    Port
	Egress  Side
	Ingress Side
	Verdict Verdict
}

// Evaluate decides whether policies allow Src to open a connection to Dst
// on Port. The connection is allowed only when the egress side of the
// source and the ingress side of the destination both allow it.
func Evaluate(in Input) Result {
	res := Result{Port: in.Port}
	res.Egress = evaluateSide(in, Egress)
	res.Ingress = evaluateSide(in, Ingress)
	switch {
	case res.Egress.Verdict == Denied || res.Ingress.Verdict == Denied:
		res.Verdict = Denied
	case res.Egress.Verdict == Allowed && res.Ingress.Verdict == Allowed:
		res.Verdict = Allowed
	default:
		res.Verdict = Unknown
	}
	return res
}

// evaluateSide decides one direction: the policies of the pod's own
// namespace that select it for dir, and their rules toward the peer.
func evaluateSide(in Input, dir Direction) Side {
	self, peer := in.Src, in.Dst
	if dir == Ingress {
		self, peer = in.Dst, in.Src
	}
	side := Side{Direction: dir}
	if self.HostNetwork {
		side.Reason = fmt.Sprintf("Pod %s/%s uses the host network; most network plugins do not apply NetworkPolicy to it.", self.Namespace, self.Name)
		return side
	}
	pols, ok := in.ByNamespace[self.Namespace]
	if !ok || pols.Err != nil {
		why := "were not read"
		if pols.Err != nil {
			why = "could not be read: " + pols.Err.Error()
		}
		side.Reason = fmt.Sprintf("NetworkPolicies in %s %s.", self.Namespace, why)
		return side
	}
	for i := range pols.Items {
		p := &pols.Items[i]
		if p.Namespace != self.Namespace || !appliesTo(p, dir) {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(&p.Spec.PodSelector)
		if err != nil {
			side.Uncertain = append(side.Uncertain, RuleRef{Namespace: p.Namespace, Policy: p.Name, Rule: -1, Why: "invalid podSelector: " + err.Error()})
			continue
		}
		if !sel.Matches(labels.Set(self.Labels)) {
			continue
		}
		side.Isolated = true
		side.Selecting = append(side.Selecting, RuleRef{Namespace: p.Namespace, Policy: p.Name, Rule: -1, Why: "selects the pod (" + selectorText(sel) + ")"})
		for r, rule := range rules(p, dir) {
			m := ruleMatches(in, p.Namespace, rule, peer)
			ref := RuleRef{Namespace: p.Namespace, Policy: p.Name, Rule: r, Why: m.why}
			switch m.verdict {
			case Allowed:
				side.Allowing = append(side.Allowing, ref)
			case Unknown:
				side.Uncertain = append(side.Uncertain, ref)
			}
		}
	}
	switch {
	case len(side.Allowing) > 0:
		side.Verdict = Allowed
		side.Reason = fmt.Sprintf("%d rule(s) allow it.", len(side.Allowing))
	case !side.Isolated && len(side.Uncertain) == 0:
		side.Verdict = Allowed
		side.Reason = fmt.Sprintf("No NetworkPolicy selects %s/%s for %s, so all %s traffic is allowed.", self.Namespace, self.Name, dir, dir)
	case len(side.Uncertain) > 0:
		side.Reason = fmt.Sprintf("No rule allows it for certain; %d rule(s) depend on data kc does not have.", len(side.Uncertain))
	default:
		side.Verdict = Denied
		side.Reason = fmt.Sprintf("%d polic(ies) isolate the pod for %s and none of their rules match.", len(side.Selecting), dir)
	}
	return side
}

// appliesTo reports whether p isolates its pods for dir. Without
// policyTypes, Ingress always applies and Egress applies when the policy
// has egress rules.
func appliesTo(p *networkingv1.NetworkPolicy, dir Direction) bool {
	want := networkingv1.PolicyTypeEgress
	if dir == Ingress {
		want = networkingv1.PolicyTypeIngress
	}
	if len(p.Spec.PolicyTypes) > 0 {
		return slices.Contains(p.Spec.PolicyTypes, want)
	}
	if dir == Ingress {
		return true
	}
	return len(p.Spec.Egress) > 0
}

// rule is one ingress or egress rule in a common shape.
type rule struct {
	peers []networkingv1.NetworkPolicyPeer
	ports []networkingv1.NetworkPolicyPort
}

func rules(p *networkingv1.NetworkPolicy, dir Direction) []rule {
	var out []rule
	if dir == Ingress {
		for _, r := range p.Spec.Ingress {
			out = append(out, rule{peers: r.From, ports: r.Ports})
		}
		return out
	}
	for _, r := range p.Spec.Egress {
		out = append(out, rule{peers: r.To, ports: r.Ports})
	}
	return out
}

type match struct {
	verdict Verdict // Allowed: matches; Denied: does not; Unknown: cannot tell
	why     string
}

// ruleMatches decides whether a rule of a policy in policyNS admits peer
// on the input's port. Both the peer and the port must match.
func ruleMatches(in Input, policyNS string, r rule, peer Pod) match {
	pm := portsMatch(r.ports, in.Port, in.Dst)
	if pm.verdict == Denied {
		return pm
	}
	peerM := peersMatch(in, policyNS, r.peers, peer)
	switch {
	case peerM.verdict == Denied:
		return peerM
	case peerM.verdict == Unknown || pm.verdict == Unknown:
		return match{verdict: Unknown, why: strings.TrimPrefix(peerM.why+"; "+pm.why, "; ")}
	default:
		return match{verdict: Allowed, why: peerM.why + "; " + pm.why}
	}
}

// portsMatch: an empty list allows every port. A named port is resolved on
// the destination pod, which is what plugins do for ingress and egress.
func portsMatch(ports []networkingv1.NetworkPolicyPort, want Port, dst Pod) match {
	if len(ports) == 0 {
		return match{verdict: Allowed, why: "all ports"}
	}
	for _, p := range ports {
		proto := corev1.ProtocolTCP
		if p.Protocol != nil {
			proto = *p.Protocol
		}
		if proto != want.Protocol {
			continue
		}
		if p.Port == nil {
			return match{verdict: Allowed, why: "all " + string(proto) + " ports"}
		}
		if p.Port.Type == 0 { // number
			lo, hi := p.Port.IntVal, p.Port.IntVal
			if p.EndPort != nil {
				hi = *p.EndPort
			}
			if want.Number >= lo && want.Number <= hi {
				if lo == hi {
					return match{verdict: Allowed, why: "port " + strconv.Itoa(int(lo))}
				}
				return match{verdict: Allowed, why: fmt.Sprintf("ports %d-%d", lo, hi)}
			}
			continue
		}
		name := p.Port.StrVal
		for _, cp := range dst.Ports {
			cproto := cp.Protocol
			if cproto == "" {
				cproto = corev1.ProtocolTCP
			}
			if cp.Name == name && cproto == proto && cp.ContainerPort == want.Number {
				return match{verdict: Allowed, why: fmt.Sprintf("named port %s = %d on %s", name, cp.ContainerPort, dst.Name)}
			}
		}
	}
	return match{verdict: Denied, why: "port " + want.String() + " is not listed"}
}

// peersMatch: an empty list admits every peer. Pod and namespace
// selectors are exact; an ipBlock against a pod IP depends on the plugin.
func peersMatch(in Input, policyNS string, peers []networkingv1.NetworkPolicyPeer, peer Pod) match {
	if len(peers) == 0 {
		return match{verdict: Allowed, why: "every peer"}
	}
	var uncertain []string
	for _, p := range peers {
		m := peerMatches(in, policyNS, p, peer)
		switch m.verdict {
		case Allowed:
			return m
		case Unknown:
			uncertain = append(uncertain, m.why)
		}
	}
	if len(uncertain) > 0 {
		return match{verdict: Unknown, why: strings.Join(uncertain, "; ")}
	}
	return match{verdict: Denied, why: fmt.Sprintf("no peer matches %s/%s", peer.Namespace, peer.Name)}
}

func peerMatches(in Input, policyNS string, p networkingv1.NetworkPolicyPeer, peer Pod) match {
	if p.IPBlock != nil {
		return ipBlockMatches(p.IPBlock, peer)
	}
	ns := namespaceMatches(in, policyNS, p.NamespaceSelector, peer)
	if ns.verdict != Allowed {
		return ns
	}
	nsWhy := ns.why
	if p.PodSelector == nil {
		return match{verdict: Allowed, why: nsWhy}
	}
	sel, err := metav1.LabelSelectorAsSelector(p.PodSelector)
	if err != nil {
		return match{verdict: Unknown, why: "invalid podSelector: " + err.Error()}
	}
	if !sel.Matches(labels.Set(peer.Labels)) {
		return match{verdict: Denied}
	}
	return match{verdict: Allowed, why: nsWhy + ", podSelector " + selectorText(sel)}
}

// namespaceMatches checks the peer's namespace: the policy's own unless a
// namespaceSelector is given.
func namespaceMatches(in Input, policyNS string, nsSelector *metav1.LabelSelector, peer Pod) match {
	if nsSelector == nil {
		if peer.Namespace != policyNS {
			return match{verdict: Denied}
		}
		return match{verdict: Allowed, why: "namespace " + policyNS}
	}
	sel, err := metav1.LabelSelectorAsSelector(nsSelector)
	if err != nil {
		return match{verdict: Unknown, why: "invalid namespaceSelector: " + err.Error()}
	}
	nsLabels, ok := in.Namespaces.Labels[peer.Namespace]
	if !ok && !sel.Empty() {
		why := "the labels of namespace " + peer.Namespace + " are unknown"
		if in.Namespaces.Err != nil {
			why += " (" + in.Namespaces.Err.Error() + ")"
		}
		return match{verdict: Unknown, why: why}
	}
	if !sel.Matches(labels.Set(nsLabels)) {
		return match{verdict: Denied}
	}
	return match{verdict: Allowed, why: "namespaceSelector " + selectorText(sel)}
}

// ipBlockMatches: whether an ipBlock applies to pod IPs is up to the
// plugin (the API documents ipBlock for cluster-external addresses), so a
// match is uncertain and a miss is certain.
func ipBlockMatches(b *networkingv1.IPBlock, peer Pod) match {
	cidr, err := netip.ParsePrefix(b.CIDR)
	if err != nil {
		return match{verdict: Unknown, why: "invalid ipBlock " + b.CIDR}
	}
	if len(peer.IPs) == 0 {
		return match{verdict: Unknown, why: fmt.Sprintf("ipBlock %s: pod %s has no IP yet", b.CIDR, peer.Name)}
	}
	for _, s := range peer.IPs {
		ip, err := netip.ParseAddr(s)
		if err != nil || !cidr.Contains(ip) {
			continue
		}
		excluded := false
		for _, e := range b.Except {
			if ep, err := netip.ParsePrefix(e); err == nil && ep.Contains(ip) {
				excluded = true
			}
		}
		if excluded {
			continue
		}
		return match{verdict: Unknown, why: fmt.Sprintf("ipBlock %s contains pod IP %s; whether ipBlock applies to pod IPs depends on the network plugin", b.CIDR, s)}
	}
	return match{verdict: Denied}
}

func selectorText(sel labels.Selector) string {
	if sel.Empty() {
		return "all"
	}
	return sel.String()
}

// Ports returns the ports to evaluate for a pair: the declared container
// ports of the destination, every numeric port the policies name (so a
// rule for an undeclared port is not missed), and last "TCP other ports",
// which stands for every TCP port no rule names.
func Ports(dst Pod, policies ...networkingv1.NetworkPolicy) []Port {
	var out []Port
	seen := make(map[string]bool)
	add := func(p Port) {
		k := string(p.Protocol) + "/" + strconv.Itoa(int(p.Number))
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, p)
	}
	for _, cp := range dst.Ports {
		proto := cp.Protocol
		if proto == "" {
			proto = corev1.ProtocolTCP
		}
		if cp.ContainerPort != OtherPort {
			add(Port{Protocol: proto, Number: cp.ContainerPort, Name: cp.Name})
		}
	}
	for _, p := range policies {
		for _, r := range p.Spec.Ingress {
			addRulePorts(r.Ports, add)
		}
		for _, r := range p.Spec.Egress {
			addRulePorts(r.Ports, add)
		}
	}
	return append(out, Port{Protocol: corev1.ProtocolTCP, Number: OtherPort})
}

func addRulePorts(ports []networkingv1.NetworkPolicyPort, add func(Port)) {
	for _, p := range ports {
		if p.Port == nil || p.Port.Type != 0 {
			continue
		}
		proto := corev1.ProtocolTCP
		if p.Protocol != nil {
			proto = *p.Protocol
		}
		add(Port{Protocol: proto, Number: p.Port.IntVal})
	}
}
