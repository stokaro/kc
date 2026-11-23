package models

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"

	kccluster "github.com/stokaro/kc/internal/cluster"
)

func commandDeps() Deps { return Deps{CtxName: "dev", KubectlContext: "dev"} }

// runnableLines are the lines of copied text that a shell runs.
func runnableLines(text string) []string {
	var out []string
	for l := range strings.SplitSeq(text, "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

func TestFindingInputsDeclareWhatTheySupply(t *testing.T) {
	t.Parallel()
	m := &overviewMeasured{stats: map[string]*nodeStats{"n1": {}}}
	for _, ns := range []string{AllNamespaces, "web"} {
		r := measuredReport(t, ns, m, readyNode("n1", "4", "8Gi", 10), readyNode("n2", "4", "8Gi", 10))
		for _, f := range r.findings {
			if len(f.kubectl) > 1 && len(f.supplies) != len(f.kubectl) {
				t.Errorf("%s (%s): %d inputs, %d supplies", f.id, ns, len(f.kubectl), len(f.supplies))
			}
			line, ok := findingCommandLine(commandDeps(), f, nil)
			if !ok {
				continue
			}
			if !strings.HasPrefix(line.Label, "Input") {
				t.Errorf("%s: label %q", f.id, line.Label)
			}
			copied := line.Copy
			if copied == "" {
				copied = line.Show
			}
			// A placeholder never reaches a runnable line.
			for _, l := range runnableLines(copied) {
				if strings.Contains(l, placeholderNode) || strings.Contains(l, placeholderWorkload) {
					t.Errorf("%s: runnable line with a placeholder: %s", f.id, l)
				}
			}
			if line.Copy != "" && !strings.Contains(line.Copy, "no single command reproduces it") {
				t.Errorf("%s: copy does not say it is inputs:\n%s", f.id, line.Copy)
			}
		}
	}
}

func TestCapacityInputsAreClusterWideInANamespace(t *testing.T) {
	t.Parallel()
	r := measuredReport(t, "web", &overviewMeasured{}, readyNode("n1", "4", "8Gi", 10))
	for _, id := range []string{"capacity/cpu", "capacity/memory", "capacity/pods"} {
		line, ok := findingCommandLine(commandDeps(), mustFinding(t, r, id), nil)
		if !ok {
			t.Fatalf("%s: no command line", id)
		}
		var pods int
		for _, l := range runnableLines(line.Copy) {
			if strings.Contains(l, " -n web") {
				t.Errorf("%s: input scoped to the namespace: %s", id, l)
			}
			if strings.Contains(l, " pods ") {
				pods++
				if !strings.Contains(l, " -A") {
					t.Errorf("%s: pod input is not cluster-wide: %s", id, l)
				}
			}
		}
		if pods == 0 {
			t.Errorf("%s: no pod input in\n%s", id, line.Copy)
		}
	}
	cpu, _ := findingCommandLine(commandDeps(), mustFinding(t, r, "capacity/cpu"), nil)
	if cpu.Label != "Inputs (5)" || !strings.HasSuffix(cpu.Show, "# +4 more, M-c copies all") {
		t.Fatalf("cpu line = %q %q", cpu.Label, cpu.Show)
	}
	if got := len(runnableLines(cpu.Copy)); got != 5 {
		t.Fatalf("copied %d commands, want 5:\n%s", got, cpu.Copy)
	}
	if !strings.Contains(cpu.Copy, "# node: the measured usage of each node") {
		t.Fatalf("copy does not say what top nodes supplies:\n%s", cpu.Copy)
	}
}

func TestDiskInputsResolveTheNode(t *testing.T) {
	t.Parallel()
	m := &overviewMeasured{stats: map[string]*nodeStats{"n1": {}}}
	r := measuredReport(t, AllNamespaces, m, readyNode("n1", "4", "8Gi", 10), readyNode("n2", "4", "8Gi", 10))
	disk := mustFinding(t, r, "capacity/disk")
	want := map[string]string{
		"n1": "kubectl get --raw /api/v1/nodes/n1/proxy/stats/summary",
		"n2": "kubectl get --raw /api/v1/nodes/n2/proxy/stats/summary",
	}

	// The aggregate row copies one command per node.
	line, _ := findingCommandLine(commandDeps(), disk, nil)
	got := runnableLines(line.Copy)
	if len(got) != 2 || !strings.Contains(line.Copy, want["n1"]+"\n") || !strings.Contains(line.Copy, want["n2"]+"\n") {
		t.Fatalf("aggregate copy:\n%s", line.Copy)
	}

	// A node row shows the command of that node alone.
	for name, cmd := range want {
		target := &overviewTarget{gvr: ovNodesGVR, name: name}
		line, _ := findingCommandLine(commandDeps(), disk, target)
		if line.Show != cmd || line.Copy != "" {
			t.Errorf("%s: show %q copy %q", name, line.Show, line.Copy)
		}
	}

	// Without a node to resolve it, the command is a described template.
	empty := disk
	empty.targets = nil
	line, _ = findingCommandLine(commandDeps(), empty, nil)
	if !strings.HasPrefix(line.Show, "# for one node, replace <node>") || len(runnableLines(line.Copy)) != 0 {
		t.Fatalf("template: show %q copy\n%s", line.Show, line.Copy)
	}
}

func TestRolloutInputResolvesTheWorkload(t *testing.T) {
	t.Parallel()
	replicas := int32(2)
	d := &appsv1.Deployment{
		Namespace: "web",
		Name:      "api",
		Spec:      appsv1.DeploymentSpec{Replicas: &replicas},
		Status:    appsv1.DeploymentStatus{UpdatedReplicas: 1},
	}
	r := measuredReport(t, "web", &overviewMeasured{}, d)
	f := mustFinding(t, r, "health/workloads-rollout")
	line, _ := findingCommandLine(commandDeps(), f, &f.targets[0])
	if !strings.Contains(line.Copy, "kubectl rollout status deployments.apps/api -n web\n") {
		t.Fatalf("copy:\n%s", line.Copy)
	}
	// A row that is not the workload leaves the template described.
	line, _ = findingCommandLine(commandDeps(), f, &overviewTarget{gvr: ovNodesGVR, name: "n1"})
	if strings.Contains(strings.Join(runnableLines(line.Copy), "\n"), "rollout") {
		t.Fatalf("rollout resolved for a node:\n%s", line.Copy)
	}
}

func TestOneInputCopiesTheCommandAlone(t *testing.T) {
	t.Parallel()
	f := overviewFinding{id: "health/pods-pending", title: "/pods pending", kubectl: [][]string{kubectlGet("pods", "-n", "web", "--field-selector", "status.phase=Pending")}}
	line, ok := findingCommandLine(commandDeps(), f, nil)
	if !ok || line.Label != "Input" || line.Copy != "" || line.Show != "kubectl get pods -n web --field-selector status.phase=Pending" {
		t.Fatalf("line = %+v", line)
	}
}

func TestKubectlCommandImpersonates(t *testing.T) {
	t.Parallel()
	deps := Deps{CtxName: "prod", KubectlContext: "dev", ClusterKey: kccluster.Key{
		ImpersonateUser: "jane doe", ImpersonateUID: "42", ImpersonateGroupsKey: "dev,ops",
	}}
	want := "kubectl --context prod --as 'jane doe' --as-uid 42 --as-group dev --as-group ops get pods -A"
	if got := kubectlCommand(deps, "get", "pods", "-A"); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if got := kubectlCommand(commandDeps(), "get", "pods"); got != "kubectl get pods" {
		t.Fatalf("no impersonation: %s", got)
	}
}
