package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/openapi"

	"github.com/stokaro/kc/internal/explain"
	"github.com/stokaro/kc/internal/models"
	modeltesting "github.com/stokaro/kc/internal/models/testing"
	nav "github.com/stokaro/kc/internal/navigation"
	table "github.com/stokaro/kc/internal/table"
)

var explainTestGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}

const explainTestDoc = `{"components":{"schemas":{"io.k8s.api.apps.v1.Deployment":{"type":"object",
 "properties":{"spec":{"type":"object","properties":{"replicas":{"type":"integer"}}}},
 "x-kubernetes-group-version-kind":[{"group":"apps","version":"v1","kind":"Deployment"}]}}}}`

type stubOpenAPI struct {
	paths map[string]string // index key -> document
	err   error
}

func (s stubOpenAPI) PathsWithContext(context.Context) (map[string]openapi.GroupVersionWithContext, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[string]openapi.GroupVersionWithContext)
	for k, v := range s.paths {
		out[k] = stubGV{doc: v}
	}
	return out, nil
}

type stubGV struct{ doc string }

func (g stubGV) SchemaWithContext(context.Context, string) ([]byte, error) { return []byte(g.doc), nil }
func (g stubGV) ServerRelativeURL() string                                 { return "/openapi/v3/x?hash=1" }

// explainApp returns an app whose left panel lists one Deployment row.
func explainApp(t *testing.T) (*App, models.Folder) {
	t.Helper()
	a := NewApp()
	ctx := t.Context()
	row := models.NewObjectRow("default/web", []string{"web"}, []string{"namespaces", "default", "deployments", "web"}, explainTestGVR, "default", "web", nil)
	origin := modeltesting.NewStaticFolder("deployments", []table.Column{{Title: " Name"}}, []table.Row{row})
	a.leftNav = nav.NewNavigator(origin)
	a.rightNav = nav.NewNavigator(mkFolder("right"))
	a.leftPanel.UseFolder(ctx, true)
	a.leftPanel.SetFolder(ctx, origin, false)
	a.leftPanel.SetCurrentPath(a.navigatorPath(a.leftNav))
	a.leftPanel.SelectByRowID(ctx, "default/web")
	a.activePanel = 0
	return a, origin
}

func TestExplainTargetOf(t *testing.T) {
	t.Parallel()
	obj := models.NewObjectRow("x", []string{"x"}, nil, explainTestGVR, "ns", "x", nil)
	if gvr, gvk, ok := explainTargetOf(obj); !ok || gvr != explainTestGVR || gvk.Kind != "" {
		t.Errorf("object row: %v %v %v", gvr, gvk, ok)
	}
	gvk := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	rt := models.NewResourceGroupItem(models.Deps{}, explainTestGVR, gvk, "", "apps/v1/deployments", []string{"deployments"}, nil, "", nil, false, nil)
	if gvr, got, ok := explainTargetOf(rt); !ok || gvr != explainTestGVR || got != gvk {
		t.Errorf("resource type row: %v %v %v", gvr, got, ok)
	}
	if _, _, ok := explainTargetOf(models.NewSimpleItem("k", []string{"k"}, nil, nil)); ok {
		t.Error("a plain row is an explain target")
	}
}

func TestLoadExplainFolder(t *testing.T) {
	t.Parallel()
	gvk := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	ok := stubOpenAPI{paths: map[string]string{"apis/apps/v1": explainTestDoc}}
	tests := []struct {
		name     string
		client   openapi.ClientWithContext
		gvr      schema.GroupVersionResource
		wantErr  bool
		absent   bool
		wantRows []string
	}{
		{name: "documented", client: ok, gvr: explainTestGVR, wantRows: []string{"__back__", "spec"}},
		{name: "other version is not published", client: ok, gvr: schema.GroupVersionResource{Group: "apps", Version: "v1beta1", Resource: "deployments"}, wantErr: true, absent: true},
		{name: "index fails", client: stubOpenAPI{err: errors.New("dial tcp: refused")}, gvr: explainTestGVR, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := loadExplainFolder(t.Context(), explain.NewLoader(2), models.Deps{}, tt.client, tt.gvr, gvk)
			if err := f.Err(); (err != nil) != tt.wantErr || (err != nil && explain.IsAbsent(err) != tt.absent) {
				t.Fatalf("err = %v, wantErr %v absent %v", err, tt.wantErr, tt.absent)
			}
			// The schema shown is the listed version's, not the kind's preferred one.
			if f.Spec().GVK.Version != tt.gvr.Version {
				t.Errorf("gvk = %v, want version %s", f.Spec().GVK, tt.gvr.Version)
			}
			if tt.wantRows != nil {
				var ids []string
				for _, r := range f.Lines(t.Context(), 0, 10) {
					id, _, _, _ := r.Columns()
					ids = append(ids, id)
				}
				if strings.Join(ids, ",") != strings.Join(tt.wantRows, ",") {
					t.Errorf("rows = %v, want %v", ids, tt.wantRows)
				}
			}
		})
	}
}

func TestExplainLoadedPushesAndBackRestores(t *testing.T) {
	a, origin := explainApp(t)
	ctx := t.Context()
	gvk := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	folder := loadExplainFolder(ctx, explain.NewLoader(2), models.Deps{}, stubOpenAPI{paths: map[string]string{"apis/apps/v1": explainTestDoc}}, explainTestGVR, gvk)

	a.Update(explainLoadedMsg{panel: 0, origin: origin, selID: "default/web", folder: folder})
	if got := a.leftPanel.GetCurrentPath(); got != "/explain/apps/v1/Deployment" {
		t.Fatalf("breadcrumb = %q", got)
	}
	if got := a.panelCommand(); got != "kubectl explain deployments --api-version=apps/v1" {
		t.Errorf("command line = %q", got)
	}
	a.handleFolderNav(true, "", nil)
	if a.leftPanel.Folder() != origin {
		t.Fatal("back did not return to the resource list")
	}
	if id := a.leftPanel.currentSelectionID(ctx); id != "default/web" {
		t.Errorf("selection after back = %q, want default/web", id)
	}
}

func TestExplainLoadedAfterThePanelMoved(t *testing.T) {
	a, _ := explainApp(t)
	other := mkFolder("elsewhere")
	folder := models.NewExplainFolder(models.Deps{}, models.ExplainSpec{GVR: explainTestGVR}, nil, explain.ErrNotPublished)
	cmd := a.handleExplainLoaded(explainLoadedMsg{panel: 0, origin: other, folder: folder})
	if a.leftPanel.Folder() == models.Folder(folder) {
		t.Fatal("pushed onto a folder the request did not come from")
	}
	if cmd == nil {
		t.Fatal("no message for the dropped result")
	}
	if msg, ok := cmd().(showToastMsg); !ok || !msg.info {
		t.Errorf("msg = %#v, want an info toast", msg)
	}
}

func TestExplainKeyWithoutCluster(t *testing.T) {
	a, origin := explainApp(t)
	for _, keys := range [][]tea.KeyPressMsg{
		{{Code: 'e', Mod: tea.ModAlt}},
		{{Code: tea.KeyEscape}, {Code: 'e', Text: "e"}},
	} {
		var cmd tea.Cmd
		for _, k := range keys {
			_, cmd = a.Update(k)
		}
		if cmd == nil {
			t.Fatalf("%v: key not handled", keys)
		}
		msg := findToast(cmd)
		if msg == nil || !strings.Contains(msg.text, "cluster connection") {
			t.Errorf("%v: msg = %#v, want the no-cluster toast", keys, msg)
		}
		if a.leftPanel.Folder() != origin {
			t.Fatalf("%v: panel changed without a schema", keys)
		}
	}
}

// findToast runs cmd and any batch it returns, looking for a toast.
func findToast(cmd tea.Cmd) *showToastMsg {
	if cmd == nil {
		return nil
	}
	switch m := cmd().(type) {
	case showToastMsg:
		return &m
	case tea.BatchMsg:
		for _, c := range m {
			if t := findToast(c); t != nil {
				return t
			}
		}
	}
	return nil
}
