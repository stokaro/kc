package models

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Gateway API types, as far as the routing diagnosis reads them. kc does
// not depend on sigs.k8s.io/gateway-api: the fields below are stable in v1
// and v1beta1, and the objects are decoded from the cache's unstructured
// form.

const gatewayGroup = "gateway.networking.k8s.io"

type gwParentRef struct {
	Group       *string `json:"group,omitempty"`
	Kind        *string `json:"kind,omitempty"`
	Namespace   *string `json:"namespace,omitempty"`
	Name        string  `json:"name"`
	SectionName *string `json:"sectionName,omitempty"`
	Port        *int32  `json:"port,omitempty"`
}

type gwObjectRef struct {
	Group     *string `json:"group,omitempty"`
	Kind      *string `json:"kind,omitempty"`
	Name      string  `json:"name"`
	Namespace *string `json:"namespace,omitempty"`
}

type gwBackendRef struct {
	gwObjectRef
	Port    *int32     `json:"port,omitempty"`
	Weight  *int32     `json:"weight,omitempty"`
	Filters []gwFilter `json:"filters,omitempty"`
}

type gwFilter struct {
	Type         string `json:"type"`
	ExtensionRef *struct {
		Group string `json:"group"`
		Kind  string `json:"kind"`
		Name  string `json:"name"`
	} `json:"extensionRef,omitempty"`
}

type gwPathMatch struct {
	Type  *string `json:"type,omitempty"`
	Value *string `json:"value,omitempty"`
}

type gwRouteMatch struct {
	Path    *gwPathMatch `json:"path,omitempty"`
	Method  *string      `json:"method,omitempty"`
	Headers []struct {
		Name string `json:"name"`
	} `json:"headers,omitempty"`
	QueryParams []struct {
		Name string `json:"name"`
	} `json:"queryParams,omitempty"`
}

type gwRouteRule struct {
	Name        *string        `json:"name,omitempty"`
	Matches     []gwRouteMatch `json:"matches,omitempty"`
	Filters     []gwFilter     `json:"filters,omitempty"`
	BackendRefs []gwBackendRef `json:"backendRefs,omitempty"`
}

type gwRouteParentStatus struct {
	ParentRef      gwParentRef        `json:"parentRef"`
	ControllerName string             `json:"controllerName"`
	Conditions     []metav1.Condition `json:"conditions,omitempty"`
}

type httpRoute struct {
	metav1.ObjectMeta `json:"metadata"`
	Spec              struct {
		ParentRefs []gwParentRef `json:"parentRefs,omitempty"`
		Hostnames  []string      `json:"hostnames,omitempty"`
		Rules      []gwRouteRule `json:"rules,omitempty"`
	} `json:"spec"`
	Status struct {
		Parents []gwRouteParentStatus `json:"parents,omitempty"`
	} `json:"status"`
}

type gwListener struct {
	Name          string           `json:"name"`
	Hostname      *string          `json:"hostname,omitempty"`
	Port          int32            `json:"port"`
	Protocol      string           `json:"protocol"`
	TLS           *gwListenerTLS   `json:"tls,omitempty"`
	AllowedRoutes *gwAllowedRoutes `json:"allowedRoutes,omitempty"`
}

type gwListenerTLS struct {
	Mode            *string       `json:"mode,omitempty"`
	CertificateRefs []gwObjectRef `json:"certificateRefs,omitempty"`
}

type gwAllowedRoutes struct {
	Namespaces *gwRouteNamespaces `json:"namespaces,omitempty"`
	Kinds      []gwRouteKind      `json:"kinds,omitempty"`
}

type gwRouteNamespaces struct {
	From     *string               `json:"from,omitempty"`
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

type gwRouteKind struct {
	Group *string `json:"group,omitempty"`
	Kind  string  `json:"kind"`
}

type gatewayObj struct {
	metav1.ObjectMeta `json:"metadata"`
	Spec              struct {
		GatewayClassName string       `json:"gatewayClassName"`
		Listeners        []gwListener `json:"listeners"`
	} `json:"spec"`
	Status struct {
		Conditions []metav1.Condition `json:"conditions,omitempty"`
		Listeners  []struct {
			Name           string             `json:"name"`
			AttachedRoutes int32              `json:"attachedRoutes"`
			Conditions     []metav1.Condition `json:"conditions,omitempty"`
		} `json:"listeners,omitempty"`
	} `json:"status"`
}

type referenceGrant struct {
	metav1.ObjectMeta `json:"metadata"`
	Spec              struct {
		From []gwGrantFrom `json:"from"`
		To   []gwGrantTo   `json:"to"`
	} `json:"spec"`
}

type gwGrantFrom struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
}

type gwGrantTo struct {
	Group string  `json:"group"`
	Kind  string  `json:"kind"`
	Name  *string `json:"name,omitempty"`
}

// httpRouteLookups adds what an HTTPRoute diagnosis reads.
type httpRouteLookups interface {
	routeLookups
	// gateway returns the Gateway, or nil when it does not exist.
	gateway(namespace, name string) (*gatewayObj, error)
	// gatewayGVR is the served Gateway type, for links.
	gatewayGVR() (schema.GroupVersionResource, bool)
	// referenceGrants lists the ReferenceGrants of a namespace.
	referenceGrants(namespace string) ([]referenceGrant, error)
	// namespaceLabels returns the labels of a namespace, for listeners
	// that select route namespaces.
	namespaceLabels(namespace string) (map[string]string, error)
}

func deref[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

// diagnoseHTTPRoute checks the parents, listener attachment, status
// conditions and Service backends of rt.
func diagnoseHTTPRoute(rt *httpRoute, look httpRouteLookups) (d diagReport) {
	if rt == nil {
		return d
	}
	defer func() { sortFindings(d.Findings) }()
	d.Note = routeDiagnosisNote
	hosts := "*"
	if len(rt.Spec.Hostnames) > 0 {
		hosts = strings.Join(rt.Spec.Hostnames, ", ")
	}
	d.Summary = append(d.Summary, "hostnames: "+hosts)
	if len(rt.Spec.ParentRefs) == 0 {
		d.Findings = append(d.Findings, diagFinding{
			Code: "no-parents", Severity: diagError,
			Title:  "No parentRefs: the route is attached to no Gateway",
			Detail: "An HTTPRoute serves traffic only through the Gateways (or Services, for a mesh) it names in spec.parentRefs.",
		})
	}
	grants := newGrantIndex(look)
	for i, p := range rt.Spec.ParentRefs {
		d.addParent(rt, i, p, look, grants)
	}
	d.addRules(rt, look, grants)
	d.Backends = uniqueLinks(d.Backends)
	return d
}

func parentText(ns string, p gwParentRef) string {
	s := ns + "/" + p.Name
	if p.SectionName != nil {
		s += " listener " + *p.SectionName
	}
	if p.Port != nil {
		s += " port " + strconv.Itoa(int(*p.Port))
	}
	return s
}

// addParent checks one parentRef: the Gateway, a listener that accepts the
// route, and the status the Gateway's controller wrote for it.
func (d *diagReport) addParent(rt *httpRoute, i int, p gwParentRef, look httpRouteLookups, grants *grantIndex) {
	code := "parent/" + strconv.Itoa(i+1)
	group, kind := deref(p.Group, gatewayGroup), deref(p.Kind, "Gateway")
	ns := deref(p.Namespace, rt.Namespace)
	if group != gatewayGroup || kind != "Gateway" {
		d.Summary = append(d.Summary, fmt.Sprintf("parent %d: %s %s/%s (not checked)", i+1, kind, ns, p.Name))
		d.Findings = append(d.Findings, diagFinding{
			Code: "unsupported-parent/" + strconv.Itoa(i+1), Severity: diagInfo,
			Title:  fmt.Sprintf("Parent %s %s/%s is not checked", kind, ns, p.Name),
			Detail: "kc checks Gateway parents only; other parents (a Service in a mesh) are interpreted by their implementation.",
		})
		d.addParentStatus(rt, i, p, ns, code)
		return
	}
	d.Summary = append(d.Summary, fmt.Sprintf("parent %d: gateway %s", i+1, parentText(ns, p)))
	gw, err := look.gateway(ns, p.Name)
	gvr, served := look.gatewayGVR()
	link := diagLink{GVR: gvr, Namespace: ns, Name: p.Name, Evidence: "spec.parentRefs[" + strconv.Itoa(i) + "]"}
	switch {
	case err != nil:
		d.Findings = append(d.Findings, diagFinding{
			Code: code + "/unreadable", Severity: diagIncomplete,
			Title:  fmt.Sprintf("Gateway %s/%s could not be read: %s", ns, p.Name, lookupProblem(err)),
			Detail: "Listener attachment is not checked for this parent; this is not a sign that the Gateway is missing.",
		})
	case gw == nil:
		link.Problem = problemNotFound
		d.Findings = append(d.Findings, diagFinding{
			Code: code + "/missing", Severity: diagError,
			Title:  fmt.Sprintf("Parent Gateway %s/%s does not exist", ns, p.Name),
			Detail: "The route cannot attach to a Gateway that does not exist.",
			Links:  []diagLink{link},
		})
	default:
		if served {
			d.Backends = append(d.Backends, link)
		}
		d.addGatewayConditions(gw, code, link)
		d.addAttachment(rt, i, p, gw, look, code, link)
		d.addListenerTLS(gw, p, look, grants, code)
	}
	d.addParentStatus(rt, i, p, ns, code)
}

// addAttachment finds the listeners of gw that the parentRef selects and
// says why none of them accepts the route.
func (d *diagReport) addAttachment(rt *httpRoute, i int, p gwParentRef, gw *gatewayObj, look httpRouteLookups, code string, link diagLink) {
	var selected []gwListener
	for _, l := range gw.Spec.Listeners {
		if p.SectionName != nil && l.Name != *p.SectionName {
			continue
		}
		if p.Port != nil && l.Port != *p.Port {
			continue
		}
		selected = append(selected, l)
	}
	if len(selected) == 0 {
		d.Findings = append(d.Findings, diagFinding{
			Code: code + "/no-listener", Severity: diagError,
			Title:  fmt.Sprintf("Gateway %s/%s has no listener %s", gw.Namespace, gw.Name, strings.TrimPrefix(parentText("", p), "/"+p.Name+" ")),
			Detail: "The parentRef's sectionName or port matches no listener, so the route attaches nowhere on this Gateway.",
			Links:  []diagLink{link},
		})
		return
	}
	var accepted []string
	var refused []string
	incomplete := false
	for _, l := range selected {
		why, unknown := listenerRefuses(rt, gw, l, look)
		switch {
		case unknown != nil:
			incomplete = true
			d.Findings = append(d.Findings, diagFinding{
				Code: code + "/namespace-unreadable", Severity: diagIncomplete,
				Title:  fmt.Sprintf("Listener %s selects route namespaces by label, and namespace %s could not be read: %s", l.Name, rt.Namespace, lookupProblem(unknown)),
				Detail: "Whether this listener admits the route's namespace is not checked.",
			})
		case why == "":
			accepted = append(accepted, l.Name)
		default:
			refused = append(refused, l.Name+": "+why)
		}
	}
	if len(accepted) > 0 {
		d.Summary = append(d.Summary, fmt.Sprintf("parent %d: listeners %s accept the route", i+1, strings.Join(accepted, ", ")))
		return
	}
	if incomplete {
		return
	}
	d.Findings = append(d.Findings, diagFinding{
		Code: code + "/not-accepted", Severity: diagError,
		Title:  fmt.Sprintf("No listener of Gateway %s/%s accepts the route", gw.Namespace, gw.Name),
		Detail: strings.Join(refused, "; ") + ".",
		Links:  []diagLink{link},
	})
}

// listenerRefuses says why listener l does not accept rt ("" when it
// does). unknown is set when the route namespace's labels are needed and
// could not be read.
func listenerRefuses(rt *httpRoute, gw *gatewayObj, l gwListener, look httpRouteLookups) (why string, unknown error) {
	if l.Protocol != "HTTP" && l.Protocol != "HTTPS" && (l.AllowedRoutes == nil || len(l.AllowedRoutes.Kinds) == 0) {
		return "protocol " + l.Protocol + " does not carry HTTPRoutes", nil
	}
	if ar := l.AllowedRoutes; ar != nil && len(ar.Kinds) > 0 && !slices.ContainsFunc(ar.Kinds, func(k gwRouteKind) bool {
		return deref(k.Group, gatewayGroup) == gatewayGroup && k.Kind == "HTTPRoute"
	}) {
		return "allowedRoutes.kinds does not include HTTPRoute", nil
	}
	if why, err := namespaceRefuses(rt, gw, l, look); why != "" || err != nil {
		return why, err
	}
	if l.Hostname != nil && *l.Hostname != "" && len(rt.Spec.Hostnames) > 0 {
		for _, h := range rt.Spec.Hostnames {
			if hostnamesIntersect(*l.Hostname, h) {
				return "", nil
			}
		}
		return "hostname " + *l.Hostname + " matches none of the route's hostnames", nil
	}
	return "", nil
}

// namespaceRefuses applies allowedRoutes.namespaces to the route's
// namespace.
func namespaceRefuses(rt *httpRoute, gw *gatewayObj, l gwListener, look httpRouteLookups) (string, error) {
	from := "Same"
	var selector *metav1.LabelSelector
	if ar := l.AllowedRoutes; ar != nil && ar.Namespaces != nil {
		from = deref(ar.Namespaces.From, "Same")
		selector = ar.Namespaces.Selector
	}
	switch from {
	case "All":
	case "Selector":
		sel, err := metav1.LabelSelectorAsSelector(selector)
		if err != nil || selector == nil {
			return "allowedRoutes.namespaces.selector is invalid", nil
		}
		nsLabels, err := look.namespaceLabels(rt.Namespace)
		if err != nil {
			return "", err
		}
		if !sel.Matches(labels.Set(nsLabels)) {
			return "namespace " + rt.Namespace + " does not match allowedRoutes.namespaces.selector " + sel.String(), nil
		}
	default: // Same
		if gw.Namespace != rt.Namespace {
			return "allowedRoutes.namespaces.from is Same and the route is in " + rt.Namespace, nil
		}
	}
	return "", nil
}

// hostnamesIntersect reports whether a listener hostname and a route
// hostname can match the same request host. A leading "*." matches one or
// more labels, as HTTPRoute hostnames are defined.
func hostnamesIntersect(listener, route string) bool {
	lw, rw := strings.HasPrefix(listener, "*."), strings.HasPrefix(route, "*.")
	switch {
	case !lw && !rw:
		return strings.EqualFold(listener, route)
	case lw && !rw:
		return hasLabelSuffix(route, listener[1:])
	case !lw && rw:
		return hasLabelSuffix(listener, route[1:])
	default:
		return hasLabelSuffix(listener[1:], route[1:]) || hasLabelSuffix(route[1:], listener[1:]) ||
			strings.EqualFold(listener, route)
	}
}

// hasLabelSuffix reports whether host ends with suffix (which starts with
// "."), with at least one label before it.
func hasLabelSuffix(host, suffix string) bool {
	host, suffix = strings.ToLower(host), strings.ToLower(suffix)
	return len(host) > len(suffix) && strings.HasSuffix(host, suffix)
}

// conditionText renders a condition with its reason, time and generation.
func conditionText(c metav1.Condition, generation int64) string {
	s := fmt.Sprintf("%s=%s", c.Type, c.Status)
	if c.Reason != "" {
		s += " (" + c.Reason + ")"
	}
	if !c.LastTransitionTime.IsZero() {
		s += " since " + c.LastTransitionTime.UTC().Format(time.RFC3339)
	}
	if c.ObservedGeneration != 0 && generation != 0 && c.ObservedGeneration < generation {
		s += fmt.Sprintf(", for generation %d of %d", c.ObservedGeneration, generation)
	}
	return s
}

// conditionFindings turns the False conditions of types into errors and
// stale ones into warnings.
func conditionFindings(code, what string, conds []metav1.Condition, generation int64, types []string, links []diagLink) []diagFinding {
	var out []diagFinding
	for _, c := range conds {
		if !slices.Contains(types, c.Type) {
			continue
		}
		f := diagFinding{
			Code: code + "/" + strings.ToLower(c.Type), Observed: c.LastTransitionTime.Time,
			Links: links,
		}
		switch {
		case c.Status == metav1.ConditionFalse:
			f.Severity = diagError
			f.Title = fmt.Sprintf("%s: %s is False (%s)", what, c.Type, c.Reason)
			f.Detail = c.Message
		case c.Status == metav1.ConditionUnknown:
			f.Severity = diagWarning
			f.Title = fmt.Sprintf("%s: %s is Unknown (%s)", what, c.Type, c.Reason)
			f.Detail = c.Message
		case c.ObservedGeneration != 0 && generation != 0 && c.ObservedGeneration < generation:
			f.Severity = diagWarning
			f.Title = fmt.Sprintf("%s: %s describes generation %d, the object is at %d", what, c.Type, c.ObservedGeneration, generation)
			f.Detail = "The controller has not processed the latest change yet, or it is not running."
		default:
			continue
		}
		if f.Detail == "" {
			f.Detail = "The controller left no message."
		}
		out = append(out, f)
	}
	return out
}

func (d *diagReport) addGatewayConditions(gw *gatewayObj, code string, link diagLink) {
	var parts []string
	for _, c := range gw.Status.Conditions {
		if c.Type == "Accepted" || c.Type == "Programmed" {
			parts = append(parts, conditionText(c, gw.Generation))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, "no Accepted or Programmed condition")
	}
	d.Summary = append(d.Summary, fmt.Sprintf("gateway %s/%s (class %s): %s", gw.Namespace, gw.Name, gw.Spec.GatewayClassName, strings.Join(parts, ", ")))
	d.Findings = append(d.Findings, conditionFindings(code+"/gateway", "Gateway "+gw.Namespace+"/"+gw.Name, gw.Status.Conditions, gw.Generation, []string{"Accepted", "Programmed"}, []diagLink{link})...)
}

// addParentStatus reports the route status the parent's controller wrote.
func (d *diagReport) addParentStatus(rt *httpRoute, i int, p gwParentRef, ns, code string) {
	var st *gwRouteParentStatus
	for j := range rt.Status.Parents {
		s := &rt.Status.Parents[j]
		if s.ParentRef.Name == p.Name && deref(s.ParentRef.Namespace, rt.Namespace) == ns &&
			deref(s.ParentRef.SectionName, "") == deref(p.SectionName, "") &&
			deref(s.ParentRef.Port, 0) == deref(p.Port, 0) {
			st = s
			break
		}
	}
	if st == nil {
		d.Findings = append(d.Findings, diagFinding{
			Code: code + "/no-status", Severity: diagWarning,
			Title:  fmt.Sprintf("No controller reported status for parent %s", parentText(ns, p)),
			Detail: "Until the parent's controller accepts or rejects the route it is not known to serve it. A missing GatewayClass controller looks the same.",
		})
		return
	}
	var parts []string
	for _, c := range st.Conditions {
		parts = append(parts, conditionText(c, rt.Generation))
	}
	d.Summary = append(d.Summary, fmt.Sprintf("parent %d status (%s): %s", i+1, st.ControllerName, strings.Join(parts, ", ")))
	d.Findings = append(d.Findings, conditionFindings(code+"/route", "Route on "+parentText(ns, p), st.Conditions, rt.Generation, []string{"Accepted", "ResolvedRefs"}, nil)...)
}

// addListenerTLS checks the certificate Secrets of the listeners the
// parentRef selects.
func (d *diagReport) addListenerTLS(gw *gatewayObj, p gwParentRef, look httpRouteLookups, grants *grantIndex, code string) {
	for _, l := range gw.Spec.Listeners {
		if (p.SectionName != nil && l.Name != *p.SectionName) || (p.Port != nil && l.Port != *p.Port) || l.TLS == nil {
			continue
		}
		for k, ref := range l.TLS.CertificateRefs {
			group, kind := deref(ref.Group, ""), deref(ref.Kind, "Secret")
			ns := deref(ref.Namespace, gw.Namespace)
			via := fmt.Sprintf("listener %s certificateRefs[%d]", l.Name, k)
			fcode := fmt.Sprintf("%s/tls/%s/%d", code, l.Name, k)
			if group != "" || kind != "Secret" {
				d.Findings = append(d.Findings, diagFinding{
					Code: fcode + "/unsupported", Severity: diagInfo,
					Title:  fmt.Sprintf("Certificate %s %s/%s is not checked (%s)", kind, ns, ref.Name, via),
					Detail: "kc checks Secret certificate references only.",
				})
				continue
			}
			if ns != gw.Namespace {
				if f, ok := grants.missing(fcode, "Gateway", gw.Namespace, "", "Secret", ns, ref.Name, via); ok {
					d.Findings = append(d.Findings, f)
				}
			}
			if f, ok := secretFinding(fcode, ns, ref.Name, via, look); ok {
				d.Findings = append(d.Findings, f)
			}
		}
	}
}

// addRules summarizes the rules and checks their Service backends.
func (d *diagReport) addRules(rt *httpRoute, look httpRouteLookups, grants *grantIndex) {
	idx := newServiceIndex(look)
	unreadable := make(map[string]bool)
	for i, r := range rt.Spec.Rules {
		rule := "rule " + strconv.Itoa(i+1)
		if r.Name != nil && *r.Name != "" {
			rule += " (" + *r.Name + ")"
		}
		d.Summary = append(d.Summary, fmt.Sprintf("%s: %s -> %s%s", rule, matchesText(r.Matches), backendsText(rt.Namespace, r.BackendRefs), filtersText(r.Filters)))
		d.addFilters(r.Filters, rule, "rule/"+strconv.Itoa(i+1))
		if len(r.BackendRefs) == 0 && !hasRedirect(r.Filters) {
			d.Findings = append(d.Findings, diagFinding{
				Code: "rule/" + strconv.Itoa(i+1) + "/no-backends", Severity: diagWarning,
				Title:  rule + " has no backendRefs",
				Detail: "Without backends or a redirect, implementations answer matching requests with 404 or 500.",
			})
		}
		for j, b := range r.BackendRefs {
			d.addBackendRef(rt, b, fmt.Sprintf("%d.%d", i+1, j+1), rule, fmt.Sprintf("%s backendRefs[%d]", rule, j), idx, unreadable, grants)
		}
	}
}

// addBackendRef checks one backendRef of a rule.
func (d *diagReport) addBackendRef(rt *httpRoute, b gwBackendRef, code, rule, via string, idx *serviceIndex, unreadable map[string]bool, grants *grantIndex) {
	group, kind := deref(b.Group, ""), deref(b.Kind, "Service")
	ns := deref(b.Namespace, rt.Namespace)
	d.addFilters(b.Filters, via, "rule/"+code)
	if group != "" || kind != "Service" {
		d.Findings = append(d.Findings, diagFinding{
			Code: "unsupported-backend/" + code, Severity: diagInfo,
			Title:  fmt.Sprintf("Backend %s %s/%s is not checked (%s)", kind, ns, b.Name, via),
			Detail: "kc checks Service backends only; other backends are interpreted by the implementation.",
		})
		return
	}
	if b.Port == nil {
		d.Findings = append(d.Findings, diagFinding{
			Code: "backend-no-port/" + code, Severity: diagError,
			Title:  fmt.Sprintf("Backend Service %s/%s has no port (%s)", ns, b.Name, via),
			Detail: "A Service backendRef needs a port; the controller reports ResolvedRefs=False for it.",
		})
		return
	}
	if ns != rt.Namespace {
		if f, ok := grants.missing("backend/"+code, "HTTPRoute", rt.Namespace, "", "Service", ns, b.Name, via); ok {
			d.Findings = append(d.Findings, f)
		}
	}
	weight := deref(b.Weight, 1)
	c := backendCheck{code: code, via: via, namespace: ns, name: b.Name, port: *b.Port,
		evidence: fmt.Sprintf("%s -> port %d, weight %d", rule, *b.Port, weight)}
	f, link := c.run(idx, unreadable)
	if f != nil {
		d.Findings = append(d.Findings, *f)
	}
	if link != nil {
		d.Backends = append(d.Backends, *link)
	}
	if weight == 0 {
		d.Findings = append(d.Findings, diagFinding{
			Code: "backend-weight-zero/" + code, Severity: diagInfo,
			Title:  fmt.Sprintf("Backend Service %s/%s has weight 0 (%s)", ns, b.Name, via),
			Detail: "A backend with weight 0 receives no requests.",
		})
	}
}

var coreFilters = map[string]bool{
	"RequestHeaderModifier": true, "ResponseHeaderModifier": true, "RequestRedirect": true,
	"URLRewrite": true, "RequestMirror": true, "CORS": true,
}

func hasRedirect(fs []gwFilter) bool {
	for _, f := range fs {
		if f.Type == "RequestRedirect" {
			return true
		}
	}
	return false
}

// addFilters reports filters kc does not evaluate: extension filters and
// types it does not know.
func (d *diagReport) addFilters(fs []gwFilter, via, code string) {
	for k, f := range fs {
		if coreFilters[f.Type] {
			continue
		}
		what := f.Type
		if f.Type == "ExtensionRef" && f.ExtensionRef != nil {
			what = fmt.Sprintf("ExtensionRef %s %s", f.ExtensionRef.Kind, f.ExtensionRef.Name)
		}
		d.Findings = append(d.Findings, diagFinding{
			Code: fmt.Sprintf("unsupported-filter/%s/%d", code, k), Severity: diagInfo,
			Title:  fmt.Sprintf("Filter %s is not evaluated (%s)", what, via),
			Detail: "Extension and implementation-specific filters change requests in ways only the implementation knows.",
		})
	}
}

func matchesText(ms []gwRouteMatch) string {
	if len(ms) == 0 {
		return "PathPrefix /"
	}
	parts := make([]string, 0, len(ms))
	for _, m := range ms {
		s := "PathPrefix /"
		if m.Path != nil {
			s = deref(m.Path.Type, "PathPrefix") + " " + deref(m.Path.Value, "/")
		}
		if m.Method != nil {
			s = *m.Method + " " + s
		}
		if n := len(m.Headers) + len(m.QueryParams); n > 0 {
			s += fmt.Sprintf(" +%d header/query matches", n)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " | ")
}

func backendsText(routeNS string, bs []gwBackendRef) string {
	if len(bs) == 0 {
		return "(no backends)"
	}
	parts := make([]string, 0, len(bs))
	for _, b := range bs {
		name := b.Name
		if ns := deref(b.Namespace, routeNS); ns != routeNS {
			name = ns + "/" + name
		}
		if k := deref(b.Kind, "Service"); k != "Service" || deref(b.Group, "") != "" {
			name = k + " " + name
		}
		if b.Port != nil {
			name += ":" + strconv.Itoa(int(*b.Port))
		}
		if b.Weight != nil {
			name += fmt.Sprintf(" (weight %d)", *b.Weight)
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, ", ")
}

func filtersText(fs []gwFilter) string {
	if len(fs) == 0 {
		return ""
	}
	types := make([]string, 0, len(fs))
	for _, f := range fs {
		types = append(types, f.Type)
	}
	return " [filters " + strings.Join(types, ", ") + "]"
}

// grantIndex answers whether a ReferenceGrant allows a cross-namespace
// reference, reading each target namespace once.
type grantIndex struct {
	look httpRouteLookups
	byNS map[string]grantList
}

type grantList struct {
	items []referenceGrant
	err   error
}

func newGrantIndex(look httpRouteLookups) *grantIndex {
	return &grantIndex{look: look, byNS: make(map[string]grantList)}
}

// missing returns a finding when no ReferenceGrant in toNS lets fromKind in
// fromNS refer to toKind toName.
func (g *grantIndex) missing(code, fromKind, fromNS, toGroup, toKind, toNS, toName, via string) (diagFinding, bool) {
	l, ok := g.byNS[toNS]
	if !ok {
		items, err := g.look.referenceGrants(toNS)
		l = grantList{items: items, err: err}
		g.byNS[toNS] = l
	}
	if l.err != nil {
		return diagFinding{
			Code: "grant-unreadable/" + code, Severity: diagIncomplete,
			Title:  fmt.Sprintf("ReferenceGrants in %s could not be read: %s", toNS, lookupProblem(l.err)),
			Detail: fmt.Sprintf("Whether %s %s may refer to %s %s/%s is not checked (%s).", fromKind, fromNS, toKind, toNS, toName, via),
		}, true
	}
	for _, rg := range l.items {
		from, to := false, false
		for _, f := range rg.Spec.From {
			if f.Group == gatewayGroup && f.Kind == fromKind && f.Namespace == fromNS {
				from = true
			}
		}
		for _, t := range rg.Spec.To {
			if t.Group == toGroup && t.Kind == toKind && (t.Name == nil || *t.Name == "" || *t.Name == toName) {
				to = true
			}
		}
		if from && to {
			return diagFinding{}, false
		}
	}
	return diagFinding{
		Code: "grant-missing/" + code, Severity: diagError,
		Title:  fmt.Sprintf("No ReferenceGrant in %s lets %s in %s refer to %s %s (%s)", toNS, fromKind, fromNS, toKind, toName, via),
		Detail: "A reference to another namespace needs a ReferenceGrant in the target namespace; without it the controller reports ResolvedRefs=False (RefNotPermitted).",
		Next:   fmt.Sprintf("Create a ReferenceGrant in %s from %s %s to %s.", toNS, fromKind, fromNS, toKind),
	}, true
}
