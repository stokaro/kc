package models

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	table "github.com/stokaro/kc/internal/table"
)

// gitopsDriftNote says what a GitOps diagnosis does not prove.
const gitopsDriftNote = "Read-only. kc does not fetch the source or render manifests: a Ready condition means the last apply succeeded, not that live objects match Git. To compare one object, open its rendered manifest in a local-files panel (M-l) and press C-x C-d with the live object in the other panel."

// hasGitOpsDiagnosis reports whether objects of gvr have a GitOps
// diagnosis: Flux Kustomizations and HelmReleases, Argo CD Applications,
// in any served version.
func hasGitOpsDiagnosis(gvr schema.GroupVersionResource) bool {
	switch gvr.GroupResource() {
	case schema.GroupResource{Group: fluxKustomizationGK.Group, Resource: "kustomizations"},
		schema.GroupResource{Group: fluxHelmReleaseGK.Group, Resource: "helmreleases"},
		schema.GroupResource{Group: argoApplicationGK.Group, Resource: "applications"}:
		return true
	}
	return false
}

// gitopsDiagnosisWatches refreshes on the object and its siblings in the
// namespace (dependsOn targets usually live there). Sources are read, not
// watched: the controller updates the object's status when a source
// changes revision or readiness.
func gitopsDiagnosisWatches(gvr schema.GroupVersionResource, namespace string, self relatedWatch) []relatedWatch {
	return []relatedWatch{self, {gvr: gvr, namespace: namespace}}
}

// gitopsLookups reads what a GitOps diagnosis cites.
type gitopsLookups interface {
	// get reads an object of kind gk, in its served version.
	get(gk schema.GroupKind, namespace, name string) (*unstructured.Unstructured, schema.GroupVersionResource, error)
}

type readerGitOpsLookups struct {
	ctx    context.Context
	reader RelatedReader
	mapper meta.RESTMapper
}

func (l readerGitOpsLookups) get(gk schema.GroupKind, namespace, name string) (*unstructured.Unstructured, schema.GroupVersionResource, error) {
	gvr, ok := servedResource(l.mapper, gk)
	if !ok {
		return nil, schema.GroupVersionResource{}, errGitOpsNotServed
	}
	obj, err := l.reader.GetByGVR(l.ctx, gvr, namespace, name)
	return obj, gvr, err
}

var errGitOpsNotServed = fmt.Errorf("%s", problemNotServed)

func (f *RelatedObjectsFolder) gitopsDiagnosisRows(ctx context.Context) ([]table.Row, error) {
	obj, err := f.reader.GetByGVR(ctx, f.gvr, f.namespace, f.name)
	if err != nil {
		return nil, err
	}
	var mapper meta.RESTMapper
	if f.Deps.Cl != nil {
		mapper = f.Deps.Cl.RESTMapper()
	}
	d := diagnoseGitOps(f.gvr, obj, readerGitOpsLookups{ctx: ctx, reader: f.reader, mapper: mapper})
	ctrllog.FromContext(ctx).WithName("diagnosis").V(1).Info("gitops diagnosis", "gvr", f.gvr.String(), "namespace", f.namespace, "name", f.name, "findings", len(d.Findings))
	return renderDiagnosis(f.Deps, f.rowDeps, f.Path(), d), nil
}

// diagnoseGitOps explains one Flux or Argo CD object: what it applies from
// where, at which revision, whether the controller reports success for
// the current spec, and what it waits for.
func diagnoseGitOps(gvr schema.GroupVersionResource, obj *unstructured.Unstructured, look gitopsLookups) diagReport {
	var d diagReport
	self := diagLink{GVR: gvr, Namespace: obj.GetNamespace(), Name: obj.GetName()}
	if gvr.Group == argoApplicationGK.Group {
		d.diagnoseArgoApplication(obj, self)
	} else {
		d.diagnoseFlux(gvr, obj, self, look)
	}
	for i, c := range gitopsClaims(obj) {
		label := "managed by"
		if i > 0 {
			label += " " + strconv.Itoa(i+1)
		}
		d.Summary = append(d.Summary, fmt.Sprintf("%s: %s %s (%s)", label, gitopsTool(c.GK), nsName(c.Namespace, c.Name), c.Via))
	}
	d.Findings = append(d.Findings, diagFinding{
		Code: "drift-unchecked", Severity: diagInfo,
		Title:  "Drift is not checked by kc",
		Detail: "Without a rendered desired object kc cannot tell whether live objects match the source; a successful reconcile is not proof of no drift.",
		Next:   "Open the rendered manifest with M-l and compare it with the live object with C-x C-d.",
	})
	sortFindings(d.Findings)
	d.Note = gitopsDriftNote
	return d
}

func nsName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "/" + name
}

func str(obj map[string]any, fields ...string) string {
	s, _, _ := unstructured.NestedString(obj, fields...)
	return s
}

// statusConditions decodes status.conditions; malformed entries are
// skipped.
func statusConditions(obj *unstructured.Unstructured) []metav1.Condition {
	raw, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	out := make([]metav1.Condition, 0, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		var c metav1.Condition
		if runtime.DefaultUnstructuredConverter.FromUnstructured(m, &c) == nil {
			out = append(out, c)
		}
	}
	return out
}

func (d *diagReport) diagnoseFlux(gvr schema.GroupVersionResource, obj *unstructured.Unstructured, self diagLink, look gitopsLookups) {
	o := obj.Object
	kustomization := gvr.Group == fluxKustomizationGK.Group
	if suspended, _, _ := unstructured.NestedBool(o, "spec", "suspend"); suspended {
		d.Findings = append(d.Findings, diagFinding{
			Code: "suspended", Severity: diagWarning,
			Title:  "Reconciliation is suspended (spec.suspend)",
			Detail: "The controller applies no change from the source and corrects no drift until it is resumed.",
			Links:  []diagLink{self},
		})
	}
	d.addStatusGeneration(obj, self)
	d.addFluxConditions(obj, self)
	applied := str(o, "status", "lastAppliedRevision")
	attempted := str(o, "status", "lastAttemptedRevision")
	if kustomization {
		if p := str(o, "spec", "path"); p != "" {
			d.Summary = append(d.Summary, "path: "+p)
		}
		if prune, found, _ := unstructured.NestedBool(o, "spec", "prune"); found {
			d.Summary = append(d.Summary, "prune: "+strconv.FormatBool(prune))
		}
		if entries, found, _ := unstructured.NestedSlice(o, "status", "inventory", "entries"); found {
			d.Summary = append(d.Summary, fmt.Sprintf("inventory: %d objects", len(entries)))
		} else {
			d.Summary = append(d.Summary, "inventory: none recorded")
		}
	}
	d.Summary = append(d.Summary, "applied revision: "+orUnrecorded(applied))
	if attempted != "" && attempted != applied {
		d.Summary = append(d.Summary, "attempted revision: "+attempted)
		d.Findings = append(d.Findings, diagFinding{
			Code: "revision-not-applied", Severity: diagError,
			Title:  "Revision " + attempted + " was attempted and not applied",
			Detail: "The live objects are at " + orUnrecorded(applied) + "; the Ready condition says why the newer revision failed.",
			Links:  []diagLink{self},
		})
	}
	ref, refField := fluxSourceRef(o, kustomization)
	if ref.kind != "" {
		d.addFluxSource(obj, ref, refField, applied, look)
	}
	d.addDependsOn(obj, gvr, look)
}

func orUnrecorded(s string) string {
	if s == "" {
		return "none recorded"
	}
	return s
}

// addStatusGeneration reports a status written for an older spec.
func (d *diagReport) addStatusGeneration(obj *unstructured.Unstructured, self diagLink) {
	observed, found, _ := unstructured.NestedInt64(obj.Object, "status", "observedGeneration")
	if found && observed < obj.GetGeneration() {
		d.Findings = append(d.Findings, diagFinding{
			Code: "stale-status", Severity: diagWarning,
			Title:  fmt.Sprintf("Status is for generation %d, the spec is generation %d", observed, obj.GetGeneration()),
			Detail: "The controller has not reconciled the latest spec yet, or is not running; the conditions describe the older spec.",
			Links:  []diagLink{self},
		})
	}
}

// addFluxConditions reports Ready when it is not True, and Stalled or
// Reconciling when they are True, each with reason, message and time.
func (d *diagReport) addFluxConditions(obj *unstructured.Unstructured, self diagLink) {
	conds := statusConditions(obj)
	ready := meta.FindStatusCondition(conds, "Ready")
	if ready == nil {
		d.Findings = append(d.Findings, diagFinding{
			Code: "ready-missing", Severity: diagWarning,
			Title:  "No Ready condition",
			Detail: "The controller has not reported on this object; it may not be running or may not watch this namespace.",
			Links:  []diagLink{self},
		})
	} else {
		d.Summary = append(d.Summary, "ready: "+gitopsConditionText(*ready, obj.GetGeneration()))
		if ready.Status != metav1.ConditionTrue {
			sev := diagError
			if ready.Status == metav1.ConditionUnknown {
				sev = diagWarning
			}
			d.Findings = append(d.Findings, gitopsConditionFinding("ready", sev, *ready, obj.GetGeneration(), self))
		}
	}
	for _, t := range []string{"Stalled", "Reconciling"} {
		c := meta.FindStatusCondition(conds, t)
		if c == nil || c.Status != metav1.ConditionTrue {
			continue
		}
		sev := diagError
		if t == "Reconciling" {
			sev = diagInfo
		}
		d.Findings = append(d.Findings, gitopsConditionFinding(strings.ToLower(t), sev, *c, obj.GetGeneration(), self))
	}
}

func gitopsConditionText(c metav1.Condition, generation int64) string {
	s := string(c.Status)
	if c.Reason != "" {
		s += " (" + c.Reason + ")"
	}
	if !c.LastTransitionTime.IsZero() {
		s += " since " + c.LastTransitionTime.UTC().Format(time.RFC3339)
	}
	if c.ObservedGeneration != 0 && c.ObservedGeneration < generation {
		s += fmt.Sprintf(", for generation %d of %d", c.ObservedGeneration, generation)
	}
	return s
}

func gitopsConditionFinding(code string, sev diagSeverity, c metav1.Condition, generation int64, link diagLink) diagFinding {
	return diagFinding{
		Code: code, Severity: sev,
		Title:    c.Type + " is " + gitopsConditionText(c, generation),
		Detail:   c.Message,
		Observed: c.LastTransitionTime.Time,
		Links:    []diagLink{link},
	}
}

type fluxRef struct {
	kind, namespace, name string
}

// fluxSourceRef returns the source a Kustomization (spec.sourceRef) or a
// HelmRelease (spec.chartRef, or spec.chart.spec.sourceRef) reads.
func fluxSourceRef(o map[string]any, kustomization bool) (fluxRef, string) {
	paths := [][]string{{"spec", "chartRef"}, {"spec", "chart", "spec", "sourceRef"}}
	if kustomization {
		paths = [][]string{{"spec", "sourceRef"}}
	}
	for _, p := range paths {
		m, found, _ := unstructured.NestedMap(o, p...)
		if !found {
			continue
		}
		return fluxRef{kind: str(m, "kind"), namespace: str(m, "namespace"), name: str(m, "name")}, strings.Join(p, ".")
	}
	return fluxRef{}, ""
}

// addFluxSource links the source and reports when it is not ready or
// offers a revision other than the applied one.
func (d *diagReport) addFluxSource(obj *unstructured.Unstructured, ref fluxRef, field, applied string, look gitopsLookups) {
	ns := ref.namespace
	if ns == "" {
		ns = obj.GetNamespace()
	}
	what := fmt.Sprintf("%s %s/%s", ref.kind, ns, ref.name)
	src, gvr, err := look.get(schema.GroupKind{Group: fluxSourceGroup, Kind: ref.kind}, ns, ref.name)
	if err != nil {
		f := diagFinding{Code: "source-unreadable", Severity: diagIncomplete,
			Title:  "Source " + what + " (" + field + ") could not be read: " + gitopsReadProblem(err),
			Detail: "Without the source kc cannot say which revision it offers."}
		if gitopsReadProblem(err) == problemNotFound {
			f.Code, f.Severity, f.Title = "source-missing", diagError, "Source "+what+" ("+field+") does not exist"
			f.Detail = "The controller cannot fetch an artifact; nothing new is applied."
		}
		d.Findings = append(d.Findings, f)
		return
	}
	revision := str(src.Object, "status", "artifact", "revision")
	evidence := field + "; artifact " + orUnrecorded(revision)
	if u := str(src.Object, "spec", "url"); u != "" {
		evidence += ", " + u
	}
	link := diagLink{GVR: gvr, Namespace: ns, Name: ref.name, Evidence: evidence}
	d.Summary = append(d.Summary, "source: "+what+" at "+orUnrecorded(revision))
	d.Backends = append(d.Backends, link)
	if ready := meta.FindStatusCondition(statusConditions(src), "Ready"); ready != nil && ready.Status != metav1.ConditionTrue {
		d.Findings = append(d.Findings, gitopsConditionFinding("source-ready", diagError, *ready, src.GetGeneration(), link))
	}
	if revision != "" && applied != "" && revision != applied {
		d.Findings = append(d.Findings, diagFinding{
			Code: "source-ahead", Severity: diagWarning,
			Title:  "Source offers " + revision + ", applied is " + applied,
			Detail: "The newer revision is waiting for the next reconcile, a dependency, or failed; see the Ready condition.",
			Links:  []diagLink{link},
		})
	}
}

func gitopsReadProblem(err error) string {
	if err == errGitOpsNotServed {
		return problemNotServed
	}
	return readProblem(err)
}

// addDependsOn links each spec.dependsOn target and reports the ones that
// are missing or not ready: the controller waits for them.
func (d *diagReport) addDependsOn(obj *unstructured.Unstructured, gvr schema.GroupVersionResource, look gitopsLookups) {
	deps, _, _ := unstructured.NestedSlice(obj.Object, "spec", "dependsOn")
	gk := fluxKustomizationGK
	if gvr.Group == fluxHelmReleaseGK.Group {
		gk = fluxHelmReleaseGK
	}
	for i, raw := range deps {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		ns, name := str(m, "namespace"), str(m, "name")
		if ns == "" {
			ns = obj.GetNamespace()
		}
		code := "depends-on/" + strconv.Itoa(i+1)
		what := fmt.Sprintf("%s %s/%s (spec.dependsOn[%d])", gk.Kind, ns, name, i)
		dep, depGVR, err := look.get(gk, ns, name)
		if err != nil {
			sev, title := diagIncomplete, "Dependency "+what+" could not be read: "+gitopsReadProblem(err)
			if gitopsReadProblem(err) == problemNotFound {
				sev, title = diagError, "Dependency "+what+" does not exist"
			}
			d.Findings = append(d.Findings, diagFinding{Code: code, Severity: sev, Title: title,
				Detail: "The controller waits for every dependency to be ready before it applies this object."})
			continue
		}
		link := diagLink{GVR: depGVR, Namespace: ns, Name: name, Evidence: fmt.Sprintf("spec.dependsOn[%d]", i)}
		ready := meta.FindStatusCondition(statusConditions(dep), "Ready")
		if ready != nil && ready.Status == metav1.ConditionTrue {
			d.Backends = append(d.Backends, link)
			continue
		}
		status := "has no Ready condition"
		if ready != nil {
			status = "is not ready: " + gitopsConditionText(*ready, dep.GetGeneration())
		}
		d.Findings = append(d.Findings, diagFinding{Code: code, Severity: diagWarning,
			Title:  "Dependency " + what + " " + status,
			Detail: "The controller waits for it before applying this object.",
			Links:  []diagLink{link}})
	}
}

// diagnoseArgoApplication reports what an Argo CD Application says about
// itself: sources, sync status and revision, health, the last operation
// and its conditions. Argo CD compares live and desired state itself; kc
// shows that result as Argo CD's, with the time it was computed.
func (d *diagReport) diagnoseArgoApplication(obj *unstructured.Unstructured, self diagLink) {
	o := obj.Object
	sources, _, _ := unstructured.NestedSlice(o, "spec", "sources")
	if src, found, _ := unstructured.NestedMap(o, "spec", "source"); found {
		sources = append([]any{src}, sources...)
	}
	for i, raw := range sources {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		where := str(m, "repoURL")
		for _, f := range []string{"path", "chart"} {
			if v := str(m, f); v != "" {
				where += " " + f + " " + v
			}
		}
		d.Summary = append(d.Summary, fmt.Sprintf("source %d: %s at %s", i+1, where, orUnrecorded(str(m, "targetRevision"))))
	}
	sync := str(o, "status", "sync", "status")
	revision := str(o, "status", "sync", "revision")
	at := str(o, "status", "reconciledAt")
	d.Summary = append(d.Summary, "sync: "+orUnrecorded(sync)+" at revision "+orUnrecorded(revision)+", compared "+orUnrecorded(at))
	d.Summary = append(d.Summary, "health: "+orUnrecorded(str(o, "status", "health", "status")))
	switch sync {
	case "OutOfSync":
		d.Findings = append(d.Findings, diagFinding{Code: "out-of-sync", Severity: diagWarning,
			Title:  "Argo CD reports OutOfSync (its own comparison, " + orUnrecorded(at) + ")",
			Detail: "Argo CD found live objects that differ from the rendered source; status.resources marks which.",
			Links:  []diagLink{self}})
	case "":
		d.Findings = append(d.Findings, diagFinding{Code: "sync-unknown", Severity: diagWarning,
			Title: "No sync status", Detail: "The application controller has not compared this Application yet.", Links: []diagLink{self}})
	}
	if h := str(o, "status", "health", "status"); h != "" && h != "Healthy" {
		sev := diagWarning
		if h == "Degraded" || h == "Missing" {
			sev = diagError
		}
		d.Findings = append(d.Findings, diagFinding{Code: "health", Severity: sev,
			Title: "Argo CD reports health " + h, Detail: str(o, "status", "health", "message"), Links: []diagLink{self}})
	}
	if phase := str(o, "status", "operationState", "phase"); phase == "Failed" || phase == "Error" {
		f := diagFinding{Code: "operation", Severity: diagError,
			Title:  "Last sync operation " + phase,
			Detail: str(o, "status", "operationState", "message"), Links: []diagLink{self}}
		if t, err := time.Parse(time.RFC3339, str(o, "status", "operationState", "finishedAt")); err == nil {
			f.Observed = t
		}
		d.Findings = append(d.Findings, f)
	}
	conds, _, _ := unstructured.NestedSlice(o, "status", "conditions")
	for i, raw := range conds {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		t := str(m, "type")
		sev := diagWarning
		if strings.HasSuffix(t, "Error") {
			sev = diagError
		}
		d.Findings = append(d.Findings, diagFinding{Code: "condition/" + strconv.Itoa(i+1), Severity: sev,
			Title: "Condition " + t, Detail: str(m, "message"), Links: []diagLink{self}})
	}
	if res, _, _ := unstructured.NestedSlice(o, "status", "resources"); len(res) > 0 {
		out := 0
		for _, r := range res {
			if m, ok := r.(map[string]any); ok && m["status"] == "OutOfSync" {
				out++
			}
		}
		d.Summary = append(d.Summary, fmt.Sprintf("resources: %d, %d out of sync", len(res), out))
	}
}
