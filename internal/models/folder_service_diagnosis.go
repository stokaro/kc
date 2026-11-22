package models

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	table "github.com/stokaro/kc/internal/table"
)

// builtinObjectRows are rows models itself adds inside objects, next to
// the rows registered with RegisterObjectRows.
func builtinObjectRows(gvr schema.GroupVersionResource) (ObjectRowsFunc, bool) {
	if gvr == serviceGVR {
		return serviceDiagnosisRows, true
	}
	return nil, false
}

// serviceDiagnosisRows offers /diagnosis inside a Service, next to its
// /endpointslices.
func serviceDiagnosisRows(deps Deps, namespace, name string, path []string) []table.Row {
	p := append(slices.Clone(path), DiagnosisSegment)
	item := NewChildResourceItem(namespace+"/"+name+"/"+DiagnosisSegment, []string{"/" + DiagnosisSegment}, p, func() (Folder, error) {
		return newServiceDiagnosisFolder(deps, namespace, name, path)
	})
	item.RowItem.details = "Check the selector, pods, EndpointSlices and target ports of this Service"
	return []table.Row{item}
}

// newServiceDiagnosisFolder builds the folder at path/diagnosis. Enter calls
// it on the UI thread, so it acquires nothing and reads nothing: the
// unscoped cluster and the scope decisions are taken on first use, in the
// background populate and watch start.
func newServiceDiagnosisFolder(deps Deps, namespace, name string, path []string) (Folder, error) {
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
	return newRelatedFolder(deps.Ctx, deps, newScopedReader(resolve), releaseLazy, RelatedDiagnosis, serviceGVR, namespace, name, path)
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

// diagnosisNote says what the check does not prove.
const diagnosisNote = "Configuration only: a consistent Service does not prove DNS, the network (CNI, NetworkPolicy, firewalls) or the application work."

// diagnosisRows reads the Service, the pods and the EndpointSlices of its
// namespace and renders the diagnosis: summary rows, each finding with the
// objects it cites, then every backend with its evidence.
func (f *RelatedObjectsFolder) diagnosisRows(ctx context.Context) ([]table.Row, error) {
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

func renderDiagnosis(deps Deps, depsFor func(RelatedRef) Deps, path []string, d serviceDiagnosis) []table.Row {
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
		id := "finding/" + fd.Code
		row := NewRowItem(id, []string{fd.Severity.String(), "", "", fd.Title}, append(slices.Clone(path), fd.Code), diagStyle(fd.Severity))
		row.details = fd.Title + ". " + fd.Detail
		rows = append(rows, row)
		rows = append(rows, relatedRowsPrefixed(deps, depsFor, path, id+"/", diagRefs(fd.Links))...)
	}
	rows = append(rows, relatedRowsPrefixed(deps, depsFor, path, "backend/", diagRefs(d.Backends))...)
	note := NewRowItem("note", []string{"note", "", "", diagnosisNote}, append(slices.Clone(path), "note"), DimStyle())
	note.details = diagnosisNote
	return append(rows, note)
}

func diagRefs(links []diagLink) []RelatedRef {
	refs := make([]RelatedRef, 0, len(links))
	for _, l := range links {
		refs = append(refs, RelatedRef{GVR: l.GVR, Namespace: l.Namespace, Name: l.Name, Relation: l.Evidence, Problem: l.Problem})
	}
	return refs
}
