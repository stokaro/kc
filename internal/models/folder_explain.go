package models

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/explain"
	table "github.com/stokaro/kc/internal/table"
)

// ExplainTarget is implemented by rows that name an API type, so the UI
// can open its field documentation (M-e).
type ExplainTarget interface {
	// ExplainType returns the resource and, when known, its kind. The kind
	// may be empty; the caller then asks the RESTMapper.
	ExplainType() (schema.GroupVersionResource, schema.GroupVersionKind)
}

// ExplainType names the object's resource; the folder lists it at this version.
func (o *ObjectRow) ExplainType() (schema.GroupVersionResource, schema.GroupVersionKind) {
	return o.gvr, schema.GroupVersionKind{}
}

// ExplainType names the resource type of the row.
func (r *ResourceGroupItem) ExplainType() (schema.GroupVersionResource, schema.GroupVersionKind) {
	return r.gvr, r.gvk
}

// ExplainSpec identifies the documented type: the resource for the kubectl
// command and the kind whose schema is shown.
type ExplainSpec struct {
	GVR schema.GroupVersionResource
	GVK schema.GroupVersionKind
}

// ExplainFolder lists the fields of one schema node: the kind itself or a
// nested object. When no schema could be shown it lists one row that says
// why, telling missing documentation apart from a failed request.
type ExplainFolder struct {
	*BaseFolder
	spec      ExplainSpec
	node      *explain.Node
	err       error
	fieldPath []string
}

const explainStatusRowID = "__explain_status__"

// NewExplainFolder builds the folder for spec at the kind's root. Either
// root is set or err says why there is none.
func NewExplainFolder(deps Deps, spec ExplainSpec, root *explain.Node, err error) *ExplainFolder {
	return newExplainFolder(deps, spec, root, err, nil)
}

func newExplainFolder(deps Deps, spec ExplainSpec, node *explain.Node, err error, fieldPath []string) *ExplainFolder {
	f := &ExplainFolder{spec: spec, node: node, err: err, fieldPath: append([]string(nil), fieldPath...)}
	cols := []table.Column{{Title: " Name"}, {Title: "Type"}, {Title: "Required"}, {Title: "Description"}}
	f.BaseFolder = NewBaseFolder(deps, cols, f.breadcrumb())
	f.SetPopulate(f.buildRows)
	return f
}

// breadcrumb is /explain/<group/version>/<kind>/<field>/...; the group
// keeps kinds of the same name in different API groups apart.
func (f *ExplainFolder) breadcrumb() []string {
	name := f.spec.GVK.Kind
	if name == "" {
		name = f.spec.GVR.Resource
	}
	gv := f.spec.GVK.GroupVersion()
	if f.spec.GVK.Version == "" {
		gv = f.spec.GVR.GroupVersion()
	}
	path := []string{"explain", gv.String(), name}
	return append(path, f.fieldPath...)
}

// StandalonePath tells the navigator that Path is the whole breadcrumb;
// the explain tree is not below the row it was opened from.
func (f *ExplainFolder) StandalonePath() bool { return true }

// Spec returns the documented type.
func (f *ExplainFolder) Spec() ExplainSpec { return f.spec }

// FieldPath returns the fields from the kind to this folder.
func (f *ExplainFolder) FieldPath() []string { return append([]string(nil), f.fieldPath...) }

// Err reports why no fields are shown, or nil.
func (f *ExplainFolder) Err() error { return f.err }

// KubectlCommand prints the same documentation with kubectl.
func (f *ExplainFolder) KubectlCommand() string {
	return explainCommand(f.Deps, f.spec, f.fieldPath)
}

// explainCommand builds `kubectl explain <resource>[.<field>...]
// --api-version=<group/version>`. kubectl takes the group from
// --api-version, not from the resource argument.
func explainCommand(deps Deps, spec ExplainSpec, fieldPath []string) string {
	resource := spec.GVR.Resource
	if resource == "" {
		resource = strings.ToLower(spec.GVK.Kind)
	}
	arg := strings.Join(append([]string{resource}, fieldPath...), ".")
	gv := spec.GVR.GroupVersion()
	if spec.GVR.Version == "" {
		gv = spec.GVK.GroupVersion()
	}
	return kubectlCommand(deps, "explain", arg, "--api-version="+gv.String())
}

func (f *ExplainFolder) buildRows(context.Context) ([]table.Row, error) {
	if f.err != nil || f.node == nil {
		return []table.Row{f.statusRow()}, nil
	}
	fields := f.node.Fields()
	if len(fields) == 0 {
		return []table.Row{f.statusRow()}, nil
	}
	rows := make([]table.Row, 0, len(fields))
	for _, field := range fields {
		rows = append(rows, f.fieldRow(field))
	}
	return rows, nil
}

func (f *ExplainFolder) statusRow() table.Row {
	text, style := f.statusText()
	item := NewSimpleItem(explainStatusRowID, []string{text, "", "", ""}, f.Path(), style)
	return item
}

// statusText explains an empty listing. A failed request is red; missing
// documentation is a normal, dim state.
func (f *ExplainFolder) statusText() (string, *lipgloss.Style) {
	gvk := f.spec.GVK
	switch {
	case f.err != nil && explain.IsAbsent(f.err):
		return fmt.Sprintf("No documentation: the cluster publishes no schema for %s %s", gvk.GroupVersion().String(), gvk.Kind), DimStyle()
	case f.err != nil:
		return "Schema request failed: " + f.err.Error(), NotReadyStyle()
	case f.node == nil:
		return "No documentation", DimStyle()
	default:
		return "No fields documented: the schema lists no properties here", DimStyle()
	}
}

func (f *ExplainFolder) fieldRow(field explain.Field) table.Row {
	name := field.Name
	if field.Node != nil {
		name = "/" + name
	}
	required := ""
	if field.Required {
		required = "yes"
	}
	desc := explain.Summary(field.Description)
	if desc == "" {
		desc = "-"
	}
	path := append(f.Path(), field.Name)
	style := WhiteStyle()
	if field.Required {
		style = FavoriteResourceStyle()
	}
	item := NewSimpleItem(field.Name, []string{name, field.Type, required, desc}, path, style)
	item.SetDetails(field.Type)
	spec, deps, parent := f.spec, f.Deps, f.FieldPath()
	item.WithViewContent(func() (string, string, string, string, string, error) {
		title := strings.Join(append(append([]string{spec.GVK.Kind}, parent...), field.Name), ".")
		body := explain.FieldText(spec.GVK, parent, field)
		return title, body, "", "text/plain", strings.ToLower(title) + ".txt", nil
	})
	item.SetViewCommand(explainCommand(deps, spec, append(append([]string(nil), parent...), field.Name)))
	if field.Node == nil {
		return item
	}
	childPath := append(append([]string(nil), parent...), field.Name)
	node := field.Node
	return &explainBranchItem{SimpleItem: item, enter: func() (Folder, error) {
		return newExplainFolder(deps, spec, node, nil, childPath), nil
	}}
}

// explainBranchItem is a field with nested fields; Enter lists them.
type explainBranchItem struct {
	*SimpleItem
	enter func() (Folder, error)
}

func (e *explainBranchItem) Enter() (Folder, error) { return e.enter() }

// ErrNoExplainTarget means the row does not name an API type.
var ErrNoExplainTarget = errors.New("explain works on objects and resource types")
