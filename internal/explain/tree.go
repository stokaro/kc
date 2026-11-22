package explain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ErrKindNotDocumented means the group-version document has no schema for
// the kind. The server answered; it just publishes no documentation for it.
var ErrKindNotDocumented = errors.New("no schema published for this kind")

// Node is an object type whose fields can be listed: the kind itself or a
// nested object reached through fields, array items and map values.
type Node struct {
	doc         *Document
	schema      *Schema
	typeName    string
	description string
}

// Field describes one property of a Node.
type Field struct {
	Name        string
	Type        string // kubectl explain style: string, Object, []Container, map[string]string
	Required    bool
	Description string
	Format      string
	Enum        []string
	Default     string // JSON, empty when the schema sets none
	ListType    string
	ListMapKeys []string
	// PreserveUnknown marks objects that accept fields the schema does not list.
	PreserveUnknown bool
	// Node is set when the field, its array items or its map values are
	// objects with documented fields to browse.
	Node *Node
}

// Root returns the field tree for gvk.
func (d *Document) Root(gvk schema.GroupVersionKind) (*Node, error) {
	_, s, ok := d.FindKind(gvk)
	if !ok {
		return nil, fmt.Errorf("%s: %w", gvk.String(), ErrKindNotDocumented)
	}
	return &Node{doc: d, schema: d.resolve(s), typeName: gvk.Kind, description: d.description(s)}, nil
}

// TypeName is the name of the node's type: the kind, a referenced type
// name such as PodSpec, or Object for inline CRD schemas.
func (n *Node) TypeName() string { return n.typeName }

// Description returns the type's documentation; it may be empty.
func (n *Node) Description() string { return n.description }

// Fields lists the node's properties sorted by name, as kubectl explain does.
func (n *Node) Fields() []Field {
	if n == nil || n.schema == nil {
		return nil
	}
	props := make(map[string]*Schema)
	required := make(map[string]bool)
	for _, part := range n.parts() {
		for name, p := range part.Properties {
			if _, seen := props[name]; !seen {
				props[name] = p
			}
		}
		for _, name := range part.Required {
			required[name] = true
		}
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	fields := make([]Field, 0, len(names))
	for _, name := range names {
		fields = append(fields, n.doc.field(name, props[name], required[name]))
	}
	return fields
}

// parts returns the schema and the members of a multi-element allOf, each
// resolved; together they carry the node's properties.
func (n *Node) parts() []*Schema {
	parts := []*Schema{n.schema}
	for _, member := range n.schema.AllOf {
		if r := n.doc.resolve(member); r != nil && r != n.schema {
			parts = append(parts, r)
		}
	}
	return parts
}

// Field returns the named property.
func (n *Node) Field(name string) (Field, bool) {
	for _, f := range n.Fields() {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

// Lookup follows a field path such as spec.template.spec.containers.
// Arrays and maps are crossed transparently, as in kubectl explain.
func (n *Node) Lookup(path []string) (*Node, error) {
	cur := n
	for i, name := range path {
		f, ok := cur.Field(name)
		if !ok {
			return nil, fmt.Errorf("field %q does not exist", strings.Join(path[:i+1], "."))
		}
		if f.Node == nil {
			return nil, fmt.Errorf("field %q has no nested fields", strings.Join(path[:i+1], "."))
		}
		cur = f.Node
	}
	return cur, nil
}

func (d *Document) field(name string, p *Schema, required bool) Field {
	r := d.resolve(p)
	f := Field{
		Name:        name,
		Type:        d.typeLabel(p),
		Required:    required,
		Description: d.description(p),
	}
	if r != nil {
		f.Format = r.Format
		f.ListType = r.ListType
		f.ListMapKeys = append([]string(nil), r.ListMapKeys...)
		f.PreserveUnknown = r.PreserveUnknownFields
		f.Enum = formatEnum(firstNonEmptyEnum(p, r))
		f.Default = compactJSON(firstNonEmptyRaw(p.Default, r.Default))
	}
	if obj, typeName := d.objectBehind(p); obj != nil {
		f.Node = &Node{doc: d, schema: obj, typeName: typeName, description: f.Description}
	}
	return f
}

// objectBehind crosses arrays and maps to the object schema whose fields a
// user browses next. It returns nil for scalars and for objects without
// listed properties (opaque or preserve-unknown-fields objects).
func (d *Document) objectBehind(p *Schema) (*Schema, string) {
	cur := p
	for range maxRefHops {
		r := d.resolve(cur)
		if r == nil {
			return nil, ""
		}
		switch {
		case r.Items != nil:
			cur = r.Items
			continue
		case r.mapValue() != nil:
			cur = r.mapValue()
			continue
		}
		if !hasProperties(d, r) {
			return nil, ""
		}
		return r, d.typeLabel(cur)
	}
	return nil, ""
}

func hasProperties(d *Document, s *Schema) bool {
	if len(s.Properties) > 0 {
		return true
	}
	for _, member := range s.AllOf {
		if r := d.resolve(member); r != nil && r != s && len(r.Properties) > 0 {
			return true
		}
	}
	return false
}

func firstNonEmptyEnum(schemas ...*Schema) []json.RawMessage {
	for _, s := range schemas {
		if s != nil && len(s.Enum) > 0 {
			return s.Enum
		}
	}
	return nil
}

func firstNonEmptyRaw(values ...json.RawMessage) json.RawMessage {
	for _, v := range values {
		if len(v) > 0 {
			return v
		}
	}
	return nil
}

// formatEnum prints enum values as kubectl does: bare strings, with the
// empty string shown as "".
func formatEnum(values []json.RawMessage) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			if s == "" {
				s = `""`
			}
			out = append(out, s)
			continue
		}
		out = append(out, compactJSON(raw))
	}
	return out
}

func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}
