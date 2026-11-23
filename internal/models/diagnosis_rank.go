package models

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

const (
	// maxSymptomGroups bounds the symptoms a workload diagnosis expands,
	// so a DaemonSet on a large cluster stays readable.
	maxSymptomGroups = 10
	// maxGroupPods bounds the pods one symptom cites.
	maxGroupPods = 20
	// maxHiddenSymptoms bounds the links of the finding that lists the
	// symptoms left unexpanded.
	maxHiddenSymptoms = 50
)

// symptomGroup is one symptom shared by one or more pods: the same
// severity, the same finding code apart from the pod's name, and the same
// title apart from the pod's name. Equal symptoms are grouped; that says
// nothing about a shared cause.
type symptomGroup struct {
	key     string
	code    string // the code with the pod's name replaced by "*"
	finding diagFinding
	pods    []*corev1.Pod
	differ  bool // a member's detail differs from the first one's
}

// symptomKey is the grouping key of f, a finding about pod.
func symptomKey(f diagFinding, pod string) (key, code string) {
	parts := strings.Split(f.Code, "/")
	for i, p := range parts {
		if p == pod {
			parts[i] = "*"
		}
	}
	code = strings.Join(parts, "/")
	return f.Severity.String() + "\x00" + code + "\x00" + strings.ReplaceAll(f.Title, pod, "\x01"), code
}

// groupSymptoms runs the same checks on every pod and groups equal
// findings. pods are sorted by name, so the first member of a group, whose
// finding represents it, is the same on every identical read.
func (in *workloadInput) groupSymptoms(pods []corev1.Pod) (groups []*symptomGroup, affected int) {
	byKey := make(map[string]*symptomGroup)
	for i := range pods {
		pod := &pods[i]
		fs := in.podFindings(pod)
		if len(fs) > 0 {
			affected++
		}
		for _, f := range fs {
			key, code := symptomKey(f, pod.Name)
			g := byKey[key]
			if g == nil {
				g = &symptomGroup{key: key, code: code, finding: f}
				byKey[key] = g
				groups = append(groups, g)
			} else if f.Detail != g.finding.Detail {
				g.differ = true
			}
			if len(g.pods) == 0 || g.pods[len(g.pods)-1] != pod {
				g.pods = append(g.pods, pod)
			}
		}
	}
	// Most severe first; then the symptom more pods share; then the first
	// affected pod's name; then the code. Every key is from the read
	// objects, so identical reads give identical order.
	slices.SortStableFunc(groups, func(a, b *symptomGroup) int {
		return cmp.Or(
			cmp.Compare(b.finding.Severity, a.finding.Severity),
			cmp.Compare(len(b.pods), len(a.pods)),
			cmp.Compare(a.pods[0].Name, b.pods[0].Name),
			cmp.Compare(a.code, b.code),
		)
	})
	return groups, affected
}

// render is the group as one finding: the first pod's finding, with the
// other pods cited when several share it.
func (g *symptomGroup) render() diagFinding {
	f := g.finding
	if len(g.pods) == 1 {
		return f
	}
	f.Code = "group/" + g.code
	f.Title = fmt.Sprintf("%s (%d pods)", f.Title, len(g.pods))
	f.Links = slices.Clone(f.Links)
	for i, p := range g.pods[1:] {
		if i == maxGroupPods-1 {
			break
		}
		f.Links = append(f.Links, diagLink{GVR: podGVR, Namespace: p.Namespace, Name: p.Name, Evidence: "same symptom, uid " + string(p.UID)})
	}
	var notes []string
	notes = append(notes, fmt.Sprintf("%d pods show this symptom; the evidence is from pod %s.", len(g.pods), g.pods[0].Name))
	if g.differ {
		notes = append(notes, "Their messages differ; open a pod's own /diagnosis for its message.")
	}
	if hidden := len(g.pods) - maxGroupPods; hidden > 0 {
		notes = append(notes, fmt.Sprintf("%d more pods are not cited.", hidden))
	}
	f.Detail = strings.TrimSpace(f.Detail + "\n" + strings.Join(notes, " "))
	return f
}

// hiddenSymptoms is the finding that names the symptoms beyond the bound,
// each with a link to its first pod.
func hiddenSymptoms(groups []*symptomGroup) diagFinding {
	f := diagFinding{
		Code: "more-symptoms", Severity: diagInfo,
		Title:  fmt.Sprintf("%d more symptoms are not expanded; only the %d most severe are", len(groups), maxSymptomGroups),
		Detail: "Each link below is the first pod with one of them; Enter opens it.",
	}
	for i, g := range groups {
		if i == maxHiddenSymptoms {
			f.Detail += fmt.Sprintf(" %d more are not listed; open the workload's pods.", len(groups)-maxHiddenSymptoms)
			break
		}
		p := g.pods[0]
		f.Links = append(f.Links, diagLink{GVR: podGVR, Namespace: p.Namespace, Name: p.Name,
			Evidence: fmt.Sprintf("%s: %s (%d pods)", g.finding.Severity, g.finding.Title, len(g.pods))})
	}
	return f
}
