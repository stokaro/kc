package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sttts/kc/internal/models"
	modeltesting "github.com/sttts/kc/internal/models/testing"
	navui "github.com/sttts/kc/internal/navigation"
	table "github.com/sttts/kc/internal/table"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// lateFolder is a folder whose rows arrive after it is shown, as with an
// informer that has not synced yet. Hidden types are known to TypeItem but
// are not rows, like empty types in the root.
type lateFolder struct {
	*models.BaseFolder
	mu   sync.Mutex
	rows []table.Row
}

func newLateFolder(path ...string) *lateFolder {
	f := &lateFolder{BaseFolder: models.NewBaseFolder(models.Deps{}, []table.Column{{Title: " Name"}}, path)}
	f.SetPopulate(func(context.Context) ([]table.Row, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return append([]table.Row(nil), f.rows...), nil
	})
	return f
}

// add makes a row appear and tells the folder to repopulate.
func (f *lateFolder) add(rows ...table.Row) {
	f.mu.Lock()
	f.rows = append(f.rows, rows...)
	f.mu.Unlock()
	f.Refresh()
}

// typeLateFolder is a lateFolder that lists resource types.
type typeLateFolder struct {
	*lateFolder
	types map[schema.GroupResource]models.Item
	ids   map[schema.GroupResource]string
}

func newTypeFolder(path ...string) *typeLateFolder {
	return &typeLateFolder{
		lateFolder: newLateFolder(path...),
		types:      map[schema.GroupResource]models.Item{},
		ids:        map[schema.GroupResource]string{},
	}
}

// addType registers a type; visible types are also rows.
func (f *typeLateFolder) addType(gr schema.GroupResource, id string, item models.Item, visible bool) {
	f.mu.Lock()
	f.types[gr] = item
	f.ids[gr] = id
	f.mu.Unlock()
	if visible {
		f.add(item)
	}
}

func (f *typeLateFolder) TypeItem(gr schema.GroupResource) (models.Item, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.types[gr]
	return item, f.ids[gr], ok
}

func enterRow(id, name string, next models.Folder) *modeltesting.EnterableItem {
	return modeltesting.NewEnterableItem(id, []string{"/" + name}, nil, func() (models.Folder, error) { return next, nil }, models.WhiteStyle())
}

func plainRow(id, name string) *models.SimpleItem {
	return models.NewSimpleItem(id, []string{name}, nil, models.WhiteStyle())
}

var podsGR = schema.GroupResource{Resource: "pods"}

// testResolver knows pods (po, pod) and deployments (deploy).
func testResolver(name string) (schema.GroupResource, error) {
	switch name {
	case "pods", "pod", "po":
		return podsGR, nil
	case "deployments", "deploy", "deployments.apps":
		return schema.GroupResource{Group: "apps", Resource: "deployments"}, nil
	case "namespaces", "ns":
		return schema.GroupResource{Resource: "namespaces"}, nil
	}
	return schema.GroupResource{}, fmt.Errorf("unknown %q", name)
}

func navPath(t *testing.T, nav *navui.Navigator) string {
	t.Helper()
	return "/" + strings.Join(nav.Current().Path(), "/")
}

func TestPathWalkWaitsForLateRows(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	root := newTypeFolder()
	pods := newLateFolder("pods")
	nginx := newLateFolder("pods", "nginx")
	nav := navui.NewNavigator(root)
	start := time.Unix(1000, 0)
	w := newPathWalk(nav, names("po", "nginx"), true, testResolver, start)

	// The type is unknown to the root at first (discovery, first populate).
	done, err := w.advance(ctx, start)
	if done || err != nil {
		t.Fatalf("advance before the type exists = %v, %v; want waiting", done, err)
	}
	// It becomes known but stays hidden, as an empty or not yet counted type.
	root.addType(podsGR, "default//v1/pods", enterRow("default//v1/pods", "pods", pods), false)
	done, err = w.advance(ctx, start.Add(time.Second))
	if done || err != nil {
		t.Fatalf("advance into pods = %v, %v; want waiting for nginx", done, err)
	}
	if got := navPath(t, nav); got != "/pods" {
		t.Fatalf("walk is at %s, want /pods", got)
	}
	// The object arrives 9 s after the walk started waiting for it: the
	// wait is per element, so this is still in time.
	pods.add(enterRow("nginx", "nginx", nginx))
	done, err = w.advance(ctx, start.Add(10*time.Second))
	if !done || err != nil {
		t.Fatalf("advance after nginx appeared = %v, %v; want done", done, err)
	}
	if got := navPath(t, nav); got != "/pods" {
		t.Fatalf("SelectLast entered nginx: at %s", got)
	}
	if nav.CurrentSelectionID() != "nginx" || w.selected != "nginx" {
		t.Fatalf("selection = %q / %q, want nginx", nav.CurrentSelectionID(), w.selected)
	}
	if got := strings.Join(nav.Steps(), ","); got != "default//v1/pods" {
		t.Fatalf("Steps = %q", got)
	}
}

func TestPathWalk(t *testing.T) {
	t.Parallel()
	start := time.Unix(1000, 0)

	// Tree: / (types: pods visible, deployments hidden; row /namespaces)
	//   /pods: nginx (enterable), note (plain row)
	//   /pods/nginx: containers
	//   /deployments: (empty)
	build := func() (*navui.Navigator, *typeLateFolder) {
		root := newTypeFolder()
		pods := newLateFolder("pods")
		nginx := newLateFolder("pods", "nginx")
		nginx.add(plainRow("containers", "containers"))
		pods.add(enterRow("nginx", "nginx", nginx), plainRow("note", "note"))
		root.addType(podsGR, "default//v1/pods", enterRow("default//v1/pods", "pods", pods), true)
		root.addType(schema.GroupResource{Group: "apps", Resource: "deployments"}, "default/apps/v1/deployments",
			enterRow("default/apps/v1/deployments", "deployments", newLateFolder("deployments")), false)
		return navui.NewNavigator(root), root
	}

	tests := []struct {
		name       string
		elems      []PathElem
		selectLast bool
		wantPath   string
		wantSel    string
		wantErr    string // substring; "" for success
	}{
		{name: "enter type and object", elems: names("pods", "nginx"), wantPath: "/pods/nginx"},
		{name: "short name", elems: names("po"), wantPath: "/pods"},
		{name: "hidden type", elems: names("deploy"), wantPath: "/deployments"},
		{name: "select last", elems: names("pods", "nginx"), selectLast: true, wantPath: "/pods", wantSel: "nginx"},
		{name: "plain last row is selected", elems: names("pods", "note"), wantPath: "/pods", wantSel: "note"},
		{name: "parent", elems: names("pods", "nginx", "..", ".."), wantPath: "/"},
		{name: "parent at root stays", elems: names(".."), wantPath: "/"},
		{name: "exact ids", elems: []PathElem{{ID: "default//v1/pods"}, {ID: "nginx"}}, wantPath: "/pods/nginx"},
		{name: "hidden type by id", elems: []PathElem{{ID: "default/apps/v1/deployments"}}, wantPath: "/deployments"},
		{name: "resource elem", elems: []PathElem{{Resource: podsGR}, {Name: "nginx"}}, selectLast: true, wantPath: "/pods", wantSel: "nginx"},
		{name: "unknown type fails at once", elems: names("bogus"), wantErr: `unknown resource type "bogus"`},
		{name: "not a folder", elems: names("pods", "note", "x"), wantErr: "note in /pods is not a folder"},
		{name: "resource elem outside a type list", elems: []PathElem{{Name: "pods"}, {Resource: podsGR}}, wantErr: "does not list resource types"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			nav, _ := build()
			w := newPathWalk(nav, tc.elems, tc.selectLast, testResolver, start)
			done, err := w.advance(t.Context(), start)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("advance = %v, %v; want error containing %q", done, err, tc.wantErr)
				}
				return
			}
			if err != nil || !done {
				t.Fatalf("advance = %v, %v; want done", done, err)
			}
			if got := navPath(t, nav); got != tc.wantPath {
				t.Fatalf("walk ended at %s, want %s", got, tc.wantPath)
			}
			if w.selected != tc.wantSel {
				t.Fatalf("selected = %q, want %q", w.selected, tc.wantSel)
			}
		})
	}
}

func TestPathWalkTimesOut(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	root := newTypeFolder()
	pods := newLateFolder("pods")
	root.addType(podsGR, "default//v1/pods", enterRow("default//v1/pods", "pods", pods), true)
	nav := navui.NewNavigator(root)
	start := time.Unix(1000, 0)
	w := newPathWalk(nav, names("pods", "ghost"), false, testResolver, start)

	if done, err := w.advance(ctx, start); done || err != nil {
		t.Fatalf("first advance = %v, %v; want waiting", done, err)
	}
	if done, err := w.advance(ctx, start.Add(pathWaitTimeout-time.Millisecond)); done || err != nil {
		t.Fatalf("advance just before the timeout = %v, %v; want waiting", done, err)
	}
	_, err := w.advance(ctx, start.Add(pathWaitTimeout))
	if err == nil || err.Error() != "ghost not found in /pods after 10s" {
		t.Fatalf("advance at the timeout = %v; want a not-found error", err)
	}
	// The panel keeps what the walk reached.
	if got := navPath(t, nav); got != "/pods" {
		t.Fatalf("walk left the navigator at %s, want /pods", got)
	}
}

func TestPathWalkStopsWhenUserNavigates(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	root := newTypeFolder()
	pods := newLateFolder("pods")
	root.addType(podsGR, "default//v1/pods", enterRow("default//v1/pods", "pods", pods), true)
	nav := navui.NewNavigator(root)
	start := time.Unix(1000, 0)
	w := newPathWalk(nav, names("pods", "ghost"), false, testResolver, start)
	if done, err := w.advance(ctx, start); done || err != nil {
		t.Fatalf("first advance = %v, %v; want waiting", done, err)
	}
	nav.Back() // the user leaves /pods
	if _, err := w.advance(ctx, start.Add(time.Second)); !errors.Is(err, errWalkMoved) {
		t.Fatalf("advance after the user moved = %v, want errWalkMoved", err)
	}
}

func TestPathWalkMatchesFirstColumn(t *testing.T) {
	t.Parallel()
	// Rows whose ID differs from the shown name (all-namespaces lists,
	// container sections) match by the first column.
	root := newLateFolder()
	child := newLateFolder("web")
	root.add(enterRow("default/web", "web", child))
	nav := navui.NewNavigator(root)
	start := time.Unix(1000, 0)
	w := newPathWalk(nav, names("web"), false, nil, start)
	if done, err := w.advance(t.Context(), start); !done || err != nil {
		t.Fatalf("advance = %v, %v; want done", done, err)
	}
	if got := strings.Join(nav.Steps(), ","); got != "default/web" {
		t.Fatalf("entered %q, want default/web", got)
	}
}
