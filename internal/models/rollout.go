package models

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/manifestdiff"
	table "github.com/stokaro/kc/internal/table"
)

// RolloutSegment is the path segment of a Deployment's rollout folder.
const RolloutSegment = "rollout"

const rolloutNote = "Facts from the cache at the read time. ReplicaSets older than revisionHistoryLimit are gone; " +
	"/timeline holds the revisions kc recorded while it ran. Usage before and after a rollout is not compared here."

// rolloutInput is what a rollout view reads. A nil slice with an error
// means the list could not be read.
type rolloutInput struct {
	Deployment     *appsv1.Deployment
	ReplicaSets    []appsv1.ReplicaSet
	ReplicaSetsErr error
	Pods           []corev1.Pod
	PodsErr        error
}

// rolloutRevision is one ReplicaSet the Deployment controls, with the
// pods it controls, both matched by controller UID.
type rolloutRevision struct {
	RS       *appsv1.ReplicaSet
	Number   int64 // 0 when the revision annotation is missing or invalid
	Current  bool  // its template is the Deployment's
	Pods     []*corev1.Pod
	Previous *rolloutRevision // the next older revision, for the template change
}

// rolloutRevisions groups ReplicaSets and pods by controller UID, newest
// revision first. Objects of a deleted Deployment with the same name are
// not controlled by this one and are left out.
func rolloutRevisions(in rolloutInput) []*rolloutRevision {
	dep := in.Deployment
	var out []*rolloutRevision
	byUID := make(map[string]*rolloutRevision)
	for i := range in.ReplicaSets {
		rs := &in.ReplicaSets[i]
		if !metav1.IsControlledBy(rs, dep) {
			continue
		}
		n, err := strconv.ParseInt(rs.Annotations["deployment.kubernetes.io/revision"], 10, 64)
		if err != nil {
			n = 0
		}
		r := &rolloutRevision{RS: rs, Number: n, Current: templatesEqual(&rs.Spec.Template, &dep.Spec.Template)}
		out = append(out, r)
		byUID[string(rs.UID)] = r
	}
	for i := range in.Pods {
		p := &in.Pods[i]
		if c := metav1.GetControllerOf(p); c != nil {
			if r, ok := byUID[string(c.UID)]; ok {
				r.Pods = append(r.Pods, p)
			}
		}
	}
	slices.SortFunc(out, func(a, b *rolloutRevision) int {
		if c := cmp.Compare(b.Number, a.Number); c != 0 {
			return c
		}
		return b.RS.CreationTimestamp.Compare(a.RS.CreationTimestamp.Time)
	})
	for i := range out {
		if i+1 < len(out) {
			out[i].Previous = out[i+1]
		}
		slices.SortFunc(out[i].Pods, func(a, b *corev1.Pod) int { return strings.Compare(a.Name, b.Name) })
	}
	return out
}

// templatesEqual compares pod templates without the pod-template-hash
// label, as the Deployment controller does.
func templatesEqual(a, b *corev1.PodTemplateSpec) bool {
	a, b = a.DeepCopy(), b.DeepCopy()
	delete(a.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
	delete(b.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
	return apiequality.Semantic.DeepEqual(a, b)
}

// rolloutState is what kubectl rollout status would say, with the
// condition behind it.
type rolloutState struct {
	Text     string
	Severity diagSeverity
	Since    time.Time
	Detail   string
}

func deploymentRolloutState(dep *appsv1.Deployment) rolloutState {
	st := dep.Status
	want := int32(1)
	if dep.Spec.Replicas != nil {
		want = *dep.Spec.Replicas
	}
	var progressing *appsv1.DeploymentCondition
	for i := range st.Conditions {
		if st.Conditions[i].Type == appsv1.DeploymentProgressing {
			progressing = &st.Conditions[i]
		}
	}
	switch {
	case dep.Spec.Paused:
		return rolloutState{Text: "paused", Severity: diagWarning, Detail: "spec.paused is true: the controller does not roll out template changes"}
	case st.ObservedGeneration < dep.Generation:
		return rolloutState{Text: fmt.Sprintf("waiting for the controller (status is for generation %d of %d)", st.ObservedGeneration, dep.Generation), Severity: diagWarning}
	case progressing != nil && progressing.Reason == "ProgressDeadlineExceeded":
		return rolloutState{Text: "stalled: " + progressing.Reason, Severity: diagError, Since: progressing.LastUpdateTime.Time, Detail: progressing.Message}
	case st.UpdatedReplicas < want:
		return rolloutState{Text: fmt.Sprintf("in progress: %d of %d replicas updated", st.UpdatedReplicas, want), Severity: diagInfo}
	case st.Replicas > st.UpdatedReplicas:
		return rolloutState{Text: fmt.Sprintf("in progress: %d old replicas pending termination", st.Replicas-st.UpdatedReplicas), Severity: diagInfo}
	case st.AvailableReplicas < st.UpdatedReplicas:
		return rolloutState{Text: fmt.Sprintf("in progress: %d of %d updated replicas available", st.AvailableReplicas, st.UpdatedReplicas), Severity: diagInfo}
	}
	s := rolloutState{Text: "complete", Severity: diagInfo}
	if progressing != nil {
		s.Since = progressing.LastUpdateTime.Time
	}
	return s
}

// templateChanges names what changed between two pod templates: images
// per container, containers added or removed, and otherwise the parts of
// the template that differ. It never says "no change" for templates that
// differ.
func templateChanges(older, newer *corev1.PodTemplateSpec) []string {
	if templatesEqual(older, newer) {
		return nil
	}
	var out []string
	oldImages := make(map[string]string)
	for _, c := range older.Spec.Containers {
		oldImages[c.Name] = c.Image
	}
	seen := make(map[string]bool)
	for _, c := range newer.Spec.Containers {
		seen[c.Name] = true
		switch img, ok := oldImages[c.Name]; {
		case !ok:
			out = append(out, "container "+c.Name+" added ("+c.Image+")")
		case img != c.Image:
			out = append(out, "container "+c.Name+" image "+img+" -> "+c.Image)
		}
	}
	both := make(map[string]bool)
	for _, c := range older.Spec.Containers {
		if !seen[c.Name] {
			out = append(out, "container "+c.Name+" removed")
		} else {
			both[c.Name] = true
		}
	}
	// What else differs, by top-level part, after the images.
	a, b := older.DeepCopy(), newer.DeepCopy()
	delete(a.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
	delete(b.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
	for i := range b.Spec.Containers {
		for j := range a.Spec.Containers {
			if a.Spec.Containers[j].Name == b.Spec.Containers[i].Name {
				a.Spec.Containers[j].Image = b.Spec.Containers[i].Image
			}
		}
	}
	parts := []struct {
		name string
		x, y any
	}{
		{"labels", a.Labels, b.Labels},
		{"annotations", a.Annotations, b.Annotations},
		{"containers", containersByName(a.Spec.Containers, both), containersByName(b.Spec.Containers, both)},
		{"initContainers", a.Spec.InitContainers, b.Spec.InitContainers},
		{"volumes", a.Spec.Volumes, b.Spec.Volumes},
	}
	for _, p := range parts {
		if !apiequality.Semantic.DeepEqual(p.x, p.y) {
			out = append(out, p.name+" changed")
		}
	}
	a.Labels, a.Annotations, a.Spec.Containers, a.Spec.InitContainers, a.Spec.Volumes = nil, nil, nil, nil, nil
	b.Labels, b.Annotations, b.Spec.Containers, b.Spec.InitContainers, b.Spec.Volumes = nil, nil, nil, nil, nil
	if !apiequality.Semantic.DeepEqual(a, b) {
		out = append(out, "other pod spec fields changed")
	}
	return out
}

// containersByName keeps the containers both templates have, so an added
// or removed container is reported once, above.
func containersByName(cs []corev1.Container, keep map[string]bool) map[string]corev1.Container {
	out := make(map[string]corev1.Container, len(cs))
	for _, c := range cs {
		if keep[c.Name] {
			out[c.Name] = c
		}
	}
	return out
}

// templateDiff renders the unified diff of two revisions' pod templates.
func templateDiff(older, newer *rolloutRevision) (string, error) {
	side := func(r *rolloutRevision) (manifestdiff.Side, error) {
		t := r.RS.Spec.Template.DeepCopy()
		delete(t.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(t)
		if err != nil {
			return manifestdiff.Side{}, err
		}
		return manifestdiff.Side{Context: "revision " + revisionText(r) + " spec.template", GVR: relReplicaSetsGVR, Namespace: r.RS.Namespace, Name: r.RS.Name, Object: m}, nil
	}
	l, err := side(older)
	if err != nil {
		return "", err
	}
	r, err := side(newer)
	if err != nil {
		return "", err
	}
	res, err := manifestdiff.Compare(l, r, manifestdiff.Options{})
	return res.Text, err
}

func revisionText(r *rolloutRevision) string {
	if r.Number == 0 {
		return "unknown"
	}
	return strconv.FormatInt(r.Number, 10)
}

func podState(p *corev1.Pod) (string, bool) {
	ready, total, restarts := 0, len(p.Spec.Containers), int32(0)
	var waiting []string
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
		restarts += cs.RestartCount
		if w := cs.State.Waiting; w != nil && w.Reason != "" {
			waiting = append(waiting, cs.Name+" "+w.Reason)
		}
	}
	phase := string(p.Status.Phase)
	if p.DeletionTimestamp != nil {
		phase = "Terminating"
	}
	s := fmt.Sprintf("%s, ready %d/%d, restarts %d", phase, ready, total, restarts)
	if len(waiting) > 0 {
		s += ", " + strings.Join(waiting, ", ")
	}
	healthy := p.DeletionTimestamp == nil && p.Status.Phase == corev1.PodRunning && ready == total
	return s, healthy
}

// rolloutRows renders the rollout of a Deployment: state, findings, then
// one group per revision, newest first, with its pods and the template
// change from the revision before.
func rolloutRows(deps Deps, depsFor func(RelatedRef) Deps, path []string, in rolloutInput) []table.Row {
	dep := in.Deployment
	var d diagReport
	state := deploymentRolloutState(dep)
	d.Summary = append(d.Summary,
		"rollout: "+state.Text,
		fmt.Sprintf("replicas: desired %d, updated %d, ready %d, available %d, unavailable %d",
			replicasOf(dep.Spec.Replicas), dep.Status.UpdatedReplicas, dep.Status.ReadyReplicas, dep.Status.AvailableReplicas, dep.Status.UnavailableReplicas),
		"strategy: "+strategyText(dep))
	self := diagLink{GVR: relDeploymentsGVR, Namespace: dep.Namespace, Name: dep.Name}
	if state.Severity != diagInfo {
		d.Findings = append(d.Findings, diagFinding{Code: "rollout", Severity: state.Severity, Title: "Rollout " + state.Text,
			Detail: state.Detail, Observed: state.Since, Links: []diagLink{self}})
	}
	if in.ReplicaSetsErr != nil {
		d.Findings = append(d.Findings, diagFinding{Code: "replicasets-unreadable", Severity: diagIncomplete,
			Title: "ReplicaSets of " + dep.Namespace + " could not be read: " + readProblem(in.ReplicaSetsErr), Detail: "Revisions and their pods are unknown."})
	}
	if in.PodsErr != nil {
		d.Findings = append(d.Findings, diagFinding{Code: "pods-unreadable", Severity: diagIncomplete,
			Title: "Pods of " + dep.Namespace + " could not be read: " + readProblem(in.PodsErr), Detail: "Pod evidence per revision is unknown."})
	}
	revs := rolloutRevisions(in)
	if state.Severity == diagError {
		d.addStalledEvidence(revs)
	}
	sortFindings(d.Findings)
	rows := renderDiagnosis(deps, depsFor, path, d)
	for _, r := range revs {
		rows = append(rows, revisionRows(deps, depsFor, path, r)...)
	}
	note := NewRowItem("note", []string{"note", "", "", rolloutNote}, append(slices.Clone(path), "note"), DimStyle())
	note.details = rolloutNote
	return append(rows, note)
}

// addStalledEvidence links the pods of the current revision that are not
// ready: they are why the rollout cannot progress.
func (d *diagReport) addStalledEvidence(revs []*rolloutRevision) {
	for _, r := range revs {
		if !r.Current {
			continue
		}
		f := diagFinding{Code: "stalled-pods", Severity: diagError,
			Title:  "Pods of revision " + revisionText(r) + " (" + r.RS.Name + ") that are not ready",
			Detail: "The new ReplicaSet's pods did not become available in progressDeadlineSeconds.",
			Next:   "Open a pod and its /diagnosis for the container states, events and logs."}
		for _, p := range r.Pods {
			if s, ok := podState(p); !ok {
				f.Links = append(f.Links, diagLink{GVR: podGVR, Namespace: p.Namespace, Name: p.Name, Evidence: s})
			}
		}
		if len(f.Links) == 0 {
			f.Title = "Revision " + revisionText(r) + " (" + r.RS.Name + ") has no pods that are not ready"
			f.Detail = "The pods may not be created at all: see the ReplicaSet's conditions and events (quota, admission)."
			f.Links = []diagLink{{GVR: relReplicaSetsGVR, Namespace: r.RS.Namespace, Name: r.RS.Name}}
		}
		d.Findings = append(d.Findings, f)
	}
}

func strategyText(dep *appsv1.Deployment) string {
	s := string(dep.Spec.Strategy.Type)
	if ru := dep.Spec.Strategy.RollingUpdate; ru != nil {
		if ru.MaxSurge != nil {
			s += ", maxSurge " + ru.MaxSurge.String()
		}
		if ru.MaxUnavailable != nil {
			s += ", maxUnavailable " + ru.MaxUnavailable.String()
		}
	}
	if dep.Spec.ProgressDeadlineSeconds != nil {
		s += fmt.Sprintf(", progressDeadlineSeconds %d", *dep.Spec.ProgressDeadlineSeconds)
	}
	if dep.Spec.RevisionHistoryLimit != nil {
		s += fmt.Sprintf(", revisionHistoryLimit %d", *dep.Spec.RevisionHistoryLimit)
	}
	return s
}

func revisionRows(deps Deps, depsFor func(RelatedRef) Deps, path []string, r *rolloutRevision) []table.Row {
	rs := r.RS
	prefix := "revision/" + string(rs.UID) + "/"
	role := "old"
	if r.Current {
		role = "current"
	}
	images := make([]string, 0, len(rs.Spec.Template.Spec.Containers))
	for _, c := range rs.Spec.Template.Spec.Containers {
		images = append(images, c.Image)
	}
	relation := fmt.Sprintf("revision %s, %s: desired %d, ready %d, available %d; %s; created %s",
		revisionText(r), role, replicasOf(rs.Spec.Replicas), rs.Status.ReadyReplicas, rs.Status.AvailableReplicas,
		strings.Join(images, ", "), rs.CreationTimestamp.UTC().Format(time.RFC3339))
	rows := relatedRowsPrefixed(deps, depsFor, path, prefix, []RelatedRef{{GVR: relReplicaSetsGVR, Namespace: rs.Namespace, Name: rs.Name, UID: rs.UID, Relation: relation}})
	var pods []RelatedRef
	for _, p := range r.Pods {
		s, _ := podState(p)
		pods = append(pods, RelatedRef{GVR: podGVR, Namespace: p.Namespace, Name: p.Name, UID: p.UID, Relation: "pod of revision " + revisionText(r) + ": " + s})
	}
	rows = append(rows, relatedRowsPrefixed(deps, depsFor, path, prefix+"pod/", pods)...)
	if r.Previous != nil {
		changes := templateChanges(&r.Previous.RS.Spec.Template, &rs.Spec.Template)
		text := "no template change from revision " + revisionText(r.Previous)
		if len(changes) > 0 {
			text = "from revision " + revisionText(r.Previous) + ": " + strings.Join(changes, "; ")
		}
		older, newer := r.Previous, r
		row := &templateDiffRow{RowItem: NewRowItem(prefix+"changes", []string{"changes", "", rs.Namespace, text}, append(slices.Clone(path), "changes"), DimStyle()),
			older: older, newer: newer}
		row.details = text + " (F3 shows the diff)"
		rows = append(rows, row)
	}
	return rows
}

// templateDiffRow shows the diff of two revisions' pod templates on F3.
type templateDiffRow struct {
	*RowItem
	older, newer *rolloutRevision
}

// ViewContent renders the template diff.
//
//revive:disable-next-line:function-result-limit the Viewable interface fixes these six results
func (r *templateDiffRow) ViewContent() (title, body, lang, mime, filename string, err error) {
	text, err := templateDiff(r.older, r.newer)
	if err != nil {
		return "", "", "", "", "", err
	}
	return fmt.Sprintf("%s: revision %s -> %s", r.newer.RS.Namespace, revisionText(r.older), revisionText(r.newer)), text, "diff", "", "", nil
}

// rolloutWatches refreshes on the Deployment, ReplicaSets and pods of
// its namespace.
func rolloutWatches(namespace string, self relatedWatch) []relatedWatch {
	return []relatedWatch{self, {gvr: relReplicaSetsGVR, namespace: namespace}, {gvr: podGVR, namespace: namespace}}
}

func (f *RelatedObjectsFolder) rolloutRows(ctx context.Context) ([]table.Row, error) {
	obj, err := f.reader.GetByGVR(ctx, relDeploymentsGVR, f.namespace, f.name)
	if err != nil {
		return nil, err
	}
	in := rolloutInput{Deployment: &appsv1.Deployment{}}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, in.Deployment); err != nil {
		return nil, fmt.Errorf("decode deployment: %w", err)
	}
	in.ReplicaSets, in.ReplicaSetsErr = listTyped[appsv1.ReplicaSet](ctx, f.reader, relReplicaSetsGVR, f.namespace)
	in.Pods, in.PodsErr = listTyped[corev1.Pod](ctx, f.reader, podGVR, f.namespace)
	ctrllog.FromContext(ctx).WithName("rollout").V(1).Info("rollout", "namespace", f.namespace, "name", f.name, "replicasets", len(in.ReplicaSets), "pods", len(in.Pods))
	return rolloutRows(f.Deps, f.rowDeps, f.Path(), in), nil
}
