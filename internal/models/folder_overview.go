package models

import (
	"context"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	table "github.com/sttts/kc/internal/table"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// overviewRowID is the ID of the /overview row in the panel root.
const overviewRowID = "__overview__"

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
	base := NewBaseFolder(deps, []table.Column{{Title: " Name"}, {Title: "Value"}, {Title: "State"}}, path)
	f := &OverviewFolder{BaseFolder: base, engine: engine, namespace: namespace}
	base.SetRowSource(newReadingRowSource(newSliceRowSource(f.populate), func() { _, _ = engine.snapshot() }))
	f.unsub = engine.subscribe(f.Refresh)
	return f
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
	return kubectlCommand(f.Deps, append(args, namespaceArgs(f.namespace, true)...)...)
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
	rows := make([]table.Row, 0, len(report.findings)+len(overviewSections))
	for _, section := range overviewSections {
		rows = append(rows, NewSimpleItem("section/"+strings.ToLower(section), []string{" " + section, "", ""}, f.Path(), overviewSectionStyle()))
		for _, finding := range report.findings {
			if finding.section != section {
				continue
			}
			rows = append(rows, f.findingRow(finding))
		}
	}
	return rows, nil
}

func (f *OverviewFolder) findingRow(finding overviewFinding) table.Row {
	path := append(f.Path(), strings.TrimPrefix(finding.title, "/"))
	style := overviewSeverityStyle(finding.severity)
	item := &OverviewFindingItem{
		RowItem: NewRowItem(finding.id, []string{finding.title, finding.value, finding.state}, path, style),
		folder:  f,
		id:      finding.id,
	}
	item.SetDetails(finding.title + ": " + finding.value)
	// The command line holds one command; F3 lists all of them.
	if len(finding.kubectl) > 0 {
		item.SetViewCommand(kubectlCommand(f.Deps, finding.kubectl[0]...))
	}
	return item
}

// findingCommands joins the kubectl commands that show a finding's inputs.
func (f *OverviewFolder) findingCommands(finding overviewFinding) string {
	cmds := make([]string, 0, len(finding.kubectl))
	for _, args := range finding.kubectl {
		cmds = append(cmds, kubectlCommand(f.Deps, args...))
	}
	return strings.Join(cmds, "\n")
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
func (i *OverviewFindingItem) ViewContent() (string, string, string, string, string, error) {
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
	scope := "all namespaces"
	if f.namespace != AllNamespaces {
		scope = "namespace " + f.namespace
	}
	fmt.Fprintf(&b, "%s\n\nValue: %s\nState: %s\nScope: %s\n", finding.title, finding.value, finding.state, scope)
	if finding.unavailable != "" {
		fmt.Fprintf(&b, "Not available: %s\n", finding.unavailable)
	}
	fmt.Fprintf(&b, "\nHow it is computed\n\n%s\n", finding.explain)
	b.WriteString(`
The overview reads kc's informer cache, recomputed at most once a second
after a change. Thresholds are set under overview: in ~/.kc/config.yaml.
`)
	b.WriteString("\nkubectl\n\n")
	b.WriteString(f.findingCommands(finding))
	b.WriteString("\n")
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
	base := NewBaseFolder(deps, []table.Column{{Title: " Name"}, {Title: "Namespace"}, {Title: "Resource"}, {Title: "Value"}}, path)
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

// KubectlCommand shows the commands of the finding.
func (f *OverviewTargetsFolder) KubectlCommand() string {
	report, _ := f.engine.snapshot()
	finding, ok := report.finding(f.findingID)
	if !ok || len(finding.kubectl) == 0 {
		return ""
	}
	return kubectlCommand(f.Deps, finding.kubectl[0]...)
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
	if hasChild && ctor != nil {
		deps, ns, name := f.Deps, t.namespace, t.name
		return NewObjectWithChildItem(obj, func() (Folder, error) { return ctor(deps, ns, name, path), nil })
	}
	return obj
}

// resourceVerbsByGVR returns the verbs discovery lists per resource, so the
// object actions (F8 delete and others) work in the targets list.
func resourceVerbsByGVR(deps Deps) map[schema.GroupResource][]string {
	out := map[schema.GroupResource][]string{}
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

// Overview styles. Until the shared row health classification (#93) lands,
// the overview keeps its own small mapping from severity to style.
var (
	overviewWarningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD75F"))
	overviewSectionSty   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#5FD7FF"))
)

func overviewSectionStyle() *lipgloss.Style { return &overviewSectionSty }

// overviewSeverityStyle maps a severity to a row style: normal, warning,
// failed, and dim when the source is not available.
func overviewSeverityStyle(s overviewSeverity) *lipgloss.Style {
	switch s {
	case severityWarning:
		return &overviewWarningStyle
	case severityFailed:
		return NotReadyStyle()
	case severityOK:
		return WhiteStyle()
	}
	return DimStyle()
}

// overviewRootItem is the /overview row of the panel root.
func overviewRootItem(deps Deps, namespace string, parentPath []string) Item {
	path := append(append([]string{}, parentPath...), "overview")
	item := NewChildResourceItem(overviewRowID, []string{"/overview", "", ""}, path, func() (Folder, error) {
		return NewOverviewFolder(deps, namespace, path), nil
	})
	item.SetDetails("Cluster overview: capacity, health, risks, activity")
	return item
}
