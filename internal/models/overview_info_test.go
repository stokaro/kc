package models

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sttts/kc/pkg/appconfig"
	corev1 "k8s.io/api/core/v1"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func infoBar(t *testing.T, s InfoSummary, label string) InfoBar {
	t.Helper()
	for _, b := range s.Bars {
		if b.Label == label {
			return b
		}
	}
	t.Fatalf("no %s bar in %+v", label, s.Bars)
	return InfoBar{}
}

func TestInfoSummary(t *testing.T) {
	t.Parallel()
	pod := func(ns, name, node, mem string) *corev1.Pod {
		return onNode(withPod(overviewRunningPod(ns, name), func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources = resources("500m", mem, "", "")
		}), node)
	}
	objs := []crclient.Object{
		readyNode("mba", "4", "10Gi", 100), readyNode("n2", "4", "10Gi", 100),
		pod("a", "web", "mba", "1Gi"), pod("a", "api", "n2", "1Gi"), pod("b", "db", "mba", "1Gi"),
	}
	m := &overviewMeasured{
		usageAt: overviewTestNow,
		nodes:   map[string]usageValues{"mba": {cpu: 1000, mem: 5632 << 20}, "n2": {cpu: 400, mem: 1 << 30}},
		pods: map[string][]containerUsage{
			"a/web": {{name: "app", cpu: 100, mem: 300 << 20}},
			"a/api": {{name: "app", cpu: 50, mem: 500 << 20}},
			"b/db":  {{name: "app", cpu: 10, mem: 900 << 20}},
		},
		stats: map[string]*nodeStats{"mba": {nodeFS: fsUse{used: gib(29), capacity: gib(915)}}},
	}
	cfg := appconfig.DefaultOverview()

	t.Run("the whole scope", func(t *testing.T) {
		t.Parallel()
		r := measuredReport(t, "a", m, objs...)
		s := infoSummary(r, nil, cfg, "")
		if s.Loading || s.Err != "" || !s.UsageAt.Equal(overviewTestNow) {
			t.Fatalf("summary state = %+v", s)
		}
		cpu := infoBar(t, s, "CPU")
		if !cpu.Known || cpu.Percent != 17.5 || !strings.HasPrefix(cpu.Detail, "use 1.4/8.0 cores, req 19%") {
			t.Fatalf("cpu = %+v", cpu)
		}
		if pods := infoBar(t, s, "Pods"); pods.Detail != "3/200" {
			t.Fatalf("pods = %+v (capacity counts every pod on the nodes)", pods)
		}
		if disk := infoBar(t, s, "Disk"); !disk.Known || disk.Detail != "29/915 Gi" {
			t.Fatalf("disk = %+v", disk)
		}
		names := []string{}
		for _, c := range s.Top {
			names = append(names, c.Namespace+"/"+c.Name+" "+c.Memory)
		}
		if !slices.Equal(names, []string{"a/api 500Mi", "a/web 300Mi"}) {
			t.Fatalf("top = %v, want the scope's pods by memory", names)
		}
	})
	t.Run("one node", func(t *testing.T) {
		t.Parallel()
		r := measuredReport(t, AllNamespaces, m, objs...)
		s := infoSummary(r, nil, cfg, "mba")
		mem := infoBar(t, s, "Memory")
		if mem.Flag != flagUnderRequested || mem.Health != RowHealthNotReady || mem.Percent != 55 {
			t.Fatalf("mba memory = %+v, want under-req at 55%%", mem)
		}
		if got := []string{s.Top[0].Name, s.Top[1].Name}; len(s.Top) != 2 || !slices.Equal(got, []string{"db", "web"}) {
			t.Fatalf("top on mba = %+v", s.Top)
		}
		if n := infoSummary(r, nil, cfg, "gone"); infoBar(t, n, "CPU").Known || !strings.Contains(infoBar(t, n, "CPU").Detail, "gone is not known") {
			t.Fatalf("unknown node = %+v", n.Bars)
		}
	})
	t.Run("nothing measured", func(t *testing.T) {
		t.Parallel()
		r := measuredReport(t, AllNamespaces, &overviewMeasured{nodesReason: "x", podsReason: "metrics.k8s.io is not served by this cluster", statsReason: "forbidden: nodes/proxy"}, objs...)
		s := infoSummary(r, nil, cfg, "")
		if cpu := infoBar(t, s, "CPU"); !cpu.Known || !strings.HasSuffix(cpu.Detail, "use n/a") || cpu.Percent != 18.75 {
			t.Fatalf("cpu without usage = %+v, want the requests", cpu)
		}
		if disk := infoBar(t, s, "Disk"); disk.Known || disk.Detail != "forbidden: nodes/proxy" {
			t.Fatalf("disk = %+v", disk)
		}
		if len(s.Top) != 0 || !strings.Contains(s.TopNote, "not served") {
			t.Fatalf("top = %+v note %q", s.Top, s.TopNote)
		}
	})
	t.Run("before the first report", func(t *testing.T) {
		t.Parallel()
		if s := infoSummary(nil, nil, cfg, ""); !s.Loading {
			t.Fatalf("summary = %+v, want loading", s)
		}
		if s := infoSummary(nil, errors.New("no cluster"), cfg, ""); s.Loading || s.Err != "no cluster" {
			t.Fatalf("summary = %+v, want the error", s)
		}
	})
}

// TestOverviewInfoStartsAndCloses checks the handle the Info mode holds: a
// read starts its engine, Close stops it.
func TestOverviewInfoStartsAndCloses(t *testing.T) {
	t.Parallel()
	info := NewOverviewInfo(Deps{Ctx: t.Context()}, AllNamespaces)
	if info.engine.isRunning() {
		t.Fatal("the engine runs before the first read")
	}
	s := info.Summary(InfoFocus{})
	// Without a cluster the engine fails to connect and says so.
	for i := 0; i < 100 && s.Err == ""; i++ {
		s = info.Summary(InfoFocus{})
		if s.Err == "" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if s.Err != "no cluster" {
		t.Fatalf("summary = %+v, want the connect error", s)
	}
	info.Close()
	if info.engine.isRunning() {
		t.Fatal("the engine runs after Close")
	}
}
