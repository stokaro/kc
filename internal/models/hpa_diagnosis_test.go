package models

import (
	"slices"
	"strings"
	"testing"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func hpaCPU(pct int32) autoscalingv2.MetricSpec {
	return autoscalingv2.MetricSpec{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{
		Name: corev1.ResourceCPU, Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: new(pct)},
	}}
}

func hpaCPUStatus(pct int32) autoscalingv2.MetricStatus {
	return autoscalingv2.MetricStatus{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricStatus{
		Name: corev1.ResourceCPU, Current: autoscalingv2.MetricValueStatus{AverageUtilization: new(pct)},
	}}
}

func hpaQueue(avg string) autoscalingv2.MetricSpec {
	return autoscalingv2.MetricSpec{Type: autoscalingv2.ExternalMetricSourceType, External: &autoscalingv2.ExternalMetricSource{
		Metric: autoscalingv2.MetricIdentifier{Name: "queue_depth"},
		Target: autoscalingv2.MetricTarget{Type: autoscalingv2.AverageValueMetricType, AverageValue: new(resource.MustParse(avg))},
	}}
}

// testHPA scales Deployment web between 2 and 10 on 50% CPU; it runs 4 and
// wants 4, and its status saw the current spec.
func testHPA(mut func(*autoscalingv2.HorizontalPodAutoscaler)) *autoscalingv2.HorizontalPodAutoscaler {
	h := &autoscalingv2.HorizontalPodAutoscaler{
		Namespace: "ns", Name: "web", UID: "hpa-uid", Generation: 3,
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "web"},
			MinReplicas:    new(int32(2)), MaxReplicas: 10,
			Metrics: []autoscalingv2.MetricSpec{hpaCPU(50)},
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{
			ObservedGeneration: new(int64(3)), CurrentReplicas: 4, DesiredReplicas: 4,
			CurrentMetrics: []autoscalingv2.MetricStatus{hpaCPUStatus(60)},
			Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
				{Type: autoscalingv2.AbleToScale, Status: corev1.ConditionTrue, Reason: "ReadyForNewScale"},
				{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionTrue, Reason: "ValidMetricFound"},
				{Type: autoscalingv2.ScalingLimited, Status: corev1.ConditionFalse, Reason: "DesiredWithinRange"},
			},
		},
	}
	if mut != nil {
		mut(h)
	}
	return h
}

// webTarget is Deployment web with containers app (requests cpu) and side
// (no requests).
func webTarget(sideCPU bool) *unstructured.Unstructured {
	side := map[string]any{"name": "side"}
	if sideCPU {
		side["resources"] = map[string]any{"requests": map[string]any{"cpu": "100m"}}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"namespace": "ns", "name": "web"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{
			map[string]any{"name": "app", "resources": map[string]any{"requests": map[string]any{"cpu": "200m"}}},
			side,
		}}}},
	}}
}

func hpaFinding(t *testing.T, d diagReport, code string) diagFinding {
	t.Helper()
	i := slices.IndexFunc(d.Findings, func(f diagFinding) bool { return f.Code == code })
	if i < 0 {
		t.Fatalf("no %s in %v", code, dgCodes(d))
	}
	return d.Findings[i]
}

func TestDiagnoseHPA(t *testing.T) {
	t.Parallel()
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "web", nil)
	for _, tc := range []struct {
		name   string
		hpa    *autoscalingv2.HorizontalPodAutoscaler
		target *unstructured.Unstructured
		tErr   error
		// want maps a finding code to a substring of its title and its
		// severity.
		want    map[string]string
		sev     map[string]diagSeverity
		without []string
	}{
		{
			name: "metric-driven, above target", hpa: testHPA(nil), target: webTarget(true),
			want:    map[string]string{"hpa/metric/0": "60%, target 50% average utilization (above target"},
			sev:     map[string]diagSeverity{"hpa/metric/0": diagInfo},
			without: []string{"hpa/at-max", "hpa/at-min", "hpa/requests/0", "hpa/stale"},
		},
		{
			name: "at max and capped",
			hpa: testHPA(func(h *autoscalingv2.HorizontalPodAutoscaler) {
				h.Status.CurrentReplicas, h.Status.DesiredReplicas = 10, 10
				h.Status.Conditions[2] = autoscalingv2.HorizontalPodAutoscalerCondition{Type: autoscalingv2.ScalingLimited, Status: corev1.ConditionTrue, Reason: "TooManyReplicas", Message: "the desired replica count is more than the maximum replica count"}
			}),
			target: webTarget(true),
			want:   map[string]string{"hpa/at-max": "desired 10 of max 10", "hpa/condition/ScalingLimited": "TooManyReplicas"},
			sev:    map[string]diagSeverity{"hpa/at-max": diagWarning},
		},
		{
			name: "at min",
			hpa: testHPA(func(h *autoscalingv2.HorizontalPodAutoscaler) {
				h.Status.CurrentReplicas, h.Status.DesiredReplicas = 2, 2
				h.Status.CurrentMetrics = []autoscalingv2.MetricStatus{hpaCPUStatus(10)}
			}),
			target: webTarget(true),
			want:   map[string]string{"hpa/at-min": "desired 2, min 2", "hpa/metric/0": "below target"},
		},
		{
			name: "scaling disabled",
			hpa: testHPA(func(h *autoscalingv2.HorizontalPodAutoscaler) {
				h.Status.Conditions[1] = autoscalingv2.HorizontalPodAutoscalerCondition{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "ScalingDisabled"}
			}),
			target: webTarget(true),
			want:   map[string]string{"hpa/condition/ScalingActive": "disabled"},
			sev:    map[string]diagSeverity{"hpa/condition/ScalingActive": diagInfo},
		},
		{
			name: "two metrics, one unavailable",
			hpa: testHPA(func(h *autoscalingv2.HorizontalPodAutoscaler) {
				h.Spec.Metrics = append(h.Spec.Metrics, hpaQueue("30"))
				h.Status.Conditions[1] = autoscalingv2.HorizontalPodAutoscalerCondition{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "FailedGetExternalMetric", Message: "unable to get external metric ns/queue_depth: no metrics returned"}
			}),
			target: webTarget(true),
			want: map[string]string{
				"hpa/metric/0":                "60%",
				"hpa/metric/1":                "external metric queue_depth: no current value reported (target 30 average)",
				"hpa/condition/ScalingActive": "FailedGetExternalMetric",
			},
			sev: map[string]diagSeverity{"hpa/metric/0": diagInfo, "hpa/metric/1": diagWarning, "hpa/condition/ScalingActive": diagError},
		},
		{
			name: "missing request", hpa: testHPA(nil), target: webTarget(false),
			want: map[string]string{"hpa/requests/0": "containers without one: side"},
			sev:  map[string]diagSeverity{"hpa/requests/0": diagError},
		},
		{
			name: "target forbidden", hpa: testHPA(nil), tErr: forbidden,
			want: map[string]string{"hpa/target": "could not be read"},
			sev:  map[string]diagSeverity{"hpa/target": diagIncomplete},
		},
		{
			name: "target missing", hpa: testHPA(nil), tErr: apierrors.NewNotFound(schema.GroupResource{Group: "apps", Resource: "deployments"}, "web"),
			want: map[string]string{"hpa/target": "does not exist"},
			sev:  map[string]diagSeverity{"hpa/target": diagError},
		},
		{
			name:   "stale status",
			hpa:    testHPA(func(h *autoscalingv2.HorizontalPodAutoscaler) { h.Generation = 4 }),
			target: webTarget(true),
			want:   map[string]string{"hpa/stale": "generation 3; the spec is at generation 4"},
		},
		{
			name: "stabilization reported by the controller",
			hpa: testHPA(func(h *autoscalingv2.HorizontalPodAutoscaler) {
				h.Status.Conditions[0] = autoscalingv2.HorizontalPodAutoscalerCondition{Type: autoscalingv2.AbleToScale, Status: corev1.ConditionTrue, Reason: "ScaleDownStabilized", Message: "recent recommendations were higher than current one"}
				h.Spec.Behavior = &autoscalingv2.HorizontalPodAutoscalerBehavior{ScaleDown: &autoscalingv2.HPAScalingRules{StabilizationWindowSeconds: new(int32(600))}}
			}),
			target: webTarget(true),
			want: map[string]string{
				"hpa/condition/AbleToScale": "stabilization window: ScaleDownStabilized",
				"hpa/behavior":              "scale up window 0s (default)",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := hpaInput{HPA: tc.hpa, Now: diagNow, TargetGVR: relDeploymentsGVR, Target: tc.target, TargetErr: tc.tErr}
			d := diagnoseHPA(in)
			for code, sub := range tc.want {
				f := hpaFinding(t, d, code)
				if !strings.Contains(f.Title, sub) {
					t.Errorf("%s title %q lacks %q", code, f.Title, sub)
				}
			}
			for code, sev := range tc.sev {
				if f := hpaFinding(t, d, code); f.Severity != sev {
					t.Errorf("%s severity %v, want %v", code, f.Severity, sev)
				}
			}
			for _, code := range tc.without {
				if slices.Contains(dgCodes(d), code) {
					t.Errorf("unexpected %s in %v", code, dgCodes(d))
				}
			}
			if !slices.Contains(dgCodes(d), "hpa/behavior") {
				t.Errorf("no behavior finding in %v", dgCodes(d))
			}
		})
	}
}

func TestDiagnoseHPABehaviorText(t *testing.T) {
	t.Parallel()
	disabled := autoscalingv2.DisabledPolicySelect
	h := testHPA(func(h *autoscalingv2.HorizontalPodAutoscaler) {
		h.Spec.Behavior = &autoscalingv2.HorizontalPodAutoscalerBehavior{
			ScaleUp:   &autoscalingv2.HPAScalingRules{Policies: []autoscalingv2.HPAScalingPolicy{{Type: autoscalingv2.PodsScalingPolicy, Value: 2, PeriodSeconds: 60}}},
			ScaleDown: &autoscalingv2.HPAScalingRules{SelectPolicy: &disabled},
		}
	})
	f := hpaFinding(t, diagnoseHPA(hpaInput{HPA: h, Now: diagNow, TargetGVR: relDeploymentsGVR, Target: webTarget(true)}), "hpa/behavior")
	for _, want := range []string{"2 pods per 60s", "scale down disabled (selectPolicy Disabled)"} {
		if !strings.Contains(f.Title, want) {
			t.Errorf("behavior %q lacks %q", f.Title, want)
		}
	}
}

func TestDiagnoseHPAUnknownTargetKind(t *testing.T) {
	t.Parallel()
	h := testHPA(func(h *autoscalingv2.HorizontalPodAutoscaler) {
		h.Spec.ScaleTargetRef = autoscalingv2.CrossVersionObjectReference{APIVersion: "example.com/v1", Kind: "Widget", Name: "w"}
	})
	d := diagnoseHPA(hpaInput{HPA: h, Now: diagNow})
	if f := hpaFinding(t, d, "hpa/target-unknown"); f.Severity != diagIncomplete {
		t.Errorf("severity %v", f.Severity)
	}
}

func TestDiagnoseHPAEvents(t *testing.T) {
	t.Parallel()
	h := testHPA(func(h *autoscalingv2.HorizontalPodAutoscaler) {
		h.Status.Conditions[1] = autoscalingv2.HorizontalPodAutoscalerCondition{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "FailedGetResourceMetric", LastTransitionTime: metav1.NewTime(diagNow.Add(-time.Minute))}
	})
	ev := dgEvent("e1", h.UID, "", corev1.EventTypeWarning, "FailedGetResourceMetric", "failed to get cpu utilization: unable to get metrics", diagNow)
	d := diagnoseHPA(hpaInput{HPA: h, Now: diagNow, TargetGVR: relDeploymentsGVR, Target: webTarget(true), Events: []corev1.Event{ev}})
	f := hpaFinding(t, d, "hpa/condition/ScalingActive")
	if !slices.ContainsFunc(f.Links, func(l diagLink) bool {
		return l.GVR == ovEventsGVR && strings.Contains(l.Evidence, "unable to get metrics")
	}) {
		t.Errorf("event not cited: %v", dgEvidence(f))
	}
	// Events that cannot be read are cited as unreadable, not as none.
	d = diagnoseHPA(hpaInput{HPA: h, Now: diagNow, TargetGVR: relDeploymentsGVR, Target: webTarget(true), EventsErr: apierrors.NewForbidden(schema.GroupResource{Resource: "events"}, "", nil)})
	f = hpaFinding(t, d, "hpa/condition/ScalingActive")
	if !slices.ContainsFunc(f.Links, func(l diagLink) bool { return l.GVR == ovEventsGVR && l.Problem == problemForbidden }) {
		t.Errorf("forbidden events not cited: %+v", f.Links)
	}
}
