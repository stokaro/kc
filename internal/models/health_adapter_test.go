package models

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stokaro/kc/pkg/appconfig"
)

// Two condition layouts and a field layout, as the README examples spell
// them.
var (
	// fluxRules: Ready, Reconciling and Stalled conditions (kstatus).
	fluxRules = []appconfig.HealthRule{
		{Condition: "Stalled", Health: appconfig.HealthFailed},
		{Condition: "Reconciling", Health: appconfig.HealthNotReady},
		{Condition: "Ready", Health: appconfig.HealthOK},
		{Condition: "Ready", Status: "False", Health: appconfig.HealthNotReady},
	}
	// crossplaneRules: Synced and Ready conditions.
	crossplaneRules = []appconfig.HealthRule{
		{Condition: "Synced", Status: "False", Health: appconfig.HealthFailed},
		{Condition: "Ready", Health: appconfig.HealthOK},
		{Condition: "Ready", Status: "False", Health: appconfig.HealthNotReady},
	}
	// argoRules: a status field instead of conditions.
	argoRules = []appconfig.HealthRule{
		{JSONPath: ".status.health.status", Value: "Healthy", Health: appconfig.HealthOK},
		{JSONPath: ".status.health.status", Value: "Progressing", Health: appconfig.HealthNotReady},
		{JSONPath: ".status.health.status", Value: "Degraded", Health: appconfig.HealthFailed},
	}
)

func testAdapter(t *testing.T, rules []appconfig.HealthRule) *healthAdapter {
	t.Helper()
	if errs := appconfig.ValidateHealthEntry("example.com/v1/widgets", rules); len(errs) > 0 {
		t.Fatalf("rules: %v", errs)
	}
	a := &healthAdapter{key: "example.com/v1/widgets"}
	for _, r := range rules {
		a.rules = append(a.rules, compileHealthRule(r))
	}
	return a
}

// installHealth installs cfg for one test and restores the previous
// adapters after it. Tests that call it must not run in parallel.
func installHealth(t *testing.T, cfg map[string][]appconfig.HealthRule) []error {
	t.Helper()
	prev := healthAdapters.Load()
	t.Cleanup(func() { healthAdapters.Store(prev) })
	return InstallHealthAdapters(cfg)
}

// cond is a status condition as the API server returns it.
func cond(typ, status, reason string, extra ...any) map[string]any {
	c := map[string]any{"type": typ, "status": status, "reason": reason, "message": reason + " message",
		"lastTransitionTime": "2026-11-23T11:00:00Z"}
	for i := 0; i+1 < len(extra); i += 2 {
		c[extra[i].(string)] = extra[i+1]
	}
	return c
}

// widget is an object with generation gen and the given status (nil for
// none).
func widget(gen int64, status map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.com/v1", "kind": "Widget",
		"metadata": map[string]any{"namespace": "ns", "name": "w", "uid": "w-uid", "generation": gen},
	}}
	if status != nil {
		u.Object["status"] = status
	}
	return u
}

func conds(cs ...map[string]any) map[string]any {
	list := make([]any, len(cs))
	for i, c := range cs {
		list[i] = c
	}
	return map[string]any{"conditions": list}
}

func TestHealthAdapterEvaluate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		rules   []appconfig.HealthRule
		obj     *unstructured.Unstructured
		want    RowHealth
		rule    int
		reason  string
		stale   bool
		wantErr string
	}{
		{name: "flux ready", rules: fluxRules, obj: widget(1, conds(cond("Ready", "True", "ReconciliationSucceeded"))),
			want: RowHealthOK, rule: 3, reason: "ReconciliationSucceeded"},
		{name: "flux reconciling wins over an old ready", rules: fluxRules,
			obj:  widget(1, conds(cond("Ready", "True", "ReconciliationSucceeded"), cond("Reconciling", "True", "Progressing"))),
			want: RowHealthNotReady, rule: 2, reason: "Progressing"},
		{name: "flux stalled", rules: fluxRules,
			obj:  widget(1, conds(cond("Ready", "False", "BuildFailed"), cond("Stalled", "True", "BuildFailed"))),
			want: RowHealthFailed, rule: 1, reason: "BuildFailed"},
		{name: "flux ready unknown matches no rule", rules: fluxRules, obj: widget(1, conds(cond("Ready", "Unknown", "Progressing"))),
			want: RowHealthUnknown},
		{name: "condition status compares in any case", rules: fluxRules, obj: widget(1, conds(cond("Ready", "true", "Done"))),
			want: RowHealthOK, rule: 3},
		{name: "condition type is case-sensitive", rules: fluxRules, obj: widget(1, conds(cond("ready", "True", "Done"))),
			want: RowHealthUnknown},
		{name: "crossplane synced false", rules: crossplaneRules,
			obj:  widget(1, conds(cond("Ready", "True", "Available"), cond("Synced", "False", "ReconcileError"))),
			want: RowHealthFailed, rule: 1, reason: "ReconcileError"},
		{name: "crossplane ready", rules: crossplaneRules,
			obj:  widget(1, conds(cond("Synced", "True", "ReconcileSuccess"), cond("Ready", "True", "Available"))),
			want: RowHealthOK, rule: 2, reason: "Available"},
		{name: "argo field healthy", rules: argoRules, obj: widget(1, map[string]any{"health": map[string]any{"status": "Healthy"}}),
			want: RowHealthOK, rule: 1},
		{name: "argo field degraded", rules: argoRules, obj: widget(1, map[string]any{"health": map[string]any{"status": "Degraded"}}),
			want: RowHealthFailed, rule: 3},
		{name: "number field", rules: []appconfig.HealthRule{{JSONPath: ".status.replicas", Value: "0", Health: appconfig.HealthNotReady}},
			obj: widget(1, map[string]any{"replicas": int64(0)}), want: RowHealthNotReady, rule: 1},
		{name: "no status", rules: fluxRules, obj: widget(1, nil), want: RowHealthUnknown},
		{name: "no status for a field rule", rules: argoRules, obj: widget(1, nil), want: RowHealthUnknown},
		{name: "nil object", rules: fluxRules, want: RowHealthUnknown},

		{name: "older observedGeneration is not current success", rules: fluxRules,
			obj:  widget(3, map[string]any{"observedGeneration": int64(2), "conditions": []any{cond("Ready", "True", "ReconciliationSucceeded")}}),
			want: RowHealthNotReady, rule: 3, reason: "ReconciliationSucceeded", stale: true},
		{name: "older condition observedGeneration", rules: fluxRules,
			obj:  widget(3, conds(cond("Ready", "True", "Done", "observedGeneration", int64(2)))),
			want: RowHealthNotReady, rule: 3, stale: true},
		{name: "current observedGeneration", rules: fluxRules,
			obj:  widget(3, map[string]any{"observedGeneration": int64(3), "conditions": []any{cond("Ready", "True", "Done", "observedGeneration", int64(3))}}),
			want: RowHealthOK, rule: 3},
		{name: "a stale failure stays failed", rules: fluxRules,
			obj:  widget(3, map[string]any{"observedGeneration": int64(2), "conditions": []any{cond("Stalled", "True", "BuildFailed")}}),
			want: RowHealthFailed, rule: 1, stale: true},
		{name: "a stale field verdict", rules: argoRules,
			obj:  widget(2, map[string]any{"observedGeneration": int64(1), "health": map[string]any{"status": "Healthy"}}),
			want: RowHealthNotReady, rule: 1, stale: true},
		{name: "stale status without a matching rule stays unknown", rules: fluxRules,
			obj:  widget(3, map[string]any{"observedGeneration": int64(2)}),
			want: RowHealthUnknown},

		{name: "conditions not a list", rules: fluxRules, obj: widget(1, map[string]any{"conditions": "Ready"}),
			want: RowHealthUnknown, wantErr: "status.conditions is string, want a list"},
		{name: "status not an object", rules: fluxRules, obj: func() *unstructured.Unstructured {
			u := widget(1, nil)
			u.Object["status"] = "ok"
			return u
		}(), want: RowHealthUnknown, wantErr: "status.conditions"},
		{name: "observedGeneration not an integer", rules: fluxRules,
			obj:  widget(1, map[string]any{"observedGeneration": "1", "conditions": []any{cond("Ready", "True", "Done")}}),
			want: RowHealthUnknown, wantErr: "status.observedGeneration is string"},
		{name: "field is an object", rules: argoRules, obj: widget(1, map[string]any{"health": map[string]any{"status": map[string]any{"code": "Healthy"}}}),
			want: RowHealthUnknown, wantErr: ".status.health.status: is a map"},
		{name: "malformed condition entries are skipped", rules: fluxRules,
			obj:  widget(1, map[string]any{"conditions": []any{"Ready", map[string]any{"type": int64(1)}, cond("Ready", "True", "Done")}}),
			want: RowHealthOK, rule: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v := testAdapter(t, tt.rules).evaluate(tt.obj)
			if v.Health != tt.want || v.Rule != tt.rule || v.Stale != tt.stale {
				t.Fatalf("verdict = health %v rule %d stale %v (%+v), want health %v rule %d stale %v", v.Health, v.Rule, v.Stale, v, tt.want, tt.rule, tt.stale)
			}
			if tt.reason != "" && v.Reason != tt.reason {
				t.Errorf("reason = %q, want %q", v.Reason, tt.reason)
			}
			switch {
			case tt.wantErr == "" && v.Err != nil:
				t.Errorf("err = %v, want none", v.Err)
			case tt.wantErr != "" && (v.Err == nil || !strings.Contains(v.Err.Error(), tt.wantErr)):
				t.Errorf("err = %v, want %q", v.Err, tt.wantErr)
			}
		})
	}
}

func TestHealthAdapterVerdictDetails(t *testing.T) {
	t.Parallel()
	obj := widget(3, map[string]any{"observedGeneration": int64(2), "conditions": []any{cond("Ready", "True", "Done")}})
	v := testAdapter(t, fluxRules).evaluate(obj)
	if v.Generation != 3 || v.Observed != 2 || v.RuleHealth != RowHealthOK {
		t.Fatalf("verdict = %+v, want generation 3, observed 2, rule health ok", v)
	}
	if want := time.Date(2026, 11, 23, 11, 0, 0, 0, time.UTC); !v.Since.Equal(want) || v.Message != "Done message" {
		t.Fatalf("since %v message %q, want %v and the condition message", v.Since, v.Message, want)
	}
	got := v.summary("example.com/v1/widgets")
	want := "health: notReady, by rule 3 of example.com/v1/widgets (status.conditions[Ready]=True, reason Done); the status is for generation 2, the spec is at 3"
	if got != want {
		t.Fatalf("summary =\n%s\nwant\n%s", got, want)
	}
	// The older of the two generations dates the status.
	obj = widget(5, map[string]any{"observedGeneration": int64(4), "conditions": []any{cond("Ready", "True", "Done", "observedGeneration", int64(3))}})
	if v := testAdapter(t, fluxRules).evaluate(obj); v.Observed != 3 {
		t.Fatalf("observed = %d, want the condition's 3", v.Observed)
	}
	obj = widget(5, map[string]any{"observedGeneration": int64(3), "conditions": []any{cond("Ready", "True", "Done", "observedGeneration", int64(4))}})
	if v := testAdapter(t, fluxRules).evaluate(obj); v.Observed != 3 {
		t.Fatalf("observed = %d, want the status's 3", v.Observed)
	}
}

var widgetsGVR = schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}

// Not parallel: it installs the package-wide health config.
func TestInstallHealthAdapters(t *testing.T) {
	errs := installHealth(t, map[string][]appconfig.HealthRule{
		"example.com/v1/widgets": fluxRules,
		"example.com/v1/gadgets": argoRules,
		// One bad rule drops the whole entry.
		"example.com/v1/broken": {fluxRules[0], {Condition: "Ready", Health: "green"}},
		// A built-in rule wins.
		"apps/v1/deployments": fluxRules,
	})
	if len(errs) != 2 || !strings.Contains(errs[0].Error(), `health "example.com/v1/broken" rule 2`) ||
		!strings.Contains(errs[1].Error(), `health "apps/v1/deployments": kc has a built-in rule`) {
		t.Fatalf("errors = %v, want the broken rule and the built-in type", errs)
	}
	ready := widget(1, conds(cond("Ready", "True", "Done")))
	if h := ClassifyRowHealth(widgetsGVR, ready); h != RowHealthOK {
		t.Fatalf("widgets health = %v, want ok", h)
	}
	gadgets := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "gadgets"}
	degraded := widget(1, map[string]any{"health": map[string]any{"status": "Degraded"}})
	if h := ClassifyRowHealth(gadgets, degraded); h != RowHealthFailed {
		t.Fatalf("gadgets health = %v, want failed", h)
	}
	// Another version of a configured type has no rules.
	if h := ClassifyRowHealth(schema.GroupVersionResource{Group: "example.com", Version: "v2", Resource: "widgets"}, ready); h != RowHealthUnknown {
		t.Fatalf("widgets v2 health = %v, want unknown", h)
	}
	broken := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "broken"}
	stalled := widget(1, conds(cond("Stalled", "True", "Failed")))
	if healthAdapterFor(broken) != nil || ClassifyRowHealth(broken, stalled) != RowHealthUnknown {
		t.Fatalf("the broken entry is installed")
	}
	// The built-in Deployment rule still decides: a Ready condition the
	// config names does not make an unavailable Deployment ok.
	dep := toUnstructured(t, dgDeployment("d-uid", func(d *appsv1.Deployment) { d.Status.AvailableReplicas = 0 }))
	if healthAdapterFor(relDeploymentsGVR) != nil || ClassifyRowHealth(relDeploymentsGVR, dep) != RowHealthNotReady {
		t.Fatalf("deployment health = %v, want the built-in notReady", ClassifyRowHealth(relDeploymentsGVR, dep))
	}

	// Row colors and the faults filter read the same verdict.
	notReady := widget(1, conds(cond("Ready", "False", "Waiting")))
	if s := applyRowStylers(RowStyleInfo{GVR: widgetsGVR, Unstructured: notReady}); s != NotReadyStyle() {
		t.Fatalf("a not ready widget row is not styled as not ready")
	}
	if base := WhiteStyle(); applyRowStylers(RowStyleInfo{GVR: widgetsGVR, Unstructured: ready, BaseStyle: base}) != base {
		t.Fatalf("a ready widget row is colored")
	}
	if f := rowFactsFor(RowStyleInfo{GVR: widgetsGVR, Unstructured: notReady}, "id", "ns", nil, 0); !f.Health.Faulty() {
		t.Fatalf("faults filter health = %v, want a fault", f.Health)
	}

	// Installing again replaces the config: nothing stays from before.
	if errs := InstallHealthAdapters(nil); len(errs) != 0 {
		t.Fatalf("errors = %v", errs)
	}
	if ClassifyRowHealth(widgetsGVR, ready) != RowHealthUnknown {
		t.Fatalf("the old config is still installed")
	}
}

// Not parallel: it installs the package-wide health config.
func TestHealthDiagnosis(t *testing.T) {
	installHealth(t, map[string][]appconfig.HealthRule{"example.com/v1/widgets": fluxRules})
	widgetUID := types.UID("w-uid")
	stalled := widget(2, map[string]any{"observedGeneration": int64(1), "conditions": []any{cond("Stalled", "True", "BuildFailed")}})
	own := dgPod("w-0", "p-0", widgetUID, nil)
	// A pod named like the widget but controlled by another object with the
	// same name (a widget that was deleted and created again).
	impostor := dgPod("w-1", "p-1", "old-w-uid", nil)
	unowned := dgPod("w-2", "p-2", "", func(p *corev1.Pod) { p.OwnerReferences = nil })
	in := workloadInput{GVR: widgetsGVR, Object: stalled, Now: diagNow, Pods: []corev1.Pod{own, impostor, unowned}}
	d := diagWorkload(t, in)

	if !hasWorkloadDiagnosis(widgetsGVR) {
		t.Fatalf("a configured type has no /diagnosis")
	}
	summary := strings.Join(d.Summary, "\n")
	for _, want := range []string{
		"health: failed, by rule 1 of example.com/v1/widgets (status.conditions[Stalled]=True, reason BuildFailed); the status is for generation 1, the spec is at 2",
		"pods: 1 owned, 1 ready",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary misses %q:\n%s", want, summary)
		}
	}
	if !strings.HasPrefix(d.Note, healthDiagnosisNote) {
		t.Errorf("note = %q, want the health config note first", d.Note)
	}
	if got := strings.Join(dgCodes(d), ","); got != "health,generation" {
		t.Fatalf("codes = %s, want health,generation", got)
	}
	f := d.Findings[0]
	if f.Severity != diagError || f.Title != "The object failed: BuildFailed" || f.Detail != "BuildFailed message" {
		t.Fatalf("finding = %+v", f)
	}
	if want := time.Date(2026, 11, 23, 11, 0, 0, 0, time.UTC); !f.Observed.Equal(want) {
		t.Errorf("observed = %v, want the condition's transition %v", f.Observed, want)
	}
	if got := dgEvidence(f)[0]; got != "status.conditions[Stalled]=True (rule 1) BuildFailed" {
		t.Errorf("evidence = %q", got)
	}

	// A healthy widget adds no finding of its own; with no pod problems
	// the diagnosis says it found none.
	in.Object = widget(1, conds(cond("Ready", "True", "Done")))
	if got := strings.Join(dgCodes(diagWorkload(t, in)), ","); got != "no-problem" {
		t.Fatalf("codes = %s, want no-problem", got)
	}
	// No rule matches: the health is unknown, and the diagnosis says so.
	in.Object = widget(1, conds(cond("Ready", "Unknown", "Progressing")))
	if got := strings.Join(dgCodes(diagWorkload(t, in)), ","); got != "health-unknown" {
		t.Fatalf("codes = %s, want health-unknown", got)
	}
	// A field of the wrong type is an adapter error, not a health.
	in.Object = widget(1, map[string]any{"conditions": map[string]any{"Ready": "True"}})
	d = diagWorkload(t, in)
	if got := strings.Join(dgCodes(d), ","); got != "health-config" || d.Findings[0].Severity != diagIncomplete {
		t.Fatalf("codes = %s (%+v), want health-config incomplete", got, d.Findings)
	}
	// A stale not-ready verdict keeps the rule's finding.
	in.Object = widget(2, map[string]any{"observedGeneration": int64(1), "conditions": []any{cond("Ready", "False", "Waiting")}})
	if got := strings.Join(dgCodes(diagWorkload(t, in)), ","); got != "generation,health" && got != "health,generation" {
		t.Fatalf("codes = %s, want health and generation", got)
	}
}

// Not parallel: it installs the package-wide health config.
func TestHealthDiagnosisRowNamespacedOnly(t *testing.T) {
	installHealth(t, map[string][]appconfig.HealthRule{"example.com/v1/widgets": fluxRules})
	fn, ok := builtinObjectRows(widgetsGVR)
	if !ok {
		t.Fatalf("no object rows for a configured type")
	}
	if rows := fn(Deps{}, "ns", "w", []string{"widgets", "w"}); len(rows) != 1 {
		t.Fatalf("namespaced rows = %d, want the /diagnosis row", len(rows))
	}
	if rows := fn(Deps{}, "", "w", []string{"widgets", "w"}); len(rows) != 0 {
		t.Fatalf("cluster-scoped rows = %d, want none", len(rows))
	}
	if _, ok := diagnosisWatches(widgetsGVR, "ns", relatedWatch{gvr: widgetsGVR, namespace: "ns"}); !ok {
		t.Fatalf("a configured type has no diagnosis watches")
	}
	if _, ok := builtinObjectRows(schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "gadgets"}); ok {
		t.Fatalf("an unconfigured type has object rows")
	}
}
