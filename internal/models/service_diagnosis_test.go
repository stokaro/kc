package models

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func diagService(mut func(*corev1.Service)) *corev1.Service {
	s := &corev1.Service{Namespace: "shop", Name: "web", Spec: corev1.ServiceSpec{
		Selector:   map[string]string{"app": "web"},
		ClusterIP:  "10.0.0.10",
		IPFamilies: []corev1.IPFamily{corev1.IPv4Protocol},
		Ports:      []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromString("http")}},
	}}
	if mut != nil {
		mut(s)
	}
	return s
}

// diagPod is a pod of the Service: ready unless told otherwise, with a
// container port "http" on 8080 unless ports is given.
func diagPod(name string, ready bool, ips []string, ports ...corev1.ContainerPort) corev1.Pod {
	if ports == nil {
		ports = []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}
	}
	status := corev1.ConditionTrue
	reason := ""
	cs := corev1.ContainerStatus{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
	if !ready {
		status, reason = corev1.ConditionFalse, "ContainersNotReady"
		cs.Ready = false
		cs.State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
		cs.RestartCount = 4
	}
	p := corev1.Pod{
		Namespace: "shop", Name: name, UID: types.UID(name + "-uid"), Labels: map[string]string{"app": "web"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Ports: ports}}},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: status, Reason: reason}},
			ContainerStatuses: []corev1.ContainerStatus{cs},
		},
	}
	for i, ip := range ips {
		if i == 0 {
			p.Status.PodIP = ip
		}
		p.Status.PodIPs = append(p.Status.PodIPs, corev1.PodIP{IP: ip})
	}
	return p
}

func diagEndpoint(pod string, uid types.UID, ready, serving, terminating *bool) discoveryv1.Endpoint {
	return discoveryv1.Endpoint{
		Addresses:  []string{"10.1.0.1"},
		Conditions: discoveryv1.EndpointConditions{Ready: ready, Serving: serving, Terminating: terminating},
		TargetRef:  &corev1.ObjectReference{Kind: "Pod", Namespace: "shop", Name: pod, UID: uid},
	}
}

func diagSlice(name string, at discoveryv1.AddressType, eps ...discoveryv1.Endpoint) discoveryv1.EndpointSlice {
	return discoveryv1.EndpointSlice{
		Namespace: "shop", Name: name, Labels: map[string]string{discoveryv1.LabelServiceName: "web"},
		AddressType: at, Endpoints: eps,
	}
}

func findingCodes(d serviceDiagnosis) []string {
	out := make([]string, 0)
	for _, f := range d.Findings {
		out = append(out, f.Severity.String()+" "+f.Code)
	}
	return out
}

func findingByCode(t *testing.T, d serviceDiagnosis, code string) diagFinding {
	t.Helper()
	for _, f := range d.Findings {
		if f.Code == code {
			return f
		}
	}
	t.Fatalf("no finding %s in %v", code, findingCodes(d))
	return diagFinding{}
}

func linkEvidence(f diagFinding) []string {
	out := make([]string, 0)
	for _, l := range f.Links {
		s := l.Name + ": " + l.Evidence
		if l.Problem != "" {
			s += " [" + l.Problem + "]"
		}
		out = append(out, s)
	}
	return out
}

func TestDiagnoseServiceFindings(t *testing.T) {
	t.Parallel()
	ip := []string{"10.1.0.1"}
	okSlice := func(pods ...string) discoveryv1.EndpointSlice {
		var eps []discoveryv1.Endpoint
		for _, p := range pods {
			eps = append(eps, diagEndpoint(p, types.UID(p+"-uid"), nil, nil, nil))
		}
		return diagSlice("web-abc", discoveryv1.AddressTypeIPv4, eps...)
	}
	cases := []struct {
		name  string
		in    serviceInput
		codes []string
		check func(t *testing.T, d serviceDiagnosis)
	}{
		{
			name:  "consistent service has no finding",
			in:    serviceInput{Service: diagService(nil), Pods: []corev1.Pod{diagPod("a", true, ip)}, Slices: []discoveryv1.EndpointSlice{okSlice("a")}},
			codes: make([]string, 0),
			check: func(t *testing.T, d serviceDiagnosis) {
				if len(d.Backends) != 2 || d.Backends[0].Evidence != "ready; http=8080" {
					t.Fatalf("backends = %+v", d.Backends)
				}
			},
		},
		{
			name: "wrong selector: no pods, candidates with their labels",
			in: serviceInput{
				Service: diagService(func(s *corev1.Service) { s.Spec.Selector = map[string]string{"app": "web-v2"} }),
				Pods: []corev1.Pod{
					diagPod("a", true, ip),
					{Namespace: "shop", Name: "db", Labels: map[string]string{"tier": "db"}},
					{Namespace: "other", Name: "elsewhere", Labels: map[string]string{"app": "web-v2"}},
				},
			},
			codes: []string{"error no-pods"},
			check: func(t *testing.T, d serviceDiagnosis) {
				f := findingByCode(t, d, "no-pods")
				if !strings.Contains(f.Title, "app=web-v2") {
					t.Errorf("title %q does not name the selector", f.Title)
				}
				if got, want := linkEvidence(f), []string{"a: labels app=web"}; !reflect.DeepEqual(got, want) {
					t.Errorf("candidates = %q, want %q", got, want)
				}
			},
		},
		{
			name:  "no candidate shares a selector key",
			in:    serviceInput{Service: diagService(func(s *corev1.Service) { s.Spec.Selector = map[string]string{"run": "x"} }), Pods: []corev1.Pod{diagPod("a", true, ip)}},
			codes: []string{"error no-pods"},
			check: func(t *testing.T, d serviceDiagnosis) {
				f := findingByCode(t, d, "no-pods")
				if len(f.Links) != 0 || !strings.Contains(f.Detail, "label keys run") {
					t.Errorf("finding = %+v", f)
				}
			},
		},
		{
			name: "one unready pod is linked with its readiness evidence",
			in: serviceInput{
				Service: diagService(nil),
				Pods:    []corev1.Pod{diagPod("a", true, ip), diagPod("b", false, []string{"10.1.0.2"})},
				Slices:  []discoveryv1.EndpointSlice{okSlice("a", "b")},
			},
			codes: []string{"warning pods-not-ready"},
			check: func(t *testing.T, d serviceDiagnosis) {
				got := linkEvidence(findingByCode(t, d, "pods-not-ready"))
				want := []string{"b: not ready (ContainersNotReady); app: waiting CrashLoopBackOff, 4 restarts"}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("links = %q, want %q", got, want)
				}
			},
		},
		{
			name:  "every selected pod unready is an error",
			in:    serviceInput{Service: diagService(nil), Pods: []corev1.Pod{diagPod("b", false, ip)}, Slices: []discoveryv1.EndpointSlice{okSlice("b")}},
			codes: []string{"error pods-not-ready"},
		},
		{
			name: "publishNotReadyAddresses explains unready pods instead",
			in: serviceInput{
				Service: diagService(func(s *corev1.Service) { s.Spec.PublishNotReadyAddresses = true }),
				Pods:    []corev1.Pod{diagPod("b", false, ip)}, Slices: []discoveryv1.EndpointSlice{okSlice("b")},
			},
			codes: []string{"info publish-not-ready", "info pods-not-ready"},
		},
		{
			name: "named targetPort resolves per pod",
			in: serviceInput{
				Service: diagService(nil),
				Pods: []corev1.Pod{
					diagPod("a", true, ip),
					diagPod("b", true, []string{"10.1.0.2"}, corev1.ContainerPort{Name: "web", ContainerPort: 9090}),
					diagPod("c", true, []string{"10.1.0.3"}, corev1.ContainerPort{Name: "http", ContainerPort: 9090}),
					diagPod("d", true, []string{"10.1.0.4"}, corev1.ContainerPort{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolUDP}),
				},
				Slices: []discoveryv1.EndpointSlice{okSlice("a", "b", "c", "d")},
			},
			codes: []string{"warning target-port-http"},
			check: func(t *testing.T, d serviceDiagnosis) {
				got := linkEvidence(findingByCode(t, d, "target-port-http"))
				want := []string{"b: no container port named http/TCP", "d: no container port named http/TCP"}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("unresolved = %q, want %q", got, want)
				}
				ev := make(map[string]string)
				for _, b := range d.Backends {
					ev[b.Name] = b.Evidence
				}
				if ev["a"] != "ready; http=8080" || ev["c"] != "ready; http=9090" || ev["b"] != "ready; http unresolved" {
					t.Errorf("per-pod ports = %v", ev)
				}
			},
		},
		{
			name: "numeric and defaulted target ports need no lookup",
			in: serviceInput{
				Service: diagService(func(s *corev1.Service) {
					s.Spec.Ports = []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt32(8080)}, {Name: "m", Port: 9100, Protocol: corev1.ProtocolUDP}}
				}),
				Pods: []corev1.Pod{diagPod("a", true, ip, corev1.ContainerPort{Name: "other", ContainerPort: 1})}, Slices: []discoveryv1.EndpointSlice{okSlice("a")},
			},
			codes: make([]string, 0),
			check: func(t *testing.T, d serviceDiagnosis) {
				if !slices.Contains(d.Summary, "ports: 80 -> 8080/TCP, m 9100 -> 9100/UDP") {
					t.Errorf("summary = %q", d.Summary)
				}
			},
		},
		{
			name: "selectorless with a manual slice",
			in: serviceInput{
				Service: diagService(func(s *corev1.Service) { s.Spec.Selector = nil }),
				Slices:  []discoveryv1.EndpointSlice{diagSlice("web-manual", discoveryv1.AddressTypeIPv4, discoveryv1.Endpoint{Addresses: []string{"192.0.2.1"}})},
			},
			codes: []string{"info selectorless"},
		},
		{
			name:  "selectorless without slices",
			in:    serviceInput{Service: diagService(func(s *corev1.Service) { s.Spec.Selector = nil })},
			codes: []string{"warning selectorless-empty", "info selectorless"},
		},
		{
			name: "ExternalName reads nothing else",
			in: serviceInput{Service: diagService(func(s *corev1.Service) {
				s.Spec.Type, s.Spec.ExternalName, s.Spec.Selector, s.Spec.ClusterIP = corev1.ServiceTypeExternalName, "db.example.com", nil, ""
			})},
			codes: []string{"info external-name"},
			check: func(t *testing.T, d serviceDiagnosis) {
				if len(d.Backends) != 0 {
					t.Errorf("backends = %+v", d.Backends)
				}
			},
		},
		{
			name: "headless is explained, its endpoints still checked",
			in: serviceInput{
				Service: diagService(func(s *corev1.Service) { s.Spec.ClusterIP, s.Spec.Ports = corev1.ClusterIPNone, nil }),
				Pods:    []corev1.Pod{diagPod("a", true, ip)},
			},
			codes: []string{"warning missing-endpoints", "info headless"},
		},
		{
			name: "dual-stack: a family without a slice",
			in: serviceInput{
				Service: diagService(func(s *corev1.Service) {
					s.Spec.IPFamilies = []corev1.IPFamily{corev1.IPv4Protocol, corev1.IPv6Protocol}
					s.Spec.IPFamilyPolicy = new(corev1.IPFamilyPolicyRequireDualStack)
				}),
				Pods:   []corev1.Pod{diagPod("a", true, []string{"10.1.0.1", "fd00::1"})},
				Slices: []discoveryv1.EndpointSlice{okSlice("a")},
			},
			codes: []string{"warning family-IPv6"},
			check: func(t *testing.T, d serviceDiagnosis) {
				if d.Summary[0] != "type ClusterIP, families IPv4, IPv6 (RequireDualStack)" {
					t.Errorf("summary = %q", d.Summary[0])
				}
			},
		},
		{
			name: "dual-stack with both slices is consistent",
			in: serviceInput{
				Service: diagService(func(s *corev1.Service) {
					s.Spec.IPFamilies = []corev1.IPFamily{corev1.IPv4Protocol, corev1.IPv6Protocol}
				}),
				Pods: []corev1.Pod{diagPod("a", true, []string{"10.1.0.1", "fd00::1"})},
				Slices: []discoveryv1.EndpointSlice{
					okSlice("a"),
					diagSlice("web-v6", discoveryv1.AddressTypeIPv6, diagEndpoint("a", "a-uid", nil, nil, nil)),
				},
			},
			codes: make([]string, 0),
		},
		{
			name:  "unreadable pods: incomplete, never no pods",
			in:    serviceInput{Service: diagService(nil), PodsErr: deniedError(podGVR, "shop", ""), Slices: make([]discoveryv1.EndpointSlice, 0)},
			codes: []string{"incomplete pods-unreadable"},
			check: func(t *testing.T, d serviceDiagnosis) {
				if !slices.Contains(d.Summary, "pods: unknown") {
					t.Errorf("summary = %q", d.Summary)
				}
			},
		},
		{
			name:  "unreadable slices: incomplete, no endpoint claims",
			in:    serviceInput{Service: diagService(nil), Pods: []corev1.Pod{diagPod("a", true, ip)}, SlicesErr: errors.New("informer not synced")},
			codes: []string{"incomplete slices-unreadable"},
			check: func(t *testing.T, d serviceDiagnosis) {
				if !slices.Contains(d.Summary, "endpoints: unknown") {
					t.Errorf("summary = %q", d.Summary)
				}
			},
		},
		{
			name: "stale, replaced and missing endpoint targets",
			in: serviceInput{
				Service: diagService(nil),
				Pods: []corev1.Pod{
					diagPod("a", true, ip),
					diagPod("new", true, []string{"10.1.0.5"}),
					diagPod("relabeled", true, ip),
					diagPod("late", true, []string{"10.1.0.9"}),
					diagPod("no-ip", true, nil),
				},
				Slices: []discoveryv1.EndpointSlice{diagSlice("web-abc", discoveryv1.AddressTypeIPv4,
					diagEndpoint("a", "a-uid", nil, nil, nil),
					diagEndpoint("gone", "gone-uid", nil, nil, nil),
					diagEndpoint("new", "old-uid", nil, nil, nil),
					diagEndpoint("relabeled", "relabeled-uid", nil, nil, nil),
					diagEndpoint("no-ip", "no-ip-uid", nil, nil, nil),
					discoveryv1.Endpoint{Addresses: []string{"192.0.2.7"}}, // no targetRef: counted, not matched
				)},
			},
			codes: []string{"warning missing-endpoints", "warning stale-endpoints"},
			check: func(t *testing.T, d serviceDiagnosis) {
				if got, want := linkEvidence(findingByCode(t, d, "missing-endpoints")), []string{"late: IP 10.1.0.9, in no EndpointSlice"}; !reflect.DeepEqual(got, want) {
					t.Errorf("missing = %q, want %q", got, want)
				}
				got := linkEvidence(findingByCode(t, d, "stale-endpoints"))
				want := []string{
					"gone: listed in web-abc [not found]",
					"new: listed in web-abc with another UID (the pod was replaced)",
					"relabeled: listed in web-abc but not selected (labels changed or the pod finished)",
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("stale = %q, want %q", got, want)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// The relabeled pod of the stale case keeps its name but loses
			// the selector label.
			for i := range tc.in.Pods {
				if tc.in.Pods[i].Name == "relabeled" {
					tc.in.Pods[i].Labels = map[string]string{"app": "old"}
				}
			}
			d := diagnoseService(tc.in)
			if got := findingCodes(d); !reflect.DeepEqual(got, tc.codes) {
				t.Fatalf("findings = %q, want %q", got, tc.codes)
			}
			if tc.check != nil {
				tc.check(t, d)
			}
		})
	}
}

// Endpoint conditions follow the API: nil ready is ready, nil serving
// follows ready, nil terminating is not terminating.
func TestCountEndpointConditions(t *testing.T) {
	t.Parallel()
	yes, no := new(true), new(false)
	cases := []struct {
		name                 string
		ready, serving, term *bool
		wantR, wantS, wantT  int
	}{
		{name: "all unset", wantR: 1, wantS: 1},
		{name: "ready", ready: yes, wantR: 1, wantS: 1},
		{name: "not ready, serving unset", ready: no},
		{name: "terminating but serving", ready: no, serving: yes, term: yes, wantS: 1, wantT: 1},
		{name: "terminating, not serving", ready: no, serving: no, term: yes, wantT: 1},
		{name: "ready, explicitly not terminating", ready: yes, serving: yes, term: no, wantR: 1, wantS: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := countEndpoints([]discoveryv1.EndpointSlice{diagSlice("s", discoveryv1.AddressTypeIPv4, diagEndpoint("p", "u", tc.ready, tc.serving, tc.term))})
			if c.Ready != tc.wantR || c.Serving != tc.wantS || c.Terminating != tc.wantT || c.Endpoints != 1 {
				t.Fatalf("counts = %+v, want ready %d serving %d terminating %d", c, tc.wantR, tc.wantS, tc.wantT)
			}
		})
	}
	// Slices of other Services are not counted.
	other := diagSlice("x", discoveryv1.AddressTypeIPv4, diagEndpoint("p", "u", nil, nil, nil))
	other.Labels = map[string]string{discoveryv1.LabelServiceName: "api"}
	own := serviceSlices([]discoveryv1.EndpointSlice{other, diagSlice("s", discoveryv1.AddressTypeIPv4)}, "web")
	if len(own) != 1 || own[0].Name != "s" {
		t.Fatalf("own slices = %+v", own)
	}
}

func TestRenderDiagnosisRows(t *testing.T) {
	t.Parallel()
	d := diagnoseService(serviceInput{
		Service: diagService(nil),
		Pods:    []corev1.Pod{diagPod("a", true, []string{"10.1.0.1"}), diagPod("b", false, []string{"10.1.0.2"})},
		Slices: []discoveryv1.EndpointSlice{diagSlice("web-abc", discoveryv1.AddressTypeIPv4,
			diagEndpoint("a", "a-uid", nil, nil, nil), diagEndpoint("b", "b-uid", new(false), nil, nil))},
	})
	rows := renderDiagnosis(Deps{}, nil, []string{"services", "web", DiagnosisSegment}, d)
	var ids []string
	for _, r := range rows {
		id, _, _, _ := r.Columns()
		ids = append(ids, id)
	}
	want := []string{
		"summary/service", "summary/endpoints", "summary/selector", "summary/pods", "summary/ports",
		"finding/pods-not-ready", "finding/pods-not-ready/v1/pods/shop/b",
		"backend/v1/pods/shop/a", "backend/v1/pods/shop/b", "backend/discovery.k8s.io/v1/endpointslices/shop/web-abc",
		"note",
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("row IDs:\n got %q\nwant %q", ids, want)
	}
	// The pod a finding cites is an object row, so it opens like the pod.
	if _, ok := rows[6].(ObjectItem); !ok {
		t.Fatalf("finding link is not an object row: %T", rows[6])
	}
	_, cells, _, _ := rows[5].Columns()
	if cells[0] != "warning" || !strings.Contains(cells[3], "1 of 2 selected pods are not ready") {
		t.Fatalf("finding row = %q", cells)
	}
}
