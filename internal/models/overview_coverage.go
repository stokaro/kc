package models

import (
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// notServedReason is the reason the engine records for a kind the cluster
// does not serve.
const notServedReason = "not served by this cluster"

// overviewOptionalKinds are kinds of add-ons a cluster may not have. A rule
// that needs one is not applicable when the cluster does not serve it,
// rather than unavailable (ADR 0172).
var overviewOptionalKinds = map[schema.GroupVersionResource]string{
	ovCertificatesGVR: "cert-manager is not installed",
}

// overviewContextKinds are read to judge other objects (a PDB covering a
// workload, the Jobs of a CronJob, the node whose stats hold a volume).
// They do not make a rule applicable unless the rule needs nothing else.
var overviewContextKinds = []schema.GroupVersionResource{ovPDBsGVR, ovJobsGVR, ovReplicaSetsGVR, ovNodesGVR}

// overviewWindowRules count what happened in a time window. Their zero is
// a quiet window, so they always apply and do not tell whether a
// namespace has workloads.
var overviewWindowRules = []string{"activity/events-warning"}

// notApplicable marks f as having nothing to check. Its value is "none"
// and its state "n/a", so it never reads as a count of zero problems.
func notApplicable(f overviewFinding, reason string) overviewFinding {
	f.notApplicable = reason
	f.value = "none"
	f.state = "n/a"
	f.severity = severityOK
	f.targets = nil
	return f
}

// stillApplicable undoes notApplicable for a finding that lists recovered
// targets: their objects are gone, so the rules found nothing to check, yet
// the row describes them and must not read as nothing to check or fold
// into the quiet checks. Its value is the count of active targets, zero,
// and its state ok, as for any other finding whose targets recovered.
func stillApplicable(f *overviewFinding) {
	if f.notApplicable == "" {
		return
	}
	f.notApplicable = ""
	f.value = "0"
	f.state = severityOK.String()
	f.severity = severityOK
}

// optionalNotServed says which add-on a rule needs that the cluster does
// not have, or "".
func optionalNotServed(in *overviewInput, needs []schema.GroupVersionResource) string {
	for _, gvr := range needs {
		if what, ok := overviewOptionalKinds[gvr]; ok && in.missing[gvr] == notServedReason {
			return what + " (" + kubectlResource(gvr) + " is " + notServedReason + ")"
		}
	}
	return ""
}

// noSubjectsReason says that a count rule has no object of the kinds it
// checks in scope, or "" when it has. Rules over events always apply: their
// zero means a quiet window, not an absent subject.
func noSubjectsReason(in *overviewInput, needs []schema.GroupVersionResource) string {
	subjects := slices.DeleteFunc(slices.Clone(needs), func(g schema.GroupVersionResource) bool {
		return slices.Contains(overviewContextKinds, g)
	})
	if len(subjects) == 0 {
		subjects = needs
	}
	if slices.Contains(subjects, ovEventsGVR) {
		return ""
	}
	names := make([]string, 0, len(subjects))
	for _, gvr := range subjects {
		if in.scopedCount(gvr) > 0 {
			return ""
		}
		names = append(names, gvr.Resource)
	}
	return "no " + strings.Join(names, ", ") + " in " + inputScopeText(in, subjects)
}

// inputScopeText is where the kinds were looked for.
func inputScopeText(in *overviewInput, gvrs []schema.GroupVersionResource) string {
	for _, gvr := range gvrs {
		if gvr != ovNodesGVR && gvr != ovPVsGVR {
			return namespaceScopeText(in.namespace)
		}
	}
	return "the cluster"
}

// scopedCount is the number of objects of gvr the rules see: those in the
// namespace scope, or all of a cluster-scoped kind.
func (in *overviewInput) scopedCount(gvr schema.GroupVersionResource) int {
	switch gvr {
	case ovNodesGVR:
		return len(in.nodes)
	case ovPVsGVR:
		return len(in.pvs)
	case ovPodsGVR:
		// Pods are listed in every namespace for the capacity rows.
		n := 0
		for i := range in.pods {
			if in.inScope(in.pods[i].Namespace) {
				n++
			}
		}
		return n
	case ovEventsGVR:
		return len(in.events)
	case ovPVCsGVR:
		return len(in.pvcs)
	case ovQuotasGVR:
		return len(in.quotas)
	case ovDeploymentsGVR:
		return len(in.deployments)
	case ovStatefulSetsGVR:
		return len(in.statefulSets)
	case ovDaemonSetsGVR:
		return len(in.daemonSets)
	case ovJobsGVR:
		return len(in.jobs)
	case ovCronJobsGVR:
		return len(in.cronJobs)
	case ovPDBsGVR:
		return len(in.pdbs)
	case ovHPAsGVR:
		return len(in.hpas)
	case ovReplicaSetsGVR:
		return len(in.replicaSets)
	case ovCertificatesGVR:
		return len(in.certificates)
	}
	return 0
}
