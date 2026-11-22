package manifestdiff_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sttts/kc/internal/manifestdiff"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var deployments = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}

func deployment(ns, name, image string, mutate ...func(map[string]any)) map[string]any {
	obj := map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":              name,
			"namespace":         ns,
			"uid":               "uid-" + ns,
			"resourceVersion":   "100" + ns,
			"generation":        int64(3),
			"creationTimestamp": "2026-01-01T00:00:00Z",
			"managedFields":     []any{map[string]any{"manager": "kubectl"}},
			"labels":            map[string]any{"app": name},
		},
		"spec": map[string]any{
			"replicas": int64(2),
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{
						map[string]any{"name": "app", "image": image},
						map[string]any{"name": "sidecar", "image": "proxy:1"},
					},
				},
			},
		},
		"status": map[string]any{"readyReplicas": int64(2)},
	}
	for _, m := range mutate {
		m(obj)
	}
	return obj
}

func side(ns string, obj map[string]any) manifestdiff.Side {
	return manifestdiff.Side{Context: "mba", GVR: deployments, Namespace: ns, Name: "web", Object: obj}
}

func TestStrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		opts        manifestdiff.Options
		wantOmitted []string
		wantMeta    []string // metadata keys left
		wantStatus  bool
	}{
		{
			name:        "configuration view",
			opts:        manifestdiff.Options{},
			wantOmitted: []string{"status", "metadata.uid", "metadata.resourceVersion", "metadata.generation", "metadata.creationTimestamp", "metadata.managedFields"},
			wantMeta:    []string{"labels", "name", "namespace"},
		},
		{
			name:        "configuration view with status",
			opts:        manifestdiff.Options{IncludeStatus: true},
			wantOmitted: []string{"metadata.uid", "metadata.resourceVersion", "metadata.generation", "metadata.creationTimestamp", "metadata.managedFields"},
			wantMeta:    []string{"labels", "name", "namespace"},
			wantStatus:  true,
		},
		{
			name:       "raw view",
			opts:       manifestdiff.Options{Mode: manifestdiff.ModeRaw},
			wantMeta:   []string{"creationTimestamp", "generation", "labels", "managedFields", "name", "namespace", "resourceVersion", "uid"},
			wantStatus: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := deployment("a", "web", "app:1")
			before := deployment("a", "web", "app:1")
			got, omitted := manifestdiff.Strip(in, tc.opts)
			if !reflect.DeepEqual(in, before) {
				t.Fatal("Strip modified its input")
			}
			if !slices.Equal(omitted, tc.wantOmitted) {
				t.Errorf("omitted = %v, want %v", omitted, tc.wantOmitted)
			}
			meta := got["metadata"].(map[string]any)
			keys := make([]string, 0, len(meta))
			for k := range meta {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			if !slices.Equal(keys, tc.wantMeta) {
				t.Errorf("metadata keys = %v, want %v", keys, tc.wantMeta)
			}
			if _, ok := got["status"]; ok != tc.wantStatus {
				t.Errorf("status present = %v, want %v", ok, tc.wantStatus)
			}
			if !reflect.DeepEqual(got["spec"], before["spec"]) {
				t.Error("spec changed")
			}
		})
	}
}

func TestStripWithoutMetadata(t *testing.T) {
	t.Parallel()
	got, omitted := manifestdiff.Strip(map[string]any{"kind": "X", "status": "s"}, manifestdiff.Options{})
	if !reflect.DeepEqual(got, map[string]any{"kind": "X"}) || !slices.Equal(omitted, []string{"status"}) {
		t.Fatalf("got %v, omitted %v", got, omitted)
	}
}

func TestCompare(t *testing.T) {
	t.Parallel()
	swapContainers := func(obj map[string]any) {
		c := obj["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		list := c["containers"].([]any)
		c["containers"] = []any{list[1], list[0]}
	}
	otherStatus := func(obj map[string]any) {
		obj["status"] = map[string]any{"readyReplicas": int64(0)}
	}
	tests := []struct {
		name       string
		left       manifestdiff.Side
		right      manifestdiff.Side
		opts       manifestdiff.Options
		wantEqual  bool
		wantHunks  int
		contains   []string
		notContain []string
	}{
		{
			name:      "equal objects in different namespaces differ only in namespace",
			left:      side("a", deployment("a", "web", "app:1")),
			right:     side("b", deployment("b", "web", "app:1")),
			wantHunks: 1,
			contains: []string{
				"# left:    context mba, gvr apps/v1/deployments, namespace a, name web",
				"# right:   context mba, gvr apps/v1/deployments, namespace b, name web",
				"# view:    configuration",
				"# omitted: metadata.creationTimestamp, metadata.generation, metadata.managedFields, metadata.resourceVersion, metadata.uid, status",
				"--- left/mba/apps/v1/deployments/a/web",
				"+++ right/mba/apps/v1/deployments/b/web",
				"-  namespace: a",
				"+  namespace: b",
			},
			notContain: []string{"uid-a", "readyReplicas", "managedFields:"},
		},
		{
			name:      "identical objects",
			left:      side("a", deployment("a", "web", "app:1")),
			right:     side("a", deployment("a", "web", "app:1")),
			wantEqual: true,
			contains:  []string{"No differences in the configuration view."},
		},
		{
			name:      "status difference is hidden by default",
			left:      side("a", deployment("a", "web", "app:1")),
			right:     side("a", deployment("a", "web", "app:1", otherStatus)),
			wantEqual: true,
		},
		{
			name:      "status difference shows when status is included",
			left:      side("a", deployment("a", "web", "app:1")),
			right:     side("a", deployment("a", "web", "app:1", otherStatus)),
			opts:      manifestdiff.Options{IncludeStatus: true},
			wantHunks: 1,
			contains:  []string{"-  readyReplicas: 2", "+  readyReplicas: 0"},
		},
		{
			name:      "substantive difference",
			left:      side("a", deployment("a", "web", "app:1")),
			right:     side("a", deployment("a", "web", "app:2")),
			wantHunks: 1,
			contains:  []string{"-      - image: app:1", "+      - image: app:2"},
		},
		{
			name:      "array order is significant",
			left:      side("a", deployment("a", "web", "app:1")),
			right:     side("a", deployment("a", "web", "app:1", swapContainers)),
			wantHunks: 1,
			contains:  []string{"-        name: app", "+        name: app"},
		},
		{
			name:      "raw view keeps server fields",
			left:      side("a", deployment("a", "web", "app:1")),
			right:     side("b", deployment("b", "web", "app:1")),
			opts:      manifestdiff.Options{Mode: manifestdiff.ModeRaw},
			wantHunks: 1,
			contains:  []string{"# view:    raw", "# omitted: nothing (raw view)", "-  uid: uid-a", "+  uid: uid-b"},
		},
		{
			name: "cluster-scoped objects have no namespace label",
			left: manifestdiff.Side{Context: "mba", GVR: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, Name: "n1",
				Object: map[string]any{"metadata": map[string]any{"name": "n1"}}},
			right: manifestdiff.Side{Context: "mba", GVR: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, Name: "n2",
				Object: map[string]any{"metadata": map[string]any{"name": "n2"}}},
			wantHunks: 1,
			contains:  []string{"# left:    context mba, gvr v1/nodes, name n1", "--- left/mba/v1/nodes/n1", "# omitted: nothing\n"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := manifestdiff.Compare(tc.left, tc.right, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got.Equal != tc.wantEqual {
				t.Errorf("Equal = %v, want %v", got.Equal, tc.wantEqual)
			}
			if len(got.ChangeLines) != tc.wantHunks {
				t.Errorf("hunks = %d, want %d", len(got.ChangeLines), tc.wantHunks)
			}
			rows := strings.Split(got.Text, "\n")
			for _, at := range got.ChangeLines {
				if !strings.HasPrefix(rows[at], "@@ ") {
					t.Errorf("row %d is %q, want a hunk header", at, rows[at])
				}
			}
			for _, s := range tc.contains {
				if !strings.Contains(got.Text, s) {
					t.Errorf("missing %q in:\n%s", s, got.Text)
				}
			}
			for _, s := range tc.notContain {
				if strings.Contains(got.Text, s) {
					t.Errorf("unexpected %q in:\n%s", s, got.Text)
				}
			}
		})
	}
}
