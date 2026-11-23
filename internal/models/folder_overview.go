package models

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"

	table "github.com/stokaro/kc/internal/table"
)

// OverviewRowID is the ID of the /overview row in the panel root.
const OverviewRowID = "__overview__"

// OverviewFolder is /overview: one row per finding, grouped in sections.
// Its rows only read the engine's latest report; the engine recomputes in
// the background.
type OverviewFolder struct {
	*BaseFolder
	engine    *overviewEngine
	namespace string
	unsub     func()
}

// NewOverviewFolder returns the overview of deps' cluster in namespace
// (AllNamespaces for the whole cluster).
func NewOverviewFolder(deps Deps, namespace string, path []string) *OverviewFolder {
	return newOverviewFolder(deps, namespace, path, newOverviewEngine(deps, namespace))
}

func newOverviewFolder(deps Deps, namespace string, path []string, engine *overviewEngine) *OverviewFolder {
	base := NewBaseFolder(deps, overviewFindingColumns(), path)
	f := &OverviewFolder{BaseFolder: base, engine: engine, namespace: namespace}
	base.SetRowSource(newReadingRowSource(newSliceRowSource(f.populate), func() { _, _ = engine.snapshot() }))
	f.unsub = engine.subscribe(f.Refresh)
	return f
}

// overviewFindingColumns are the columns of finding rows. In a narrow pane
// the name and a long value give up width, so the state stays visible
// without horizontal panning (ADR 0176); F3 and Info show the full text.
func overviewFindingColumns() []table.Column {
	return []table.Column{{Title: " Name", MinWidth: 12}, {Title: "Value", MinWidth: 10}, {Title: "State"}}
}

// Close stops the engine; navigators call it when they drop the folder.
func (f *OverviewFolder) Close() {
	if f.unsub != nil {
		f.unsub()
	}
	f.engine.close()
}

// NamespaceScope returns the namespace the overview covers, or AllNamespaces.
func (f *OverviewFolder) NamespaceScope() string { return f.namespace }

// KubectlCommand shows the closest single command: the nodes and the
// workloads in scope.
func (f *OverviewFolder) KubectlCommand() string {
	args := []string{"get", "nodes,pods,deployments,statefulsets,daemonsets,jobs"}
	return kubectlCommand(f.Deps, append(args, namespaceArgs(f.namespace, true)...)...) + "  # objects the overview reads; a finding row shows its inputs"
}

func (f *OverviewFolder) populate(context.Context) ([]table.Row, error) {
	report, err := f.engine.snapshot()
	if report == nil {
		msg := "loading"
		if err != nil {
			msg = "unavailable: " + err.Error()
		}
		return []table.Row{NewSimpleItem("overview/loading", []string{" " + msg, "", ""}, f.Path(), DimStyle())}, nil
	}
	rows := make([]table.Row, 0, len(report.findings)+2*len(overviewSections)+1)
	// The first row counts the whole report, so one warning among many
	// quiet checks is seen at once (ADR 0172).
	rows = append(rows, f.attentionRow(report))
	for _, section := range overviewSections {
		// The header names the scope, so cluster capacity next to an
		// empty namespace does not read as that namespace's figures.
		scope, mixed := sectionScope(report.findings, section, f.namespace)
		var shown, quiet []overviewFinding
		for _, finding := range report.findings {
			switch {
			case finding.section != section:
			case quietFinding(finding):
				quiet = append(quiet, finding)
			default:
				shown = append(shown, finding)
			}
		}
		counts := countClasses(append(slices.Clone(shown), quiet...))
		header := NewSimpleItem("section/"+strings.ToLower(section), []string{" " + section + " · " + scope, counts.problems(), counts.passed()}, f.Path(), overviewSectionStyle())
		header.SetDetails(section + " covers " + scope + ". " + overviewSwitchHint)
		rows = append(rows, header)
		for _, finding := range shown {
			rows = append(rows, f.findingRow(finding, mixed, f.Path()))
		}
		if len(quiet) > 0 {
			rows = append(rows, f.quietRow(section, quiet))
		}
	}
	return rows, nil
}

// findingRow is the row of finding in a folder at parent. In a section
// that mixes namespace and cluster findings (mixed), a cluster finding's
// title says so.
func (f *OverviewFolder) findingRow(finding overviewFinding, mixed bool, parent []string) table.Row {
	path := append(slices.Clone(parent), strings.TrimPrefix(finding.title, "/"))
	style := overviewSeverityStyle(finding.severity)
	if finding.notApplicable != "" {
		style = DimStyle()
	}
	title := finding.title
	if _, cluster := findingScope(finding.id, f.namespace); cluster && mixed {
		title += " (cluster)"
	}
	item := &OverviewFindingItem{
		RowItem: NewRowItem(finding.id, []string{title, finding.value, finding.state}, path, style),
		folder:  f,
		id:      finding.id,
	}
	item.SetDetails(title + ": " + finding.value)
	// The viewer's command line holds one runnable command; with several
	// inputs F3 lists them all and the viewer shows none, so it never
	// presents one of them as the source of the value.
	if line, ok := findingCommandLine(f.Deps, finding, nil); ok && line.Copy == "" {
		item.SetViewCommand(line.Show)
	}
	return item
}

// findingCommands lists the inputs of a finding for F3: what each one
// supplies, then its commands.
func (f *OverviewFolder) findingCommands(finding overviewFinding) string {
	var b strings.Builder
	for i, in := range findingInputs(f.Deps, finding, nil) {
		if i > 0 {
			b.WriteString("\n")
		}
		if in.supplies != "" {
			fmt.Fprintf(&b, "# %s\n", in.supplies)
		}
		if len(in.commands) == 0 {
			fmt.Fprintf(&b, "# %s\n", templateHint(in.template))
			continue
		}
		for _, c := range in.commands {
			b.WriteString(c + "\n")
		}
		if in.omitted > 0 {
			fmt.Fprintf(&b, "# %d more; %s\n", in.omitted, templateHint(in.template))
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// OverviewFindingItem is an overview row. Enter lists the objects behind it;
// F3 explains the value.
type OverviewFindingItem struct {
	*RowItem
	folder *OverviewFolder
	id     string
}

// Enter opens the objects behind the finding.
func (i *OverviewFindingItem) Enter() (Folder, error) {
	f := i.folder
	finding, _ := f.currentFinding(i.id)
	return newOverviewTargetsFolder(f.Deps, f.engine, i.id, finding.title, i.Path()), nil
}

// ViewContent explains how the value was computed.
//
//revive:disable-next-line:function-result-limit the Viewable interface fixes these six results; internal/ui calls it
func (i *OverviewFindingItem) ViewContent() (title, body, lang, mime, filename string, err error) {
	finding, ok := i.folder.currentFinding(i.id)
	if !ok {
		return "", "", "", "", "", ErrNoViewContent
	}
	return finding.title, i.folder.explainFinding(finding), "", "text/plain", "", nil
}

func (f *OverviewFolder) currentFinding(id string) (overviewFinding, bool) {
	report, _ := f.engine.snapshot()
	return report.finding(id)
}

func (f *OverviewFolder) explainFinding(finding overviewFinding) string {
	var b strings.Builder
	scope, cluster := findingScope(finding.id, f.namespace)
	if cluster && f.namespace != AllNamespaces {
		scope += ", whatever the namespace scope (" + namespaceScopeText(f.namespace) + ")"
	}
	fmt.Fprintf(&b, "%s\n\nValue: %s\nState: %s\nScope: %s\n", finding.title, finding.value, finding.state, scope)
	if finding.unavailable != "" {
		fmt.Fprintf(&b, "Not available: %s\n", finding.unavailable)
	}
	if finding.notApplicable != "" {
		fmt.Fprintf(&b, "Not applicable: %s; nothing was checked, which is not a pass.\n", finding.notApplicable)
	}
	fmt.Fprintf(&b, "\nHow it is computed\n\n%s\n", finding.explain)
	b.WriteString(`
The overview reads kc's informer cache, recomputed at most once a second
after a change. Thresholds are set under overview: in ~/.kc/config.yaml.
`)
	b.WriteString("\nkubectl\n\n")
	b.WriteString(f.findingCommands(finding))
	b.WriteString("\n\n" + overviewSwitchHint + "\n")
	return b.String()
}

// OverviewTargetsFolder lists exactly the objects behind one finding, with
// the reason each one is listed. It follows the engine's latest report.
type OverviewTargetsFolder struct {
	*BaseFolder
	engine    *overviewEngine
	findingID string
	title     string
	unsub     func()
}

func newOverviewTargetsFolder(deps Deps, engine *overviewEngine, findingID, title string, path []string) *OverviewTargetsFolder {
	// The value says why the object is listed; the name, namespace and
	// resource give up width first in a narrow pane (ADR 0176).
	base := NewBaseFolder(deps, []table.Column{{Title: " Name", MinWidth: 14}, {Title: "Namespace", MinWidth: 8}, {Title: "Resource", MinWidth: 8}, {Title: "Value"}}, path)
	f := &OverviewTargetsFolder{BaseFolder: base, engine: engine, findingID: findingID, title: title}
	base.SetRowSource(newReadingRowSource(newSliceRowSource(f.populate), func() { _, _ = engine.snapshot() }))
	f.unsub = engine.subscribe(f.Refresh)
	return f
}

// Close stops following the report; the overview folder below keeps the
// engine.
func (f *OverviewTargetsFolder) Close() {
	if f.unsub != nil {
		f.unsub()
	}
}

// KubectlCommand shows the first input of the finding, with a count when
// there are more; CommandLineFor gives the whole set.
func (f *OverviewTargetsFolder) KubectlCommand() string {
	finding, ok := f.finding()
	if !ok {
		return ""
	}
	line, ok := findingCommandLine(f.Deps, finding, nil)
	if !ok {
		return ""
	}
	return line.Show
}

func (f *OverviewTargetsFolder) populate(context.Context) ([]table.Row, error) {
	report, _ := f.engine.snapshot()
	finding, ok := report.finding(f.findingID)
	if !ok {
		return nil, nil
	}
	verbs := resourceVerbsByGVR(f.Deps)
	rows := make([]table.Row, 0, len(finding.targets))
	for _, t := range finding.targets {
		rows = append(rows, f.targetRow(t, verbs[schema.GroupResource{Group: t.gvr.Group, Resource: t.gvr.Resource}]))
	}
	return rows, nil
}

// overviewTargetID is the row ID of an object: its full GVR, namespace and
// name, because one list can mix resources.
func overviewTargetID(gvr schema.GroupVersionResource, namespace, name string) string {
	parts := []string{gvr.GroupVersion().String(), gvr.Resource}
	if namespace != "" {
		parts = append(parts, namespace)
	}
	return strings.Join(append(parts, name), "/")
}

func (f *OverviewTargetsFolder) targetRow(t overviewTarget, verbs []string) table.Row {
	path := append(f.Path(), t.name)
	ctor, hasChild := ChildFor(t.gvr)
	title := t.name
	if hasChild {
		title = "/" + t.name
	}
	cells := []string{title, t.namespace, kubectlResource(t.gvr), t.value}
	obj := NewObjectRow(overviewTargetID(t.gvr, t.namespace, t.name), cells, path, t.gvr, t.namespace, t.name, overviewSeverityStyle(t.severity))
	obj.SetResourceVerbs(verbs)
	obj.WithViewContent(objectViewContent(f.Deps, t.gvr, t.namespace, t.name))
	args := []string{"get", kubectlResource(t.gvr), t.name}
	if t.namespace != "" {
		args = append(args, "-n", t.namespace)
	}
	obj.SetViewCommand(kubectlCommand(f.Deps, append(args, "-o", "yaml")...))
	obj.SetDetails(objectDetails(t.namespace, t.name, "", t.gvr.GroupVersion().String()))
	if t.involved != nil {
		ref, missing := involvedObjectRef(f.Deps, *t.involved, t.namespace)
		return &OverviewEventItem{ObjectRow: obj, ref: ref, missing: missing}
	}
	if hasChild && ctor != nil {
		deps, ns, name := f.Deps, t.namespace, t.name
		return NewObjectWithChildItem(obj, func() (Folder, error) { return ctor(deps, ns, name, path), nil })
	}
	return obj
}

// OverviewEventItem is a warning event in the overview. Enter opens the
// object the event is about in the other panel; F3 and F8 act on the event.
type OverviewEventItem struct {
	*ObjectRow
	ref     ObjectRef
	missing string
}

var _ ObjectLink = (*OverviewEventItem)(nil)

// LinkTarget returns the involved object.
func (i *OverviewEventItem) LinkTarget() (ObjectRef, string) { return i.ref, i.missing }

// involvedObjectRef maps an event's involved object to a GVR with the
// RESTMapper (cached discovery, no request). An object without a namespace
// of its own is in the event's namespace when its kind is namespaced.
func involvedObjectRef(deps Deps, o corev1.ObjectReference, eventNamespace string) (ObjectRef, string) {
	if deps.Cl == nil {
		return ObjectRef{}, "the cluster's API types are not known yet; try again"
	}
	gv, err := schema.ParseGroupVersion(o.APIVersion)
	if err != nil || o.Kind == "" || o.Name == "" {
		return ObjectRef{}, fmt.Sprintf("the event names no object kc can open (%s %s %s)", o.APIVersion, o.Kind, o.Name)
	}
	mapper := deps.Cl.RESTMapper()
	if mapper == nil {
		return ObjectRef{}, "the cluster's API types are not known yet; try again"
	}
	mapping, err := mapper.RESTMapping(schema.GroupKind{Group: gv.Group, Kind: o.Kind}, gv.Version)
	if err != nil {
		return ObjectRef{}, fmt.Sprintf("%s %s is not served by the cluster", o.APIVersion, o.Kind)
	}
	namespaced := mapping.Scope.Name() == meta.RESTScopeNameNamespace
	ns := ""
	if namespaced {
		ns = o.Namespace
		if ns == "" {
			ns = eventNamespace
		}
	}
	return ObjectRef{GVR: mapping.Resource, Namespaced: namespaced, Namespace: ns, Name: o.Name}, ""
}

// resourceVerbsByGVR returns the verbs discovery lists per resource, so the
// object actions (F8 delete and others) work in the targets list.
func resourceVerbsByGVR(deps Deps) map[schema.GroupResource][]string {
	out := make(map[schema.GroupResource][]string)
	if deps.Cl == nil {
		return out
	}
	infos, err := deps.Cl.GetResourceInfos()
	if err != nil {
		return out
	}
	for _, info := range infos {
		out[schema.GroupResource{Group: info.GVK.Group, Resource: info.Resource}] = info.Verbs
	}
	return out
}

// readingRowSource counts every read of a folder as a read of the engine, so
// the engine keeps running while the folder is on screen even when nothing
// changes.
type readingRowSource struct {
	rowSource
	read func()
}

func newReadingRowSource(src rowSource, read func()) *readingRowSource {
	return &readingRowSource{rowSource: src, read: read}
}

func (s *readingRowSource) Lines(ctx context.Context, top, num int) []table.Row {
	s.read()
	return s.rowSource.Lines(ctx, top, num)
}

func (s *readingRowSource) Len(ctx context.Context) int {
	s.read()
	return s.rowSource.Len(ctx)
}

var overviewSectionSty = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#5FD7FF"))

func overviewSectionStyle() *lipgloss.Style { return &overviewSectionSty }

// RowHealth maps a severity to the row health of object lists, so a
// warning in the overview has the color of a not-ready row in a list and a
// failure that of a failed one.
func (s overviewSeverity) RowHealth() RowHealth {
	switch s {
	case severityOK:
		return RowHealthOK
	case severityWarning:
		return RowHealthNotReady
	case severityFailed:
		return RowHealthFailed
	}
	return RowHealthUnknown
}

// overviewSeverityStyle returns the shared row health style of a severity
// (RowHealthStyle), and dim when the source is not available.
func overviewSeverityStyle(s overviewSeverity) *lipgloss.Style {
	if s == severityUnknown {
		return DimStyle()
	}
	return RowHealthStyle(s.RowHealth(), WhiteStyle())
}

// overviewRootItem is the /overview row of the panel root.
func overviewRootItem(deps Deps, namespace string, parentPath []string) Item {
	path := append(slices.Clone(parentPath), "overview")
	item := NewChildResourceItem(OverviewRowID, []string{"/overview", "", ""}, path, func() (Folder, error) {
		return NewOverviewFolder(deps, namespace, path), nil
	})
	item.SetDetails("Cluster overview: capacity, health, risks, activity")
	return item
}
