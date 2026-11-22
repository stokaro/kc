// Package manifestdiff compares two Kubernetes manifests as YAML.
//
// The configuration view leaves out fields the API server writes (see
// ServerFields) and, unless asked, the status. The raw view compares the
// objects as read. Neither view reorders lists or touches other fields: the
// order of containers, env vars and ports is part of the comparison.
package manifestdiff

import (
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/stokaro/kc/internal/textdiff"
)

// ContextLines is the number of unchanged lines shown around each change,
// as in diff -u.
const ContextLines = 3

// ServerFields are the metadata fields the configuration view omits. The API
// server sets them; they differ between any two objects and say nothing
// about the configuration.
var ServerFields = []string{
	"uid",
	"resourceVersion",
	"generation",
	"creationTimestamp",
	"managedFields",
	"selfLink",
}

// Mode selects what the comparison includes.
type Mode int

const (
	// ModeConfig omits ServerFields and, unless IncludeStatus is set, status.
	ModeConfig Mode = iota
	// ModeRaw compares the objects as read.
	ModeRaw
)

func (m Mode) String() string {
	if m == ModeRaw {
		return "raw"
	}
	return "configuration"
}

// Options control a comparison.
type Options struct {
	Mode Mode
	// IncludeStatus keeps status in the configuration view.
	IncludeStatus bool
}

// Side is one of the two compared objects.
type Side struct {
	Context   string
	GVR       schema.GroupVersionResource
	Namespace string
	Name      string
	// Object is the manifest as unstructured JSON. Compare does not modify it.
	Object map[string]any
}

// Label names the object the way the diff headers show it:
// context, full GVR, namespace and name.
func (s Side) Label() string {
	parts := []string{
		"context " + orDash(s.Context),
		"gvr " + gvrString(s.GVR),
	}
	if s.Namespace != "" {
		parts = append(parts, "namespace "+s.Namespace)
	}
	parts = append(parts, "name "+s.Name)
	return strings.Join(parts, ", ")
}

// path is the short identity used on the "---" and "+++" lines.
func (s Side) path() string {
	parts := []string{orDash(s.Context), gvrString(s.GVR)}
	if s.Namespace != "" {
		parts = append(parts, s.Namespace)
	}
	return strings.Join(append(parts, s.Name), "/")
}

func gvrString(gvr schema.GroupVersionResource) string {
	return gvr.GroupVersion().String() + "/" + gvr.Resource
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Result is a rendered comparison.
type Result struct {
	// Text is the header followed by the unified diff, without a trailing
	// newline.
	Text string
	// ChangeLines holds the zero-based row in Text of each hunk header.
	ChangeLines []int
	// Omitted lists the fields left out of either side, as dotted paths.
	Omitted []string
	// Equal reports that the compared YAML is identical.
	Equal bool
}

// Compare renders the differences between left and right.
func Compare(left, right Side, opts Options) (Result, error) {
	l, lOmit := Strip(left.Object, opts)
	r, rOmit := Strip(right.Object, opts)
	ly, err := yaml.Marshal(l)
	if err != nil {
		return Result{}, fmt.Errorf("encoding left manifest: %w", err)
	}
	ry, err := yaml.Marshal(r)
	if err != nil {
		return Result{}, fmt.Errorf("encoding right manifest: %w", err)
	}
	omitted := append(lOmit, rOmit...)
	slices.Sort(omitted)
	omitted = slices.Compact(omitted)

	header := []string{
		"# left:    " + left.Label(),
		"# right:   " + right.Label(),
		"# view:    " + opts.Mode.String(),
		"# omitted: " + omittedText(omitted, opts),
		"",
	}
	diff := textdiff.UnifiedDiff("left/"+left.path(), "right/"+right.path(), string(ly), string(ry), ContextLines)
	res := Result{Omitted: omitted, Equal: diff.Equal()}
	if res.Equal {
		res.Text = strings.Join(append(header, "No differences in the "+opts.Mode.String()+" view."), "\n")
		return res, nil
	}
	res.Text = strings.Join(header, "\n") + "\n" + diff.Text
	for _, at := range diff.HunkLines {
		res.ChangeLines = append(res.ChangeLines, at+len(header))
	}
	return res, nil
}

func omittedText(omitted []string, opts Options) string {
	if opts.Mode == ModeRaw {
		return "nothing (raw view)"
	}
	if len(omitted) == 0 {
		return "nothing"
	}
	return strings.Join(omitted, ", ")
}

// Strip returns obj without the fields the view omits, and the dotted paths
// of the fields it removed. obj is not modified; nested values are shared
// with the result.
func Strip(obj map[string]any, opts Options) (map[string]any, []string) {
	if opts.Mode == ModeRaw || obj == nil {
		return obj, nil
	}
	out := make(map[string]any, len(obj))
	for k, v := range obj {
		out[k] = v
	}
	var omitted []string
	if !opts.IncludeStatus {
		if _, ok := out["status"]; ok {
			delete(out, "status")
			omitted = append(omitted, "status")
		}
	}
	meta, ok := out["metadata"].(map[string]any)
	if !ok {
		return out, omitted
	}
	m := make(map[string]any, len(meta))
	for k, v := range meta {
		m[k] = v
	}
	for _, f := range ServerFields {
		if _, ok := m[f]; ok {
			delete(m, f)
			omitted = append(omitted, "metadata."+f)
		}
	}
	out["metadata"] = m
	return out, omitted
}
