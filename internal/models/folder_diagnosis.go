package models

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"charm.land/lipgloss/v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	table "github.com/stokaro/kc/internal/table"
	"github.com/stokaro/kc/pkg/appconfig"
)

// builtinObjectRows are rows models itself adds inside objects, next to
// the rows registered with RegisterObjectRows.
func builtinObjectRows(gvr schema.GroupVersionResource) (ObjectRowsFunc, bool) {
	diagnosis := gvr == serviceGVR || gvr == pvcGVR || hasWorkloadDiagnosis(gvr)
	if !diagnosis && !hasTimeline(gvr) {
		return nil, false
	}
	return func(deps Deps, namespace, name string, path []string) []table.Row {
		var rows []table.Row
		// A configured cluster-scoped type has no diagnosis: its pods and
		// events would need cluster-wide reads.
		if diagnosis && namespace != "" {
			rows = append(rows, diagnosisRow(deps, gvr, namespace, name, path))
		}
		if hasTimeline(gvr) {
			rows = append(rows, timelineRow(deps, gvr, namespace, name, path))
		}
		return rows
	}, true
}

// diagnosisRow is the /diagnosis row inside an object at path.
func diagnosisRow(deps Deps, gvr schema.GroupVersionResource, namespace, name string, path []string) table.Row {
	p := append(slices.Clone(path), DiagnosisSegment)
	item := NewChildResourceItem(namespace+"/"+name+"/"+DiagnosisSegment, []string{"/" + DiagnosisSegment}, p, func() (Folder, error) {
		return newDiagnosisFolder(deps, gvr, namespace, name, path)
	})
	item.RowItem.details = "Explain the conditions, failing containers, events and claims of this object"
	switch gvr {
	case serviceGVR:
		item.RowItem.details = "Check the selector, pods, EndpointSlices and target ports of this Service"
	case pvcGVR:
		item.RowItem.details = "Explain provisioning, binding, attachment and mount events of this claim"
	}
	return item
}

// newDiagnosisFolder builds the folder at path/diagnosis. Enter calls it on
// the UI thread, so it acquires nothing and reads nothing: the unscoped
// cluster and the scope decisions are taken on first use, in the background
// populate and watch start.
func newDiagnosisFolder(deps Deps, gvr schema.GroupVersionResource, namespace, name string, path []string) (Folder, error) {
	var (
		once     sync.Once
		unscoped Deps
		release  = noRelease
	)
	acquire := func() Deps {
		once.Do(func() { unscoped, release = deps.unscoped() })
		return unscoped
	}
	resolve := func(gvr schema.GroupVersionResource, ns string) (relatedScope, func()) {
		return depsResolver(acquire())(gvr, ns)
	}
	releaseLazy := func() {
		once.Do(func() {}) // nothing acquired after Close
		release()
	}
	return newRelatedFolder(deps.Ctx, deps, newScopedReader(resolve), releaseLazy, RelatedDiagnosis, gvr, namespace, name, path)
}

// diagnosisWatches returns what a diagnosis of gvr reads, so changes to
// any of it refresh the folder.
func diagnosisWatches(gvr schema.GroupVersionResource, namespace string, self relatedWatch) ([]relatedWatch, bool) {
	in := func(g schema.GroupVersionResource) relatedWatch {
		return relatedWatch{gvr: g, namespace: namespace}
	}
	switch {
	case gvr == serviceGVR:
		return []relatedWatch{self, in(podGVR), in(endpointSlicesGVR)}, true
	case gvr == podGVR:
		return []relatedWatch{self, in(ovEventsGVR), in(pvcGVR)}, true
	case gvr == pvcGVR:
		// Classes, volumes and attachments are read on each refresh, not
		// watched: the claim's status and events follow their changes.
		return []relatedWatch{self, in(podGVR), in(ovEventsGVR)}, true
	case gvr == relDeploymentsGVR:
		return []relatedWatch{self, in(relReplicaSetsGVR), in(podGVR), in(ovEventsGVR), in(pvcGVR)}, true
	case hasWorkloadDiagnosis(gvr):
		return []relatedWatch{self, in(podGVR), in(ovEventsGVR), in(pvcGVR)}, true
	}
	return nil, false
}

var (
	diagWarningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#E5C07B"))
	diagErrorStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF5C5C"))
)

func diagStyle(s diagSeverity) *lipgloss.Style {
	switch s {
	case diagError:
		return &diagErrorStyle
	case diagWarning, diagIncomplete:
		return &diagWarningStyle
	default:
		return WhiteStyle()
	}
}

// serviceDiagnosisNote says what the Service check does not prove.
const serviceDiagnosisNote = "Configuration only: a consistent Service does not prove DNS, the network (CNI, NetworkPolicy, firewalls) or the application work."

// diagnosisRows reads what the diagnosis of the folder's object needs and
// renders it: summary rows, each finding with the sources it cites, then
// the backends of a Service.
func (f *RelatedObjectsFolder) diagnosisRows(ctx context.Context) ([]table.Row, error) {
	switch f.gvr {
	case pvcGVR:
		return f.claimDiagnosisRows(ctx)
	case serviceGVR:
	default:
		return f.workloadDiagnosisRows(ctx)
	}
	log := ctrllog.FromContext(ctx).WithName("diagnosis")
	obj, err := f.reader.GetByGVR(ctx, serviceGVR, f.namespace, f.name)
	if err != nil {
		return nil, err
	}
	in := serviceInput{Service: &corev1.Service{}}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, in.Service); err != nil {
		return nil, fmt.Errorf("decode service: %w", err)
	}
	if in.Service.Spec.Type != corev1.ServiceTypeExternalName {
		in.Pods, in.PodsErr = listTyped[corev1.Pod](ctx, f.reader, podGVR, f.namespace)
		in.Slices, in.SlicesErr = listTyped[discoveryv1.EndpointSlice](ctx, f.reader, endpointSlicesGVR, f.namespace)
	}
	d := diagnoseService(in)
	log.V(1).Info("service diagnosis", "namespace", f.namespace, "name", f.name, "findings", len(d.Findings), "backends", len(d.Backends))
	return renderDiagnosis(f.Deps, f.rowDeps, f.Path(), d), nil
}

// workloadDiagnosisRows reads the workload, its pods (and the ReplicaSets
// of a Deployment), the events and the claims of its namespace from the
// cache and renders the diagnosis.
func (f *RelatedObjectsFolder) workloadDiagnosisRows(ctx context.Context) ([]table.Row, error) {
	log := ctrllog.FromContext(ctx).WithName("diagnosis")
	obj, err := f.reader.GetByGVR(ctx, f.gvr, f.namespace, f.name)
	if err != nil {
		return nil, err
	}
	in := workloadInput{GVR: f.gvr, Object: obj, Now: time.Now(), Grace: appconfig.DefaultOverview().StartupGrace.Duration}
	if f.Deps.AppConfig != nil {
		in.Grace = f.Deps.AppConfig.Overview.Normalized().StartupGrace.Duration
	}
	if f.gvr != podGVR {
		in.Pods, in.PodsErr = listTyped[corev1.Pod](ctx, f.reader, podGVR, f.namespace)
	}
	if f.gvr == relDeploymentsGVR {
		in.ReplicaSets, in.ReplicaSetsErr = listTyped[appsv1.ReplicaSet](ctx, f.reader, relReplicaSetsGVR, f.namespace)
	}
	in.Events, in.EventsErr = listTyped[corev1.Event](ctx, f.reader, ovEventsGVR, f.namespace)
	in.Claims, in.ClaimsErr = listTyped[corev1.PersistentVolumeClaim](ctx, f.reader, pvcGVR, f.namespace)
	if f.gvr == podGVR {
		in.Scheduling = f.readScheduling(ctx, obj, in)
		in.Storage = f.podStorage(ctx, obj, in)
	}
	d, err := diagnoseWorkload(in)
	if err != nil {
		return nil, err
	}
	log.V(1).Info("workload diagnosis", "gvr", f.gvr.String(), "namespace", f.namespace, "name", f.name, "findings", len(d.Findings))
	return renderDiagnosis(f.Deps, f.rowDeps, f.Path(), d), nil
}

// readScheduling reads what kc's node checks of an unschedulable pod need,
// through the same scoped reads as the rest of the diagnosis: the nodes,
// the pods of every namespace when the pod requests resources, and the
// volumes of its bound claims. Nothing is read for another pod. These
// reads do not refresh the folder; C-r reads them again.
func (f *RelatedObjectsFolder) readScheduling(ctx context.Context, obj *unstructured.Unstructured, in workloadInput) *schedulingInput {
	var pod corev1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &pod); err != nil || !needsSchedulingExplanation(&pod) {
		return nil
	}
	log := ctrllog.FromContext(ctx).WithName("diagnosis")
	s := &schedulingInput{Pod: &pod, Now: in.Now, Claims: in.Claims, ClaimsErr: in.ClaimsErr}
	s.Nodes, s.NodesErr = listTyped[corev1.Node](ctx, f.reader, relNodesGVR, "")
	if s.NodesErr != nil {
		return s
	}
	if requests, _ := podRequestsAndLimits(&pod); len(requests) == 0 {
		s.AssignedSkipped = true
	} else {
		s.Assigned, s.AssignedErr = listTyped[corev1.Pod](ctx, f.reader, podGVR, "")
	}
	if in.ClaimsErr == nil {
		for _, name := range boundClaimVolumes(&pod, in.Claims) {
			pv, err := readVolume(ctx, f.reader, name)
			if err != nil {
				if s.VolumeErrs == nil {
					s.VolumeErrs = make(map[string]error)
				}
				s.VolumeErrs[name] = err
				continue
			}
			if s.Volumes == nil {
				s.Volumes = make(map[string]*corev1.PersistentVolume)
			}
			s.Volumes[name] = pv
		}
	}
	log.V(1).Info("scheduling inputs", "namespace", pod.Namespace, "pod", pod.Name, "nodes", len(s.Nodes), "assigned", len(s.Assigned), "volumes", len(s.Volumes))
	return s
}

// readVolume reads one PersistentVolume through r.
func readVolume(ctx context.Context, r RelatedReader, name string) (*corev1.PersistentVolume, error) {
	u, err := r.GetByGVR(ctx, pvGVR, "", name)
	if err != nil {
		return nil, err
	}
	var v corev1.PersistentVolume
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &v); err != nil {
		return nil, fmt.Errorf("decode persistentvolume %s: %w", name, err)
	}
	return &v, nil
}

func listTyped[T any](ctx context.Context, r RelatedReader, gvr schema.GroupVersionResource, namespace string) ([]T, error) {
	list, err := r.ListByGVR(ctx, gvr, namespace)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(list.Items))
	for i := range list.Items {
		var v T
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, &v); err != nil {
			return nil, fmt.Errorf("decode %s %s: %w", gvr.Resource, list.Items[i].GetName(), err)
		}
		out = append(out, v)
	}
	return out, nil
}

func renderDiagnosis(deps Deps, depsFor func(RelatedRef) Deps, path []string, d diagReport) []table.Row {
	var rows []table.Row
	for _, s := range d.Summary {
		label, value, ok := strings.Cut(s, ": ")
		if !ok {
			label, value = "service", s
		}
		row := NewRowItem("summary/"+label, []string{label, "", "", value}, append(slices.Clone(path), label), WhiteStyle())
		row.details = s
		rows = append(rows, row)
	}
	for _, fd := range d.Findings {
		rows = append(rows, findingRows(deps, depsFor, path, fd)...)
	}
	rows = append(rows, relatedRowsPrefixed(deps, depsFor, path, "backend/", diagRefs(d.Backends))...)
	if d.Note != "" {
		note := NewRowItem("note", []string{"note", "", "", d.Note}, append(slices.Clone(path), "note"), DimStyle())
		note.details = d.Note
		rows = append(rows, note)
	}
	return rows
}

// findingRows renders one finding: the observed fact with its time, the
// sources it cites, the logs it offers, then the possible explanation and
// the next step, each on a row of its own.
func findingRows(deps Deps, depsFor func(RelatedRef) Deps, path []string, fd diagFinding) []table.Row {
	id := "finding/" + fd.Code
	title := fd.Title
	if !fd.Observed.IsZero() {
		title += " (observed " + fd.Observed.UTC().Format(time.RFC3339) + ")"
	}
	row := NewRowItem(id, []string{fd.Severity.String(), "", "", title}, append(slices.Clone(path), fd.Code), diagStyle(fd.Severity))
	row.details = strings.TrimSuffix(fd.Title+". "+fd.Detail, " ")
	rows := []table.Row{row}
	rows = append(rows, relatedRowsPrefixed(deps, depsFor, path, id+"/", diagRefs(fd.Links))...)
	for _, l := range fd.Logs {
		rows = append(rows, diagLogRow(deps, depsFor, path, id, l))
	}
	for _, extra := range []struct{ label, text string }{{"maybe", fd.Hint}, {"next", fd.Next}} {
		if extra.text == "" {
			continue
		}
		r := NewRowItem(id+"/"+extra.label, []string{extra.label, "", "", extra.text}, append(slices.Clone(path), extra.label), DimStyle())
		r.details = extra.text
		rows = append(rows, r)
	}
	return rows
}

// diagLogRow is a log row for the exact pod and container of a finding.
func diagLogRow(deps Deps, depsFor func(RelatedRef) Deps, path []string, findingID string, l diagLog) table.Row {
	logDeps := deps
	if depsFor != nil {
		logDeps = depsFor(RelatedRef{GVR: podGVR, Namespace: l.Spec.Namespace, Name: l.Spec.Pod})
	}
	id := findingID + "/logs/" + l.Spec.Container
	what := "current instance"
	if l.Spec.Previous {
		id += "/previous"
		what = "previous instance"
	}
	cells := []string{l.Label, "log", l.Spec.Namespace, fmt.Sprintf("pod %s, container %s, %s", l.Spec.Pod, l.Spec.Container, what)}
	item := NewContainerLogItem(logDeps, id, cells, append(slices.Clone(path), l.Label), l.Spec)
	item.RowItem.details = fmt.Sprintf("Logs of container %s of pod %s (%s)", l.Spec.Container, l.Spec.Pod, what)
	return item
}

func diagRefs(links []diagLink) []RelatedRef {
	refs := make([]RelatedRef, 0, len(links))
	for _, l := range links {
		refs = append(refs, RelatedRef{GVR: l.GVR, Namespace: l.Namespace, Name: l.Name, Relation: l.Evidence, Problem: l.Problem})
	}
	return refs
}
