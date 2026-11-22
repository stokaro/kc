package appconfig

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"k8s.io/client-go/util/jsonpath"
)

// CustomColumn is one entry of the columns config for a resource type.
//
// An entry with JSONPath adds a column computed from the object, as
// `kubectl get -o custom-columns` does. An entry with Hide hides the server
// Table column of that name; the name "*" hides every server column except
// Name. The two are exclusive.
type CustomColumn struct {
	// Name is the column title, or the server column to hide.
	Name string `json:"name"`
	// JSONPath is a kubectl custom-columns expression such as
	// ".spec.template.spec.containers[*].image" or "{.metadata.uid}".
	JSONPath string `json:"jsonPath,omitempty"`
	// Hide hides the server column Name instead of adding a column.
	Hide bool `json:"hide,omitempty"`
	// Wide shows the added column only in wide mode (objects.columns: wide).
	Wide bool `json:"wide,omitempty"`
}

// HideAllServerColumns is the Name of a hide entry that hides every server
// column except Name.
const HideAllServerColumns = "*"

// ColumnsKey is the columns config key for a resource:
// "<group>/<version>/<resource>", or "<version>/<resource>" for the core
// group (gvr.GroupVersion().String() + "/" + resource).
func ColumnsKey(groupVersion, resource string) string {
	return groupVersion + "/" + resource
}

// ParseColumnsKey splits a columns config key into group, version and
// resource. Core resources have two parts ("v1/pods"), others three
// ("apps/v1/deployments").
//
//revive:disable-next-line:function-result-limit the three named parts of a GVR key and the error
func ParseColumnsKey(key string) (group, version, resource string, err error) {
	parts := strings.Split(key, "/")
	for _, p := range parts {
		if strings.TrimSpace(p) == "" || p != strings.TrimSpace(p) {
			return "", "", "", fmt.Errorf("columns key %q: want group/version/resource, e.g. apps/v1/deployments or v1/pods", key)
		}
	}
	switch len(parts) {
	case 2:
		return "", parts[0], parts[1], nil
	case 3:
		return parts[0], parts[1], parts[2], nil
	}
	return "", "", "", fmt.Errorf("columns key %q: want group/version/resource, e.g. apps/v1/deployments or v1/pods", key)
}

// relaxedJSONPath matches the expressions kubectl custom-columns accepts.
// Copied from k8s.io/kubectl/pkg/cmd/get/customcolumn.go (v0.37.2), so kc
// reads the same expressions without importing kubectl's command packages.
var relaxedJSONPath = regexp.MustCompile(`^\{\.?([^{}]+)\}$|^\.?([^{}]+)$`)

// CustomColumnTemplate turns a kubectl custom-columns expression
// (".a.b", "a.b", "{.a.b}" or "{a.b}") into a JSONPath template "{.a.b}",
// as kubectl's RelaxedJSONPathExpression does.
func CustomColumnTemplate(expr string) (string, error) {
	m := relaxedJSONPath.FindStringSubmatch(expr)
	if m == nil {
		return "", fmt.Errorf("unexpected path string %q, expected a 'name1.name2' or '.name1.name2' or '{name1.name2}' or '{.name1.name2}'", expr)
	}
	spec := m[1]
	if spec == "" {
		spec = m[2]
	}
	return "{." + spec + "}", nil
}

// ParseCustomColumn compiles the JSONPath of an added column with the
// settings kubectl custom-columns uses: a missing field is not an error.
func ParseCustomColumn(expr string) (*jsonpath.JSONPath, error) {
	tmpl, err := CustomColumnTemplate(expr)
	if err != nil {
		return nil, err
	}
	p := jsonpath.New("column").AllowMissingKeys(true)
	if err := p.Parse(tmpl); err != nil {
		return nil, err
	}
	return p, nil
}

// Validate checks one entry. It does not know the server columns, so hiding
// a column the server does not send is not an error.
func (c CustomColumn) Validate() error {
	name := strings.TrimSpace(c.Name)
	switch {
	case name == "":
		return fmt.Errorf("name is empty")
	case c.Hide && c.JSONPath != "":
		return fmt.Errorf("%s: set either jsonPath or hide, not both", name)
	case c.Hide && c.Wide:
		return fmt.Errorf("%s: wide applies to jsonPath columns only", name)
	case c.Hide && strings.EqualFold(name, "name"):
		return fmt.Errorf("%s: the Name column cannot be hidden", name)
	case c.Hide:
		return nil
	case strings.TrimSpace(c.JSONPath) == "":
		return fmt.Errorf("%s: jsonPath is empty", name)
	}
	if _, err := ParseCustomColumn(strings.TrimSpace(c.JSONPath)); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// ValidateColumns checks the columns config and returns one error per bad
// key or entry, sorted by key.
func ValidateColumns(columns map[string][]CustomColumn) []error {
	keys := make([]string, 0, len(columns))
	for k := range columns {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var errs []error
	for _, k := range keys {
		if _, _, _, err := ParseColumnsKey(k); err != nil {
			errs = append(errs, err)
			continue
		}
		for i, c := range columns[k] {
			if err := c.Validate(); err != nil {
				errs = append(errs, fmt.Errorf("columns %q entry %d: %w", k, i, err))
			}
		}
	}
	return errs
}
