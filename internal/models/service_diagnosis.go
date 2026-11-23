package models

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	netutils "k8s.io/utils/net"
)

var endpointSlicesGVR = schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}

// diagSeverity orders the findings of a Service diagnosis.
type diagSeverity int

const (
	// diagInfo explains a configuration that is expected to look unusual.
	diagInfo diagSeverity = iota
	// diagWarning is a mismatch that may lose traffic.
	diagWarning
	// diagError is a mismatch that loses traffic.
	diagError
	// diagIncomplete means kc could not read what a check needs.
	diagIncomplete
)

func (s diagSeverity) String() string {
	switch s {
	case diagWarning:
		return "warning"
	case diagError:
		return "error"
	case diagIncomplete:
		return "incomplete"
	default:
		return "info"
	}
}

// diagLink is an object a finding cites, with the evidence for it.
type diagLink struct {
	GVR       schema.GroupVersionResource
	Namespace string
	Name      string
	Evidence  string
	// Problem is set when the object cannot be opened, such as a pod an
	// endpoint names that no longer exists.
	Problem string
}

// diagFinding is one observation about a Service.
type diagFinding struct {
	Code     string // stable, for row IDs
	Severity diagSeverity
	Title    string
	Detail   string
	Links    []diagLink
}

// serviceInput is what a Service diagnosis reads. A nil slice with an error
// means the list could not be read; the checks that need it then report
// that the result is incomplete instead of a missing backend.
type serviceInput struct {
	Service   *corev1.Service
	Pods      []corev1.Pod // the pods of the Service's namespace
	PodsErr   error
	Slices    []discoveryv1.EndpointSlice // the slices of the namespace
	SlicesErr error
}

// endpointCounts sums the endpoints of the Service's slices.
type endpointCounts struct {
	Slices, Endpoints, Ready, Serving, Terminating int
	AddressTypes                                   []string
}

// serviceDiagnosis is the result: a summary and the findings, the most
// severe first, and the backends with their evidence.
type serviceDiagnosis struct {
	Summary  []string
	Findings []diagFinding
	Backends []diagLink
}

// diagnoseService checks the path Service -> selected pods -> EndpointSlices
// -> readiness and target ports, as the EndpointSlice controller builds it.
// It reports only what the objects show; it does not test DNS, the network
// or the application.
func diagnoseService(in serviceInput) (d serviceDiagnosis) {
	svc := in.Service
	if svc == nil {
		return d
	}
	defer func() { sortFindings(d.Findings) }()
	d.Summary = append(d.Summary, serviceTypeSummary(svc))
	if svc.Spec.Type == corev1.ServiceTypeExternalName {
		d.Findings = append(d.Findings, diagFinding{
			Code: "external-name", Severity: diagInfo,
			Title:  "ExternalName: DNS answers with a CNAME to " + svc.Spec.ExternalName,
			Detail: "An ExternalName Service has no selector, pods or endpoints; traffic goes to the external name.",
		})
		return d
	}
	d.Findings = append(d.Findings, specFindings(&svc.Spec)...)
	ownSlices := serviceSlices(in.Slices, svc.Name)
	d.addEndpoints(in.SlicesErr, ownSlices)
	if len(svc.Spec.Selector) == 0 {
		d.addSelectorless(svc, in.SlicesErr, ownSlices)
		return d
	}
	d.addSelection(in, ownSlices)
	return d
}

// specFindings explains headless and publishNotReadyAddresses Services.
func specFindings(spec *corev1.ServiceSpec) []diagFinding {
	var out []diagFinding
	if spec.ClusterIP == corev1.ClusterIPNone {
		out = append(out, diagFinding{
			Code: "headless", Severity: diagInfo,
			Title:  "Headless: no virtual IP, DNS returns the endpoint addresses",
			Detail: "clusterIP is None, so kube-proxy does not balance this Service; clients resolve the pod addresses directly.",
		})
	}
	if spec.PublishNotReadyAddresses {
		out = append(out, diagFinding{
			Code: "publish-not-ready", Severity: diagInfo,
			Title:  "publishNotReadyAddresses: pods are published as ready before they pass readiness",
			Detail: "The EndpointSlice controller marks every endpoint ready, so unready pods receive traffic.",
		})
	}
	return out
}

// addEndpoints adds the endpoint summary and the slices as backends, or
// says they could not be read.
func (d *serviceDiagnosis) addEndpoints(slicesErr error, own []discoveryv1.EndpointSlice) {
	if slicesErr != nil {
		d.Summary = append(d.Summary, "endpoints: unknown")
		d.Findings = append(d.Findings, diagFinding{
			Code: "slices-unreadable", Severity: diagIncomplete,
			Title:  "EndpointSlices could not be read: " + readProblem(slicesErr),
			Detail: "Endpoint counts and the endpoint checks are left out; this is not a sign of zero backends.",
		})
		return
	}
	d.Summary = append(d.Summary, endpointSummary(countEndpoints(own)))
	for i := range own {
		s := &own[i]
		d.Backends = append(d.Backends, diagLink{GVR: endpointSlicesGVR, Namespace: s.Namespace, Name: s.Name, Evidence: sliceEvidence(s)})
	}
}

func (d *serviceDiagnosis) addSelectorless(svc *corev1.Service, slicesErr error, own []discoveryv1.EndpointSlice) {
	d.Summary = append(d.Summary, "selector: none")
	d.Findings = append(d.Findings, diagFinding{
		Code: "selectorless", Severity: diagInfo,
		Title:  "No selector: endpoints are managed outside Kubernetes",
		Detail: "The EndpointSlice controller ignores a Service without a selector; whoever manages its EndpointSlices or Endpoints decides the backends.",
	})
	if slicesErr == nil && len(own) == 0 {
		d.Findings = append(d.Findings, diagFinding{
			Code: "selectorless-empty", Severity: diagWarning,
			Title:  "No EndpointSlice names this Service",
			Detail: "A selectorless Service has backends only when something creates EndpointSlices labeled kubernetes.io/service-name=" + svc.Name + ".",
		})
	}
}

// addSelection checks the selected pods: readiness, target ports and
// their endpoints.
func (d *serviceDiagnosis) addSelection(in serviceInput, own []discoveryv1.EndpointSlice) {
	svc := in.Service
	selector := labels.SelectorFromSet(svc.Spec.Selector)
	d.Summary = append(d.Summary, "selector: "+selector.String())
	if in.PodsErr != nil {
		d.Summary = append(d.Summary, "pods: unknown")
		d.Findings = append(d.Findings, diagFinding{
			Code: "pods-unreadable", Severity: diagIncomplete,
			Title:  "Pods could not be read: " + readProblem(in.PodsErr),
			Detail: "Selection, readiness and target ports are left out; this is not a sign that no pod matches.",
		})
		return
	}
	selected, ready := selectPods(svc.Namespace, selector, in.Pods)
	d.Summary = append(d.Summary, fmt.Sprintf("pods: %d selected, %d ready", len(selected), ready))
	if len(selected) == 0 {
		d.Findings = append(d.Findings, noMatchFinding(svc, in.Pods))
		return
	}
	ports := resolvePorts(svc.Spec.Ports, selected)
	d.Findings = append(d.Findings, ports.findings...)
	if ports.summary != "" {
		d.Summary = append(d.Summary, ports.summary)
	}
	if f, ok := unreadyFinding(svc.Spec.PublishNotReadyAddresses, selected); ok {
		d.Findings = append(d.Findings, f)
	}
	if in.SlicesErr == nil {
		d.Findings = append(d.Findings, endpointFindings(svc, selected, in.Pods, own)...)
	}
	podBackends := make([]diagLink, 0, len(selected))
	for _, p := range selected {
		ev := podReadiness(p)
		if pe := ports.perPod[p.Name]; pe != "" {
			ev += "; " + pe
		}
		podBackends = append(podBackends, diagLink{GVR: podGVR, Namespace: p.Namespace, Name: p.Name, Evidence: ev})
	}
	d.Backends = append(podBackends, d.Backends...)
}

// selectPods returns the pods the selector picks, by name, and how many of
// them are ready. Finished pods are never endpoints.
func selectPods(namespace string, selector labels.Selector, pods []corev1.Pod) ([]*corev1.Pod, int) {
	var selected []*corev1.Pod
	ready := 0
	for i := range pods {
		p := &pods[i]
		if p.Namespace != namespace || !selector.Matches(labels.Set(p.Labels)) || podTerminal(p) {
			continue
		}
		selected = append(selected, p)
		if podReady(p) {
			ready++
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Name < selected[j].Name })
	return selected, ready
}

func sortFindings(fs []diagFinding) {
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].Severity > fs[j].Severity })
}

func serviceTypeSummary(svc *corev1.Service) string {
	t := string(svc.Spec.Type)
	if t == "" {
		t = string(corev1.ServiceTypeClusterIP)
	}
	parts := []string{"type " + t}
	if len(svc.Spec.IPFamilies) > 0 {
		fams := make([]string, 0, len(svc.Spec.IPFamilies))
		for _, f := range svc.Spec.IPFamilies {
			fams = append(fams, string(f))
		}
		fam := "families " + strings.Join(fams, ", ")
		if svc.Spec.IPFamilyPolicy != nil {
			fam += " (" + string(*svc.Spec.IPFamilyPolicy) + ")"
		}
		parts = append(parts, fam)
	}
	return strings.Join(parts, ", ")
}

// serviceSlices returns the slices the standard label ties to the Service.
func serviceSlices(all []discoveryv1.EndpointSlice, service string) []discoveryv1.EndpointSlice {
	var out []discoveryv1.EndpointSlice
	for i := range all {
		if all[i].Labels[discoveryv1.LabelServiceName] == service {
			out = append(out, all[i])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Endpoint conditions as the API defines them: a nil ready is ready, a nil
// serving is the same as ready, a nil terminating is not terminating.
func endpointReady(e discoveryv1.Endpoint) bool {
	return e.Conditions.Ready == nil || *e.Conditions.Ready
}

func endpointServing(e discoveryv1.Endpoint) bool {
	if e.Conditions.Serving == nil {
		return endpointReady(e)
	}
	return *e.Conditions.Serving
}

func endpointTerminating(e discoveryv1.Endpoint) bool {
	return e.Conditions.Terminating != nil && *e.Conditions.Terminating
}

func countEndpoints(sl []discoveryv1.EndpointSlice) endpointCounts {
	c := endpointCounts{Slices: len(sl)}
	for _, s := range sl {
		if t := string(s.AddressType); !slices.Contains(c.AddressTypes, t) {
			c.AddressTypes = append(c.AddressTypes, t)
		}
		for _, e := range s.Endpoints {
			c.Endpoints++
			if endpointReady(e) {
				c.Ready++
			}
			if endpointServing(e) {
				c.Serving++
			}
			if endpointTerminating(e) {
				c.Terminating++
			}
		}
	}
	sort.Strings(c.AddressTypes)
	return c
}

func endpointSummary(c endpointCounts) string {
	s := fmt.Sprintf("endpoints: %d in %d slices, %d ready, %d serving, %d terminating", c.Endpoints, c.Slices, c.Ready, c.Serving, c.Terminating)
	if len(c.AddressTypes) > 0 {
		s += ", " + strings.Join(c.AddressTypes, ", ")
	}
	return s
}

func sliceEvidence(s *discoveryv1.EndpointSlice) string {
	c := countEndpoints([]discoveryv1.EndpointSlice{*s})
	return fmt.Sprintf("%s, %d endpoints, %d ready", s.AddressType, c.Endpoints, c.Ready)
}

func podReady(p *corev1.Pod) bool {
	c := podCondition(p, corev1.PodReady)
	return c != nil && c.Status == corev1.ConditionTrue
}

// podReadiness is the readiness evidence of one pod: its Ready condition and
// the containers that are not ready.
func podReadiness(p *corev1.Pod) string {
	if p.DeletionTimestamp != nil {
		return "terminating"
	}
	if podReady(p) {
		return "ready"
	}
	parts := []string{"not ready"}
	if c := podCondition(p, corev1.PodReady); c != nil && c.Reason != "" {
		parts[0] += " (" + c.Reason + ")"
	}
	if p.Status.PodIP == "" {
		parts = append(parts, "no pod IP")
	}
	for _, s := range p.Status.ContainerStatuses {
		if s.Ready {
			continue
		}
		state := "not ready"
		switch {
		case s.State.Waiting != nil:
			state = "waiting " + s.State.Waiting.Reason
		case s.State.Terminated != nil:
			state = "terminated " + s.State.Terminated.Reason
		case s.State.Running != nil:
			state = "running, readiness failing"
		}
		if s.RestartCount > 0 {
			state += fmt.Sprintf(", %d restarts", s.RestartCount)
		}
		parts = append(parts, s.Name+": "+state)
	}
	return strings.Join(parts, "; ")
}

// noMatchFinding explains an empty selection and lists the pods that share
// a selector key, with their values, so a typo in the selector or the labels
// is easy to see.
func noMatchFinding(svc *corev1.Service, pods []corev1.Pod) diagFinding {
	sel := labels.SelectorFromSet(svc.Spec.Selector).String()
	f := diagFinding{
		Code: "no-pods", Severity: diagError,
		Title:  "No running pod matches the selector " + sel,
		Detail: "The Service has no backends. Compare the selector with the labels of the pods below (pods that share a selector key).",
	}
	keys := make([]string, 0, len(svc.Spec.Selector))
	for k := range svc.Spec.Selector {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	const maxCandidates = 10
	for i := range pods {
		p := &pods[i]
		if p.Namespace != svc.Namespace {
			continue
		}
		var have []string
		for _, k := range keys {
			if v, ok := p.Labels[k]; ok {
				have = append(have, k+"="+v)
			}
		}
		if len(have) == 0 {
			continue
		}
		ev := "labels " + strings.Join(have, ", ")
		if podTerminal(p) {
			ev += " (" + string(p.Status.Phase) + ", not selected)"
		}
		f.Links = append(f.Links, diagLink{GVR: podGVR, Namespace: p.Namespace, Name: p.Name, Evidence: ev})
		if len(f.Links) == maxCandidates {
			break
		}
	}
	if len(f.Links) == 0 {
		f.Detail = "The Service has no backends, and no pod in " + svc.Namespace + " has any of the label keys " + strings.Join(keys, ", ") + "."
	}
	return f
}

func unreadyFinding(publishNotReady bool, selected []*corev1.Pod) (diagFinding, bool) {
	f := diagFinding{Code: "pods-not-ready", Severity: diagWarning}
	for _, p := range selected {
		if p.DeletionTimestamp != nil || podReady(p) {
			continue
		}
		f.Links = append(f.Links, diagLink{GVR: podGVR, Namespace: p.Namespace, Name: p.Name, Evidence: podReadiness(p)})
	}
	if len(f.Links) == 0 {
		return f, false
	}
	f.Title = fmt.Sprintf("%d of %d selected pods are not ready", len(f.Links), len(selected))
	f.Detail = "Unready pods are published with ready=false and get no traffic from kube-proxy."
	if publishNotReady {
		f.Severity = diagInfo
		f.Detail = "publishNotReadyAddresses is set, so these pods are published as ready and receive traffic."
	}
	if len(f.Links) == len(selected) && !publishNotReady {
		f.Severity = diagError
		f.Detail = "No selected pod is ready, so the Service has no ready backend."
	}
	return f, true
}

// portResolution is the targetPort of every Service port resolved on every
// selected pod.
type portResolution struct {
	summary  string
	perPod   map[string]string // pod name -> "http=8080/TCP, ..."
	findings []diagFinding
}

// resolvePorts resolves each targetPort per pod, as the EndpointSlice
// controller does: a number is used as is, a name is looked up in that
// pod's container ports with the same protocol. Pods of one Service may
// declare different ports, so each pod is checked on its own.
func resolvePorts(ports []corev1.ServicePort, pods []*corev1.Pod) portResolution {
	r := portResolution{perPod: make(map[string]string)}
	var summary []string
	for _, sp := range ports {
		proto := sp.Protocol
		if proto == "" {
			proto = corev1.ProtocolTCP
		}
		label := strconv.Itoa(int(sp.Port))
		if sp.Name != "" {
			label = sp.Name + " " + label
		}
		tp := sp.TargetPort
		if tp.Type == intstr.Int || tp.StrVal == "" {
			n := tp.IntValue()
			if n == 0 {
				n = int(sp.Port)
			}
			summary = append(summary, fmt.Sprintf("%s -> %d/%s", label, n, proto))
			continue
		}
		summary = append(summary, fmt.Sprintf("%s -> %s/%s", label, tp.StrVal, proto))
		var missing []diagLink
		for _, p := range pods {
			n, ok := findContainerPort(p, tp.StrVal, proto)
			if !ok {
				missing = append(missing, diagLink{GVR: podGVR, Namespace: p.Namespace, Name: p.Name, Evidence: "no container port named " + tp.StrVal + "/" + string(proto)})
				appendPortNote(r.perPod, p.Name, tp.StrVal+" unresolved")
				continue
			}
			appendPortNote(r.perPod, p.Name, fmt.Sprintf("%s=%d", tp.StrVal, n))
		}
		if len(missing) > 0 {
			sev := diagWarning
			if len(missing) == len(pods) {
				sev = diagError
			}
			r.findings = append(r.findings, diagFinding{
				Code: "target-port-" + tp.StrVal, Severity: sev,
				Title:  fmt.Sprintf("targetPort %s does not resolve on %d of %d pods", tp.StrVal, len(missing), len(pods)),
				Detail: "These pods get no endpoint port for " + label + ", so traffic to that port does not reach them.",
				Links:  missing,
			})
		}
	}
	if len(summary) > 0 {
		r.summary = "ports: " + strings.Join(summary, ", ")
	}
	return r
}

func appendPortNote(m map[string]string, pod, note string) {
	if m[pod] != "" {
		m[pod] += ", "
	}
	m[pod] += note
}

func findContainerPort(p *corev1.Pod, name string, proto corev1.Protocol) (int32, bool) {
	for _, c := range p.Spec.Containers {
		for _, cp := range c.Ports {
			cproto := cp.Protocol
			if cproto == "" {
				cproto = corev1.ProtocolTCP
			}
			if cp.Name == name && cproto == proto {
				return cp.ContainerPort, true
			}
		}
	}
	return 0, false
}

// endpointFindings compares the selected pods with the endpoints: a pod
// with an IP that no slice lists, an endpoint whose pod is gone, was
// replaced or is no longer selected, and an IP family without a slice.
func endpointFindings(svc *corev1.Service, selected []*corev1.Pod, all []corev1.Pod, own []discoveryv1.EndpointSlice) []diagFinding {
	listed, stale := matchEndpoints(svc.Namespace, selected, all, own)
	var out []diagFinding
	var missing []diagLink
	for _, p := range selected {
		if p.Status.PodIP != "" && !listed[p.Name] {
			missing = append(missing, diagLink{GVR: podGVR, Namespace: p.Namespace, Name: p.Name, Evidence: "IP " + p.Status.PodIP + ", in no EndpointSlice"})
		}
	}
	if len(missing) > 0 {
		out = append(out, diagFinding{
			Code: "missing-endpoints", Severity: diagWarning,
			Title:  fmt.Sprintf("%d selected pods with an IP are in no EndpointSlice", len(missing)),
			Detail: "The EndpointSlice controller may not have caught up, or a targetPort does not resolve on them.",
			Links:  missing,
		})
	}
	if len(stale) > 0 {
		out = append(out, diagFinding{
			Code: "stale-endpoints", Severity: diagWarning,
			Title:  fmt.Sprintf("%d endpoints point to pods that are not selected", len(stale)),
			Detail: "Stale endpoints until the EndpointSlice controller updates the slices.",
			Links:  stale,
		})
	}
	// Without any slice the missing endpoints say it all.
	if len(own) > 0 {
		out = append(out, familyFindings(svc, selected, own)...)
	}
	return out
}

// matchEndpoints returns the pod names the slices list and the endpoints
// whose pod is gone, replaced or not selected. Endpoints without a pod
// targetRef are not matched.
func matchEndpoints(namespace string, selected []*corev1.Pod, all []corev1.Pod, own []discoveryv1.EndpointSlice) (map[string]bool, []diagLink) {
	exists := make(map[string]bool, len(all))
	for i := range all {
		if all[i].Namespace == namespace {
			exists[all[i].Name] = true
		}
	}
	byName := make(map[string]*corev1.Pod, len(selected))
	for _, p := range selected {
		byName[p.Name] = p
	}
	listed := make(map[string]bool)
	var stale []diagLink
	for _, s := range own {
		for _, e := range s.Endpoints {
			ref := e.TargetRef
			if ref == nil || ref.Kind != "Pod" {
				continue
			}
			listed[ref.Name] = true
			link := diagLink{GVR: podGVR, Namespace: s.Namespace, Name: ref.Name, Evidence: "listed in " + s.Name}
			p, ok := byName[ref.Name]
			switch {
			case !ok && !exists[ref.Name]:
				link.Problem = problemNotFound
			case !ok:
				link.Evidence += " but not selected (labels changed or the pod finished)"
			case ref.UID != "" && ref.UID != p.UID:
				link.Evidence += " with another UID (the pod was replaced)"
			default:
				continue
			}
			stale = append(stale, link)
		}
	}
	return listed, stale
}

// familyFindings reports an IP family of the Service without a slice of
// that address type while a selected pod has an address of the family.
func familyFindings(svc *corev1.Service, selected []*corev1.Pod, own []discoveryv1.EndpointSlice) []diagFinding {
	have := make(map[discoveryv1.AddressType]bool)
	for _, s := range own {
		have[s.AddressType] = true
	}
	var out []diagFinding
	for _, fam := range svc.Spec.IPFamilies {
		at := discoveryv1.AddressTypeIPv4
		if fam == corev1.IPv6Protocol {
			at = discoveryv1.AddressTypeIPv6
		}
		if have[at] || !anyPodHasFamily(selected, fam) {
			continue
		}
		out = append(out, diagFinding{
			Code: "family-" + string(at), Severity: diagWarning,
			Title:  "No " + string(at) + " EndpointSlice although selected pods have " + string(fam) + " addresses",
			Detail: "Clients of the Service's " + string(fam) + " address get no backend.",
		})
	}
	return out
}

func anyPodHasFamily(pods []*corev1.Pod, fam corev1.IPFamily) bool {
	for _, p := range pods {
		for _, ip := range p.Status.PodIPs {
			if (fam == corev1.IPv6Protocol) == netutils.IsIPv6String(ip.IP) {
				return true
			}
		}
	}
	return false
}
