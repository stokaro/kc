package explain_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/explain"
)

var (
	appsDeployment    = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	fixtureDeployment = schema.GroupVersionKind{Group: "fixture.kc.dev", Version: "v1", Kind: "Deployment"}
)

func loadDoc(t *testing.T, name string) *explain.Document {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := explain.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func root(t *testing.T, doc *explain.Document, gvk schema.GroupVersionKind) *explain.Node {
	t.Helper()
	n, err := doc.Root(gvk)
	if err != nil {
		t.Fatalf("Root(%s): %v", gvk, err)
	}
	return n
}

func field(t *testing.T, n *explain.Node, path ...string) explain.Field {
	t.Helper()
	parent, err := n.Lookup(path[:len(path)-1])
	if err != nil {
		t.Fatalf("Lookup(%v): %v", path, err)
	}
	f, ok := parent.Field(path[len(path)-1])
	if !ok {
		t.Fatalf("field %s not found; have %v", strings.Join(path, "."), names(parent.Fields()))
	}
	return f
}

func names(fields []explain.Field) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.Name)
	}
	return out
}

func TestFieldTypesRequiredAndRefs(t *testing.T) {
	t.Parallel()
	apps := root(t, loadDoc(t, "apis_apps_v1.json"), appsDeployment)
	crd := root(t, loadDoc(t, "apis_fixture.kc.dev_v1.json"), fixtureDeployment)

	tests := []struct {
		name     string
		node     *explain.Node
		path     []string
		typ      string
		required bool
		nested   bool
		desc     string // prefix; empty means not checked
		noDesc   bool
	}{
		{name: "allOf ref to object", node: apps, path: []string{"spec"}, typ: "DeploymentSpec", nested: true, desc: "Specification of the desired behavior"},
		{name: "required from referenced type", node: apps, path: []string{"spec", "template"}, typ: "PodTemplateSpec", required: true, nested: true},
		{name: "optional scalar with format", node: apps, path: []string{"spec", "replicas"}, typ: "integer", desc: "Number of desired pods"},
		{name: "array of refs", node: apps, path: []string{"spec", "template", "spec", "containers"}, typ: "[]Container", required: true, nested: true},
		{name: "array item fields", node: apps, path: []string{"spec", "template", "spec", "containers", "ports", "containerPort"}, typ: "integer", required: true},
		{name: "map of strings is a leaf", node: apps, path: []string{"spec", "template", "spec", "nodeSelector"}, typ: "map[string]string"},
		{name: "map of ref to scalar type", node: apps, path: []string{"spec", "template", "spec", "overhead"}, typ: "map[string]Quantity"},
		{name: "ref to untyped scalar uses the type name", node: apps, path: []string{"spec", "strategy", "rollingUpdate", "maxSurge"}, typ: "IntOrString"},
		{name: "ref to typed scalar uses its type", node: apps, path: []string{"metadata", "creationTimestamp"}, typ: "string", desc: "CreationTimestamp is"},
		{name: "missing description", node: apps, path: []string{"spec", "paused"}, typ: "boolean", noDesc: true},
		{name: "description falls back to the type", node: apps, path: []string{"status"}, typ: "DeploymentStatus", nested: true, desc: "DeploymentStatus is the most recently observed"},
		{name: "crd inline object", node: crd, path: []string{"spec"}, typ: "Object", required: true, nested: true, desc: "Spec of the fixture deployment."},
		{name: "crd required leaf", node: crd, path: []string{"spec", "target"}, typ: "string", required: true},
		{name: "crd missing description", node: crd, path: []string{"spec", "undocumented"}, typ: "integer", noDesc: true},
		{name: "crd preserve unknown fields is a leaf", node: crd, path: []string{"spec", "config"}, typ: "Object"},
		{name: "crd int-or-string", node: crd, path: []string{"spec", "port"}, typ: "IntOrString"},
		{name: "crd array of objects", node: crd, path: []string{"spec", "steps"}, typ: "[]Object", nested: true},
		{name: "crd map of objects", node: crd, path: []string{"spec", "routes"}, typ: "map[string]Object", nested: true},
		{name: "crd nested arrays", node: crd, path: []string{"spec", "matrix"}, typ: "[][]Object", nested: true},
		{name: "crd map value field", node: crd, path: []string{"spec", "routes", "backend"}, typ: "string", desc: "Backend service."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := field(t, tt.node, tt.path...)
			if f.Type != tt.typ {
				t.Errorf("type = %q, want %q", f.Type, tt.typ)
			}
			if f.Required != tt.required {
				t.Errorf("required = %v, want %v", f.Required, tt.required)
			}
			if (f.Node != nil) != tt.nested {
				t.Errorf("nested = %v, want %v", f.Node != nil, tt.nested)
			}
			if tt.noDesc && f.Description != "" {
				t.Errorf("description = %q, want none", f.Description)
			}
			if tt.desc != "" && !strings.HasPrefix(f.Description, tt.desc) {
				t.Errorf("description = %q, want prefix %q", f.Description, tt.desc)
			}
		})
	}
}

func TestEnumDefaultAndListType(t *testing.T) {
	t.Parallel()
	apps := root(t, loadDoc(t, "apis_apps_v1.json"), appsDeployment)
	crd := root(t, loadDoc(t, "apis_fixture.kc.dev_v1.json"), fixtureDeployment)

	protocol := field(t, apps, "spec", "template", "spec", "containers", "ports", "protocol")
	if want := []string{"SCTP", "TCP", "UDP"}; !slices.Equal(protocol.Enum, want) {
		t.Errorf("protocol enum = %v, want %v", protocol.Enum, want)
	}
	if protocol.Default != `"TCP"` {
		t.Errorf("protocol default = %q, want %q", protocol.Default, `"TCP"`)
	}
	strategy := field(t, apps, "spec", "strategy", "type")
	if want := []string{"Recreate", "RollingUpdate"}; !slices.Equal(strategy.Enum, want) {
		t.Errorf("strategy enum = %v, want %v", strategy.Enum, want)
	}
	if replicas := field(t, apps, "spec", "replicas"); replicas.Default != "" || replicas.Enum != nil {
		t.Errorf("replicas has default %q enum %v, want neither", replicas.Default, replicas.Enum)
	}
	mode := field(t, crd, "spec", "mode")
	if want := []string{"Blue", "Green", `""`}; !slices.Equal(mode.Enum, want) {
		t.Errorf("mode enum = %v, want %v", mode.Enum, want)
	}
	if mode.Default != `"Blue"` {
		t.Errorf("mode default = %q", mode.Default)
	}
	containers := field(t, apps, "spec", "template", "spec", "containers")
	if containers.ListType != "map" || !slices.Equal(containers.ListMapKeys, []string{"name"}) {
		t.Errorf("containers list type = %q keys %v", containers.ListType, containers.ListMapKeys)
	}
	if !field(t, crd, "spec", "config").PreserveUnknown {
		t.Error("config: preserve unknown fields not reported")
	}
}

func TestDuplicateKindAcrossGroups(t *testing.T) {
	t.Parallel()
	apps := root(t, loadDoc(t, "apis_apps_v1.json"), appsDeployment)
	crd := root(t, loadDoc(t, "apis_fixture.kc.dev_v1.json"), fixtureDeployment)
	if _, ok := apps.Field("status"); !ok {
		t.Fatal("apps Deployment lacks status")
	}
	if f, _ := crd.Field("spec"); f.Type != "Object" {
		t.Errorf("fixture Deployment spec type = %q; the apps schema was used", f.Type)
	}
	if _, ok := field(t, crd, "spec").Node.Field("target"); !ok {
		t.Error("fixture Deployment spec lacks target")
	}
	if _, ok := field(t, apps, "spec").Node.Field("target"); ok {
		t.Error("apps Deployment spec has the fixture's target field")
	}

	// The apps document does not hold the fixture kind, and the other way
	// round: the whole GVK must match, not the kind name.
	for _, tc := range []struct {
		doc string
		gvk schema.GroupVersionKind
	}{
		{"apis_apps_v1.json", fixtureDeployment},
		{"apis_fixture.kc.dev_v1.json", appsDeployment},
		{"apis_apps_v1.json", schema.GroupVersionKind{Group: "apps", Version: "v1beta2", Kind: "Deployment"}},
	} {
		_, err := loadDoc(t, tc.doc).Root(tc.gvk)
		if !errors.Is(err, explain.ErrKindNotDocumented) || !explain.IsAbsent(err) {
			t.Errorf("Root(%s) in %s: err = %v, want ErrKindNotDocumented", tc.gvk, tc.doc, err)
		}
	}

	// A schema listing several GVKs is found under each of them.
	for _, gvk := range []schema.GroupVersionKind{
		{Group: "", Version: "v1", Kind: "DeleteOptions"},
		{Group: "apps", Version: "v1", Kind: "DeleteOptions"},
	} {
		if _, err := loadDoc(t, "apis_apps_v1.json").Root(gvk); err != nil {
			t.Errorf("Root(%s): %v", gvk, err)
		}
	}
}

func TestLookupPaths(t *testing.T) {
	t.Parallel()
	apps := root(t, loadDoc(t, "apis_apps_v1.json"), appsDeployment)
	tests := []struct {
		name    string
		path    []string
		fields  []string // expected field names at the end of the path
		errText string
	}{
		{name: "root", path: nil, fields: []string{"apiVersion", "kind", "metadata", "spec", "status"}},
		{name: "through refs", path: []string{"spec", "template", "spec"}, fields: []string{"containers", "nodeSelector", "overhead"}},
		{name: "through array items", path: []string{"spec", "template", "spec", "containers"}, fields: []string{"image", "imagePullPolicy", "name", "ports"}},
		{name: "array in array path", path: []string{"metadata", "ownerReferences"}, fields: []string{"apiVersion", "kind", "name", "uid"}},
		{name: "unknown field", path: []string{"spec", "nope"}, errText: `field "spec.nope" does not exist`},
		{name: "leaf has no fields", path: []string{"spec", "replicas"}, errText: `field "spec.replicas" has no nested fields`},
		{name: "map of strings has no fields", path: []string{"metadata", "labels"}, errText: "has no nested fields"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n, err := apps.Lookup(tt.path)
			if tt.errText != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errText) {
					t.Fatalf("err = %v, want %q", err, tt.errText)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := names(n.Fields()); !slices.Equal(got, tt.fields) {
				t.Errorf("fields = %v, want %v", got, tt.fields)
			}
		})
	}
}

func TestCyclesAndBrokenRefs(t *testing.T) {
	t.Parallel()
	loop := root(t, loadDoc(t, "apis_fixture.kc.dev_v1.json"), schema.GroupVersionKind{Group: "fixture.kc.dev", Version: "v1", Kind: "Loop"})
	deep, err := loop.Lookup(slices.Repeat([]string{"next"}, 50))
	if err != nil {
		t.Fatalf("a self-referencing type must stay browsable: %v", err)
	}
	if f, _ := deep.Field("next"); f.Type != "Loop" || f.Node == nil {
		t.Errorf("next at depth 50: type %q nested %v", f.Type, f.Node != nil)
	}
	broken, ok := loop.Field("broken")
	if !ok {
		t.Fatal("broken field missing")
	}
	if broken.Node != nil || broken.Type != "Missing" {
		t.Errorf("broken ref: type %q nested %v, want a leaf named after the ref", broken.Type, broken.Node != nil)
	}
}

func TestOpaqueKindHasNoFields(t *testing.T) {
	t.Parallel()
	opaque := root(t, loadDoc(t, "apis_fixture.kc.dev_v1.json"), schema.GroupVersionKind{Group: "fixture.kc.dev", Version: "v1", Kind: "Opaque"})
	if fields := opaque.Fields(); len(fields) != 0 {
		t.Errorf("fields = %v, want none", names(fields))
	}
	if opaque.Description() != "" {
		t.Errorf("description = %q, want none", opaque.Description())
	}
}

func TestFieldText(t *testing.T) {
	t.Parallel()
	apps := root(t, loadDoc(t, "apis_apps_v1.json"), appsDeployment)
	ports := field(t, apps, "spec", "template", "spec", "containers", "ports")
	text := explain.FieldText(appsDeployment, []string{"spec", "template", "spec", "containers"}, ports)
	for _, want := range []string{
		"GROUP:      apps\n",
		"KIND:       Deployment\n",
		"VERSION:    v1\n",
		"FIELD:      spec.template.spec.containers.ports <[]ContainerPort>\n",
		"    List of ports to expose from the container.\n",
		"  containerPort\t<integer> -required-\n",
		"  protocol\t<string>\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "REQUIRED:") {
		t.Errorf("optional field marked required:\n%s", text)
	}

	paused := field(t, apps, "spec", "paused")
	text = explain.FieldText(appsDeployment, []string{"spec"}, paused)
	if !strings.Contains(text, "<no description>") {
		t.Errorf("missing description not stated:\n%s", text)
	}

	protocol := field(t, ports.Node, "protocol")
	text = explain.FieldText(appsDeployment, nil, protocol)
	for _, want := range []string{"ENUM:       SCTP, TCP, UDP\n", "DEFAULT:    \"TCP\"\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	core := explain.FieldText(schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, nil, protocol)
	if strings.Contains(core, "GROUP:") {
		t.Errorf("core group printed:\n%s", core)
	}
}

func TestSummary(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"":                               "",
		"One line.":                      "One line.",
		"First sentence. Second one.":    "First sentence.",
		"First line\nsecond line":        "First line",
		"  padded e.g.x and more.  ":     "padded e.g.x and more.",
		"Type of deployment.\n\nEnum: x": "Type of deployment.",
	}
	for in, want := range tests {
		if got := explain.Summary(in); got != want {
			t.Errorf("Summary(%q) = %q, want %q", in, got, want)
		}
	}
}
