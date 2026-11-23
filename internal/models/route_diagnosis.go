package models

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Routing diagnosis: the path from an Ingress or HTTPRoute to its parents
// and backend Services, as the objects declare it. See
// docs/adr/0260-routing-diagnosis.md.

var ingressClassGVR = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingressclasses"}

// routeDiagnosisNote says what a routing diagnosis does not prove.
const routeDiagnosisNote = "Configuration only: kc reads the objects; it does not test DNS, TLS handshakes, the controller's own rules (annotations, policies) or whether traffic arrives."

// errTypeNotServed means the cluster does not serve the API type a check
// needs, such as Gateway API ReferenceGrants.
var errTypeNotServed = errors.New("the type is not served by this cluster")

// routeLookups reads what a routing diagnosis needs. Every method
// distinguishes "does not exist" (found false, nil error) from "could not be
// read" (an error): forbidden, not served or unavailable. Only the first is
// a configuration problem.
type routeLookups interface {
	// services returns the Services of a namespace.
	services(namespace string) ([]corev1.Service, error)
	// secretExists reads only the identity of a Secret, never its data.
	secretExists(namespace, name string) (bool, error)
}

// lookupProblem names why a source could not be read.
func lookupProblem(err error) string {
	if errors.Is(err, errTypeNotServed) {
		return "not served by the cluster"
	}
	return readProblem(err)
}

// serviceIndex finds Services by namespace and name, reading each
// namespace once.
type serviceIndex struct {
	look routeLookups
	byNS map[string]serviceList
}

type serviceList struct {
	items []corev1.Service
	err   error
}

func newServiceIndex(look routeLookups) *serviceIndex {
	return &serviceIndex{look: look, byNS: make(map[string]serviceList)}
}

// get returns the Service, or nil when it does not exist; err says that the
// namespace's Services could not be read.
func (s *serviceIndex) get(namespace, name string) (*corev1.Service, error) {
	l, ok := s.byNS[namespace]
	if !ok {
		items, err := s.look.services(namespace)
		l = serviceList{items: items, err: err}
		s.byNS[namespace] = l
	}
	if l.err != nil {
		return nil, l.err
	}
	for i := range l.items {
		if l.items[i].Namespace == namespace && l.items[i].Name == name {
			return &l.items[i], nil
		}
	}
	return nil, nil
}

// servicePortProblem says why port (a number, or a name when number is 0)
// is not a port of svc; "" when it is.
func servicePortProblem(svc *corev1.Service, number int32, name string) string {
	var have []string
	for _, p := range svc.Spec.Ports {
		if (number != 0 && p.Port == number) || (number == 0 && name != "" && p.Name == name) {
			return ""
		}
		label := strconv.Itoa(int(p.Port))
		if p.Name != "" {
			label = p.Name + "/" + label
		}
		have = append(have, label)
	}
	want := name
	if number != 0 {
		want = strconv.Itoa(int(number))
	}
	if len(have) == 0 {
		return fmt.Sprintf("Service %s has no ports, so port %s cannot be reached", svc.Name, want)
	}
	return fmt.Sprintf("Service %s has no port %s (it has %s)", svc.Name, want, strings.Join(have, ", "))
}

// backendCheck checks one Service backend and returns the finding when it
// is broken, plus the backend link when the Service exists.
type backendCheck struct {
	code      string // stable part of the finding code
	via       string // where the reference is, such as `rule 1 "/api"`
	namespace string
	name      string
	port      int32
	portName  string
	evidence  string
}

func (c backendCheck) run(idx *serviceIndex, unreadable map[string]bool) (*diagFinding, *diagLink) {
	svc, err := idx.get(c.namespace, c.name)
	link := diagLink{GVR: serviceGVR, Namespace: c.namespace, Name: c.name, Evidence: c.evidence}
	switch {
	case err != nil:
		// One incomplete finding per namespace, not one per backend.
		key := c.namespace
		if unreadable[key] {
			return nil, nil
		}
		unreadable[key] = true
		return &diagFinding{
			Code: "services-unreadable/" + c.namespace, Severity: diagIncomplete,
			Title:  fmt.Sprintf("Services in %s could not be read: %s", c.namespace, lookupProblem(err)),
			Detail: "The backend checks for this namespace are left out; this is not a sign that a backend is missing.",
		}, nil
	case svc == nil:
		link.Problem = problemNotFound
		return &diagFinding{
			Code: "backend-missing/" + c.code, Severity: diagError,
			Title:  fmt.Sprintf("Backend Service %s/%s does not exist (%s)", c.namespace, c.name, c.via),
			Detail: "Requests that match this path have no backend; controllers usually answer them with 503 or 404.",
			Links:  []diagLink{link},
			Next:   "Check the Service name and namespace, or create the Service.",
		}, nil
	}
	if p := servicePortProblem(svc, c.port, c.portName); p != "" {
		return &diagFinding{
			Code: "backend-port/" + c.code, Severity: diagError,
			Title:  fmt.Sprintf("%s (%s)", p, c.via),
			Detail: "The route names a port the Service does not expose, so the controller cannot pick endpoints for it.",
			Links:  []diagLink{link},
		}, &link
	}
	return nil, &link
}

// --- Ingress ---

// ingressLookups adds what an Ingress diagnosis reads.
type ingressLookups interface {
	routeLookups
	// ingressClasses lists the cluster's IngressClasses (cluster-scoped).
	ingressClasses() ([]networkingv1.IngressClass, error)
}

const (
	ingressClassAnnotation   = "kubernetes.io/ingress.class"
	defaultIngressClassLabel = "ingressclass.kubernetes.io/is-default-class"
)

// diagnoseIngress checks the class, rules, backends and TLS references of
// ing. Services are those its backends name; Secrets are checked for
// existence only.
func diagnoseIngress(ing *networkingv1.Ingress, look ingressLookups) (d diagReport) {
	if ing == nil {
		return d
	}
	defer func() { sortFindings(d.Findings) }()
	d.Note = routeDiagnosisNote
	d.addIngressClass(ing, look)
	d.addIngressAddress(ing)

	idx := newServiceIndex(look)
	unreadable := make(map[string]bool)
	ruleHosts := make(map[string]bool)
	add := func(b *networkingv1.IngressBackend, code, via string) {
		if b == nil {
			return
		}
		if b.Resource != nil {
			d.Findings = append(d.Findings, diagFinding{
				Code: "unsupported-backend/" + code, Severity: diagInfo,
				Title:  fmt.Sprintf("Resource backend %s %s is not checked (%s)", b.Resource.Kind, b.Resource.Name, via),
				Detail: "kc checks Service backends only; a resource backend is interpreted by the Ingress controller.",
			})
			return
		}
		if b.Service == nil {
			return
		}
		c := backendCheck{code: code, via: via, namespace: ing.Namespace, name: b.Service.Name,
			port: b.Service.Port.Number, portName: b.Service.Port.Name}
		c.evidence = via + " -> port " + ingressPort(b.Service.Port)
		f, link := c.run(idx, unreadable)
		if f != nil {
			d.Findings = append(d.Findings, *f)
		}
		if link != nil {
			d.Backends = append(d.Backends, *link)
		}
	}

	rules := 0
	if b := ing.Spec.DefaultBackend; b != nil {
		d.Summary = append(d.Summary, "default: "+ingressBackendText(b))
		add(b, "default", "default backend")
	}
	for i, r := range ing.Spec.Rules {
		host := r.Host
		if host == "" {
			host = "*"
		}
		ruleHosts[r.Host] = true
		if r.HTTP == nil {
			d.Summary = append(d.Summary, fmt.Sprintf("rule %d: %s (no paths)", i+1, host))
			continue
		}
		for j, p := range r.HTTP.Paths {
			rules++
			pt := "ImplementationSpecific"
			if p.PathType != nil {
				pt = string(*p.PathType)
			}
			path := p.Path
			if path == "" {
				path = "/"
			}
			d.Summary = append(d.Summary, fmt.Sprintf("rule %d.%d: %s%s (%s) -> %s", i+1, j+1, host, path, pt, ingressBackendText(&p.Backend)))
			add(&p.Backend, fmt.Sprintf("%d.%d", i+1, j+1), fmt.Sprintf("rule %d.%d %s%s", i+1, j+1, host, path))
		}
	}
	if rules == 0 && ing.Spec.DefaultBackend == nil {
		d.Findings = append(d.Findings, diagFinding{
			Code: "no-backends", Severity: diagError,
			Title:  "No rule path and no default backend",
			Detail: "An Ingress without paths or a default backend routes nothing to the cluster.",
		})
	}
	d.addIngressTLS(ing, look, ruleHosts)
	d.Backends = uniqueLinks(d.Backends)
	return d
}

func ingressPort(p networkingv1.ServiceBackendPort) string {
	if p.Number != 0 {
		return strconv.Itoa(int(p.Number))
	}
	if p.Name != "" {
		return p.Name
	}
	return "(none)"
}

func ingressBackendText(b *networkingv1.IngressBackend) string {
	switch {
	case b.Service != nil:
		return "service " + b.Service.Name + ":" + ingressPort(b.Service.Port)
	case b.Resource != nil:
		return "resource " + b.Resource.Kind + " " + b.Resource.Name
	default:
		return "(no backend)"
	}
}

// addIngressClass checks which IngressClass claims ing.
func (d *diagReport) addIngressClass(ing *networkingv1.Ingress, look ingressLookups) {
	class := ""
	if ing.Spec.IngressClassName != nil {
		class = *ing.Spec.IngressClassName
	}
	annotated := ing.Annotations[ingressClassAnnotation]
	if class == "" && annotated != "" {
		d.Summary = append(d.Summary, "class: "+annotated+" (annotation)")
		d.Findings = append(d.Findings, diagFinding{
			Code: "class-annotation", Severity: diagInfo,
			Title:  "The class is set with the deprecated annotation " + ingressClassAnnotation + "=" + annotated,
			Detail: "Controllers that still read the annotation claim this Ingress; spec.ingressClassName replaces it.",
		})
		return
	}
	classes, err := look.ingressClasses()
	if err != nil {
		if class == "" {
			d.Summary = append(d.Summary, "class: none set")
		} else {
			d.Summary = append(d.Summary, "class: "+class)
		}
		d.Findings = append(d.Findings, diagFinding{
			Code: "classes-unreadable", Severity: diagIncomplete,
			Title:  "IngressClasses could not be read: " + lookupProblem(err),
			Detail: "Whether a controller claims this Ingress is not checked.",
		})
		return
	}
	if class == "" {
		var defaults []string
		for _, c := range classes {
			if c.Annotations[defaultIngressClassLabel] == "true" {
				defaults = append(defaults, c.Name)
			}
		}
		switch len(defaults) {
		case 0:
			d.Summary = append(d.Summary, "class: none set, no default class")
			d.Findings = append(d.Findings, diagFinding{
				Code: "no-class", Severity: diagWarning,
				Title:  "No IngressClass is set and none is marked default",
				Detail: "Only a controller that claims Ingresses without a class serves this one; most do not.",
				Next:   "Set spec.ingressClassName to one of the cluster's IngressClasses.",
			})
		case 1:
			d.Summary = append(d.Summary, "class: "+defaults[0]+" (default)")
		default:
			d.Summary = append(d.Summary, "class: none set, several defaults")
			d.Findings = append(d.Findings, diagFinding{
				Code: "several-default-classes", Severity: diagWarning,
				Title:  "No IngressClass is set and several are marked default: " + strings.Join(defaults, ", "),
				Detail: "The API server does not default the class when more than one is marked default.",
			})
		}
		return
	}
	for _, c := range classes {
		if c.Name == class {
			d.Summary = append(d.Summary, "class: "+class+" (controller "+c.Spec.Controller+")")
			return
		}
	}
	d.Summary = append(d.Summary, "class: "+class+" (missing)")
	d.Findings = append(d.Findings, diagFinding{
		Code: "class-missing", Severity: diagError,
		Title:  "IngressClass " + class + " does not exist",
		Detail: "No controller claims an Ingress whose class does not exist, so nothing routes it.",
		Links:  []diagLink{{GVR: ingressClassGVR, Name: class, Evidence: "spec.ingressClassName", Problem: problemNotFound}},
	})
}

// addIngressAddress reports the address the controller published.
func (d *diagReport) addIngressAddress(ing *networkingv1.Ingress) {
	var addrs []string
	for _, lb := range ing.Status.LoadBalancer.Ingress {
		switch {
		case lb.Hostname != "":
			addrs = append(addrs, lb.Hostname)
		case lb.IP != "":
			addrs = append(addrs, lb.IP)
		}
	}
	if len(addrs) > 0 {
		d.Summary = append(d.Summary, "address: "+strings.Join(addrs, ", "))
		return
	}
	d.Summary = append(d.Summary, "address: none")
	d.Findings = append(d.Findings, diagFinding{
		Code: "no-address", Severity: diagWarning,
		Title:  "No address in status.loadBalancer",
		Detail: "The controller has not published an address. Some controllers never do; for most it means the Ingress was not admitted yet.",
	})
}

// addIngressTLS checks that the TLS Secrets exist and that their hosts are
// routed.
func (d *diagReport) addIngressTLS(ing *networkingv1.Ingress, look routeLookups, ruleHosts map[string]bool) {
	for i, t := range ing.Spec.TLS {
		code := strconv.Itoa(i + 1)
		hosts := strings.Join(t.Hosts, ", ")
		if hosts == "" {
			hosts = "(no hosts)"
		}
		if t.SecretName == "" {
			d.Summary = append(d.Summary, fmt.Sprintf("tls %d: %s, no Secret (controller default certificate)", i+1, hosts))
		} else {
			d.Summary = append(d.Summary, fmt.Sprintf("tls %d: %s, Secret %s", i+1, hosts, t.SecretName))
			if f, ok := secretFinding("tls/"+code, ing.Namespace, t.SecretName, "spec.tls["+strconv.Itoa(i)+"]", look); ok {
				d.Findings = append(d.Findings, f)
			}
		}
		for _, h := range t.Hosts {
			if !ruleHosts[h] && !ruleHosts[""] {
				d.Findings = append(d.Findings, diagFinding{
					Code: "tls-host-unrouted/" + h, Severity: diagInfo,
					Title:  "TLS host " + h + " has no rule",
					Detail: "The certificate is offered for this host, but no rule routes it.",
				})
			}
		}
	}
}

// secretFinding checks that a referenced TLS Secret exists. It reads the
// Secret's identity only.
func secretFinding(code, namespace, name, via string, look routeLookups) (diagFinding, bool) {
	found, err := look.secretExists(namespace, name)
	link := diagLink{GVR: secretGVR, Namespace: namespace, Name: name, Evidence: via}
	switch {
	case err != nil:
		return diagFinding{
			Code: "secret-unreadable/" + code, Severity: diagIncomplete,
			Title:  fmt.Sprintf("TLS Secret %s/%s could not be checked: %s", namespace, name, lookupProblem(err)),
			Detail: "kc reads only whether the Secret exists, never its data.",
		}, true
	case !found:
		link.Problem = problemNotFound
		return diagFinding{
			Code: "secret-missing/" + code, Severity: diagError,
			Title:  fmt.Sprintf("TLS Secret %s/%s does not exist (%s)", namespace, name, via),
			Detail: "Without the Secret the controller cannot serve this certificate; most fall back to a default certificate or refuse the listener.",
			Links:  []diagLink{link},
		}, true
	}
	return diagFinding{}, false
}

// uniqueLinks drops repeated links to the same object, joining their
// evidence.
func uniqueLinks(links []diagLink) []diagLink {
	var out []diagLink
	index := make(map[string]int)
	for _, l := range links {
		key := l.GVR.String() + "/" + l.Namespace + "/" + l.Name
		if i, ok := index[key]; ok {
			if !strings.Contains(out[i].Evidence, l.Evidence) {
				out[i].Evidence += "; " + l.Evidence
			}
			continue
		}
		index[key] = len(out)
		out = append(out, l)
	}
	return out
}

// notFoundAsAbsent turns a NotFound error into (false, nil).
func notFoundAsAbsent(err error) (bool, error) {
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}
