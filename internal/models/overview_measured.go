package models

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// overviewMeasured is what the overview measured outside its informer
// cache: CPU and memory usage from metrics.k8s.io and the kubelet stats
// summary. A reason is set when that source is not known; the rules that
// need it are then dim with the reason in F3.
type overviewMeasured struct {
	// usageAt is when metrics-server measured the usage: the newest
	// sample of the node and of the cluster pod metrics, the older of the
	// two. Zero when the metrics carry no timestamp. usageWindow is the
	// window of that sample; usageReadAt is when kc read it.
	usageAt     time.Time
	usageWindow time.Duration
	usageReadAt time.Time
	nodes       map[string]usageValues      // node usage by node name
	nodesReason string                      // why node usage is not known
	pods        map[string][]containerUsage // container usage by pod key
	podsReason  string                      // why pod usage is not known
	// clusterPods is the pod usage of every namespace, which the capacity
	// rows and the Info mode sum per node. It is pods itself in an
	// all-namespaces scope, and nil while it is not known.
	clusterPods       map[string][]containerUsage
	clusterPodsReason string                // why clusterPods is not known
	stats             map[string]*nodeStats // kubelet stats by node name
	statsErrors       map[string]string     // nodes whose stats failed, with the error
	statsReason       string                // why no kubelet stats are known
}

// usageNote is the line F3 adds when the measured usage is not known.
func usageNote(reason string) string {
	if reason == "" {
		return ""
	}
	return "\n\nUsage is not available: " + reason + "."
}

// usageReason says why a usage snapshot cannot be used, or "" when it can.
func usageReason(snap *usageSnapshot, fresh bool) string {
	switch {
	case snap == nil:
		return "loading"
	case !snap.served:
		return "metrics.k8s.io is not served by this cluster"
	case snap.err != nil:
		return "metrics.k8s.io: " + snap.err.Error()
	case !fresh:
		return "loading"
	case snap.stale():
		return fmt.Sprintf("metrics-server's newest sample was %s old when read", shortDuration(snap.at.Sub(snap.sampled.newest)))
	}
	return ""
}

// stale reports whether the newest sample was already UsageStaleAfter old
// when kc read it: the list succeeded, but metrics-server stopped
// measuring.
func (s *usageSnapshot) stale() bool {
	return !s.sampled.newest.IsZero() && s.at.Sub(s.sampled.newest) >= UsageStaleAfter
}

// timed reports whether a snapshot's times belong in the age kc shows:
// a usable snapshot, or a stale one, whose age is the point.
func timed(snap *usageSnapshot, reason string) bool {
	return snap != nil && (reason == "" || snap.stale() && snap.err == nil)
}

// measuredUsage reads the latest metrics-only snapshots of the overview's
// pollers and marks the engine as their reader: notify runs after each
// poll. The pollers run while some engine reads them, and stop two
// intervals after the last read. It also reads the pod usage of every
// namespace when namespace is one namespace: the capacity rows sum the pods
// of each node in every namespace. That poller is shared by key, so an Info
// panel and /overview of any one-namespace scope read the same poll.
func measuredUsage(deps Deps, namespace string, watcher any, notify func()) *overviewMeasured {
	m := &overviewMeasured{clusterPodsReason: "not measured"}
	if deps.Usage == nil {
		m.nodesReason, m.podsReason, m.clusterPodsReason = "usage metrics are off", "usage metrics are off", "usage metrics are off"
		return m
	}
	nodes, fresh := deps.Usage.metricsSnapshot(deps, usageNodes, "", watcher, notify)
	if m.nodesReason = usageReason(nodes, fresh); m.nodesReason == "" {
		m.nodes = nodes.values
	}
	if timed(nodes, m.nodesReason) {
		m.addUsageTime(nodes)
	}
	pods, fresh := deps.Usage.metricsSnapshot(deps, usagePods, namespace, watcher, notify)
	if m.podsReason = usageReason(pods, fresh); m.podsReason == "" {
		m.pods = pods.containers
	}
	if namespace == AllNamespaces {
		m.clusterPods, m.clusterPodsReason = m.pods, m.podsReason
		if timed(pods, m.podsReason) {
			m.addUsageTime(pods)
		}
		return m
	}
	all, fresh := deps.Usage.metricsSnapshot(deps, usagePods, AllNamespaces, watcher, notify)
	if m.clusterPodsReason = usageReason(all, fresh); m.clusterPodsReason == "" {
		m.clusterPods = all.containers
	}
	if timed(all, m.clusterPodsReason) {
		m.addUsageTime(all)
	}
	return m
}

// addUsageTime folds the times of a usable snapshot into m: the oldest
// newest-sample and the oldest read win, so the age shown is never younger
// than any part of the usage.
func (m *overviewMeasured) addUsageTime(snap *usageSnapshot) {
	if m.usageReadAt.IsZero() || snap.at.Before(m.usageReadAt) {
		m.usageReadAt = snap.at
	}
	at := snap.sampled.newest
	if at.IsZero() {
		return
	}
	if m.usageAt.IsZero() || at.Before(m.usageAt) {
		m.usageAt, m.usageWindow = at, snap.sampled.window
	}
}

// containerLimitRule lists pods with a container whose usage of resource
// reaches warnPercent of its limit.
func containerLimitRule(rc *ruleContext, f overviewFinding, resource corev1.ResourceName, warnPercent int, value func(c string, pct float64, limit int64) string) overviewFinding {
	if rc.m.podsReason != "" {
		if reason := rc.in.missingReason(ovPodsGVR); reason != "" {
			f.unavailable = reason
		} else {
			f.unavailable = rc.m.podsReason
		}
		f.value, f.state, f.severity = "n/a", severityUnknown.String(), severityUnknown
		return f
	}
	return countFinding(rc, f, []schema.GroupVersionResource{ovPodsGVR}, func() []overviewTarget {
		var out []overviewTarget
		rc.scopedPods(func(p *corev1.Pod) {
			if podTerminal(p) {
				return
			}
			used := rc.m.pods[usageObjectKey(p.Namespace, p.Name)]
			if len(used) == 0 {
				return
			}
			best, bestName, bestLimit := highestLimitUse(p, used, resource)
			if bestName == "" {
				return
			}
			t := overviewTarget{gvr: ovPodsGVR, namespace: p.Namespace, name: p.Name, uid: p.UID, severity: severityWarning, rank: best}
			held := rc.heldBelow(f.id, t, best, warnPercent)
			if best < float64(warnPercent) && !held {
				return
			}
			t.value = value(bestName, best, bestLimit)
			if held {
				t.value += fmt.Sprintf(", held until below %d%%", warnPercent-rc.cfg.UsageHysteresisPercent)
			}
			out = append(out, t)
		})
		return out
	})
}

// highestLimitUse returns the container of p that uses the largest share of
// its resource limit, with that share in percent and the limit. name is empty
// when no container with a limit has usage.
func highestLimitUse(p *corev1.Pod, used []containerUsage, resource corev1.ResourceName) (pct float64, name string, limit int64) {
	pct = -1.0
	for _, c := range podLimitedContainers(p) {
		lim, ok := c.Resources.Limits[resource]
		if !ok || lim.IsZero() {
			continue
		}
		cLimit := lim.Value()
		if resource == corev1.ResourceCPU {
			cLimit = lim.MilliValue()
		}
		for _, u := range used {
			if u.name != c.Name {
				continue
			}
			use := u.mem
			if resource == corev1.ResourceCPU {
				use = u.cpu
			}
			if share := percent(use, cLimit); share > pct {
				pct, name, limit = share, c.Name, cLimit
			}
		}
	}
	return pct, name, limit
}

// podLimitedContainers returns the containers metrics cover: the app
// containers and the restartable init containers (sidecars).
func podLimitedContainers(p *corev1.Pod) []corev1.Container {
	out := append(make([]corev1.Container, 0, len(p.Spec.Containers)), p.Spec.Containers...)
	for _, c := range p.Spec.InitContainers {
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			out = append(out, c)
		}
	}
	return out
}

func podsNearMemoryLimitRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "risks/pods-memory-limit", section: overviewRisks, title: "/pods near memory limit",
		explain: fmt.Sprintf(`Pods with a container whose measured memory usage (metrics.k8s.io
PodMetrics, the working set) reaches memoryLimitWarnPercent (%d%%) of its
memory limit. The kernel kills such a container (OOMKilled) when it needs a
little more. The value shows the container closest to its limit.`, rc.cfg.MemoryLimitWarnPercent) + usageNote(rc.m.podsReason),
		kubectl: [][]string{{"top", "pods", "--containers"}},
	}
	f.kubectl[0] = append(f.kubectl[0], rc.nsArgs()...)
	return containerLimitRule(rc, f, corev1.ResourceMemory, rc.cfg.MemoryLimitWarnPercent, func(c string, pct float64, limit int64) string {
		return fmt.Sprintf("%s %.0f%% of %s", c, pct, formatMemory(limit))
	})
}

func podsAtCPULimitRule(rc *ruleContext) overviewFinding {
	f := overviewFinding{
		id: "risks/pods-cpu-limit", section: overviewRisks, title: "/pods at cpu limit",
		explain: fmt.Sprintf(`Pods with a container whose measured CPU usage (metrics.k8s.io PodMetrics)
reaches cpuLimitWarnPercent (%d%%) of its CPU limit. The kernel throttles a
container at its limit, so it runs slower than it could. The value shows
the container closest to its limit.`, rc.cfg.CPULimitWarnPercent) + usageNote(rc.m.podsReason),
		kubectl: [][]string{{"top", "pods", "--containers"}},
	}
	f.kubectl[0] = append(f.kubectl[0], rc.nsArgs()...)
	return containerLimitRule(rc, f, corev1.ResourceCPU, rc.cfg.CPULimitWarnPercent, func(c string, pct float64, limit int64) string {
		return fmt.Sprintf("%s %.0f%% of %s", c, pct, formatMilliCPU(limit))
	})
}
