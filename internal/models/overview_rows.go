package models

import (
	"cmp"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// maxUsageRows bounds UsageAmounts.Rows.
const maxUsageRows = 200

// UsageRow is one line of a breakdown of UsageAmounts: a workload with its
// pods, or one pod.
type UsageRow struct {
	// Workload is set for a workload row: the top controller of its pods.
	// A pod row has none; Owner is then the top controller of the pod,
	// when it has one.
	Workload *InfoWorkload
	Owner    *InfoWorkload
	// Namespace and Name are the workload's or the pod's.
	Namespace, Name string
	// Pods counts the pods that have not terminated, Ready those that are
	// ready, Measured the running ones with PodMetrics.
	Pods, Ready, Measured int
	// CPU and Memory are summed like UsageAmounts' own; Capacity is zero.
	CPU, Memory ResourceAmount
	// Restarts sums the restarts of the containers; OOMKilled counts the
	// containers whose last termination was an OOM kill.
	Restarts  int32
	OOMKilled int
	// Problem names why a pod is not well, the first one found: a waiting
	// reason such as CrashLoopBackOff, "Pending" or "not ready". Empty when
	// every pod is running and ready.
	Problem string
	// Containers are the containers of a pod row, init containers first;
	// a workload row has none.
	Containers []UsageContainer
}

// UsageContainer is one container of a pod: whether it is ready, how
// often it restarted, what it does now and why it last stopped.
type UsageContainer struct {
	Name     string
	Init     bool
	Ready    bool
	Restarts int32
	// State is "running", "waiting" or "terminated", with the Reason of a
	// waiting or terminated one (CrashLoopBackOff, Completed). Since is
	// when a running or terminated state began, zero when unknown.
	State, Reason string
	Since         time.Time
	// LastReason, LastExit and LastAt are of the last termination before
	// the current state; LastReason is empty when there was none.
	LastReason string
	LastExit   int32
	LastAt     time.Time
}

// containerOf reads one container status.
func containerOf(s corev1.ContainerStatus, init bool) UsageContainer {
	c := UsageContainer{Name: s.Name, Init: init, Ready: s.Ready, Restarts: s.RestartCount}
	switch st := s.State; {
	case st.Running != nil:
		c.State, c.Since = "running", st.Running.StartedAt.Time
	case st.Waiting != nil:
		c.State, c.Reason = "waiting", st.Waiting.Reason
	case st.Terminated != nil:
		c.State, c.Reason, c.Since = "terminated", st.Terminated.Reason, st.Terminated.FinishedAt.Time
	}
	if t := s.LastTerminationState.Terminated; t != nil {
		c.LastReason, c.LastExit, c.LastAt = cmp.Or(t.Reason, "exited"), t.ExitCode, t.FinishedAt.Time
	}
	return c
}

// usagePodHealth is the health of one pod that UsageRow shows.
type usagePodHealth struct {
	ready      bool
	restarts   int32
	oomKilled  int
	problem    string
	containers []UsageContainer
}

// healthOf reads the health of p from its status.
func healthOf(p *corev1.Pod) usagePodHealth {
	var h usagePodHealth
	if c := podCondition(p, corev1.PodReady); c != nil && c.Status == corev1.ConditionTrue {
		h.ready = true
	}
	for _, s := range p.Status.InitContainerStatuses {
		h.containers = append(h.containers, containerOf(s, true))
	}
	for _, s := range p.Status.ContainerStatuses {
		h.containers = append(h.containers, containerOf(s, false))
	}
	for _, s := range podStatuses(p) {
		h.restarts += s.RestartCount
		if t := s.LastTerminationState.Terminated; t != nil && t.Reason == "OOMKilled" {
			h.oomKilled++
		} else if t := s.State.Terminated; t != nil && t.Reason == "OOMKilled" {
			h.oomKilled++
		}
		if w := s.State.Waiting; w != nil && w.Reason != "" && h.problem == "" {
			h.problem = w.Reason
		}
	}
	switch {
	case h.problem != "":
	case p.Status.Phase == corev1.PodPending:
		h.problem = "Pending"
	case !h.ready:
		h.problem = "not ready"
	}
	return h
}

// usageRows groups the pods in focus: by their top workload for the scope
// and a node, one row per pod for a workload or a pod. Rows are sorted by
// memory used, most first, then by name.
func usageRows(pods []overviewPodAmount, in func(*overviewPodAmount) bool, byWorkload bool) []UsageRow {
	var rows []UsageRow
	index := make(map[InfoWorkload]int)
	for i := range pods {
		p := &pods[i]
		if !in(p) {
			continue
		}
		var top *InfoWorkload
		if n := len(p.owners); n > 0 {
			w := p.owners[n-1]
			top = &w
		}
		var r *UsageRow
		if byWorkload && top != nil {
			j, ok := index[*top]
			if !ok {
				j = len(rows)
				index[*top] = j
				rows = append(rows, UsageRow{Workload: top, Namespace: top.Namespace, Name: top.Name})
			}
			r = &rows[j]
		} else {
			rows = append(rows, UsageRow{Owner: top, Namespace: p.namespace, Name: p.name})
			r = &rows[len(rows)-1]
		}
		r.add(p)
	}
	slices.SortStableFunc(rows, func(x, y UsageRow) int {
		if c := cmp.Compare(y.Memory.Used, x.Memory.Used); c != 0 {
			return c
		}
		return cmp.Compare(x.Namespace+"/"+x.Name, y.Namespace+"/"+y.Name)
	})
	if len(rows) > maxUsageRows {
		rows = rows[:maxUsageRows]
	}
	return rows
}

// add counts pod p into r.
func (r *UsageRow) add(p *overviewPodAmount) {
	if r.Workload == nil {
		r.Containers = p.health.containers
	}
	r.Pods++
	if p.health.ready {
		r.Ready++
	}
	r.Restarts += p.health.restarts
	r.OOMKilled += p.health.oomKilled
	if r.Problem == "" {
		r.Problem = p.health.problem
	}
	r.CPU.Requests += cores(p.cpuReq)
	r.CPU.Limits += cores(p.cpuLim)
	r.Memory.Requests += float64(p.memReq)
	r.Memory.Limits += float64(p.memLim)
	r.CPU.LimitsPartial = r.CPU.LimitsPartial || p.cpuUnlimited
	r.Memory.LimitsPartial = r.Memory.LimitsPartial || p.memUnlimited
	if !p.measured {
		return
	}
	if p.running {
		r.Measured++
	}
	r.CPU.Used += cores(p.cpu)
	r.Memory.Used += float64(p.mem)
	r.CPU.UsedKnown, r.Memory.UsedKnown = true, true
}
