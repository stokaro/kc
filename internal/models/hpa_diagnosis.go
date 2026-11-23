package models

import (
	"fmt"
	"slices"
	"strings"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// hpaGVR is the HorizontalPodAutoscaler version kc explains.
var hpaGVR = schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}

// The defaults the HPA controller applies when spec.behavior leaves a
// field out (autoscaling/v2 API reference).
const (
	hpaDefaultScaleDownWindow = 300 // seconds
	hpaDefaultScaleUpWindow   = 0
)

const hpaDiagnosisNote = "Facts from the HPA's spec and status and its events, read from the cache. kc does not query the metrics APIs, " +
	"does not know the controller's recommendation history and does not predict when it scales next."

// hpaInput is what an HPA diagnosis reads. A nil Target with TargetErr
// means the scale target could not be read.
type hpaInput struct {
	HPA       *autoscalingv2.HorizontalPodAutoscaler
	Now       time.Time
	TargetGVR schema.GroupVersionResource // zero when the kind is not known
	Target    *unstructured.Unstructured
	TargetErr error
	Events    []corev1.Event
	EventsErr error
}

func (in *hpaInput) link(evidence string) diagLink {
	return diagLink{GVR: hpaGVR, Namespace: in.HPA.Namespace, Name: in.HPA.Name, Evidence: evidence}
}

func (in *hpaInput) targetLink(evidence string) diagLink {
	ref := in.HPA.Spec.ScaleTargetRef
	l := diagLink{GVR: in.TargetGVR, Namespace: in.HPA.Namespace, Name: ref.Name, Evidence: evidence}
	if in.TargetErr != nil {
		l.Problem = readProblem(in.TargetErr)
	}
	return l
}

func (in *hpaInput) events() *workloadInput {
	return &workloadInput{Events: in.Events, EventsErr: in.EventsErr}
}

// diagnoseHPA explains the HPA's current state: the replica bounds, the
// controller's conditions, each configured metric against what the status
// reports, the requests resource metrics need, and the configured
// behavior. Each finding names the field it comes from.
func diagnoseHPA(in hpaInput) diagReport {
	d := diagReport{Note: hpaDiagnosisNote}
	h := in.HPA
	if h == nil {
		return d
	}
	ref := h.Spec.ScaleTargetRef
	minR := int32(1)
	if h.Spec.MinReplicas != nil {
		minR = *h.Spec.MinReplicas
	}
	d.Summary = append(d.Summary,
		fmt.Sprintf("object: horizontalpodautoscalers.autoscaling/%s, uid %s", h.Name, h.UID),
		"read at: "+in.Now.UTC().Format(time.RFC3339),
		fmt.Sprintf("target: %s %s (%s)", ref.Kind, ref.Name, ref.APIVersion),
		fmt.Sprintf("replicas: min %d, current %d, desired %d, max %d", minR, h.Status.CurrentReplicas, h.Status.DesiredReplicas, h.Spec.MaxReplicas))
	if t := h.Status.LastScaleTime; t != nil {
		d.Summary = append(d.Summary, "last scale: "+t.UTC().Format(time.RFC3339))
	} else {
		d.Summary = append(d.Summary, "last scale: not recorded")
	}

	d.Findings = append(d.Findings, in.staleFindings()...)
	d.Findings = append(d.Findings, in.conditionFindings()...)
	d.Findings = append(d.Findings, in.boundFindings(minR)...)
	d.Findings = append(d.Findings, in.metricFindings()...)
	d.Findings = append(d.Findings, in.targetFindings()...)
	d.Findings = append(d.Findings, in.behaviorFinding())
	sortFindings(d.Findings)
	return d
}

// staleFindings reports a status that has not seen the current spec, or
// no status at all.
func (in *hpaInput) staleFindings() []diagFinding {
	h := in.HPA
	if len(h.Status.Conditions) == 0 && h.Status.ObservedGeneration == nil {
		return []diagFinding{{
			Code: "hpa/no-status", Severity: diagWarning, Observed: in.Now,
			Title:  "The HPA controller has not reported a status",
			Detail: "status.conditions is empty: the controller has not processed the HPA yet, or it does not run.",
			Links:  append([]diagLink{in.link("status.conditions empty")}, in.events().eventLinks(h.Namespace, h.UID, "", false)...),
		}}
	}
	if g := h.Status.ObservedGeneration; g != nil && *g < h.Generation {
		return []diagFinding{{
			Code: "hpa/stale", Severity: diagWarning, Observed: in.Now,
			Title:  fmt.Sprintf("The status describes generation %d; the spec is at generation %d", *g, h.Generation),
			Detail: "The findings from status below may not reflect the current spec yet.",
			Links:  []diagLink{in.link(fmt.Sprintf("status.observedGeneration=%d, metadata.generation=%d", *g, h.Generation))},
		}}
	}
	return nil
}

// conditionFindings reports the controller's own conditions with their
// reasons and messages; these are the controller's words, kept as they are.
func (in *hpaInput) conditionFindings() []diagFinding {
	h := in.HPA
	var out []diagFinding
	for _, c := range h.Status.Conditions {
		f := diagFinding{
			Code: "hpa/condition/" + string(c.Type), Observed: c.LastTransitionTime.Time, Detail: c.Message,
			Links: append([]diagLink{in.link(fmt.Sprintf("status.conditions[%s]=%s %s", c.Type, c.Status, c.Reason))},
				in.events().eventLinks(h.Namespace, h.UID, "", true)...),
		}
		switch {
		case c.Type == autoscalingv2.AbleToScale && c.Status == corev1.ConditionFalse:
			f.Severity, f.Title = diagError, "The controller cannot scale the target: "+c.Reason
			f.Next = "The message names the scale subresource read or update that failed; check the target and the controller's permissions."
		case c.Type == autoscalingv2.AbleToScale && strings.HasPrefix(c.Reason, "Backoff"):
			f.Severity, f.Title = diagInfo, "The controller reports a backoff after a recent scale: "+c.Reason
			f.Hint = "This is the controller's reported state, not kc's estimate; the API does not expose when the backoff ends."
		case c.Type == autoscalingv2.AbleToScale && c.Reason == "ScaleDownStabilized":
			f.Severity, f.Title = diagInfo, "The controller holds the replicas within the scale-down stabilization window: "+c.Reason
			f.Hint = "Reported by the controller. Its recommendations over the window are not in the API; kc does not show a countdown."
		case c.Type == autoscalingv2.ScalingActive && c.Status == corev1.ConditionFalse && c.Reason == "ScalingDisabled":
			f.Severity, f.Title = diagInfo, "Scaling is disabled: the target has 0 replicas"
			f.Next = "The HPA does not act while the target is scaled to zero by hand."
		case c.Type == autoscalingv2.ScalingActive && c.Status == corev1.ConditionFalse:
			f.Severity, f.Title = diagError, "The controller cannot compute a replica count: "+c.Reason
			f.Next = "A Failed*Metric reason means the metrics API (metrics-server or an adapter) did not answer for a metric; the message names it."
		case c.Type == autoscalingv2.ScalingLimited && c.Status == corev1.ConditionTrue:
			f.Severity, f.Title = diagWarning, "The desired replica count is capped: "+c.Reason
			f.Next = "TooManyReplicas means the metrics ask for more than maxReplicas; TooFewReplicas for fewer than minReplicas."
		default:
			continue // a condition that reports nothing wrong
		}
		out = append(out, f)
	}
	return out
}

// boundFindings reports an HPA at its maximum or minimum.
func (in *hpaInput) boundFindings(minR int32) []diagFinding {
	h := in.HPA
	evidence := fmt.Sprintf("status.currentReplicas=%d, status.desiredReplicas=%d, spec.minReplicas=%d, spec.maxReplicas=%d",
		h.Status.CurrentReplicas, h.Status.DesiredReplicas, minR, h.Spec.MaxReplicas)
	switch {
	case h.Spec.MaxReplicas > 0 && h.Status.DesiredReplicas >= h.Spec.MaxReplicas:
		return []diagFinding{{
			Code: "hpa/at-max", Severity: diagWarning, Observed: in.Now,
			Title: fmt.Sprintf("At maxReplicas: desired %d of max %d", h.Status.DesiredReplicas, h.Spec.MaxReplicas),
			Links: []diagLink{in.link(evidence)},
			Next:  "If the metrics below stay above target, raise maxReplicas or reduce the load per pod.",
		}}
	case h.Status.DesiredReplicas > 0 && h.Status.DesiredReplicas <= minR:
		return []diagFinding{{
			Code: "hpa/at-min", Severity: diagInfo, Observed: in.Now,
			Title: fmt.Sprintf("At minReplicas: desired %d, min %d", h.Status.DesiredReplicas, minR),
			Links: []diagLink{in.link(evidence)},
		}}
	}
	return nil
}

// metricKey identifies a metric in spec and status alike.
func metricKey(t autoscalingv2.MetricSourceType, resource corev1.ResourceName, container string, metric *autoscalingv2.MetricIdentifier, object *autoscalingv2.CrossVersionObjectReference) string {
	parts := []string{string(t)}
	switch {
	case resource != "":
		parts = append(parts, string(resource), container)
	case metric != nil:
		parts = append(parts, metric.Name)
		if metric.Selector != nil {
			parts = append(parts, metric.Selector.String())
		}
	}
	if object != nil {
		parts = append(parts, object.Kind, object.Name)
	}
	return strings.Join(parts, "|")
}

func specMetricKey(m autoscalingv2.MetricSpec) (key, label string, target autoscalingv2.MetricTarget) {
	switch {
	case m.Resource != nil:
		return metricKey(m.Type, m.Resource.Name, "", nil, nil), "resource " + string(m.Resource.Name), m.Resource.Target
	case m.ContainerResource != nil:
		return metricKey(m.Type, m.ContainerResource.Name, m.ContainerResource.Container, nil, nil),
			fmt.Sprintf("resource %s of container %s", m.ContainerResource.Name, m.ContainerResource.Container), m.ContainerResource.Target
	case m.Pods != nil:
		return metricKey(m.Type, "", "", &m.Pods.Metric, nil), "pods metric " + m.Pods.Metric.Name, m.Pods.Target
	case m.Object != nil:
		return metricKey(m.Type, "", "", &m.Object.Metric, &m.Object.DescribedObject),
			fmt.Sprintf("object metric %s of %s %s", m.Object.Metric.Name, m.Object.DescribedObject.Kind, m.Object.DescribedObject.Name), m.Object.Target
	case m.External != nil:
		return metricKey(m.Type, "", "", &m.External.Metric, nil), "external metric " + m.External.Metric.Name, m.External.Target
	}
	return string(m.Type), string(m.Type), autoscalingv2.MetricTarget{}
}

func statusMetricKey(m autoscalingv2.MetricStatus) (string, autoscalingv2.MetricValueStatus) {
	switch {
	case m.Resource != nil:
		return metricKey(m.Type, m.Resource.Name, "", nil, nil), m.Resource.Current
	case m.ContainerResource != nil:
		return metricKey(m.Type, m.ContainerResource.Name, m.ContainerResource.Container, nil, nil), m.ContainerResource.Current
	case m.Pods != nil:
		return metricKey(m.Type, "", "", &m.Pods.Metric, nil), m.Pods.Current
	case m.Object != nil:
		return metricKey(m.Type, "", "", &m.Object.Metric, &m.Object.DescribedObject), m.Object.Current
	case m.External != nil:
		return metricKey(m.Type, "", "", &m.External.Metric, nil), m.External.Current
	}
	return string(m.Type), autoscalingv2.MetricValueStatus{}
}

func targetText(t autoscalingv2.MetricTarget) string {
	switch t.Type {
	case autoscalingv2.UtilizationMetricType:
		if t.AverageUtilization != nil {
			return fmt.Sprintf("%d%% average utilization", *t.AverageUtilization)
		}
	case autoscalingv2.AverageValueMetricType:
		if t.AverageValue != nil {
			return t.AverageValue.String() + " average"
		}
	case autoscalingv2.ValueMetricType:
		if t.Value != nil {
			return t.Value.String()
		}
	}
	return "target " + string(t.Type)
}

// currentText is the reported value in the target's terms, and the ratio
// of current to target when both are known.
func currentText(t autoscalingv2.MetricTarget, c autoscalingv2.MetricValueStatus) (string, float64, bool) {
	switch t.Type {
	case autoscalingv2.UtilizationMetricType:
		if c.AverageUtilization != nil && t.AverageUtilization != nil && *t.AverageUtilization > 0 {
			return fmt.Sprintf("%d%%", *c.AverageUtilization), float64(*c.AverageUtilization) / float64(*t.AverageUtilization), true
		}
	case autoscalingv2.AverageValueMetricType:
		if c.AverageValue != nil && t.AverageValue != nil && !t.AverageValue.IsZero() {
			return c.AverageValue.String(), c.AverageValue.AsApproximateFloat64() / t.AverageValue.AsApproximateFloat64(), true
		}
	case autoscalingv2.ValueMetricType:
		if c.Value != nil && t.Value != nil && !t.Value.IsZero() {
			return c.Value.String(), c.Value.AsApproximateFloat64() / t.Value.AsApproximateFloat64(), true
		}
	}
	return "", 0, false
}

// metricFindings compares each configured metric with what the status
// reports. A metric without a reported value is unknown, not zero.
func (in *hpaInput) metricFindings() []diagFinding {
	h := in.HPA
	current := make(map[string]autoscalingv2.MetricValueStatus)
	for _, m := range h.Status.CurrentMetrics {
		k, v := statusMetricKey(m)
		current[k] = v
	}
	var out []diagFinding
	for i, m := range h.Spec.Metrics {
		key, label, target := specMetricKey(m)
		field := fmt.Sprintf("spec.metrics[%d]", i)
		f := diagFinding{Code: fmt.Sprintf("hpa/metric/%d", i), Observed: in.Now}
		cur, reported := current[key]
		text, ratio, ok := currentText(target, cur)
		switch {
		case !reported || !ok:
			f.Severity = diagWarning
			f.Title = fmt.Sprintf("Metric %s: no current value reported (target %s)", label, targetText(target))
			f.Detail = "status.currentMetrics has no value for this metric; the controller could not read it, or has not yet. See the ScalingActive condition and the events for the API error."
			f.Links = append([]diagLink{in.link(field + " has no matching status.currentMetrics entry")}, in.events().eventLinks(h.Namespace, h.UID, "", true)...)
		default:
			f.Severity = diagInfo
			dir := "at target"
			switch {
			case ratio > 1.0:
				dir = "above target: asks for more replicas"
			case ratio < 1.0:
				dir = "below target: asks for fewer replicas"
			}
			f.Title = fmt.Sprintf("Metric %s: %s, target %s (%s)", label, text, targetText(target), dir)
			f.Detail = fmt.Sprintf("Ratio %.2f. The controller ignores a ratio within its tolerance (0.1 by default) and applies the behavior below.", ratio)
			f.Links = []diagLink{in.link(fmt.Sprintf("%s target, status.currentMetrics current %s", field, text))}
		}
		out = append(out, f)
	}
	if len(h.Spec.Metrics) == 0 {
		out = append(out, diagFinding{
			Code: "hpa/metric/default", Severity: diagInfo, Observed: in.Now,
			Title: "No metrics configured: the controller uses 80% average CPU utilization",
			Links: []diagLink{in.link("spec.metrics empty")},
		})
	}
	return out
}

// targetFindings reads the scale target: whether it exists, and whether
// its pod template has the requests resource metrics divide by.
func (in *hpaInput) targetFindings() []diagFinding {
	h := in.HPA
	ref := h.Spec.ScaleTargetRef
	if in.TargetGVR.Resource == "" {
		return []diagFinding{{
			Code: "hpa/target-unknown", Severity: diagIncomplete,
			Title:  fmt.Sprintf("kc does not know the target kind %s (%s)", ref.Kind, ref.APIVersion),
			Detail: "The API server does not serve it, or discovery failed; requests were not checked.",
			Links:  []diagLink{in.link("spec.scaleTargetRef " + ref.Kind + "/" + ref.Name)},
		}}
	}
	if in.TargetErr != nil {
		sev, title := diagIncomplete, fmt.Sprintf("The target %s %s could not be read; its requests are unknown", ref.Kind, ref.Name)
		if readProblem(in.TargetErr) == problemNotFound {
			sev, title = diagError, fmt.Sprintf("The target %s %s does not exist", ref.Kind, ref.Name)
		}
		return []diagFinding{{
			Code: "hpa/target", Severity: sev, Title: title, Detail: in.TargetErr.Error(), Observed: in.Now,
			Links: []diagLink{in.link("spec.scaleTargetRef"), in.targetLink("scale target")},
		}}
	}
	containers, found, err := unstructured.NestedSlice(in.Target.Object, "spec", "template", "spec", "containers")
	if err != nil || !found {
		var out []diagFinding
		if needsRequests(h.Spec.Metrics) {
			out = append(out, diagFinding{
				Code: "hpa/requests-unknown", Severity: diagIncomplete,
				Title: fmt.Sprintf("kc cannot read a pod template in %s %s; the requests resource metrics need were not checked", ref.Kind, ref.Name),
				Links: []diagLink{in.targetLink("no spec.template.spec.containers")},
			})
		}
		return out
	}
	var out []diagFinding
	for i, m := range h.Spec.Metrics {
		var res corev1.ResourceName
		container := ""
		switch {
		case m.Resource != nil && m.Resource.Target.Type == autoscalingv2.UtilizationMetricType:
			res = m.Resource.Name
		case m.ContainerResource != nil && m.ContainerResource.Target.Type == autoscalingv2.UtilizationMetricType:
			res, container = m.ContainerResource.Name, m.ContainerResource.Container
		default:
			continue
		}
		var missing []string
		seen := false
		for _, c := range containers {
			cm, _ := c.(map[string]any)
			name, _, _ := unstructured.NestedString(cm, "name")
			if container != "" && name != container {
				continue
			}
			seen = true
			if _, ok, _ := unstructured.NestedFieldNoCopy(cm, "resources", "requests", string(res)); !ok {
				missing = append(missing, name)
			}
		}
		field := fmt.Sprintf("spec.metrics[%d]", i)
		switch {
		case container != "" && !seen:
			out = append(out, diagFinding{
				Code: fmt.Sprintf("hpa/requests/%d", i), Severity: diagError, Observed: in.Now,
				Title: fmt.Sprintf("Container %s named by %s is not in the pod template of %s %s", container, field, ref.Kind, ref.Name),
				Links: []diagLink{in.link(field + ".containerResource.container=" + container), in.targetLink("spec.template.spec.containers")},
			})
		case len(missing) > 0:
			slices.Sort(missing)
			out = append(out, diagFinding{
				Code: fmt.Sprintf("hpa/requests/%d", i), Severity: diagError, Observed: in.Now,
				Title:  fmt.Sprintf("%s utilization needs a %s request; containers without one: %s", res, res, strings.Join(missing, ", ")),
				Detail: "Utilization is usage divided by the request. Without a request on every container the metric counts, the controller cannot compute it.",
				Links:  []diagLink{in.link(field), in.targetLink("spec.template.spec.containers[*].resources.requests." + string(res) + " missing")},
				Next:   "Set the request in the pod template, or use an AverageValue target.",
			})
		}
	}
	return out
}

func needsRequests(metrics []autoscalingv2.MetricSpec) bool {
	return slices.ContainsFunc(metrics, func(m autoscalingv2.MetricSpec) bool {
		return (m.Resource != nil && m.Resource.Target.Type == autoscalingv2.UtilizationMetricType) ||
			(m.ContainerResource != nil && m.ContainerResource.Target.Type == autoscalingv2.UtilizationMetricType)
	})
}

// behaviorFinding states the configured scale-up and scale-down behavior,
// with the API defaults for what is left out. It is configuration, not a
// report of what the controller is doing.
func (in *hpaInput) behaviorFinding() diagFinding {
	var b autoscalingv2.HorizontalPodAutoscalerBehavior
	if in.HPA.Spec.Behavior != nil {
		b = *in.HPA.Spec.Behavior
	}
	up := rulesText(b.ScaleUp, hpaDefaultScaleUpWindow, "policies default: double, or +4 pods, per 15s")
	down := rulesText(b.ScaleDown, hpaDefaultScaleDownWindow, "policies default: up to 100% per 15s")
	return diagFinding{
		Code: "hpa/behavior", Severity: diagInfo,
		Title:  "Configured behavior: scale up " + up + "; scale down " + down,
		Detail: "Configuration from spec.behavior, with the API defaults for omitted fields. The stabilization window uses the controller's past recommendations, which the API does not expose.",
		Links:  []diagLink{in.link("spec.behavior")},
	}
}

func rulesText(r *autoscalingv2.HPAScalingRules, defWindow int32, defPolicies string) string {
	if r == nil {
		return fmt.Sprintf("window %ds (default), %s", defWindow, defPolicies)
	}
	parts := make([]string, 0, 4)
	if r.StabilizationWindowSeconds != nil {
		parts = append(parts, fmt.Sprintf("window %ds", *r.StabilizationWindowSeconds))
	} else {
		parts = append(parts, fmt.Sprintf("window %ds (default)", defWindow))
	}
	if r.SelectPolicy != nil {
		if *r.SelectPolicy == autoscalingv2.DisabledPolicySelect {
			return "disabled (selectPolicy Disabled)"
		}
		parts = append(parts, "select "+string(*r.SelectPolicy))
	}
	if len(r.Policies) == 0 {
		parts = append(parts, defPolicies)
	}
	for _, p := range r.Policies {
		parts = append(parts, fmt.Sprintf("%d %s per %ds", p.Value, strings.ToLower(string(p.Type)), p.PeriodSeconds))
	}
	if r.Tolerance != nil {
		parts = append(parts, "tolerance "+r.Tolerance.String())
	}
	return strings.Join(parts, ", ")
}
