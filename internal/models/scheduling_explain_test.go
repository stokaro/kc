package models

import (
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// schedNode is a node with 4 CPUs, 8Gi and 110 pods allocatable.
func schedNode(name string, labels map[string]string, mut func(*corev1.Node)) corev1.Node {
	n := corev1.Node{
		Name: name, Labels: labels,
		Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("4"),
			corev1.ResourceMemory: resource.MustParse("8Gi"),
			corev1.ResourcePods:   resource.MustParse("110"),
		}},
	}
	if mut != nil {
		mut(&n)
	}
	return n
}

// pendingPod is an unschedulable pod requesting cpu.
func schedPendingPod(cpu string, mut func(*corev1.Pod)) *corev1.Pod {
	p := dgPod("web-0", "pod-uid", "rs-1", dgUnschedulable)
	p.Spec.Containers = []corev1.Container{{Name: "app", Image: "app:1"}}
	if cpu != "" {
		p.Spec.Containers[0].Resources.Requests = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu)}
	}
	if mut != nil {
		mut(&p)
	}
	return &p
}

// boundPod runs on node and requests cpu.
func boundPod(name, node, cpu string) corev1.Pod {
	p := dgPod(name, types.UID("uid-"+name), "rs-x", nil)
	p.Spec.NodeName = node
	p.Spec.Containers = []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu)}}}}
	return p
}

func schedFinding(fs []diagFinding, code string) (diagFinding, bool) {
	i := slices.IndexFunc(fs, func(f diagFinding) bool { return f.Code == code })
	if i < 0 {
		return diagFinding{}, false
	}
	return fs[i], true
}

func schedCodes(fs []diagFinding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Code)
	}
	return out
}

func TestExplainScheduling(t *testing.T) {
	t.Parallel()
	gpuTaint := corev1.Taint{Key: "gpu", Value: "true", Effect: corev1.TaintEffectNoSchedule}
	for _, tc := range []struct {
		name string
		in   schedulingInput
		// want maps finding codes to substrings of their title.
		want map[string]string
		// sev is the severity of sched/nodes.
		sev diagSeverity
		// nodeEvidence is evidence a node link of the named finding carries.
		nodeEvidence map[string]string
	}{
		{
			name: "node selector",
			in: schedulingInput{
				Pod: schedPendingPod("", func(p *corev1.Pod) { p.Spec.NodeSelector = map[string]string{"zone": "a"} }),
				Nodes: []corev1.Node{
					schedNode("n1", map[string]string{"zone": "b"}, nil),
					schedNode("n2", map[string]string{"zone": "c"}, nil),
				},
				AssignedSkipped: true,
			},
			want: map[string]string{
				"sched/nodes":    "0 of 2 nodes pass (2 no match for spec.nodeSelector",
				"sched/affinity": "2 nodes rejected",
			},
			sev: diagWarning,
		},
		{
			name: "taints and cordon, then a node that passes",
			in: schedulingInput{
				Pod: schedPendingPod("", nil),
				Nodes: []corev1.Node{
					schedNode("gpu-1", nil, func(n *corev1.Node) { n.Spec.Taints = []corev1.Taint{gpuTaint} }),
					schedNode("cordoned", nil, func(n *corev1.Node) { n.Spec.Unschedulable = true }),
					schedNode("free", nil, nil),
				},
				AssignedSkipped: true,
			},
			want: map[string]string{
				"sched/nodes":         "1 of 3 nodes pass (rejected: 1 a taint",
				"sched/taint":         "1 nodes rejected",
				"sched/unschedulable": "cordoned",
			},
			sev:          diagInfo,
			nodeEvidence: map[string]string{"sched/taint": "taint gpu=true:NoSchedule not tolerated", "sched/nodes": "passes every check kc runs"},
		},
		{
			name: "tolerated taint passes",
			in: schedulingInput{
				Pod: schedPendingPod("", func(p *corev1.Pod) {
					p.Spec.Tolerations = []corev1.Toleration{{Key: "gpu", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}}
				}),
				Nodes:           []corev1.Node{schedNode("gpu-1", nil, func(n *corev1.Node) { n.Spec.Taints = []corev1.Taint{gpuTaint} })},
				AssignedSkipped: true,
			},
			want: map[string]string{"sched/nodes": "1 of 1 nodes pass"},
			sev:  diagInfo,
		},
		{
			name: "insufficient requests, with init container semantics",
			in: schedulingInput{
				// The init container asks for 3 CPUs, more than the app's 1;
				// the effective request is 3.
				Pod: schedPendingPod("1", func(p *corev1.Pod) {
					p.Spec.InitContainers = []corev1.Container{{Name: "init", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3")}}}}
				}),
				Nodes:    []corev1.Node{schedNode("n1", nil, nil), schedNode("n2", nil, nil)},
				Assigned: []corev1.Pod{boundPod("a", "n1", "2"), boundPod("b", "n2", "500m"), boundPod("c", "n2", "1")},
			},
			want: map[string]string{
				"sched/nodes":        "0 of 2 nodes pass (2 insufficient cpu)",
				"sched/resource/cpu": "2 nodes rejected",
			},
			sev:          diagWarning,
			nodeEvidence: map[string]string{"sched/resource/cpu": "allocatable cpu 4, requested by 1 bound pods 2, this pod 3"},
		},
		{
			name: "usage does not count, terminal pods free their requests",
			in: schedulingInput{
				Pod:   schedPendingPod("3", nil),
				Nodes: []corev1.Node{schedNode("n1", nil, nil)},
				Assigned: []corev1.Pod{func() corev1.Pod {
					p := boundPod("done", "n1", "4")
					p.Status.Phase = corev1.PodSucceeded
					return p
				}()},
			},
			want: map[string]string{"sched/nodes": "1 of 1 nodes pass"},
			sev:  diagInfo,
		},
		{
			name: "pod count",
			in: schedulingInput{
				Pod:      schedPendingPod("100m", nil),
				Nodes:    []corev1.Node{schedNode("n1", nil, func(n *corev1.Node) { n.Status.Allocatable[corev1.ResourcePods] = resource.MustParse("1") })},
				Assigned: []corev1.Pod{boundPod("a", "n1", "100m")},
			},
			want: map[string]string{"sched/pods": "too many pods"},
			sev:  diagWarning,
		},
		{
			name: "bound volume in another zone",
			in: schedulingInput{
				Pod:             schedPendingPod("", nil),
				Nodes:           []corev1.Node{schedNode("n1", map[string]string{"zone": "b"}, nil)},
				AssignedSkipped: true,
				Volumes: map[string]*corev1.PersistentVolume{"pv-1": {Name: "pv-1", Spec: corev1.PersistentVolumeSpec{NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"a"}}}}},
				}}}}},
			},
			want:         map[string]string{"sched/volume": "node affinity of a bound volume"},
			sev:          diagWarning,
			nodeEvidence: map[string]string{"sched/volume": "persistentvolume pv-1"},
		},
		{
			name: "nodes forbidden: incomplete, never no node fits",
			in: schedulingInput{
				Pod:      schedPendingPod("1", nil),
				NodesErr: apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "", nil),
			},
			want: map[string]string{"sched/nodes-unknown": "Nodes could not be read"},
		},
		{
			name: "pods of all namespaces forbidden: capacity unchecked",
			in: schedulingInput{
				Pod:         schedPendingPod("1", nil),
				Nodes:       []corev1.Node{schedNode("n1", map[string]string{"zone": "b"}, nil)},
				AssignedErr: apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", nil),
			},
			want: map[string]string{
				"sched/nodes":        "1 of 1 nodes pass",
				"sched/pods-unknown": "capacity was not checked",
			},
			sev: diagIncomplete,
		},
		{
			name: "unbound claim and pod affinity are unevaluated",
			in: schedulingInput{
				Pod: schedPendingPod("", func(p *corev1.Pod) {
					p.Spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{}}
					p.Spec.SchedulerName = "custom"
					p.Spec.Volumes = []corev1.Volume{{Name: "data", PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}
				}),
				Nodes:           []corev1.Node{schedNode("n1", nil, nil)},
				AssignedSkipped: true,
				Claims:          []corev1.PersistentVolumeClaim{{Name: "data", Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending}}},
			},
			want: map[string]string{"sched/unevaluated": "scheduler custom"},
			sev:  diagInfo,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.in.Now = diagNow
			fs := explainScheduling(tc.in)
			if _, ok := schedFinding(fs, "sched/unevaluated"); !ok {
				t.Errorf("no unevaluated finding in %v", schedCodes(fs))
			}
			for code, sub := range tc.want {
				f, ok := schedFinding(fs, code)
				if !ok {
					t.Fatalf("no %s in %v", code, schedCodes(fs))
				}
				if !strings.Contains(f.Title, sub) {
					t.Errorf("%s title %q lacks %q", code, f.Title, sub)
				}
			}
			if nodes, ok := schedFinding(fs, "sched/nodes"); ok && nodes.Severity != tc.sev {
				t.Errorf("sched/nodes severity %v, want %v", nodes.Severity, tc.sev)
			}
			for code, sub := range tc.nodeEvidence {
				f, _ := schedFinding(fs, code)
				if !slices.ContainsFunc(f.Links, func(l diagLink) bool { return l.GVR == relNodesGVR && strings.Contains(l.Evidence, sub) }) {
					t.Errorf("%s links %v lack node evidence %q", code, dgEvidence(f), sub)
				}
			}
		})
	}
}

func TestExplainSchedulingUnevaluatedNames(t *testing.T) {
	t.Parallel()
	pod := schedPendingPod("", func(p *corev1.Pod) {
		p.Spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{}}
		p.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{MaxSkew: 1}}
		p.Spec.Containers[0].Ports = []corev1.ContainerPort{{HostPort: 80, ContainerPort: 80}}
		p.Spec.Volumes = []corev1.Volume{{Name: "data", PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}
	})
	fs := explainScheduling(schedulingInput{
		Pod: pod, Now: diagNow, Nodes: []corev1.Node{schedNode("n1", nil, nil)}, AssignedSkipped: true,
		Claims: []corev1.PersistentVolumeClaim{{Name: "data", Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending}}},
	})
	f, _ := schedFinding(fs, "sched/unevaluated")
	for _, want := range []string{"pod affinity", "topology spread", "host ports", "unbound claim data", "preemption", "pod count of each node"} {
		if !strings.Contains(f.Title, want) {
			t.Errorf("unevaluated %q lacks %q", f.Title, want)
		}
	}
}

func TestSchedulingStates(t *testing.T) {
	t.Parallel()
	scheduled := dgPod("ok", "u1", "rs-1", nil)
	gated := dgPod("gated", "u2", "rs-1", func(p *corev1.Pod) {
		dgUnschedulable(p)
		p.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: "example.com/quota"}}
		p.Status.Conditions[0].Reason = corev1.PodReasonSchedulingGated
	})
	unschedulable := dgPod("pending", "u3", "rs-1", dgUnschedulable)
	in := workloadInput{GVR: podGVR, Now: diagNow}
	for _, tc := range []struct {
		pod      corev1.Pod
		state    podSchedulingState
		code     string
		severity diagSeverity
	}{
		{scheduled, schedScheduled, "", 0},
		{gated, schedGated, "gated/gated", diagWarning},
		{unschedulable, schedUnschedulable, "unschedulable/pending", diagError},
	} {
		if got := schedulingStateOf(&tc.pod); got != tc.state {
			t.Errorf("%s: state %v, want %v", tc.pod.Name, got, tc.state)
		}
		f, ok := in.schedulingFinding(&tc.pod)
		if ok != (tc.code != "") || f.Code != tc.code || (ok && f.Severity != tc.severity) {
			t.Errorf("%s: finding %q %v (ok %v), want %q %v", tc.pod.Name, f.Code, f.Severity, ok, tc.code, tc.severity)
		}
	}
	// A gated pod gets no node checks.
	if fs := explainScheduling(schedulingInput{Pod: &gated, Now: diagNow}); fs != nil {
		t.Errorf("gated pod got node checks %v", schedCodes(fs))
	}
}

// The pod diagnosis keeps the scheduler's condition and events first and
// adds kc's node checks after them.
func TestDiagnosePodWithScheduling(t *testing.T) {
	t.Parallel()
	pod := schedPendingPod("1", nil)
	ev := dgEvent("ev", pod.UID, "", corev1.EventTypeWarning, "FailedScheduling", "0/1 nodes are available: 1 Insufficient cpu.", diagNow.Add(-time.Minute))
	in := workloadInput{
		GVR: podGVR, Object: toUnstructured(t, pod), Now: diagNow, Events: []corev1.Event{ev},
		Scheduling: &schedulingInput{Pod: pod, Now: diagNow, Nodes: []corev1.Node{schedNode("n1", nil, nil)}, Assigned: []corev1.Pod{boundPod("a", "n1", "3500m")}},
	}
	d := diagWorkload(t, in)
	got := dgCodes(d)
	if len(got) < 3 || got[0] != "unschedulable/web-0" || !slices.Contains(got, "sched/nodes") || !slices.Contains(got, "sched/resource/cpu") {
		t.Fatalf("codes %v", got)
	}
	first := d.Findings[0]
	if !strings.Contains(first.Detail, "Insufficient cpu") || !slices.ContainsFunc(first.Links, func(l diagLink) bool { return strings.Contains(l.Evidence, "FailedScheduling") }) {
		t.Errorf("the scheduler's message and event are not first: %+v", first)
	}
}
