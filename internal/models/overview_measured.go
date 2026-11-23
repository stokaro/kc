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
	usageAt     time.Time                   // when the node usage was polled
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
	}
	return ""
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
		m.nodes, m.usageAt = nodes.values, nodes.at
	}
	pods, fresh := deps.Usage.metricsSnapshot(deps, usagePods, namespace, watcher, notify)
	if m.podsReason = usageReason(pods, fresh); m.podsReason == "" {
		m.pods = pods.containers
	}
	if namespace == AllNamespaces {
		m.clusterPods, m.clusterPodsReason = m.pods, m.podsReason
		return m
	}
	all, fresh := deps.Usage.metricsSnapshot(deps, usagePods, AllNamespaces, watcher, notify)
	if m.clusterPodsReason = usageReason(all, fresh); m.clusterPodsReason == "" {
		m.clusterPods = all.containers
	}
	return m
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
