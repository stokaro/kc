package ui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stokaro/kc/internal/access"
	kccluster "github.com/stokaro/kc/internal/cluster"
	models "github.com/stokaro/kc/internal/models"
	table "github.com/stokaro/kc/internal/table"
	"github.com/stokaro/kc/pkg/appconfig"
)

// markedConfigMap is a configmap row of namespace team that F8 can delete:
// it knows its instance, kind and the resource's verbs.
func markedConfigMap(cl *kccluster.Cluster, name string) *models.ObjectRow {
	row := models.NewObjectRow(name, []string{name}, nil, gvrConfigMaps, "team", name, nil)
	row.SetInstance(types.UID("uid-"+name), models.ObjectOrigin{Cluster: cl, Context: "kind-dev"})
	row.SetKind("ConfigMap")
	row.SetResourceVerbs([]string{"get", "list", "watch", "delete"})
	return row
}

// deleteTestApp lists "..", the rows and then ux-a, ux-b, ux-c and
// web-config in the left panel, with delete allowed as on a live cluster.
func deleteTestApp(t *testing.T, extra ...table.Row) *App {
	t.Helper()
	cl := &kccluster.Cluster{}
	rows := []table.Row{models.BackItem{}}
	rows = append(rows, extra...)
	for _, n := range []string{"ux-a", "ux-b", "ux-c", "web-config"} {
		rows = append(rows, markedConfigMap(cl, n))
	}
	app := execTestApp(t, rows...)
	t.Cleanup(app.cancel)
	app.leftPanel.SetEnvironmentSupplier(func() PanelEnvironment {
		// The real environment needs a live cluster for delete; keep its
		// read-only and access wiring and allow the rest.
		env := app.panelEnvironment(app.leftPanel)
		env.AllowDeleteObjects = true
		return env
	})
	return app
}

func pressApp(app *App, keys ...tea.KeyPressMsg) []tea.Msg {
	var out []tea.Msg
	for _, k := range keys {
		out = append(out, sendApp(app, k)...)
	}
	return out
}

var (
	keyDown   = tea.KeyPressMsg{Code: tea.KeyDown}
	keyEnd    = tea.KeyPressMsg{Code: tea.KeyEnd}
	keyInsert = tea.KeyPressMsg{Code: tea.KeyInsert}
	keyF8     = tea.KeyPressMsg{Code: tea.KeyF8}
)

// pendingNames lists what a Yes in the open delete dialog would delete.
func pendingNames(app *App) []string {
	var out []string
	if app.pendingDeleteSet != nil {
		for _, t := range app.pendingDeleteSet.targets {
			out = append(out, t.name)
		}
	}
	if app.pendingDelete != nil {
		out = append(out, app.pendingDelete.name)
	}
	return out
}

// cursorObject names the object under the left cursor, "" on another row.
func cursorObject(t *testing.T, app *App) string {
	t.Helper()
	item, _ := app.leftPanel.SelectedNavItem(t.Context())
	if obj, isObj := item.(models.ObjectItem); isObj {
		return obj.Name()
	}
	return ""
}

// F8 with marked rows asks about the marked rows, never the row under the
// cursor, which Insert leaves on the next row (#373).
func TestDeleteMarkedNeverTakesTheCursorRow(t *testing.T) {
	for _, tc := range []struct {
		name       string
		prepare    func(t *testing.T, app *App)
		cursor     string // the row under the cursor when F8 is pressed
		want       []string
		title      string
		names      []string
		onlyCursor bool // no marks: the dialog is the one of the cursor row
	}{
		{
			name: "Insert on two rows leaves the cursor on a third",
			prepare: func(t *testing.T, app *App) {
				pressApp(app, keyDown, keyInsert, keyInsert)
			},
			cursor: "ux-c",
			want:   []string{"ux-a", "ux-b"},
			title:  "Delete 2 marked configmaps?",
			names:  []string{"team/ux-a", "team/ux-b"},
		},
		{
			name: "a group mark, then End",
			prepare: func(t *testing.T, app *App) {
				if cmd := app.markGroup(0, "ux-*", true); cmd != nil {
					t.Fatalf("markGroup said %v", collect(cmd))
				}
				pressApp(app, keyEnd)
			},
			cursor: "web-config",
			want:   []string{"ux-a", "ux-b", "ux-c"},
			title:  "Delete 3 marked configmaps?",
			names:  []string{"team/ux-a", "team/ux-b", "team/ux-c"},
		},
		{
			name: "one marked row, cursor on ..",
			prepare: func(t *testing.T, app *App) {
				pressApp(app, keyEnd, keyInsert)
				app.leftPanel.SelectByRowID(t.Context(), "__back__")
			},
			cursor: "",
			want:   []string{"web-config"},
			title:  "Delete 1 marked configmap?",
			names:  []string{"team/web-config"},
		},
		{
			name:       "no marks: the cursor row",
			prepare:    func(t *testing.T, app *App) { pressApp(app, keyEnd) },
			cursor:     "web-config",
			want:       []string{"web-config"},
			title:      "Delete configmap team/web-config?",
			onlyCursor: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := deleteTestApp(t)
			tc.prepare(t, app)
			if got := cursorObject(t, app); got != tc.cursor {
				t.Fatalf("cursor on %q before F8, want %q", got, tc.cursor)
			}
			for _, msg := range pressApp(app, keyF8) {
				switch msg.(type) {
				case showToastMsg, DeleteConfirmMsg:
					t.Fatalf("F8 answered %#v, want the dialog", msg)
				}
			}
			if app.modalManager.TopName() != "delete_confirm" {
				t.Fatalf("F8 opened %q, want the delete dialog", app.modalManager.TopName())
			}
			if got := pendingNames(app); !slices.Equal(got, tc.want) {
				t.Fatalf("a Yes would delete %v, want %v", got, tc.want)
			}
			if tc.onlyCursor != (app.pendingDeleteSet == nil) {
				t.Fatalf("pending set %+v with onlyCursor %v", app.pendingDeleteSet, tc.onlyCursor)
			}
			d := app.deleteConfirm
			if d.title != tc.title || !slices.Equal(d.names, tc.names) || d.more != 0 || d.context != "kind-dev" {
				t.Fatalf("dialog %q %v (+%d) in %q, want %q %v in kind-dev", d.title, d.names, d.more, d.context, tc.title, tc.names)
			}

			// A plain y neither deletes nor reaches the shell line; M-y
			// confirms what the dialog names.
			for _, msg := range pressApp(app, tea.KeyPressMsg{Code: 'y', Text: "y"}) {
				if _, ok := msg.(DeleteConfirmMsg); ok {
					t.Fatalf("y answered %#v", msg)
				}
			}
			if app.modalManager.TopName() != "delete_confirm" || app.terminal.HasInput() {
				t.Fatalf("after y: dialog %q, shell line typed %v", app.modalManager.TopName(), app.terminal.HasInput())
			}
			var answer *DeleteConfirmMsg
			for _, msg := range pressApp(app, tea.KeyPressMsg{Code: 'y', Mod: tea.ModAlt}) {
				if m, ok := msg.(DeleteConfirmMsg); ok {
					answer = &m
				}
			}
			if answer == nil || !answer.Confirm {
				t.Fatalf("M-y answered %+v, want a confirmation", answer)
			}
		})
	}
}

// F8 deletes what the user marked, or nothing: one marked row that cannot
// be deleted refuses the whole set, before the dialog opens, and names it.
func TestDeleteMarkedRefusesTheWholeSet(t *testing.T) {
	noUID := models.NewObjectRow("no-uid", []string{"no-uid"}, nil, gvrConfigMaps, "team", "no-uid", nil)
	noUID.SetResourceVerbs([]string{"delete"})
	for _, tc := range []struct {
		name   string
		extra  []table.Row
		mark   []string
		setup  func(app *App)
		want   string
		prefix bool
	}{
		{
			name:  "a row that is no object",
			extra: []table.Row{models.NewSimpleItem("Scale...", []string{"Scale..."}, nil, models.WhiteStyle())},
			mark:  []string{"Scale...", "ux-a"},
			want:  "Cannot delete the marked rows: kc cannot delete Scale...; unmark it first",
		},
		{
			name:  "a row without a UID",
			extra: []table.Row{noUID},
			mark:  []string{"no-uid", "ux-a"},
			want:  "Cannot delete the marked rows: team/no-uid does not carry the object's UID; refresh the panel and try again",
		},
		{
			name: "an object the identity may not delete",
			mark: []string{"ux-a", "ux-b", "ux-c"},
			setup: func(app *App) {
				st := app.accessState()
				for _, n := range []string{"ux-b", "ux-c"} {
					check := access.DeleteChecks(access.Target{Resource: gvrConfigMaps, Namespace: "team", Name: n})[0]
					st.cache.Store(app.accessIdentity(nil), check, access.Result{Decision: access.Denied, CheckedAt: time.Now()})
				}
			},
			want: "Cannot delete the marked rows: you may not delete team/ux-b, team/ux-c (M-k shows why); unmark them first",
		},
		{
			name:  "a read-only context",
			mark:  []string{"ux-a", "ux-b"},
			setup: func(app *App) { app.cfg.ReadOnly = appconfig.ReadOnlyConfig{Contexts: []string{"kind-*"}} },
			want:  "Read-only mode: Delete is disabled",
		},
		{
			name:  "a port forward",
			extra: []table.Row{stopRow{models.NewSimpleItem("fwd", []string{"fwd"}, nil, models.WhiteStyle())}},
			mark:  []string{"fwd", "ux-a"},
			want:  "2 rows are marked; F8 stops one port forward. Unmark them first (-)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := deleteTestApp(t, tc.extra...)
			app.leftPanel.SelectRowIDs(t.Context(), tc.mark)
			if got := markedRowIDs(app); !slices.Equal(got, tc.mark) {
				t.Fatalf("marked %v, want %v", got, tc.mark)
			}
			if tc.setup != nil {
				tc.setup(app)
			}
			msgs := pressApp(app, keyF8)
			var toasts []string
			for _, msg := range msgs {
				if toast, ok := msg.(showToastMsg); ok {
					toasts = append(toasts, toast.text)
				}
			}
			if !slices.Equal(toasts, []string{tc.want}) {
				t.Fatalf("F8 said %q, want %q", toasts, tc.want)
			}
			if app.modalManager.IsModalVisible() || app.pendingDeleteSet != nil || app.pendingDelete != nil {
				t.Fatalf("a refused set opened %q with %v pending", app.modalManager.TopName(), pendingNames(app))
			}
		})
	}
}

// stopRow is a row F8 stops, as a port forward row.
type stopRow struct{ *models.SimpleItem }

func (stopRow) StopAction() models.ConfirmedAction { return nil }

// The result of a set unmarks the rows whose object is gone and keeps the
// marks of the ones that failed otherwise, so the list shows what is left.
// Rows of a folder shown since keep their marks.
func TestFinishDeleteSetUnmarksGoneRows(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "ux-c", errors.New("no rule"))
	conflict := apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "web-config", errors.New("uid differs"))
	notFound := apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, "ux-b")
	errs := map[string]error{"ux-b": notFound, "ux-c": forbidden, "web-config": conflict}
	all := []string{"ux-a", "ux-b", "ux-c", "web-config"}
	for _, tc := range []struct {
		name       string
		staleMarks bool
		wantMarked []string
	}{
		{name: "same folder", wantMarked: []string{"ux-c"}},
		{name: "another folder since", staleMarks: true, wantMarked: all},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := deleteTestApp(t)
			ctx := t.Context()
			app.leftPanel.SelectRowIDs(ctx, all)
			set, notDeletable, noInstance := buildDeleteSet(0, app.leftPanel.markedRows())
			if len(set.targets) != len(all) || len(notDeletable) > 0 || len(noInstance) > 0 {
				t.Fatalf("set %+v, not deletable %v, no instance %v", set, notDeletable, noInstance)
			}
			set.marks = app.leftPanel.marksGeneration()
			if tc.staleMarks {
				set.marks--
			}
			var results []resourceDeletedMsg
			for _, target := range set.targets {
				results = append(results, resourceDeletedMsg{target: target, err: errs[target.name]})
			}
			msgs := sendApp(app, deleteSetResultMsg{set: set, results: results})
			if got := markedRowIDs(app); !slices.Equal(got, tc.wantMarked) {
				t.Fatalf("marked after the result %v, want %v", got, tc.wantMarked)
			}
			want := "Deleted 1 of 4 configmaps; not deleted: team/ux-b (already gone), team/ux-c (forbidden), team/web-config (replaced by a new object)"
			if len(msgs) != 1 || msgs[0].(showToastMsg).text != want || msgs[0].(showToastMsg).info {
				t.Fatalf("result %#v, want the red line %q", msgs, want)
			}
		})
	}
}

func TestDeleteSetSummary(t *testing.T) {
	t.Parallel()
	cl := &kccluster.Cluster{}
	configMaps := func(names ...string) []deleteTarget {
		var out []deleteTarget
		for _, n := range names {
			target, err := deleteTargetFor(0, markedConfigMap(cl, n))
			if err != nil {
				t.Fatalf("deleteTargetFor(%s): %v", n, err)
			}
			out = append(out, target)
		}
		return out
	}
	deploy := deleteTarget{gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, namespace: "team", name: "web",
		noun: "deployment.apps", label: "deployment.apps team/web"}
	nodes := deleteTarget{gvr: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, name: "worker-1", noun: "node", label: "node worker-1"}
	for _, tc := range []struct {
		name    string
		targets []deleteTarget
		title   string
		names   []string
		more    int
	}{
		{name: "one object", targets: configMaps("a"), title: "Delete 1 marked configmap?", names: []string{"team/a"}},
		{name: "a group", targets: []deleteTarget{deploy, deploy}, title: "Delete 2 marked deployments.apps?", names: []string{"team/web", "team/web"}},
		{name: "cluster-scoped", targets: []deleteTarget{nodes}, title: "Delete 1 marked node?", names: []string{"worker-1"}},
		{name: "two resources", targets: append(configMaps("a"), deploy), title: "Delete 2 marked objects?", names: []string{"configmap team/a", "deployment.apps team/web"}},
		{name: "six: all listed", targets: configMaps("a", "b", "c", "d", "e", "f"), title: "Delete 6 marked configmaps?",
			names: []string{"team/a", "team/b", "team/c", "team/d", "team/e", "team/f"}},
		{name: "seven: five and 2 more", targets: configMaps("a", "b", "c", "d", "e", "f", "g"), title: "Delete 7 marked configmaps?",
			names: []string{"team/a", "team/b", "team/c", "team/d", "team/e"}, more: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			title, names, more := deleteSetSummary(tc.targets)
			if title != tc.title || !slices.Equal(names, tc.names) || more != tc.more {
				t.Fatalf("summary %q %v +%d, want %q %v +%d", title, names, more, tc.title, tc.names, tc.more)
			}
		})
	}
}

func TestDeleteSetResultText(t *testing.T) {
	t.Parallel()
	cl := &kccluster.Cluster{}
	result := func(name string, err error) resourceDeletedMsg {
		target, _ := deleteTargetFor(0, markedConfigMap(cl, name))
		return resourceDeletedMsg{target: target, err: err}
	}
	gone := apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, "x")
	for _, tc := range []struct {
		name    string
		results []resourceDeletedMsg
		want    string
		ok      bool
	}{
		{name: "one deleted", results: []resourceDeletedMsg{result("a", nil)}, want: "Deleted configmap team/a", ok: true},
		{name: "one gone", results: []resourceDeletedMsg{result("a", gone)}, want: "configmap team/a is already gone; nothing was deleted"},
		{name: "all deleted", results: []resourceDeletedMsg{result("a", nil), result("b", nil)}, want: "Deleted 2 configmaps", ok: true},
		{name: "one of two failed", results: []resourceDeletedMsg{result("a", nil), result("b", errors.New("timeout"))},
			want: "Deleted 1 of 2 configmaps; not deleted: team/b (timeout)"},
		{name: "many failed", results: []resourceDeletedMsg{result("a", gone), result("b", gone), result("c", gone), result("d", gone), result("e", nil)},
			want: "Deleted 1 of 5 configmaps; not deleted: team/a (already gone), team/b (already gone), team/c (already gone) and 1 more"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := deleteSetResultText(tc.results)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("result %q (ok %v), want %q (ok %v)", got, ok, tc.want, tc.ok)
			}
			if strings.Contains(got, "uid-") {
				t.Fatalf("result %q shows a UID", got)
			}
		})
	}
}
