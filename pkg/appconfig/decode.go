package appconfig

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	yaml "sigs.k8s.io/yaml"
)

// readOnlySection is the top-level key of ReadOnlyConfig. Load fails closed
// when this section does not load.
const readOnlySection = "readOnly"

// sectionProblem is a top-level section of the file that did not load.
type sectionProblem struct {
	index int    // the field of Config
	name  string // its key, as kc spells it
	err   error  // names the field that failed and why
}

// decodedFile is a configuration file that parsed.
type decodedFile struct {
	cfg *Config
	// raw is the file as a generic map, for Update.
	raw map[string]any
	// problems lists the sections that did not load, in the order of
	// Config; cfg holds their defaults.
	problems []sectionProblem
}

// decodeFile parses a configuration file onto the defaults.
//
// A file that is not valid YAML, or whose top level is not a mapping,
// returns an error and nothing else: none of it is used. Otherwise each
// top-level section decodes and validates on its own. A section that fails
// keeps its defaults as a whole, because the fields of one section depend
// on each other (nodeShell.enabled and nodeShell.contexts; a command and
// its dangerous flag), and its problem names the field that failed. The
// other sections load.
func decodeFile(data []byte) (decodedFile, error) {
	var f decodedFile
	if err := yaml.Unmarshal(data, &f.raw); err != nil {
		return decodedFile{}, fileError(err)
	}
	f.cfg = Default()
	if err := unmarshalOverDefaults(data, f.cfg); err != nil {
		// Something did not decode. Decoding the sections one by one keeps
		// the others and finds the field that failed.
		f.cfg, f.problems = decodeSections(f.raw)
	}
	normalizeLoaded(f.cfg)
	f.problems = append(f.problems, validateSections(f.cfg)...)
	slices.SortFunc(f.problems, func(a, b sectionProblem) int { return a.index - b.index })
	return f, nil
}

// decodeSections decodes each top-level section of raw onto the defaults on
// its own and keeps those that decode. Keys kc does not know are ignored,
// as the decoder ignores them.
func decodeSections(raw map[string]any) (*Config, []sectionProblem) {
	t := reflect.TypeFor[Config]()
	groups := make(map[int]map[string]any)
	for key, value := range raw {
		i, _, ok := fieldByJSONName(t, key)
		if !ok {
			continue
		}
		if groups[i] == nil {
			groups[i] = make(map[string]any)
		}
		// Keys that differ only in case go to one field; decoding them
		// together keeps the decoder's rule for them.
		groups[i][key] = value
	}
	cfg := Default()
	dst := reflect.ValueOf(cfg).Elem()
	var problems []sectionProblem
	for _, i := range slices.Sorted(maps.Keys(groups)) {
		name := jsonName(t.Field(i))
		section, err := decodeSection(groups[i])
		if err != nil {
			problems = append(problems, sectionProblem{index: i, name: name, err: locateGroup(name, groups[i], t.Field(i).Type, err)})
			continue
		}
		dst.Field(i).Set(reflect.ValueOf(section).Elem().Field(i))
	}
	return cfg, problems
}

// decodeSection decodes one section, given as its key and value, onto the
// defaults.
func decodeSection(group map[string]any) (*Config, error) {
	data, err := yaml.Marshal(group)
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := unmarshalOverDefaults(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validateSections checks the sections whose values decode but can still
// be wrong, and puts the defaults back for each one that is.
func validateSections(cfg *Config) []sectionProblem {
	t := reflect.TypeFor[Config]()
	var problems []sectionProblem
	add := func(name string, err error) {
		i, _, _ := fieldByJSONName(t, name)
		problems = append(problems, sectionProblem{index: i, name: name, err: err})
	}
	if err := cfg.Layout.Validate(); err != nil {
		add("layout", err)
		cfg.Layout = Default().Layout
	}
	if err := cfg.Charts.Validate(); err != nil {
		add("charts", err)
		cfg.Charts = DefaultCharts()
	}
	if err := cfg.Confirm.Validate(); err != nil {
		add("confirm", err)
		cfg.Confirm = Default().Confirm
	}
	return problems
}

// loadError reports the sections that did not load.
func loadError(path string, problems []sectionProblem) *LoadError {
	names := make([]string, 0, len(problems))
	errs := make([]error, 0, len(problems))
	for _, p := range problems {
		names = append(names, p.name)
		errs = append(errs, p.err)
	}
	err := errs[0]
	if len(errs) > 1 {
		err = errors.Join(errs...)
	}
	return &LoadError{Path: path, Err: err, Sections: names}
}

// locateGroup names the field of a section that did not decode. err is the
// section's error, used when no single value can be blamed.
func locateGroup(name string, group map[string]any, t reflect.Type, err error) error {
	for _, key := range slices.Sorted(maps.Keys(group)) {
		if found := locate(name, group[key], t); found != nil {
			return found
		}
	}
	return fmt.Errorf("%s: %s", name, innermost(err))
}

// locate returns the problem of the first value under path, in key and
// list order, that does not decode into a t, or nil when v decodes. The
// problem starts with the path of that value: "readOnly.contexts: want a
// list, got "prod"".
func locate(path string, v any, t reflect.Type) error {
	err := decodeValue(v, t)
	if err == nil {
		return nil
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if !decodesItself(t) {
		switch v := v.(type) {
		case map[string]any:
			for _, key := range slices.Sorted(maps.Keys(v)) {
				child, childPath, ok := childOf(t, path, key)
				if !ok {
					continue
				}
				if found := locate(childPath, v[key], child); found != nil {
					return found
				}
			}
		case []any:
			if t.Kind() == reflect.Slice {
				for i, item := range v {
					if found := locate(fmt.Sprintf("%s[%d]", path, i), item, t.Elem()); found != nil {
						return found
					}
				}
			}
		}
	}
	return fmt.Errorf("%s: %s", path, describeProblem(err, t, v, lastKey(path)))
}

// childOf returns the type and path of the value under key in a t.
func childOf(t reflect.Type, path, key string) (reflect.Type, string, bool) {
	switch t.Kind() {
	case reflect.Struct:
		i, name, ok := fieldByJSONName(t, key)
		if !ok {
			return nil, "", false
		}
		return t.Field(i).Type, path + "." + name, true
	case reflect.Map:
		return t.Elem(), path + "." + key, true
	}
	return nil, "", false
}

// decodeValue decodes v, a value of the file as a generic map, into a new t
// the way the file's decoder does.
func decodeValue(v any, t reflect.Type) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, reflect.New(t).Interface())
}

var (
	jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	durationType        = reflect.TypeFor[metav1.Duration]()
)

// decodesItself reports a type with its own decoder; its value is blamed
// as a whole.
func decodesItself(t reflect.Type) bool {
	p := reflect.PointerTo(t)
	return p.Implements(jsonUnmarshalerType) || p.Implements(textUnmarshalerType)
}

// describeProblem says what is wrong with v, the value at a field of type
// t, given the decoder's error.
func describeProblem(err error, t reflect.Type, v any, key string) string {
	if t == durationType {
		return "want a duration such as 30s or 2m, got " + showValue(v)
	}
	if _, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		return "want " + describeType(t) + ", got " + showValue(v)
	}
	// A type with its own decoder may name the key itself.
	return strings.TrimPrefix(innermost(err).Error(), key+": ")
}

// describeType names what a field of type t takes, in the words of a YAML
// file.
func describeType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "a list"
	case reflect.Map, reflect.Struct:
		return "a mapping"
	}
	return "a " + t.String()
}

// showValue prints a value of the file in a problem.
func showValue(v any) string {
	const maxLen = 40
	switch v := v.(type) {
	case nil:
		return "null"
	case string:
		if len(v) > maxLen {
			v = v[:maxLen] + "..."
		}
		return strconv.Quote(v)
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case []any:
		return "a list"
	case map[string]any:
		return "a mapping"
	}
	return fmt.Sprint(v)
}

// fileError describes a file that is not valid YAML or not a mapping.
func fileError(err error) error {
	if ute, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		got := map[string]string{"array": "a list", "string": "a string", "number": "a number", "bool": "a boolean"}[ute.Value]
		if got == "" {
			got = ute.Value
		}
		return fmt.Errorf("want a mapping of settings at the top level, got %s", got)
	}
	return innermost(err)
}

// innermost drops the wrappers sigs.k8s.io/yaml puts around the error of
// the YAML parser or the JSON decoder.
func innermost(err error) error {
	for {
		inner := errors.Unwrap(err)
		if inner == nil {
			return err
		}
		msg := err.Error()
		if !strings.HasPrefix(msg, "error converting YAML to JSON: ") &&
			!strings.HasPrefix(msg, "error unmarshaling JSON: ") &&
			!strings.HasPrefix(msg, "while decoding JSON: ") {
			return err
		}
		err = inner
	}
}

// fieldByJSONName finds the field of struct type t that the decoder fills
// from key: the exact name first, then a match without regard to case.
func fieldByJSONName(t reflect.Type, key string) (int, string, bool) {
	folded := -1
	for i := range t.NumField() {
		f := t.Field(i)
		name := jsonName(f)
		if !f.IsExported() || name == "-" {
			continue
		}
		if name == key {
			return i, name, true
		}
		if folded < 0 && strings.EqualFold(name, key) {
			folded = i
		}
	}
	if folded < 0 {
		return 0, "", false
	}
	return folded, jsonName(t.Field(folded)), true
}

// jsonName is the key of struct field f in the file.
func jsonName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "" {
		return f.Name
	}
	return name
}

// lastKey returns the last key of a path such as "commands[0].confirm".
func lastKey(path string) string {
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		path = path[i+1:]
	}
	if i := strings.IndexByte(path, '['); i >= 0 {
		path = path[:i]
	}
	return path
}
