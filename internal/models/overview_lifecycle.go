package models

import (
	"cmp"
	"fmt"
	"slices"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// The lifecycle of overview findings. Rules stay pure: they see the state
// as the API server dates it (a condition's lastTransitionTime, an object's
// creationTimestamp) and apply the startup grace to it. What only a
// session can know (how long kc has seen a target, that it recovered, that
// its source went away) is kept by overviewLifecycle in the engine, which
// reads each new report against the previous one.

const (
	// recoveredRetention is how long a recovered target stays listed.
	recoveredRetention = 10 * time.Minute
	// maxRecoveredPerFinding bounds the recovered targets one finding keeps.
	maxRecoveredPerFinding = 20
)

// gracedFindings are the findings whose warning-level targets get the
// startup grace: a new pod that is not ready yet, a pod still creating its
// containers, a Deployment rolling out or not yet available. Their explicit
// failures (Unschedulable, Progressing=False, an image or start error)
// bypass it.
var gracedFindings = []string{
	"health/pods-not-ready", "health/pods-pending", "health/workloads-unavailable", "health/workloads-rollout",
}

// lifecycleSections are the sections whose targets the session tracks:
// states that come and go. Capacity rows list every node, ok or not.
var lifecycleSections = []string{overviewHealth, overviewRisks}

// graced applies the startup grace to a warning-level target whose state
// the server dates at since. Inside the grace the target is "starting": it
// is listed, with severity ok, so the row neither colors nor survives the
// faults-only filter. After the grace the value says how long the state has
// lasted. An explicit failure, or a state without a server date, is left as
// it is: kc does not guess when an undated state began.
func (rc *ruleContext) graced(t overviewTarget, since time.Time, explicit bool) overviewTarget {
	t.since = since
	if since.IsZero() || t.severity == severityOK {
		return t
	}
	age := rc.now.Sub(since)
	if !explicit && age < rc.cfg.StartupGrace.Duration {
		t.starting = true
		t.severity = severityOK
		t.value += fmt.Sprintf(", starting %s (grace %s)", shortDuration(age), shortDuration(rc.cfg.StartupGrace.Duration))
		return t
	}
	t.value += ", for " + shortDuration(age)
	return t
}

// podNotReadySince dates a pod that is not ready. A pod younger than the
// grace is dated by its creation and may be starting. An older pod that
// lost readiness is dated by its Ready condition and does not get the
// grace: it is not starting.
func (rc *ruleContext) podNotReadySince(p *corev1.Pod) (time.Time, bool) {
	created := p.CreationTimestamp.Time
	if !created.IsZero() && rc.now.Sub(created) < rc.cfg.StartupGrace.Duration {
		return created, false
	}
	if c := podCondition(p, corev1.PodReady); c != nil && !c.LastTransitionTime.IsZero() {
		return c.LastTransitionTime.Time, true
	}
	return time.Time{}, true
}

// waitingFailure reports a waiting reason that is a failure, not a step of
// a normal start.
func waitingFailure(reason string) bool {
	return slices.Contains(imagePullReasons, reason) || slices.Contains(configReasons, reason) || reason == "CrashLoopBackOff"
}

// deploymentFailed reports an explicit failure of a Deployment: its
// rollout does not progress or it cannot create pods.
func deploymentFailed(d *appsv1.Deployment) bool {
	if c := deploymentCondition(d, appsv1.DeploymentProgressing); c != nil && c.Status == corev1.ConditionFalse {
		return true
	}
	c := deploymentCondition(d, appsv1.DeploymentReplicaFailure)
	return c != nil && c.Status == corev1.ConditionTrue
}

// deploymentUnavailableSince dates an unavailable Deployment by its
// Available=False condition; zero when it has none.
func deploymentUnavailableSince(d *appsv1.Deployment) time.Time {
	if c := deploymentCondition(d, appsv1.DeploymentAvailable); c != nil && c.Status == corev1.ConditionFalse {
		return c.LastTransitionTime.Time
	}
	return time.Time{}
}

// deploymentRolloutSince dates a rollout by the creation of the newest
// ReplicaSet the Deployment controls: the controller creates it when the
// rollout starts. Zero when the ReplicaSets are not known.
func (rc *ruleContext) deploymentRolloutSince(d *appsv1.Deployment) time.Time {
	var newest time.Time
	for i := range rc.in.replicaSets {
		rs := &rc.in.replicaSets[i]
		if ref := metav1.GetControllerOf(rs); ref != nil && ref.UID == d.UID && rs.CreationTimestamp.After(newest) {
			newest = rs.CreationTimestamp.Time
		}
	}
	return newest
}

// heldBelow reports whether a target below its warning percent stays
// listed: it was listed in the previous report and its value has not yet
// dropped usageHysteresisPercent points below the threshold.
func (rc *ruleContext) heldBelow(findingID string, t overviewTarget, value float64, warnPercent int) bool {
	if value >= float64(warnPercent) || value < float64(warnPercent-rc.cfg.UsageHysteresisPercent) {
		return false
	}
	return rc.life.wasActive(targetKey(findingID, t))
}

// anyStarting reports whether a target is inside its startup grace.
func anyStarting(ts []overviewTarget) bool {
	return slices.ContainsFunc(ts, func(t overviewTarget) bool { return t.starting })
}

// addGraceNotes says in the explanation of each graced finding how the
// grace applies.
func addGraceNotes(rc *ruleContext, findings []overviewFinding) {
	for i := range findings {
		if !slices.Contains(gracedFindings, findings[i].id) {
			continue
		}
		findings[i].explain += fmt.Sprintf(`

Startup grace (startupGrace, %s): a target whose state the API server dates
(the pod's Ready condition or creation, the Deployment's Available
condition) less than the grace ago is listed as "starting" with severity
ok. Later it shows how long the state has lasted. Explicit failures
(Unschedulable, Progressing=False, image and start errors) never wait.`, shortDuration(rc.cfg.StartupGrace.Duration))
	}
}

// lifecycleKey identifies one target of one finding. The UID is part of
// it: a recreated object with the same name starts a new lifecycle.
type lifecycleKey struct {
	finding   string
	gvr       schema.GroupVersionResource
	namespace string
	name      string
	uid       types.UID
}

func targetKey(findingID string, t overviewTarget) lifecycleKey {
	return lifecycleKey{finding: findingID, gvr: t.gvr, namespace: t.namespace, name: t.name, uid: t.uid}
}

// lifecycleEntry is what the session knows about one target.
type lifecycleEntry struct {
	target      overviewTarget // as last seen active
	firstSeen   time.Time
	lastSeen    time.Time
	recoveredAt time.Time // zero while active
}

// overviewLifecycle follows the targets of successive reports. It is not
// safe for concurrent apply calls; the engine computes one report at a
// time, and active is read by the rules of that compute.
type overviewLifecycle struct {
	mu      sync.Mutex
	entries map[lifecycleKey]*lifecycleEntry
}

func newOverviewLifecycle() *overviewLifecycle {
	return &overviewLifecycle{entries: make(map[lifecycleKey]*lifecycleEntry)}
}

// wasActive reports whether the target was active in the previous report.
// The rules with hysteresis use it to hold a target until its value drops
// clearly below the threshold. A nil lifecycle knows nothing.
func (l *overviewLifecycle) wasActive(k lifecycleKey) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[k]
	return ok && e.recoveredAt.IsZero()
}

// apply reads report against what the session saw before and changes it in
// place:
//
//   - an active target kc has seen for a minute or more, and whose
//     state has no server date, says how long it has been seen this session;
//   - a target that was active and is gone, in a finding that could be
//     computed, is listed as recovered (severity ok) for recoveredRetention;
//   - when a finding cannot be computed (its source is denied or not
//     synced), its last active targets are listed as unknown: a gap in the
//     observation never resolves a finding.
//
// The value and severity of a row stay those the rules computed from the
// current input.
func (l *overviewLifecycle) apply(report *overviewReport, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range report.findings {
		f := &report.findings[i]
		if !slices.Contains(lifecycleSections, f.section) {
			continue
		}
		if f.unavailable != "" {
			f.targets = append(f.targets, l.unknownTargets(f.id, f.unavailable)...)
			continue
		}
		current := make(map[lifecycleKey]bool, len(f.targets))
		names := make(map[string]bool, len(f.targets))
		for j := range f.targets {
			t := &f.targets[j]
			names[overviewTargetID(t.gvr, t.namespace, t.name)] = true
			if t.severity < severityWarning {
				continue
			}
			k := targetKey(f.id, *t)
			current[k] = true
			e, ok := l.entries[k]
			if !ok || !e.recoveredAt.IsZero() {
				e = &lifecycleEntry{firstSeen: now}
				l.entries[k] = e
			}
			e.lastSeen = now
			if t.since.IsZero() && now.Sub(e.firstSeen) >= time.Minute {
				t.value += ", seen " + shortDuration(now.Sub(e.firstSeen)) + " this session"
			}
			e.target = *t
		}
		// A kind a partial row cannot read now resolves nothing: its
		// targets stay active and are listed as unknown.
		for k, e := range l.entries {
			if k.finding == f.id && e.recoveredAt.IsZero() && slices.Contains(f.partialGVRs, k.gvr) {
				current[k] = true
			}
		}
		recovered := l.recovered(f.id, current, names, now)
		f.targets = append(f.targets, recovered...)
		if len(recovered) > 0 {
			// The objects that recovered are gone, but the check had them
			// to look at (ADR 0172).
			stillApplicable(f)
		}
		if f.partial != "" {
			f.targets = append(f.targets, l.unknownTargets(f.id, f.partial, f.partialGVRs...)...)
		}
	}
}

// recovered marks the entries of finding that are no longer active and
// returns the recovered targets still inside the retention, newest first.
// A recovered target whose name is listed again (a recreated object) is
// not shown twice.
func (l *overviewLifecycle) recovered(findingID string, current map[lifecycleKey]bool, names map[string]bool, now time.Time) []overviewTarget {
	var out []*lifecycleEntry
	for k, e := range l.entries {
		if k.finding != findingID || current[k] {
			continue
		}
		if e.recoveredAt.IsZero() {
			e.recoveredAt = now
		}
		if now.Sub(e.recoveredAt) > recoveredRetention {
			delete(l.entries, k)
			continue
		}
		if !names[overviewTargetID(k.gvr, k.namespace, k.name)] {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b *lifecycleEntry) int {
		if c := b.recoveredAt.Compare(a.recoveredAt); c != 0 {
			return c
		}
		return compareTargets(a.target, b.target)
	})
	if len(out) > maxRecoveredPerFinding {
		out = out[:maxRecoveredPerFinding]
	}
	targets := make([]overviewTarget, 0, len(out))
	for _, e := range out {
		t := e.target
		t.severity = severityOK
		t.starting = false
		t.value = fmt.Sprintf("recovered %s ago (was: %s)", shortDuration(now.Sub(e.recoveredAt)), e.target.value)
		targets = append(targets, t)
	}
	return targets
}

// unknownTargets lists the active targets of a finding whose source is not
// available now, or only those of the kinds in only. They stay active:
// nothing has shown them recovered.
func (l *overviewLifecycle) unknownTargets(findingID, reason string, only ...schema.GroupVersionResource) []overviewTarget {
	var out []overviewTarget
	for k, e := range l.entries {
		if k.finding != findingID || !e.recoveredAt.IsZero() {
			continue
		}
		if len(only) > 0 && !slices.Contains(only, k.gvr) {
			continue
		}
		t := e.target
		t.severity = severityUnknown
		t.value = fmt.Sprintf("unknown (%s); last seen %s", reason, e.target.value)
		out = append(out, t)
	}
	slices.SortFunc(out, compareTargets)
	return out
}

func compareTargets(a, b overviewTarget) int {
	if c := cmp.Compare(a.namespace, b.namespace); c != 0 {
		return c
	}
	if c := cmp.Compare(a.name, b.name); c != 0 {
		return c
	}
	return cmp.Compare(a.gvr.String(), b.gvr.String())
}
