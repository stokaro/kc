package appconfig_test

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	yaml "sigs.k8s.io/yaml"

	"github.com/stokaro/kc/pkg/appconfig"
)

// defaultsNotInSample are fields of Default() that config-default.yaml
// leaves out on purpose.
var defaultsNotInSample = map[string]string{
	"resources.columns": "an older spelling of objects.columns that kc still writes; the sample shows objects.columns",
}

// TestConfigDefaultsYAMLMatchesCode reads config-default.yaml from the repo
// root and compares every field, as written, with Default(). A key missing
// from the file fails here instead of being filled in by the loader, so a
// new setting cannot reach Default() without its sample line.
func TestConfigDefaultsYAMLMatchesCode(t *testing.T) {
	// The default shell command is named after $SHELL; the sample spells
	// the variable.
	t.Setenv("SHELL", "$SHELL")
	data, err := os.ReadFile("../../config-default.yaml")
	if err != nil {
		t.Fatalf("read defaults yaml: %v", err)
	}
	var fromYAML appconfig.Config
	if err := yaml.UnmarshalStrict(data, &fromYAML); err != nil {
		t.Fatalf("unmarshal defaults yaml: %v", err)
	}
	var diffs []string
	diffConfig("", reflect.ValueOf(fromYAML), reflect.ValueOf(*appconfig.Default()), &diffs)
	for _, d := range diffs {
		t.Error(d)
	}
}

// diffConfig appends the JSON paths where got (the sample) and want (the
// code) differ. Empty and nil collections are equal.
func diffConfig(path string, got, want reflect.Value, out *[]string) {
	if _, skip := defaultsNotInSample[strings.TrimPrefix(path, ".")]; skip {
		return
	}
	switch got.Kind() {
	case reflect.Struct:
		if !hasExportedFields(got.Type()) {
			if !reflect.DeepEqual(got.Interface(), want.Interface()) {
				*out = append(*out, fmt.Sprintf("%s: yaml %v, code %v", path, got.Interface(), want.Interface()))
			}
			return
		}
		for i := range got.NumField() {
			f := got.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" {
				name = f.Name
			}
			diffConfig(path+"."+name, got.Field(i), want.Field(i), out)
		}
	case reflect.Pointer:
		if got.IsNil() || want.IsNil() {
			if got.IsNil() != want.IsNil() {
				*out = append(*out, fmt.Sprintf("%s: yaml %v, code %v", path, got, want))
			}
			return
		}
		diffConfig(path, got.Elem(), want.Elem(), out)
	case reflect.Slice, reflect.Map:
		if got.Len() == 0 && want.Len() == 0 {
			return
		}
		if got.Kind() == reflect.Map || got.Len() != want.Len() {
			if !reflect.DeepEqual(got.Interface(), want.Interface()) {
				*out = append(*out, fmt.Sprintf("%s: yaml %v, code %v", path, got.Interface(), want.Interface()))
			}
			return
		}
		for i := range got.Len() {
			diffConfig(fmt.Sprintf("%s[%d]", path, i), got.Index(i), want.Index(i), out)
		}
	default:
		if !reflect.DeepEqual(got.Interface(), want.Interface()) {
			*out = append(*out, fmt.Sprintf("%s: yaml %v, code %v (missing or different in config-default.yaml)", path, got.Interface(), want.Interface()))
		}
	}
}

func hasExportedFields(t reflect.Type) bool {
	for f := range t.Fields() {
		if f.IsExported() {
			return true
		}
	}
	return false
}

// The comparison must see a field the sample leaves out; otherwise the test
// above passes for the wrong reason.
func TestDiffConfigSeesAMissingKey(t *testing.T) {
	var sample appconfig.Config
	if err := yaml.Unmarshal([]byte("terminal:\n  mode: overlay\n"), &sample); err != nil {
		t.Fatal(err)
	}
	want := appconfig.Config{Terminal: appconfig.TerminalConfig{Follow: true, Mode: appconfig.TerminalModeOverlay}}
	var diffs []string
	diffConfig("", reflect.ValueOf(sample), reflect.ValueOf(want), &diffs)
	if len(diffs) != 1 || !strings.HasPrefix(diffs[0], ".terminal.follow:") {
		t.Fatalf("diffs = %q, want one for terminal.follow", diffs)
	}
}
