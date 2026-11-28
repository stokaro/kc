package ui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	kccluster "github.com/stokaro/kc/internal/cluster"
	models "github.com/stokaro/kc/internal/models"
	table "github.com/stokaro/kc/internal/table"
)

var gvrNamespaces = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}

// namespaceRow is a row of /namespaces that F8 can delete.
func namespaceRow(cl *kccluster.Cluster, name string) *models.ObjectRow {
	row := models.NewObjectRow(name, []string{name}, nil, gvrNamespaces, "", name, nil)
	row.SetInstance(types.UID("uid-"+name), models.ObjectOrigin{Cluster: cl, Context: "kind-dev"})
	row.SetKind("Namespace")
	row.SetResourceVerbs(models.NamespaceResourceVerbs())
	return row
}

// countCall is one count the app started: what it counts and the context
// the count runs with.
type countCall struct {
	target deleteTarget
	ctx    context.Context //nolint:containedctx // the test checks the count's context after the dialog closed
}

// namespaceDeleteApp lists "..", shop, ux-a and ux-b in the left panel,
// with delete allowed, and counts with a fake that records each count and
// answers impact.
func namespaceDeleteApp(t *testing.T, impact deleteImpact, extra ...table.Row) (*App, *[]countCall) {
	t.Helper()
	cl := &kccluster.Cluster{}
	rows := []table.Row{models.BackItem{}}
	for _, n := range []string{"shop", "ux-a", "ux-b"} {
		rows = append(rows, namespaceRow(cl, n))
	}
	rows = append(rows, extra...)
	app := execTestApp(t, rows...)
	t.Cleanup(app.cancel)
	app.leftPanel.SetEnvironmentSupplier(func() PanelEnvironment {
		env := app.panelEnvironment(app.leftPanel)
		env.AllowDeleteObjects = true
		return env
	})
	calls := new([]countCall)
	app.countImpact = func(ctx context.Context, target deleteTarget) deleteImpact {
		*calls = append(*calls, countCall{target: target, ctx: ctx})
		return impact
	}
	return app, calls
}

// openNamespaceDelete presses F8 on the left panel's row id and returns the
// command that counts what goes with it, not run yet: the dialog shows
// what it shows before the count arrives.
func openNamespaceDelete(t *testing.T, app *App, id string) tea.Cmd {
	t.Helper()
	app.leftPanel.SelectByRowID(t.Context(), id)
	_, cmd := app.Update(keyF8)
	if app.modalManager.TopName() != "delete_confirm" {
		t.Fatalf("F8 on %s opened %q, want the delete dialog", id, app.modalManager.TopName())
	}
	if cmd == nil {
		t.Fatalf("F8 on %s started no count", id)
	}
	return cmd
}

// deliverCount runs the count cmd and delivers its result to the app.
func deliverCount(t *testing.T, app *App, cmd tea.Cmd) deleteImpactMsg {
	t.Helper()
	for _, msg := range collect(cmd) {
		if res, ok := msg.(deleteImpactMsg); ok {
			sendApp(app, res)
			return res
		}
	}
	t.Fatal("the count sent no result")
	return deleteImpactMsg{}
}

// deleteDialogText is the open delete dialog as plain text.
func deleteDialogText(app *App) string {
	return ansi.Strip(app.deleteConfirm.View().Content)
}

// deleteAnswer runs the dialog's answer to keys: the DeleteConfirmMsg the last
// key sent, or nil when none answered.
func deleteAnswer(t *testing.T, app *App, keys ...tea.KeyPressMsg) *DeleteConfirmMsg {
	t.Helper()
	var got *DeleteConfirmMsg
	for _, msg := range pressDialog(t, app, keys...) {
		if res, ok := msg.(DeleteConfirmMsg); ok {
			got = &res
		}
	}
	return got
}

func textKeys(s string) []tea.KeyPressMsg {
	var out []tea.KeyPressMsg
	for _, r := range s {
		out = append(out, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return out
}

var keyAltY = tea.KeyPressMsg{Code: 'y', Mod: tea.ModAlt}

// F8 on a namespace opens the dialog at once, says kc is counting, and
// shows the counts when they arrive; Yes waits for the exact name (#374).
func TestDeleteNamespaceShowsCountsAndWaitsForTheName(t *testing.T) {
	impact := deleteImpact{lines: []string{"It holds 5 deployments, 9 pods, 1 PVC and 1 Helm release."}}
	app, calls := namespaceDeleteApp(t, impact)
	count := openNamespaceDelete(t, app, "shop")

	view := deleteDialogText(app)
	for _, want := range []string{"Delete namespace shop?", "Context: kind-dev", "Counting what is in it…", `Type "shop" to enable Yes:`, "[ Yes ]", "[< No >]"} {
		if !strings.Contains(view, want) {
			t.Fatalf("dialog before the count has no %q:\n%s", want, view)
		}
	}
	if app.pendingDelete == nil || app.pendingDelete.name != "shop" || app.pendingDelete.uid != "uid-shop" {
		t.Fatalf("pending delete %+v, want the instance shop", app.pendingDelete)
	}
	if got := deleteAnswer(t, app, keyAltY); got != nil {
		t.Fatalf("M-y before the name was typed answered %+v", *got)
	}

	deliverCount(t, app, count)
	if len(*calls) != 1 || (*calls)[0].target.name != "shop" || (*calls)[0].target.uid != "uid-shop" {
		t.Fatalf("counts %+v, want one of the instance shop", *calls)
	}
	view = deleteDialogText(app)
	if !strings.Contains(view, impact.lines[0]) || strings.Contains(view, "Counting") {
		t.Fatalf("dialog after the count:\n%s", view)
	}
	// The window grew to show the counts: its frame holds every line.
	if modal := app.modalManager.GetActiveModal(); modal.winHeight != app.deleteConfirm.Height(modal.winWidth-2)+2 {
		t.Fatalf("window height %d, content %d lines", modal.winHeight, app.deleteConfirm.Height(modal.winWidth-2))
	}

	if got := deleteAnswer(t, app, textKeys("sho")...); got != nil {
		t.Fatalf("typing answered %+v", *got)
	}
	if got := deleteAnswer(t, app, keyAltY); got != nil {
		t.Fatalf("M-y with %q typed answered %+v", "sho", *got)
	}
	got := deleteAnswer(t, app, append(textKeys("p"), keyAltY)...)
	if got == nil || !got.Confirm {
		t.Fatalf("M-y with the name typed answered %+v, want a confirmation", got)
	}
}

// A count that arrives after its dialog closed, or after F8 opened the
// dialog again, changes nothing; closing the dialog cancels the count.
func TestDeleteImpactStaleResultIsDropped(t *testing.T) {
	first := deleteImpact{lines: []string{"It holds 3 pods."}}
	app, calls := namespaceDeleteApp(t, first)

	stale := openNamespaceDelete(t, app, "shop")
	pressDialog(t, app, tea.KeyPressMsg{Code: tea.KeyEscape})
	if app.modalManager.IsModalVisible() {
		t.Fatal("Esc left the dialog open")
	}
	staleMsg := collect(stale)
	if len(*calls) != 1 || (*calls)[0].ctx.Err() == nil {
		t.Fatalf("the count of the closed dialog runs on: %+v", *calls)
	}

	// F8 on another namespace: the late result of shop must not show.
	app.countImpact = func(context.Context, deleteTarget) deleteImpact {
		return deleteImpact{lines: []string{"It holds 1 configmap."}}
	}
	fresh := openNamespaceDelete(t, app, "ux-a")
	for _, msg := range staleMsg {
		sendApp(app, msg)
	}
	view := deleteDialogText(app)
	if strings.Contains(view, "3 pods") || !strings.Contains(view, "Counting what is in it…") {
		t.Fatalf("the dialog of ux-a after the stale count of shop:\n%s", view)
	}
	deliverCount(t, app, fresh)
	if view := deleteDialogText(app); !strings.Contains(view, "It holds 1 configmap.") || !strings.Contains(view, `Type "ux-a"`) {
		t.Fatalf("the dialog of ux-a after its count:\n%s", view)
	}

	// After No, the count is stale as well.
	late := openNamespaceDelete(t, app, "ux-b")
	if got := deleteAnswer(t, app, tea.KeyPressMsg{Code: tea.KeyEnter}); got == nil || got.Confirm {
		t.Fatalf("Enter answered %+v, want No", got)
	}
	sendApp(app, DeleteConfirmMsg{Close: true})
	for _, msg := range collect(late) {
		sendApp(app, msg)
	}
	if app.deleteConfirm.impact != nil || app.modalManager.IsModalVisible() {
		t.Fatalf("a count after No reached the closed dialog: %+v", app.deleteConfirm.impact)
	}
}

// Marked namespaces: one is asked about as the cursor row is, with its
// name typed; several are refused, as each takes everything in it.
func TestDeleteMarkedNamespaces(t *testing.T) {
	t.Run("one marked namespace asks for its name", func(t *testing.T) {
		app, calls := namespaceDeleteApp(t, deleteImpact{lines: []string{"It holds 2 pods."}})
		app.leftPanel.SelectRowIDs(t.Context(), []string{"ux-a"})
		app.leftPanel.SelectByRowID(t.Context(), "shop")
		_, cmd := app.Update(keyF8)
		if app.modalManager.TopName() != "delete_confirm" || app.pendingDeleteSet == nil || !slices.Equal(pendingNames(app), []string{"ux-a"}) {
			t.Fatalf("F8 opened %q for %v, want the marked ux-a", app.modalManager.TopName(), pendingNames(app))
		}
		deliverCount(t, app, cmd)
		if len(*calls) != 1 || (*calls)[0].target.name != "ux-a" {
			t.Fatalf("counted %+v, want ux-a", *calls)
		}
		if view := deleteDialogText(app); !strings.Contains(view, "Delete 1 marked namespace?") || !strings.Contains(view, `Type "ux-a" to enable Yes:`) {
			t.Fatalf("dialog:\n%s", view)
		}
		if got := deleteAnswer(t, app, append(textKeys("shop"), keyAltY)...); got != nil {
			t.Fatalf("the cursor row's name confirmed the marked ux-a: %+v", *got)
		}
		pressDialog(t, app, tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
		if got := deleteAnswer(t, app, append(textKeys("ux-a"), keyAltY)...); got == nil || !got.Confirm {
			t.Fatalf("M-y with ux-a typed answered %+v", got)
		}
	})
	t.Run("several marked namespaces are refused", func(t *testing.T) {
		app, calls := namespaceDeleteApp(t, deleteImpact{})
		app.leftPanel.SelectRowIDs(t.Context(), []string{"ux-a", "ux-b"})
		var toasts []string
		for _, msg := range pressApp(app, keyF8) {
			if toast, ok := msg.(showToastMsg); ok {
				toasts = append(toasts, toast.text)
			}
		}
		want := "Cannot delete 2 marked namespaces together: each asks for its name to be typed. Unmark all but one"
		if !slices.Equal(toasts, []string{want}) {
			t.Fatalf("F8 said %q, want %q", toasts, want)
		}
		if app.modalManager.IsModalVisible() || app.pendingDeleteSet != nil || len(*calls) > 0 {
			t.Fatalf("a refused set opened %q, pending %v, counts %d", app.modalManager.TopName(), pendingNames(app), len(*calls))
		}
	})
	t.Run("a namespace among other objects is refused", func(t *testing.T) {
		app, calls := namespaceDeleteApp(t, deleteImpact{}, markedConfigMap(&kccluster.Cluster{}, "web-config"))
		app.leftPanel.SelectRowIDs(t.Context(), []string{"ux-a", "web-config"})
		var toasts []string
		for _, msg := range pressApp(app, keyF8) {
			if toast, ok := msg.(showToastMsg); ok {
				toasts = append(toasts, toast.text)
			}
		}
		want := "Cannot delete the marked rows together: namespace ux-a asks for its name to be typed. Delete it alone"
		if !slices.Equal(toasts, []string{want}) {
			t.Fatalf("F8 said %q, want %q", toasts, want)
		}
		if app.modalManager.IsModalVisible() || app.pendingDeleteSet != nil || len(*calls) > 0 {
			t.Fatalf("a refused set opened %q, pending %v, counts %d", app.modalManager.TopName(), pendingNames(app), len(*calls))
		}
	})
}

// A ConfigMap's dialog asks for no name and counts nothing.
func TestDeleteConfigMapAsksNoName(t *testing.T) {
	app := deleteTestApp(t)
	app.countImpact = func(context.Context, deleteTarget) deleteImpact {
		t.Fatal("F8 on a configmap counted what goes with it")
		return deleteImpact{}
	}
	app.leftPanel.SelectByRowID(t.Context(), "ux-a")
	for _, msg := range pressApp(app, keyF8) {
		if _, ok := msg.(deleteImpactMsg); ok {
			t.Fatal("F8 on a configmap sent a count")
		}
	}
	if view := deleteDialogText(app); strings.Contains(view, "Type") || strings.Contains(view, "Counting") {
		t.Fatalf("the configmap dialog asks for a name:\n%s", view)
	}
	if got := deleteAnswer(t, app, keyAltY); got == nil || !got.Confirm {
		t.Fatalf("M-y answered %+v, want a confirmation", got)
	}
}

func TestTakesOthersAlong(t *testing.T) {
	t.Parallel()
	for gvr, want := range map[schema.GroupVersionResource]bool{
		gvrNamespaces: true,
		{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}: true,
		{Version: "v1", Resource: "persistentvolumes"}:                                        true,
		{Version: "v1", Resource: "nodes"}:                                                    true,
		gvrConfigMaps:                                                                         false,
		{Version: "v1", Resource: "persistentvolumeclaims"}:                                   false,
		{Group: "example.com", Version: "v1", Resource: "namespaces"}:                         false,
	} {
		if got := takesOthersAlong(gvr); got != want {
			t.Errorf("takesOthersAlong(%s) = %v, want %v", gvr, got, want)
		}
	}
}

func TestNamespaceImpactLines(t *testing.T) {
	t.Parallel()
	kind := func(many string) impactKind {
		for _, k := range namespaceImpactKinds {
			if k.many == many {
				return k
			}
		}
		t.Fatalf("no counted kind %q", many)
		return impactKind{}
	}
	all := func(n map[string]int, errs map[string]error) []impactCount {
		var out []impactCount
		for _, k := range namespaceImpactKinds {
			out = append(out, impactCount{kind: k, n: n[k.many], err: errs[k.many]})
		}
		return out
	}
	counted := "Counted: deployments, statefulsets, daemonsets, cronjobs, jobs, pods, services, ingresses, PVCs, configmaps, secrets, Helm releases. Other kinds are not counted; everything in it is deleted."
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New("no rule"))
	for _, tc := range []struct {
		name   string
		counts []impactCount
		want   []string
	}{
		{
			name:   "as in the issue",
			counts: all(map[string]int{"deployments": 5, "pods": 9, "PVCs": 1, "Helm releases": 1}, nil),
			want:   []string{"It holds 5 deployments, 9 pods, 1 PVC and 1 Helm release.", counted},
		},
		{
			name:   "nothing of the counted kinds",
			counts: all(nil, nil),
			want:   []string{"It holds none of the kinds kc counted.", counted},
		},
		{
			name: "more than kc counts",
			counts: []impactCount{
				{kind: kind("pods"), n: 2000, atLeast: true},
				{kind: kind("configmaps"), n: 1},
			},
			want: []string{"It holds 2000+ pods and 1 configmap.", "Counted: pods, configmaps. Other kinds are not counted; everything in it is deleted."},
		},
		{
			name: "secrets forbidden",
			counts: []impactCount{
				{kind: kind("pods"), n: 2},
				{kind: kind("secrets"), err: forbidden},
				{kind: kind("Helm releases"), err: forbidden},
				{kind: kind("jobs"), err: context.DeadlineExceeded},
			},
			want: []string{
				"It holds 2 pods.",
				"Not counted: secrets, Helm releases (forbidden).",
				"Not counted: jobs (timed out).",
				"Counted: pods. Other kinds are not counted; everything in it is deleted.",
			},
		},
		{
			name:   "nothing counted",
			counts: []impactCount{{kind: kind("pods"), err: forbidden}},
			want:   []string{"kc could not count what is in it; everything in it is deleted.", "Not counted: pods (forbidden)."},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := namespaceImpact(tc.counts).lines; !slices.Equal(got, tc.want) {
				t.Fatalf("lines\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

func TestNodeImpactLines(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		count impactCount
		want  string
	}{
		{impactCount{kind: podsOnNode, n: 9}, "9 pods bound to it are deleted with it."},
		{impactCount{kind: podsOnNode, n: 1}, "1 pod bound to it is deleted with it."},
		{impactCount{kind: podsOnNode}, "No pods are bound to it."},
		{impactCount{kind: podsOnNode, err: context.DeadlineExceeded}, "kc could not count the pods on it (timed out); they are deleted with it."},
	} {
		if got := nodeImpact(tc.count).lines; !slices.Equal(got, []string{tc.want}) {
			t.Errorf("nodeImpact(%+v) = %q, want %q", tc.count, got, tc.want)
		}
	}
}
