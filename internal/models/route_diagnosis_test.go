package models

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var errRouteForbidden = apierrors.NewForbidden(schema.GroupResource{Resource: "x"}, "", errors.New("no"))

// fakeRouteLookups serves a routing diagnosis from maps. A namespace in
// an *Err map fails with that error.
type fakeRouteLookups struct {
	svcs       map[string][]corev1.Service
	svcErr     map[string]error
	classes    []networkingv1.IngressClass
	classErr   error
	secrets    map[string]bool // "ns/name"
	secretErr  error
	gateways   map[string]*gatewayObj // "ns/name"
	gatewayErr map[string]error
	notServed  bool
	grants     map[string][]referenceGrant
	grantErr   error
	nsLabels   map[string]map[string]string
	nsErr      error
	grantReads []string
}

func (f *fakeRouteLookups) services(ns string) ([]corev1.Service, error) {
	if err := f.svcErr[ns]; err != nil {
		return nil, err
	}
	return f.svcs[ns], nil
}

func (f *fakeRouteLookups) secretExists(ns, name string) (bool, error) {
	if f.secretErr != nil {
		return false, f.secretErr
	}
	return f.secrets[ns+"/"+name], nil
}

func (f *fakeRouteLookups) ingressClasses() ([]networkingv1.IngressClass, error) {
	return f.classes, f.classErr
}

func (f *fakeRouteLookups) gateway(ns, name string) (*gatewayObj, error) {
	if f.notServed {
		return nil, errTypeNotServed
	}
	if err := f.gatewayErr[ns]; err != nil {
		return nil, err
	}
	return f.gateways[ns+"/"+name], nil
}

func (f *fakeRouteLookups) gatewayGVR() (schema.GroupVersionResource, bool) {
	return schema.GroupVersionResource{Group: gatewayGroup, Version: "v1", Resource: "gateways"}, !f.notServed
}

func (f *fakeRouteLookups) referenceGrants(ns string) ([]referenceGrant, error) {
	f.grantReads = append(f.grantReads, ns)
	if f.grantErr != nil {
		return nil, f.grantErr
	}
	return f.grants[ns], nil
}

func (f *fakeRouteLookups) namespaceLabels(ns string) (map[string]string, error) {
	if f.nsErr != nil {
		return nil, f.nsErr
	}
	return f.nsLabels[ns], nil
}

func routeSvc(ns, name string, ports ...corev1.ServicePort) corev1.Service {
	if ports == nil {
		ports = []corev1.ServicePort{{Name: "http", Port: 80}}
	}
	return corev1.Service{Namespace: ns, Name: name, Spec: corev1.ServiceSpec{Ports: ports}}
}

// routeFindingCodes returns the codes of d's findings with their severity.
func routeFindingCodes(d diagReport) map[string]diagSeverity {
	out := make(map[string]diagSeverity, len(d.Findings))
	for _, f := range d.Findings {
		out[f.Code] = f.Severity
	}
	return out
}

func testIngress(mut func(*networkingv1.Ingress)) *networkingv1.Ingress {
	prefix := networkingv1.PathTypePrefix
	ing := &networkingv1.Ingress{Namespace: "shop", Name: "web", Spec: networkingv1.IngressSpec{
		IngressClassName: new("nginx"),
		Rules: []networkingv1.IngressRule{{Host: "shop.example.com", HTTP: &networkingv1.HTTPIngressRuleValue{
			Paths: []networkingv1.HTTPIngressPath{{Path: "/", PathType: &prefix, Backend: networkingv1.IngressBackend{
				Service: &networkingv1.IngressServiceBackend{Name: "web", Port: networkingv1.ServiceBackendPort{Number: 80}},
			}}},
		}}},
	}}
	ing.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: "10.0.0.1"}}
	if mut != nil {
		mut(ing)
	}
	return ing
}

func okIngressLookups() *fakeRouteLookups {
	return &fakeRouteLookups{
		svcs:    map[string][]corev1.Service{"shop": {routeSvc("shop", "web")}},
		classes: []networkingv1.IngressClass{{Name: "nginx", Spec: networkingv1.IngressClassSpec{Controller: "k8s.io/ingress-nginx"}}},
		secrets: map[string]bool{"shop/web-tls": true},
	}
}

func TestDiagnoseIngress(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ing  func(*networkingv1.Ingress)
		look func(*fakeRouteLookups)
		want map[string]diagSeverity
		// absent codes must not be reported.
		absent       []string
		wantBackends int
	}{
		{name: "healthy", wantBackends: 1},
		{name: "class does not exist", ing: func(i *networkingv1.Ingress) { i.Spec.IngressClassName = new("traefik") },
			want: map[string]diagSeverity{"class-missing": diagError}, wantBackends: 1},
		{name: "classes forbidden is incomplete, not missing", look: func(l *fakeRouteLookups) { l.classErr = errRouteForbidden },
			want: map[string]diagSeverity{"classes-unreadable": diagIncomplete}, absent: []string{"class-missing"}, wantBackends: 1},
		{name: "no class and no default", ing: func(i *networkingv1.Ingress) { i.Spec.IngressClassName = nil },
			want: map[string]diagSeverity{"no-class": diagWarning}, wantBackends: 1},
		{name: "no class, one default", ing: func(i *networkingv1.Ingress) { i.Spec.IngressClassName = nil },
			look: func(l *fakeRouteLookups) {
				l.classes[0].Annotations = map[string]string{defaultIngressClassLabel: "true"}
			},
			absent: []string{"no-class"}, wantBackends: 1},
		{name: "deprecated annotation", ing: func(i *networkingv1.Ingress) {
			i.Spec.IngressClassName = nil
			i.Annotations = map[string]string{ingressClassAnnotation: "nginx"}
		}, want: map[string]diagSeverity{"class-annotation": diagInfo}, wantBackends: 1},
		{name: "backend Service missing", look: func(l *fakeRouteLookups) { l.svcs = nil },
			want: map[string]diagSeverity{"backend-missing/1.1": diagError}, wantBackends: 0},
		{name: "Services forbidden is incomplete, not missing", look: func(l *fakeRouteLookups) { l.svcErr = map[string]error{"shop": errRouteForbidden} },
			want: map[string]diagSeverity{"services-unreadable/shop": diagIncomplete}, absent: []string{"backend-missing/1.1"}},
		{name: "port number not exposed", ing: func(i *networkingv1.Ingress) {
			i.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number = 8080
		}, want: map[string]diagSeverity{"backend-port/1.1": diagError}, wantBackends: 1},
		{name: "port name not exposed", ing: func(i *networkingv1.Ingress) {
			i.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port = networkingv1.ServiceBackendPort{Name: "grpc"}
		}, want: map[string]diagSeverity{"backend-port/1.1": diagError}, wantBackends: 1},
		{name: "port name exposed", ing: func(i *networkingv1.Ingress) {
			i.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port = networkingv1.ServiceBackendPort{Name: "http"}
		}, absent: []string{"backend-port/1.1"}, wantBackends: 1},
		{name: "resource backend is unsupported", ing: func(i *networkingv1.Ingress) {
			i.Spec.Rules[0].HTTP.Paths[0].Backend = networkingv1.IngressBackend{Resource: &corev1.TypedLocalObjectReference{Kind: "StorageBucket", Name: "static"}}
		}, want: map[string]diagSeverity{"unsupported-backend/1.1": diagInfo}},
		{name: "nothing to route", ing: func(i *networkingv1.Ingress) { i.Spec.Rules = nil },
			want: map[string]diagSeverity{"no-backends": diagError}},
		{name: "default backend only", ing: func(i *networkingv1.Ingress) {
			i.Spec.Rules = nil
			i.Spec.DefaultBackend = &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "web", Port: networkingv1.ServiceBackendPort{Number: 80}}}
		}, absent: []string{"no-backends"}, wantBackends: 1},
		{name: "TLS Secret missing", ing: func(i *networkingv1.Ingress) {
			i.Spec.TLS = []networkingv1.IngressTLS{{Hosts: []string{"shop.example.com"}, SecretName: "gone"}}
		}, want: map[string]diagSeverity{"secret-missing/tls/1": diagError}, wantBackends: 1},
		{name: "TLS Secret present", ing: func(i *networkingv1.Ingress) {
			i.Spec.TLS = []networkingv1.IngressTLS{{Hosts: []string{"shop.example.com"}, SecretName: "web-tls"}}
		}, absent: []string{"secret-missing/tls/1", "tls-host-unrouted/shop.example.com"}, wantBackends: 1},
		{name: "TLS Secret forbidden", ing: func(i *networkingv1.Ingress) {
			i.Spec.TLS = []networkingv1.IngressTLS{{SecretName: "web-tls"}}
		}, look: func(l *fakeRouteLookups) { l.secretErr = errRouteForbidden },
			want: map[string]diagSeverity{"secret-unreadable/tls/1": diagIncomplete}, absent: []string{"secret-missing/tls/1"}, wantBackends: 1},
		{name: "TLS host without rule", ing: func(i *networkingv1.Ingress) {
			i.Spec.TLS = []networkingv1.IngressTLS{{Hosts: []string{"admin.example.com"}, SecretName: "web-tls"}}
		}, want: map[string]diagSeverity{"tls-host-unrouted/admin.example.com": diagInfo}, wantBackends: 1},
		{name: "no address", ing: func(i *networkingv1.Ingress) { i.Status.LoadBalancer.Ingress = nil },
			want: map[string]diagSeverity{"no-address": diagWarning}, wantBackends: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			look := okIngressLookups()
			if tc.look != nil {
				tc.look(look)
			}
			d := diagnoseIngress(testIngress(tc.ing), look)
			got := routeFindingCodes(d)
			for code, sev := range tc.want {
				if s, ok := got[code]; !ok || s != sev {
					t.Errorf("finding %s = %v (present %v), want %v; all: %v", code, s, ok, sev, got)
				}
			}
			for _, code := range tc.absent {
				if _, ok := got[code]; ok {
					t.Errorf("finding %s reported; all: %v", code, got)
				}
			}
			if tc.want == nil && tc.absent == nil {
				for code, sev := range got {
					if sev != diagInfo {
						t.Errorf("healthy Ingress reports %s (%v)", code, sev)
					}
				}
			}
			if len(d.Backends) != tc.wantBackends {
				t.Errorf("backends = %+v, want %d", d.Backends, tc.wantBackends)
			}
		})
	}
}

func TestDiagnoseIngressBackendEvidence(t *testing.T) {
	t.Parallel()
	d := diagnoseIngress(testIngress(func(i *networkingv1.Ingress) {
		// Two paths to the same Service give one backend with both paths.
		p := i.Spec.Rules[0].HTTP.Paths[0]
		p.Path = "/api"
		i.Spec.Rules[0].HTTP.Paths = append(i.Spec.Rules[0].HTTP.Paths, p)
	}), okIngressLookups())
	if len(d.Backends) != 1 {
		t.Fatalf("backends = %+v", d.Backends)
	}
	ev := d.Backends[0].Evidence
	if !strings.Contains(ev, "shop.example.com/ -> port 80") || !strings.Contains(ev, "shop.example.com/api -> port 80") {
		t.Errorf("evidence = %q", ev)
	}
	if !slices.ContainsFunc(d.Summary, func(s string) bool { return strings.Contains(s, "class: nginx (controller k8s.io/ingress-nginx)") }) {
		t.Errorf("summary = %v", d.Summary)
	}
}

func testGateway(ns, name string, mut func(*gatewayObj)) *gatewayObj {
	gw := &gatewayObj{}
	gw.Namespace, gw.Name, gw.Generation = ns, name, 3
	gw.Spec.GatewayClassName = "eg"
	gw.Spec.Listeners = []gwListener{
		{Name: "http", Port: 80, Protocol: "HTTP"},
		{Name: "https", Port: 443, Protocol: "HTTPS", Hostname: new("*.example.com")},
	}
	gw.Status.Conditions = []metav1.Condition{
		{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted", ObservedGeneration: 3},
		{Type: "Programmed", Status: metav1.ConditionTrue, Reason: "Programmed", ObservedGeneration: 3},
	}
	if mut != nil {
		mut(gw)
	}
	return gw
}

var routeTransition = metav1.NewTime(time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC))

func testRoute(mut func(*httpRoute)) *httpRoute {
	rt := &httpRoute{}
	rt.Namespace, rt.Name, rt.Generation = "shop", "web", 2
	rt.Spec.Hostnames = []string{"shop.example.com"}
	rt.Spec.ParentRefs = []gwParentRef{{Name: "edge", Namespace: new("infra")}}
	rt.Spec.Rules = []gwRouteRule{{
		Matches:     []gwRouteMatch{{Path: &gwPathMatch{Type: new("PathPrefix"), Value: new("/")}}},
		BackendRefs: []gwBackendRef{{Name: "web", Port: new(int32(80))}},
	}}
	rt.Status.Parents = []gwRouteParentStatus{{
		ParentRef:      gwParentRef{Name: "edge", Namespace: new("infra")},
		ControllerName: "gateway.envoyproxy.io/gatewayclass-controller",
		Conditions: []metav1.Condition{
			{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted", ObservedGeneration: 2, LastTransitionTime: routeTransition},
			{Type: "ResolvedRefs", Status: metav1.ConditionTrue, Reason: "ResolvedRefs", ObservedGeneration: 2, LastTransitionTime: routeTransition},
		},
	}}
	if mut != nil {
		mut(rt)
	}
	return rt
}

func okRouteLookups() *fakeRouteLookups {
	return &fakeRouteLookups{
		svcs: map[string][]corev1.Service{"shop": {routeSvc("shop", "web")}, "payments": {routeSvc("payments", "pay")}},
		gateways: map[string]*gatewayObj{"infra/edge": testGateway("infra", "edge", func(g *gatewayObj) {
			for i := range g.Spec.Listeners {
				g.Spec.Listeners[i].AllowedRoutes = allowFrom("All")
			}
		})},
		secrets:  map[string]bool{"infra/edge-cert": true},
		nsLabels: map[string]map[string]string{"shop": {"team": "shop"}},
	}
}

func allowFrom(from string) *gwAllowedRoutes {
	return &gwAllowedRoutes{Namespaces: &gwRouteNamespaces{From: new(from)}}
}

func setGateway(l *fakeRouteLookups, mut func(*gatewayObj)) {
	mut(l.gateways["infra/edge"])
}

func grantFor(fromKind, fromNS, toKind string) referenceGrant {
	var rg referenceGrant
	rg.Name = "allow"
	rg.Spec.From = []gwGrantFrom{{Group: gatewayGroup, Kind: fromKind, Namespace: fromNS}}
	rg.Spec.To = []gwGrantTo{{Kind: toKind}}
	return rg
}

func TestDiagnoseHTTPRoute(t *testing.T) {
	t.Parallel()
	crossNS := func(rt *httpRoute) {
		rt.Spec.Rules[0].BackendRefs = append(rt.Spec.Rules[0].BackendRefs, gwBackendRef{
			Name: "pay", Namespace: new("payments"), Port: new(int32(80)), Weight: new(int32(10)),
		})
	}
	cases := []struct {
		name   string
		rt     func(*httpRoute)
		look   func(*fakeRouteLookups)
		want   map[string]diagSeverity
		absent []string
	}{
		{name: "healthy"},
		{name: "cross-namespace backend without ReferenceGrant", rt: crossNS,
			want: map[string]diagSeverity{"grant-missing/backend/1.2": diagError}},
		{name: "cross-namespace backend with ReferenceGrant", rt: crossNS,
			look: func(l *fakeRouteLookups) {
				l.grants = map[string][]referenceGrant{"payments": {grantFor("HTTPRoute", "shop", "Service")}}
			},
			absent: []string{"grant-missing/backend/1.2"}},
		{name: "ReferenceGrant for another namespace does not count", rt: crossNS,
			look: func(l *fakeRouteLookups) {
				l.grants = map[string][]referenceGrant{"payments": {grantFor("HTTPRoute", "other", "Service")}}
			},
			want: map[string]diagSeverity{"grant-missing/backend/1.2": diagError}},
		{name: "ReferenceGrants forbidden", rt: crossNS, look: func(l *fakeRouteLookups) { l.grantErr = errRouteForbidden },
			want: map[string]diagSeverity{"grant-unreadable/backend/1.2": diagIncomplete}, absent: []string{"grant-missing/backend/1.2"}},
		{name: "ReferenceGrant not served", rt: crossNS, look: func(l *fakeRouteLookups) { l.grantErr = errTypeNotServed },
			want: map[string]diagSeverity{"grant-unreadable/backend/1.2": diagIncomplete}},
		{name: "backend Service missing", look: func(l *fakeRouteLookups) { l.svcs["shop"] = nil },
			want: map[string]diagSeverity{"backend-missing/1.1": diagError}},
		{name: "backend Services forbidden", look: func(l *fakeRouteLookups) { l.svcErr = map[string]error{"shop": errRouteForbidden} },
			want: map[string]diagSeverity{"services-unreadable/shop": diagIncomplete}, absent: []string{"backend-missing/1.1"}},
		{name: "backend without port", rt: func(rt *httpRoute) { rt.Spec.Rules[0].BackendRefs[0].Port = nil },
			want: map[string]diagSeverity{"backend-no-port/1.1": diagError}},
		{name: "extension backend and filter are unsupported", rt: func(rt *httpRoute) {
			rt.Spec.Rules[0].BackendRefs[0].gwObjectRef = gwObjectRef{Name: "bucket", Group: new("storage.example.com"), Kind: new("Bucket")}
			rt.Spec.Rules[0].Filters = []gwFilter{{Type: "ExtensionRef"}, {Type: "RequestHeaderModifier"}}
		}, want: map[string]diagSeverity{"unsupported-backend/1.1": diagInfo, "unsupported-filter/rule/1/0": diagInfo},
			absent: []string{"unsupported-filter/rule/1/1"}},
		{name: "parent Gateway missing", look: func(l *fakeRouteLookups) { l.gateways = nil },
			want: map[string]diagSeverity{"parent/1/missing": diagError}},
		{name: "parent Gateway forbidden", look: func(l *fakeRouteLookups) { l.gatewayErr = map[string]error{"infra": errRouteForbidden} },
			want: map[string]diagSeverity{"parent/1/unreadable": diagIncomplete}, absent: []string{"parent/1/missing"}},
		{name: "Gateway API not served", look: func(l *fakeRouteLookups) { l.notServed = true },
			want: map[string]diagSeverity{"parent/1/unreadable": diagIncomplete}},
		{name: "sectionName matches no listener", rt: func(rt *httpRoute) {
			rt.Spec.ParentRefs[0].SectionName = new("grpc")
			rt.Status.Parents[0].ParentRef.SectionName = new("grpc")
		}, want: map[string]diagSeverity{"parent/1/no-listener": diagError}},
		{name: "listener allows only its own namespace", look: func(l *fakeRouteLookups) {
			setGateway(l, func(g *gatewayObj) {
				for i := range g.Spec.Listeners {
					g.Spec.Listeners[i].AllowedRoutes = nil
				}
			})
		}, want: map[string]diagSeverity{"parent/1/not-accepted": diagError}},
		{name: "namespace selector matches", look: func(l *fakeRouteLookups) {
			setGateway(l, func(g *gatewayObj) {
				ar := allowFrom("Selector")
				ar.Namespaces.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"team": "shop"}}
				g.Spec.Listeners[0].AllowedRoutes = ar
			})
		}, absent: []string{"parent/1/not-accepted"}},
		{name: "namespace labels unreadable", look: func(l *fakeRouteLookups) {
			l.nsErr = errRouteForbidden
			setGateway(l, func(g *gatewayObj) {
				for i := range g.Spec.Listeners {
					ar := allowFrom("Selector")
					ar.Namespaces.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"team": "shop"}}
					g.Spec.Listeners[i].AllowedRoutes = ar
				}
			})
		}, want: map[string]diagSeverity{"parent/1/namespace-unreadable": diagIncomplete}, absent: []string{"parent/1/not-accepted"}},
		{name: "hostname matches no listener", rt: func(rt *httpRoute) {
			rt.Spec.Hostnames = []string{"shop.other.org"}
			rt.Spec.ParentRefs[0].SectionName = new("https")
			rt.Status.Parents[0].ParentRef.SectionName = new("https")
		}, want: map[string]diagSeverity{"parent/1/not-accepted": diagError}},
		{name: "route not accepted keeps the reason", rt: func(rt *httpRoute) {
			rt.Status.Parents[0].Conditions[0].Status = metav1.ConditionFalse
			rt.Status.Parents[0].Conditions[0].Reason = "NotAllowedByListeners"
		}, want: map[string]diagSeverity{"parent/1/route/accepted": diagError}},
		{name: "stale route status", rt: func(rt *httpRoute) { rt.Generation = 5 },
			want: map[string]diagSeverity{"parent/1/route/accepted": diagWarning, "parent/1/route/resolvedrefs": diagWarning}},
		{name: "no status for the parent", rt: func(rt *httpRoute) { rt.Status.Parents = nil },
			want: map[string]diagSeverity{"parent/1/no-status": diagWarning}},
		{name: "Gateway not programmed", look: func(l *fakeRouteLookups) {
			setGateway(l, func(g *gatewayObj) {
				g.Status.Conditions[1].Status = metav1.ConditionFalse
				g.Status.Conditions[1].Reason = "AddressNotAssigned"
			})
		}, want: map[string]diagSeverity{"parent/1/gateway/programmed": diagError}},
		{name: "second parent missing, first fine", rt: func(rt *httpRoute) {
			rt.Spec.ParentRefs = append(rt.Spec.ParentRefs, gwParentRef{Name: "internal", Namespace: new("infra")})
		}, want: map[string]diagSeverity{"parent/2/missing": diagError, "parent/2/no-status": diagWarning},
			absent: []string{"parent/1/missing", "parent/1/no-status"}},
		{name: "mesh parent is unsupported", rt: func(rt *httpRoute) {
			rt.Spec.ParentRefs = []gwParentRef{{Name: "web", Group: new(""), Kind: new("Service")}}
			rt.Status.Parents = nil
		}, want: map[string]diagSeverity{"unsupported-parent/1": diagInfo}},
		{name: "no parents", rt: func(rt *httpRoute) { rt.Spec.ParentRefs = nil; rt.Status.Parents = nil },
			want: map[string]diagSeverity{"no-parents": diagError}},
		{name: "listener certificate missing", look: func(l *fakeRouteLookups) {
			setGateway(l, func(g *gatewayObj) {
				g.Spec.Listeners[1].TLS = &gwListenerTLS{CertificateRefs: []gwObjectRef{{Name: "gone"}}}
			})
		}, want: map[string]diagSeverity{"secret-missing/parent/1/tls/https/0": diagError}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			look := okRouteLookups()
			if tc.look != nil {
				tc.look(look)
			}
			d := diagnoseHTTPRoute(testRoute(tc.rt), look)
			got := routeFindingCodes(d)
			for code, sev := range tc.want {
				if s, ok := got[code]; !ok || s != sev {
					t.Errorf("finding %s = %v (present %v), want %v; all: %v", code, s, ok, sev, got)
				}
			}
			for _, code := range tc.absent {
				if _, ok := got[code]; ok {
					t.Errorf("finding %s reported; all: %v", code, got)
				}
			}
			if tc.want == nil && tc.absent == nil {
				for code, sev := range got {
					if sev != diagInfo {
						t.Errorf("healthy route reports %s (%v)", code, sev)
					}
				}
			}
		})
	}
}

// Conditions keep their reason, transition time and generation, and a
// cross-namespace backend shows its weight.
func TestDiagnoseHTTPRouteEvidence(t *testing.T) {
	t.Parallel()
	look := okRouteLookups()
	look.grants = map[string][]referenceGrant{"payments": {grantFor("HTTPRoute", "shop", "Service")}}
	d := diagnoseHTTPRoute(testRoute(func(rt *httpRoute) {
		rt.Spec.Rules[0].BackendRefs = append(rt.Spec.Rules[0].BackendRefs, gwBackendRef{
			Name: "pay", Namespace: new("payments"), Port: new(int32(80)), Weight: new(int32(10)),
		})
		rt.Status.Parents[0].Conditions[0].Status = metav1.ConditionFalse
		rt.Status.Parents[0].Conditions[0].Reason = "NoMatchingListenerHostname"
		rt.Status.Parents[0].Conditions[0].Message = "no listener hostname matches"
		rt.Status.Parents[0].Conditions[0].ObservedGeneration = 1
	}), look)
	summary := strings.Join(d.Summary, "\n")
	for _, want := range []string{
		"Accepted=False (NoMatchingListenerHostname) since 2026-11-20T10:00:00Z, for generation 1 of 2",
		"web:80, payments/pay:80 (weight 10)",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary lacks %q:\n%s", want, summary)
		}
	}
	var accepted *diagFinding
	for i := range d.Findings {
		if d.Findings[i].Code == "parent/1/route/accepted" {
			accepted = &d.Findings[i]
		}
	}
	if accepted == nil || !accepted.Observed.Equal(routeTransition.Time) || accepted.Detail != "no listener hostname matches" {
		t.Fatalf("accepted finding = %+v", accepted)
	}
	var names []string
	for _, b := range d.Backends {
		names = append(names, b.GVR.Resource+"/"+b.Namespace+"/"+b.Name+": "+b.Evidence)
	}
	want := []string{"gateways/infra/edge: spec.parentRefs[0]", "services/shop/web: rule 1 -> port 80, weight 1", "services/payments/pay: rule 1 -> port 80, weight 10"}
	if !slices.Equal(names, want) {
		t.Errorf("backends = %q, want %q", names, want)
	}
	if !slices.Equal(look.grantReads, []string{"payments"}) {
		t.Errorf("ReferenceGrants read in %v, want only payments", look.grantReads)
	}
}

func TestHostnamesIntersect(t *testing.T) {
	t.Parallel()
	cases := []struct {
		listener, route string
		want            bool
	}{
		{"shop.example.com", "shop.example.com", true},
		{"shop.example.com", "SHOP.example.com", true},
		{"shop.example.com", "api.example.com", false},
		{"*.example.com", "shop.example.com", true},
		{"*.example.com", "a.b.example.com", true},
		{"*.example.com", "example.com", false},
		{"shop.example.com", "*.example.com", true},
		{"example.com", "*.example.com", false},
		{"*.example.com", "*.example.com", true},
		{"*.example.com", "*.shop.example.com", true},
		{"*.shop.example.com", "*.example.com", true},
		{"*.example.com", "*.example.org", false},
	}
	for _, tc := range cases {
		if got := hostnamesIntersect(tc.listener, tc.route); got != tc.want {
			t.Errorf("hostnamesIntersect(%q, %q) = %v, want %v", tc.listener, tc.route, got, tc.want)
		}
	}
}
