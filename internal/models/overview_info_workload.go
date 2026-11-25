package models

import (
	"fmt"
	"slices"

	"github.com/stokaro/kc/pkg/appconfig"
)

// flagNearLimit is the state of a workload bar whose usage reaches the
// warn percent of its limits.
const flagNearLimit = "near-limit"

// workloadInfoSummary is the Info summary of one workload: CPU and memory
// of its pods against their limits (or requests when a pod has no limit),
// ready pods against the desired replicas, and its pods by memory.
func workloadInfoSummary(report *overviewReport, err error, cfgIn appconfig.OverviewConfig, w InfoWorkload) InfoSummary {
	cfg := cfgIn.Normalized()
	out := InfoSummary{Workload: &w}
	if report == nil {
		out.Loading = err == nil
		if err != nil {
			out.Err = err.Error()
		}
		return out
	}
	// Dated by the PodMetrics of the scope that the use is summed from,
	// not by the node and cluster polls the capacity rows use.
	pt := report.podsTiming
	out.UsageAt, out.UsageWindow, out.UsageReadAt = pt.newest, pt.window, pt.readAt
	out.BarsTitle, out.TopTitle = "Pods of "+w.String(), "Top pods of "+w.String()+" by memory"
	out.TopNamespace = w.Namespace
	note := ""
	switch {
	case report.workloadsReason != "":
		note = report.workloadsReason
	case w.GVR == ovDeploymentsGVR && report.replicaSetsReason != "":
		// Pods of a Deployment are owned by its ReplicaSets.
		note = "pods cannot be grouped by Deployment: " + report.replicaSetsReason
	}
	s := report.workloads[w]
	if note == "" && s == nil {
		note = w.String() + " has no pods that have not terminated"
	}
	if note != "" {
		out.Bars = []InfoBar{{Label: "CPU", Detail: note}, {Label: "Memory", Detail: note}, {Label: "Pods", Detail: note}}
		out.TopNote = "no pods"
		return out
	}

	if !s.usageAt.IsZero() {
		out.UsageAt = s.usageAt
	}
	usageNote := fmt.Sprintf("%d of %d pods measured", s.measured, s.pods)
	if f, ok := report.finding("risks/pods-memory-limit"); ok && f.unavailable != "" {
		usageNote = f.unavailable
	}
	hasUse := s.pods > 0 && s.measured == s.pods
	out.Bars = []InfoBar{
		workloadResourceBar(cfg, "CPU", s.pods, hasUse, s.cpuUse, s.cpuReq, s.cpuLim, s.cpuUnlimited, cfg.CPULimitWarnPercent, formatMilliCPU, usageNote),
		workloadResourceBar(cfg, "Memory", s.pods, hasUse, s.memUse, s.memReq, s.memLim, s.memUnlimited, cfg.MemoryLimitWarnPercent, formatMemory, usageNote),
		workloadPodsBar(s),
	}
	for _, p := range report.pods {
		if len(out.Top) == InfoTopN {
			break
		}
		if slices.Contains(p.owners, w) {
			out.Top = append(out.Top, InfoConsumer{Namespace: p.namespace, Name: p.name, CPU: formatMilliCPU(p.cpu), Memory: formatMemory(p.mem)})
		}
	}
	if len(out.Top) == 0 {
		out.TopNote = "metrics unavailable: " + usageNote
	}
	return out
}

// workloadResourceBar compares the summed usage of a workload's pods with
// their summed limits. When a pod has no limit the sum is no ceiling, and
// the bar compares with the requests instead. Without usage it shows the
// requests of the limits. The text names the pods it sums, so the sum is
// not read as the use of one pod. It is formatted from the exact sum: the
// pod rows below are each rounded down and can add up to less.
func workloadResourceBar(cfg appconfig.OverviewConfig, label string, pods int64, hasUse bool, use, req, lim int64, unlimited bool, warnPercent int, format func(int64) string, usageNote string) InfoBar {
	limited := !unlimited && lim > 0
	who := "1 pod uses"
	if pods != 1 {
		who = fmt.Sprintf("%d pods use", pods)
	}
	bar := InfoBar{Label: label, Flag: "ok", Health: severityOK.RowHealth()}
	warn := func(flag string) { bar.Flag, bar.Health = flag, severityWarning.RowHealth() }
	switch {
	case hasUse && limited:
		bar.Known, bar.Usage, bar.Percent, bar.Base = true, true, percent(use, lim), "limits"
		bar.Detail = fmt.Sprintf("%s %s of lim %s, req %s", who, format(use), format(lim), format(req))
		bar.Marks = limitMarks(req, lim)
		if bar.Percent >= float64(warnPercent) {
			warn(flagNearLimit)
		}
	case hasUse && req > 0:
		bar.Known, bar.Usage, bar.Percent, bar.Base = true, true, percent(use, req), "requests"
		bar.Detail = fmt.Sprintf("%s %s of req %s, no limit", who, format(use), format(req))
		bar.Marks = []InfoMark{{InfoMarkRequest, 100}}
		if float64(use) >= cfg.UnderRequestedFactor*float64(req) {
			warn(flagUnderRequested)
		}
	case hasUse:
		bar.Detail = fmt.Sprintf("%s %s, no requests or limits", who, format(use))
	case limited:
		bar.Known, bar.Percent = true, percent(req, lim)
		bar.Detail = fmt.Sprintf("req %s of lim %s, use n/a: %s", format(req), format(lim), usageNote)
		// The bar shows the requests: only the limit is a mark.
		bar.Marks = []InfoMark{{InfoMarkLimit, 100}}
	default:
		bar.Detail = fmt.Sprintf("req %s, no limit, use n/a: %s", format(req), usageNote)
	}
	return bar
}

// limitMarks are the marks of a bar of usage against the limits: the
// requests, when the pods have any, and the limits at the end.
func limitMarks(req, lim int64) []InfoMark {
	marks := []InfoMark{{InfoMarkLimit, 100}}
	if req > 0 {
		marks = append([]InfoMark{{InfoMarkRequest, percent(req, lim)}}, marks...)
	}
	return marks
}

// workloadPodsBar shows the ready pods of the desired replicas, or of the
// pods that run when the workload has no replica count (Jobs, CronJobs,
// ReplicaSets, which the overview reads as metadata only, and custom kinds
// without a scale subresource). A custom kind says why it has no count.
func workloadPodsBar(s *overviewWorkloadSummary) InfoBar {
	bar := InfoBar{Label: "Pods", Known: true, Flag: "ok", Health: severityOK.RowHealth()}
	of := s.pods
	if s.hasDesired {
		of = s.desired
	} else if s.desiredNote != "" {
		bar.More, bar.MoreHealth = []string{s.desiredNote}, []RowHealth{RowHealthUnknown}
	}
	switch {
	case of == 0 && s.hasDesired:
		bar.Detail = fmt.Sprintf("scaled to 0, %d pods", s.pods)
		return bar
	case of == 0:
		return InfoBar{Label: "Pods", Detail: "no pods"}
	}
	bar.Percent = percent(s.ready, of)
	if s.hasDesired {
		bar.Detail = fmt.Sprintf("%d/%d ready, %d pods", s.ready, s.desired, s.pods)
	} else {
		bar.Detail = fmt.Sprintf("%d/%d ready", s.ready, s.pods)
	}
	if s.ready < of {
		bar.Flag, bar.Health = "not ready", severityWarning.RowHealth()
	}
	return bar
}
