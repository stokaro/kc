package models

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// healthDiagnosisNote says where a configured health comes from and what
// kc does not follow.
const healthDiagnosisNote = "The health comes from the health config. Pods are those whose controller owner reference has this object's UID; " +
	"pods of objects it creates (a StatefulSet, a Deployment) are not followed, so \"0 owned\" does not mean the operator runs nothing."

// healthFindings turns the verdict of a configured type into findings. A
// healthy verdict adds none; the summary line states it.
func (in *workloadInput) healthFindings(a *healthAdapter, v healthVerdict) []diagFinding {
	var out []diagFinding
	switch {
	case v.Err != nil:
		out = append(out, diagFinding{
			Code: "health-config", Severity: diagIncomplete, Observed: in.Now,
			Title:  "The health config cannot read this object; its health is unknown",
			Detail: fmt.Sprintf("health %q: %v.", a.key, v.Err),
			Links:  []diagLink{in.selfLink("as read")},
			Next:   "Compare the rules of the health config with the object's status (Manifest).",
		})
		return out
	case v.Rule == 0:
		out = append(out, diagFinding{
			Code: "health-unknown", Severity: diagInfo, Observed: in.Now,
			Title:  "No rule of the health config matches; the health is unknown",
			Detail: fmt.Sprintf("health %q has %d rules.", a.key, len(a.rules)),
			Links:  append([]diagLink{in.selfLink("status as read")}, in.eventLinks(in.Object.GetNamespace(), in.Object.GetUID(), "", true)...),
			Next:   "Read the status conditions (Describe); add a rule for the state they show.",
		})
	case v.RuleHealth == RowHealthFailed || v.RuleHealth == RowHealthNotReady:
		sev, title := diagWarning, "The object is not ready"
		if v.RuleHealth == RowHealthFailed {
			sev, title = diagError, "The object failed"
		}
		f := in.conditionFinding("health", sev, title, v.Matched, "", v.Reason, v.Message, metav1.NewTime(v.Since))
		f.Links[0] = in.selfLink(fmt.Sprintf("%s (rule %d) %s", v.Matched, v.Rule, v.Reason))
		if v.Since.IsZero() {
			f.Observed = in.Now
		}
		out = append(out, f)
	}
	if v.Stale {
		out = append(out, diagFinding{
			Code: "generation", Severity: diagWarning, Observed: in.Now,
			Title:  "The status does not reflect the current spec yet",
			Detail: fmt.Sprintf("metadata.generation=%d, status written for generation %d. The controller has not reported on the latest change; the health shown is not current.", v.Generation, v.Observed),
			Links:  []diagLink{in.selfLink(fmt.Sprintf("metadata.generation=%d, observedGeneration=%d", v.Generation, v.Observed))},
			Next:   "Wait for the controller, or check that it runs and watches this namespace.",
		})
	}
	return out
}
