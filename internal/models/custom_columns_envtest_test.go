package models

import (
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/pkg/appconfig"
)

// TestCustomColumnsFromSharedCache proves that the added columns read fields
// the Table rows do not carry (.data) from the shared cache, and that a hidden
// server column is gone, against a real API server.
func TestCustomColumnsFromSharedCache(t *testing.T) {
	if testCfg == nil {
		t.Skip("envtest unavailable")
	}
	ctx := t.Context()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	cli, err := crclient.New(testCfg, crclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	ns := kctesting.UniqueName("custom-columns")
	mustCreate(t, cli, &corev1.Namespace{Name: ns})
	mustCreate(t, cli, &corev1.ConfigMap{Name: "cm1", Namespace: ns, Data: map[string]string{"color": "blue"}})
	mustCreate(t, cli, &corev1.ConfigMap{Name: "cm2", Namespace: ns})

	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("kccluster: %v", err)
	}
	kctesting.RunInBackground(t, "cluster", cl.Start)

	cfg := appconfig.Default()
	cfg.Columns = map[string][]appconfig.CustomColumn{
		"v1/configmaps": {
			{Name: "Data", Hide: true},
			{Name: "COLOR", JSONPath: ".data.color"},
		},
	}
	deps := Deps{Cl: cl, Ctx: ctx, AppConfig: cfg}
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	folder := NewNamespacedObjectsFolder(deps, gvr, ns, []string{"namespaces", ns, "configmaps"}, nil)
	defer folder.Close()

	want := map[string][]string{"cm1": {"/cm1", "blue"}, "cm2": {"/cm2", "<none>"}}
	var got map[string][]string
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		got = make(map[string][]string)
		n := folder.Len(ctx)
		for _, r := range folder.Lines(ctx, 0, n) {
			id, cells, _, _ := r.Columns()
			if len(cells) > 0 && id != "__back__" {
				// Name, Age (server), COLOR; drop Age, which changes.
				got[id] = []string{cells[0], cells[len(cells)-1]}
			}
		}
		if reflect.DeepEqual(got, want) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("configmaps in %s: rows (name, last cell) = %v, want %v", ns, got, want)
		}
	}
	var titles []string
	for _, c := range folder.Columns() {
		titles = append(titles, c.Title)
	}
	if wantTitles := []string{"Name", "Age", "COLOR"}; !reflect.DeepEqual(titles, wantTitles) {
		t.Fatalf("columns = %v, want %v", titles, wantTitles)
	}
}
