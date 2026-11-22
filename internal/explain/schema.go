// Package explain reads the OpenAPI v3 documents a cluster publishes under
// /openapi/v3 and turns them into a browsable field tree, the same data
// `kubectl explain` prints.
package explain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Document is the part of an OpenAPI v3 group-version document kc reads:
// the named schemas under components.
type Document struct {
	Components struct {
		Schemas map[string]*Schema `json:"schemas"`
	} `json:"components"`
}

// Schema is the subset of an OpenAPI v3 schema object that explain shows.
// Built-in types reference each other with $ref (often wrapped in a
// one-element allOf so a description can sit next to the reference); CRD
// schemas are inline.
type Schema struct {
	Ref                  string             `json:"$ref,omitempty"`
	Type                 string             `json:"type,omitempty"`
	Format               string             `json:"format,omitempty"`
	Description          string             `json:"description,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	AdditionalProperties *SchemaOrBool      `json:"additionalProperties,omitempty"`
	AllOf                []*Schema          `json:"allOf,omitempty"`
	Enum                 []json.RawMessage  `json:"enum,omitempty"`
	Default              json.RawMessage    `json:"default,omitempty"`

	GroupVersionKinds     []GroupVersionKind `json:"x-kubernetes-group-version-kind,omitempty"`
	PreserveUnknownFields bool               `json:"x-kubernetes-preserve-unknown-fields,omitempty"`
	IntOrString           bool               `json:"x-kubernetes-int-or-string,omitempty"`
	EmbeddedResource      bool               `json:"x-kubernetes-embedded-resource,omitempty"`
	ListType              string             `json:"x-kubernetes-list-type,omitempty"`
	ListMapKeys           []string           `json:"x-kubernetes-list-map-keys,omitempty"`
}

// GroupVersionKind is the x-kubernetes-group-version-kind entry of a top-level type.
type GroupVersionKind struct {
	Group   string `json:"group"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
}

// SchemaOrBool holds additionalProperties, which is either a schema or a
// boolean. Only a schema describes map values.
type SchemaOrBool struct {
	Schema *Schema
	Allows bool
}

// UnmarshalJSON accepts both forms.
func (s *SchemaOrBool) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("true")) || bytes.Equal(trimmed, []byte("false")) {
		s.Allows = bytes.Equal(trimmed, []byte("true"))
		return nil
	}
	var sch Schema
	if err := json.Unmarshal(trimmed, &sch); err != nil {
		return err
	}
	s.Schema = &sch
	s.Allows = true
	return nil
}

// mapValue returns the map value schema, or nil when the schema is not a map.
func (s *Schema) mapValue() *Schema {
	if s == nil || s.AdditionalProperties == nil {
		return nil
	}
	return s.AdditionalProperties.Schema
}

// Parse decodes an OpenAPI v3 group-version document in JSON.
func Parse(data []byte) (*Document, error) {
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decoding OpenAPI v3 document: %w", err)
	}
	if doc.Components.Schemas == nil {
		doc.Components.Schemas = map[string]*Schema{}
	}
	return &doc, nil
}

const refPrefix = "#/components/schemas/"

// lookup returns the schema a $ref points to.
func (d *Document) lookup(ref string) *Schema {
	if d == nil || !strings.HasPrefix(ref, refPrefix) {
		return nil
	}
	return d.Components.Schemas[strings.TrimPrefix(ref, refPrefix)]
}

// maxRefHops bounds reference chains so a malformed document cannot loop.
const maxRefHops = 32

// resolve follows $ref and single-element allOf wrappers to the schema that
// carries the type's content. A broken reference resolves to nil.
func (d *Document) resolve(s *Schema) *Schema {
	for range maxRefHops {
		switch {
		case s == nil:
			return nil
		case s.Ref != "":
			s = d.lookup(s.Ref)
		case len(s.AllOf) == 1 && len(s.Properties) == 0 && s.Type == "" && s.Items == nil && s.mapValue() == nil:
			s = s.AllOf[0]
		default:
			return s
		}
	}
	return nil
}

// description returns the first non-empty description along the reference
// chain, outermost first, so a field's own text wins over its type's text.
func (d *Document) description(s *Schema) string {
	for range maxRefHops {
		if s == nil {
			return ""
		}
		if s.Description != "" {
			return s.Description
		}
		switch {
		case s.Ref != "":
			s = d.lookup(s.Ref)
		case len(s.AllOf) == 1:
			s = s.AllOf[0]
		default:
			return ""
		}
	}
	return ""
}

// typeLabel names a field type the way kubectl explain does: string,
// integer, Object, []Container, map[string]string, IntOrString.
func (d *Document) typeLabel(s *Schema) string {
	return d.typeLabelDepth(s, 0)
}

func (d *Document) typeLabelDepth(s *Schema, depth int) string {
	if s == nil || depth > maxRefHops {
		return "Object"
	}
	switch {
	case s.Items != nil:
		return "[]" + d.typeLabelDepth(s.Items, depth+1)
	case s.mapValue() != nil:
		return "map[string]" + d.typeLabelDepth(s.mapValue(), depth+1)
	case s.Ref == "" && len(s.AllOf) == 1 && len(s.Properties) == 0:
		return d.typeLabelDepth(s.AllOf[0], depth+1)
	case s.Ref != "":
		target := d.lookup(s.Ref)
		if target == nil || target.Type == "" || target.Type == "object" {
			name := strings.TrimPrefix(s.Ref, refPrefix)
			if i := strings.LastIndex(name, "."); i >= 0 {
				name = name[i+1:]
			}
			if name == "" {
				return "Object"
			}
			return name
		}
		return target.Type
	case s.IntOrString:
		return "IntOrString"
	case s.Type == "" || s.Type == "object":
		return "Object"
	default:
		return s.Type
	}
}

// FindKind returns the top-level schema for gvk. A group-version document
// can hold types of other groups (referenced ones), and two groups can
// define the same kind, so the whole group, version and kind must match.
func (d *Document) FindKind(gvk schema.GroupVersionKind) (string, *Schema, bool) {
	if d == nil {
		return "", nil, false
	}
	names := make([]string, 0, len(d.Components.Schemas))
	for name := range d.Components.Schemas {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s := d.Components.Schemas[name]
		if s == nil {
			continue
		}
		for _, g := range s.GroupVersionKinds {
			if g.Group == gvk.Group && g.Version == gvk.Version && g.Kind == gvk.Kind {
				return name, s, true
			}
		}
	}
	return "", nil, false
}
