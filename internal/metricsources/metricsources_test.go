package metricsources_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/stokaro/kc/internal/metricsources"
	"github.com/stokaro/kc/internal/promhistory"
)

var now = time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)

// fakeQuerier answers each expression from a map; an expression it does
// not know is a test failure.
type fakeQuerier struct {
	t       *testing.T
	labels  map[string]string
	answers map[string]float64
	err     error
	asked   []string
}

func (f *fakeQuerier) Scalar(_ context.Context, expr string, _ time.Time) (float64, []string, error) {
	f.asked = append(f.asked, expr)
	if f.err != nil {
		return 0, nil, f.err
	}
	v, ok := f.answers[expr]
	if !ok {
		f.t.Errorf("unexpected query %s", expr)
	}
	return v, nil, nil
}

func (f *fakeQuerier) Selector(extra ...string) string {
	var m []string
	for k, v := range f.labels {
		m = append(m, promhistory.Eq(k, v))
	}
	return "{" + strings.Join(append(m, extra...), ",") + "}"
}

func (f *fakeQuerier) Labels() map[string]string { return f.labels }

func byName(checks []metricsources.Check) map[string]metricsources.Check {
	out := make(map[string]metricsources.Check)
	for _, c := range checks {
		out[c.Name] = c
	}
	return out
}

// allPresent answers every family's count with 3 series in namespace shop.
func allPresent(labels string) map[string]float64 {
	out := make(map[string]float64)
	for _, f := range metricsources.Families {
		sel := labels
		if f.Namespaced {
			sel = strings.TrimSuffix(labels, "}")
			if sel != "{" {
				sel += ","
			}
			sel += `namespace="shop"}`
		}
		out["count("+f.Metric+sel+")"] = 3
	}
	return out
}

func TestProbePrometheus(t *testing.T) {
	t.Parallel()
	t.Run("every family present, one cluster", func(t *testing.T) {
		t.Parallel()
		a := allPresent("{}")
		a[`count(count by (cluster) (kube_pod_owner{}))`] = 1
		q := &fakeQuerier{t: t, answers: a}
		got := byName(metricsources.ProbePrometheus(t.Context(), q, "mon/prom:9090", "shop", now))
		for _, f := range metricsources.Families {
			if c := got[f.Metric]; c.State != metricsources.Available {
				t.Errorf("%s: %v %q, want ok", f.Metric, c.State, c.Detail)
			}
		}
		if c := got["cluster labels"]; c.State != metricsources.Available {
			t.Errorf("cluster labels: %v, want ok", c.State)
		}
	})
	t.Run("missing kube-state-metrics only disables its families", func(t *testing.T) {
		t.Parallel()
		a := allPresent("{}")
		a[`count(kube_pod_owner{namespace="shop"})`] = 0
		a[`count(last_over_time(kube_pod_owner{namespace="shop"}[1h]))`] = 0
		a[`count(count by (cluster) (kube_pod_owner{}))`] = 0
		q := &fakeQuerier{t: t, answers: a}
		got := byName(metricsources.ProbePrometheus(t.Context(), q, "s", "shop", now))
		if c := got["kube_pod_owner"]; c.State != metricsources.Missing || !strings.Contains(c.Detail, "kube-state-metrics") {
			t.Errorf("kube_pod_owner: %v %q, want missing naming kube-state-metrics", c.State, c.Detail)
		}
		if c := got["container_cpu_usage_seconds_total"]; c.State != metricsources.Available {
			t.Errorf("CPU usage: %v, want ok: a missing owner family must not hide usage", c.State)
		}
	})
	t.Run("stale family", func(t *testing.T) {
		t.Parallel()
		a := allPresent("{}")
		a[`count(node_cpu_seconds_total{})`] = 0
		a[`count(last_over_time(node_cpu_seconds_total{}[1h]))`] = 4
		a[`count(count by (cluster) (kube_pod_owner{}))`] = 1
		q := &fakeQuerier{t: t, answers: a}
		got := byName(metricsources.ProbePrometheus(t.Context(), q, "s", "shop", now))
		if c := got["node_cpu_seconds_total"]; c.State != metricsources.Stale {
			t.Errorf("node_cpu_seconds_total: %v %q, want stale", c.State, c.Detail)
		}
	})
	t.Run("labels that match nothing", func(t *testing.T) {
		t.Parallel()
		labels := map[string]string{"cluster": "prod-eu"}
		a := allPresent(`{cluster="prod-eu"}`)
		a[`count(kube_pod_owner{cluster="prod-eu",namespace="shop"})`] = 0
		a[`count(last_over_time(kube_pod_owner{cluster="prod-eu",namespace="shop"}[1h]))`] = 0
		a[`count(kube_pod_owner{namespace="shop"})`] = 12
		q := &fakeQuerier{t: t, labels: labels, answers: a}
		got := metricsources.ProbePrometheus(t.Context(), q, "s", "shop", now)
		m := byName(got)
		if c := m["kube_pod_owner"]; c.State != metricsources.Mismatch || !strings.Contains(c.Detail, `cluster="prod-eu"`) {
			t.Errorf("kube_pod_owner: %v %q, want a label mismatch naming the labels", c.State, c.Detail)
		}
		// With labels configured the cluster-mixing check is not needed.
		if _, ok := m["cluster labels"]; ok {
			t.Error("cluster labels checked although labels are configured")
		}
		// Every query of the other families carries the label.
		for _, e := range q.asked {
			if strings.Contains(e, "container_") && !strings.Contains(e, `cluster="prod-eu"`) {
				t.Errorf("query %s lacks the configured label", e)
			}
		}
	})
	t.Run("several clusters without labels", func(t *testing.T) {
		t.Parallel()
		a := allPresent("{}")
		a[`count(count by (cluster) (kube_pod_owner{}))`] = 3
		q := &fakeQuerier{t: t, answers: a}
		c := byName(metricsources.ProbePrometheus(t.Context(), q, "s", "shop", now))["cluster labels"]
		if c.State != metricsources.Warning || !strings.Contains(c.Detail, "3 values") {
			t.Errorf("cluster labels: %v %q, want a warning about 3 clusters", c.State, c.Detail)
		}
	})
	t.Run("forbidden proxy stops after the first query", func(t *testing.T) {
		t.Parallel()
		q := &fakeQuerier{t: t, err: fmt.Errorf("prometheus s: denied: %w", promhistory.ErrForbidden)}
		got := metricsources.ProbePrometheus(t.Context(), q, "s", "shop", now)
		if len(got) != 1 || got[0].State != metricsources.Forbidden {
			t.Fatalf("got %+v, want one forbidden check", got)
		}
		if len(q.asked) != 1 {
			t.Errorf("asked %d queries, want 1", len(q.asked))
		}
	})
	t.Run("unreachable backend fails", func(t *testing.T) {
		t.Parallel()
		q := &fakeQuerier{t: t, err: errors.New("no endpoints available for service")}
		got := metricsources.ProbePrometheus(t.Context(), q, "s", "", now)
		if len(got) != 1 || got[0].State != metricsources.Failed {
			t.Fatalf("got %+v, want one failed check", got)
		}
	})
	t.Run("all namespaces does not add a namespace matcher", func(t *testing.T) {
		t.Parallel()
		a := map[string]float64{`count(count by (cluster) (kube_pod_owner{}))`: 1}
		for _, f := range metricsources.Families {
			a["count("+f.Metric+"{})"] = 1
		}
		q := &fakeQuerier{t: t, answers: a}
		for _, c := range metricsources.ProbePrometheus(t.Context(), q, "s", "", now) {
			if c.State != metricsources.Available {
				t.Errorf("%s: %v", c.Name, c.State)
			}
		}
	})
}

// kube returns a Kube whose access reviews allow what allowed names
// ("group/resource/namespace") and whose discovery serves metrics when
// metrics is true.
func kube(metrics bool, allowed ...string) (metricsources.Kube, *[]string) {
	cs := fake.NewClientset()
	reviews := new([]string)
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		r := a.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		ra := r.Spec.ResourceAttributes
		key := ra.Group + "/" + ra.Resource + "/" + ra.Namespace
		*reviews = append(*reviews, key)
		for _, k := range allowed {
			if k == key {
				r.Status.Allowed = true
			}
		}
		return true, r, nil
	})
	d := cs.Discovery().(*fakediscovery.FakeDiscovery)
	if metrics {
		d.Resources = []*metav1.APIResourceList{{GroupVersion: "metrics.k8s.io/v1beta1", APIResources: []metav1.APIResource{{Name: "pods"}}}}
	}
	return metricsources.Kube{Discovery: d, Reviews: cs.AuthorizationV1()}, reviews
}

func TestProbeKubeNamespaceOnly(t *testing.T) {
	t.Parallel()
	k, _ := kube(true, "metrics.k8s.io/pods/shop", "/pods/shop")
	got := byName(metricsources.ProbeKube(t.Context(), k, "shop"))
	if c := got["pod usage"]; c.State != metricsources.Available {
		t.Errorf("pod usage: %v %q, want ok in the namespace", c.State, c.Detail)
	}
	if c := got["pods"]; c.State != metricsources.Available {
		t.Errorf("pods: %v, want ok", c.State)
	}
	c := got["nodes"]
	if c.State != metricsources.Forbidden || !strings.Contains(c.Detail, "kubectl auth can-i list nodes") || !strings.Contains(c.Needs, "capacity") {
		t.Errorf("nodes: %v %q needs %q, want forbidden naming the check and capacity", c.State, c.Detail, c.Needs)
	}
	if c := got["node usage"]; c.State != metricsources.Forbidden {
		t.Errorf("node usage: %v, want forbidden", c.State)
	}
}

func TestProbeKubeWithoutMetricsServer(t *testing.T) {
	t.Parallel()
	k, reviews := kube(false, "/pods/", "/nodes/")
	got := byName(metricsources.ProbeKube(t.Context(), k, ""))
	if c := got["metrics API"]; c.State != metricsources.Missing || !strings.Contains(c.Detail, "metrics-server") {
		t.Errorf("metrics API: %v %q, want missing naming metrics-server", c.State, c.Detail)
	}
	if _, ok := got["pod usage"]; ok {
		t.Error("pod usage checked although the metrics API is not served")
	}
	if len(*reviews) != 2 {
		t.Errorf("reviews %v, want only pods and nodes", *reviews)
	}
}

func TestRunWithoutBackend(t *testing.T) {
	t.Parallel()
	k, _ := kube(true)
	got := metricsources.Run(t.Context(), metricsources.Request{Context: "prod", Kube: k})
	last := got[len(got)-1]
	if last.State != metricsources.NoBackend || !strings.Contains(last.Detail, "prod") {
		t.Errorf("last check %+v, want no backend for prod", last)
	}
	for _, line := range metricsources.Text(got) {
		if line == "" {
			t.Error("empty report line")
		}
	}
}

func TestCandidates(t *testing.T) {
	t.Parallel()
	svc := func(ns, name string, ports ...int32) corev1.Service {
		s := corev1.Service{Namespace: ns, Name: name}
		for _, p := range ports {
			s.Spec.Ports = append(s.Spec.Ports, corev1.ServicePort{Port: p})
		}
		return s
	}
	got := metricsources.Candidates([]corev1.Service{
		svc("monitoring", "prometheus-k8s", 9090, 8080),
		svc("monitoring", "alertmanager-main", 9093),
		svc("monitoring", "prometheus-operator", 8443), // no query port
		svc("thanos", "thanos-query", 10902, 10901),
		svc("shop", "web", 80),
	})
	want := []string{"monitoring/prometheus-k8s:9090", "thanos/thanos-query:10902"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("candidate %d = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestFindCandidatesForbidden(t *testing.T) {
	t.Parallel()
	cs := fake.NewClientset()
	cs.PrependReactor("list", "services", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, "", errors.New("no"))
	})
	_, err := metricsources.FindCandidates(t.Context(), cs.CoreV1())
	if err == nil || !strings.Contains(err.Error(), "enter the Service") {
		t.Fatalf("err = %v, want a hint to enter the Service", err)
	}
}
