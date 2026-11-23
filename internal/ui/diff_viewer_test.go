package ui

// These tests are white-box: the viewer's results arrive as an unexported
// message, and the key test drives the App's own key routing.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/manifestdiff"
	"github.com/stokaro/kc/internal/models"
	modeltesting "github.com/stokaro/kc/internal/models/testing"
	navui "github.com/stokaro/kc/internal/navigation"
	"github.com/stokaro/kc/internal/table"
)

var compareGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}

// compareObject builds a manifest with 40 env vars; vars[i] overrides one.
func compareObject(ns string, vars map[int]string) map[string]any {
	env := make([]any, 40)
	for i := range env {
		val := fmt.Sprintf("v%d", i)
		if v, ok := vars[i]; ok {
			val = v
		}
		env[i] = map[string]any{"name": fmt.Sprintf("E%02d", i), "value": val}
	}
	return map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "web", "namespace": ns, "uid": "uid-" + ns},
		"spec":       map[string]any{"env": env},
		"status":     map[string]any{"ready": ns},
	}
}

func compareSnap(rightNS string, right map[int]string) *compareSnapshot {
	return &compareSnapshot{
		left:  manifestdiff.Side{Context: "mba", GVR: compareGVR, Namespace: "a", Name: "web", Object: compareObject("a", nil)},
		right: manifestdiff.Side{Context: "mba", GVR: compareGVR, Namespace: rightNS, Name: "web", Object: compareObject(rightNS, right)},
	}
}

// loadedViewer returns a viewer showing the diff of snap.
func loadedViewer(t *testing.T, snap *compareSnapshot) *DiffViewer {
	t.Helper()
	var fail error
	v := NewDiffViewer("", "kubectl get l", "kubectl get r", func(seq int, opts manifestdiff.Options) tea.Cmd {
		return func() tea.Msg {
			if fail != nil {
				return compareResultMsg{seq: seq, err: fail}
			}
			res, err := manifestdiff.Compare(snap.left, snap.right, opts)
			return compareResultMsg{seq: seq, snap: snap, opts: opts, result: res, err: err}
		}
	}, nil)
	v.SetDimensions(80, 12)
	runCompare(t, v, v.Start())
	return v
}

// The read result redraws the screen: the App caches its rendered view,
// and a compare viewer that got its diff behind a cached "Reading both
// manifests..." frame stayed on it until some other event redrew.
func TestCompareObjectsResultRedraws(t *testing.T) {
	t.Parallel()
	row := func(ns string, vars map[int]string) *models.ObjectRow {
		obj := &unstructured.Unstructured{Object: compareObject(ns, vars)}
		r := models.NewObjectRow("web", []string{"web"}, []string{"web"}, compareGVR, ns, "web", models.WhiteStyle())
		return r.WithManifest("mba", func(context.Context) (*unstructured.Unstructured, error) { return obj, nil })
	}
	a := NewApp()
	t.Cleanup(a.cancel)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	a.leftNav = navui.NewNavigator(modeltesting.NewStaticFolder("/", nil, []table.Row{row("a", nil)}))
	a.rightNav = navui.NewNavigator(modeltesting.NewStaticFolder("/", nil, []table.Row{row("b", map[int]string{5: "changed-value"})}))
	a.leftPanel.UseFolder(t.Context(), true)
	a.rightPanel.UseFolder(t.Context(), true)
	a.syncPanelWithNavigator(0)
	a.syncPanelWithNavigator(1)
	a.leftPanel.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	a.rightPanel.Update(tea.KeyPressMsg{Code: tea.KeyDown})

	a.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	_, cmd := a.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if m := a.modalManager.modals["compare_viewer"]; m == nil || !m.IsVisible() {
		t.Fatal("C-x C-d did not open the compare viewer")
	}
	if got := viewString(a.View()); !strings.Contains(got, "Reading both manifests") {
		t.Fatalf("the viewer does not show the loading state:\n%s", got)
	}
	msg := findCompareResult(t, cmd)
	a.Update(msg)
	if got := viewString(a.View()); !strings.Contains(got, "changed-value") {
		t.Fatalf("the screen still shows the old frame after the result:\n%s", got)
	}
}

// findCompareResult runs cmd, and the commands of any batch it returns,
// until one yields a compareResultMsg.
func findCompareResult(t *testing.T, cmd tea.Cmd) compareResultMsg {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch m := c().(type) {
		case compareResultMsg:
			return m
		case tea.BatchMsg:
			queue = append(queue, m...)
		}
	}
	t.Fatal("no compare result")
	return compareResultMsg{}
}

// runCompare runs cmd, which must produce a compareResultMsg, and applies it.
func runCompare(t *testing.T, v *DiffViewer, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command")
	}
	msg, ok := cmd().(compareResultMsg)
	if !ok {
		t.Fatalf("command returned %T", msg)
	}
	v.apply(msg)
	v.View()
}

func pressDiffKey(v *DiffViewer, key string) tea.Cmd {
	var msg tea.KeyPressMsg
	switch key {
	case "ctrl+r":
		msg = tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
	case "backspace":
		msg = tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "space":
		msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	default:
		msg = tea.KeyPressMsg{Code: rune(key[0]), Text: key}
	}
	_, cmd := v.Update(msg)
	return cmd
}

func TestCompareCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		left, right string
		raw         bool
		want        string
	}{
		{"configuration view", "kubectl get a -o yaml", "kubectl get b -o yaml", false,
			"diff -u <(kubectl get a -o yaml) <(kubectl get b -o yaml)"},
		{"raw view shows managed fields", "kubectl get a -o yaml", "kubectl get b -o yaml", true,
			"diff -u <(kubectl get a -o yaml --show-managed-fields) <(kubectl get b -o yaml --show-managed-fields)"},
		{"missing command", "", "kubectl get b -o yaml", false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := compareCommand(tc.left, tc.right, tc.raw); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDiffViewerJumpsBetweenChanges(t *testing.T) {
	t.Parallel()
	v := loadedViewer(t, compareSnap("a", map[int]string{2: "x", 20: "y", 38: "z"}))
	lines := v.result.ChangeLines
	if len(lines) != 3 {
		t.Fatalf("want 3 changes, got %v\n%s", lines, v.result.Text)
	}
	steps := []struct {
		key  string
		want int // index of the current change
	}{
		{"n", 0},
		{"space", 1},
		{"n", 2},
		{"n", 2}, // no change after the last one
		{"p", 1},
		{"backspace", 0},
		{"p", 0},
	}
	for i, s := range steps {
		pressDiffKey(v, s.key)
		if v.current != s.want {
			t.Fatalf("step %d (%s): current = %d, want %d", i, s.key, v.current, s.want)
		}
	}
	if got := v.FooterStatus(40); got != "change 1/3" {
		t.Fatalf("footer status = %q", got)
	}
}

func TestDiffViewerToggles(t *testing.T) {
	t.Parallel()
	v := loadedViewer(t, compareSnap("b", map[int]string{5: "x"}))
	if !strings.Contains(v.inner.PlainText(), "# view:    configuration") || strings.Contains(v.inner.PlainText(), "uid-a") {
		t.Fatalf("unexpected first view:\n%s", v.inner.PlainText())
	}

	runCompare(t, v, pressDiffKey(v, "r"))
	if v.shown.Mode != manifestdiff.ModeRaw || !strings.Contains(v.inner.PlainText(), "-  uid: uid-a") {
		t.Fatalf("r did not switch to the raw view:\n%s", v.inner.PlainText())
	}
	if !strings.Contains(v.bar.command, "--show-managed-fields") {
		t.Fatalf("raw command = %q", v.bar.command)
	}

	runCompare(t, v, pressDiffKey(v, "s"))
	if v.shown.Mode != manifestdiff.ModeConfig || !v.shown.IncludeStatus || !strings.Contains(v.inner.PlainText(), "-  ready: a") {
		t.Fatalf("s did not show status in the configuration view:\n%s", v.inner.PlainText())
	}
	if strings.Contains(v.bar.command, "--show-managed-fields") {
		t.Fatalf("configuration command = %q", v.bar.command)
	}
}

func TestDiffViewerDropsStaleResults(t *testing.T) {
	t.Parallel()
	v := loadedViewer(t, compareSnap("a", map[int]string{5: "x"}))
	before := v.inner.PlainText()
	stale := pressDiffKey(v, "r")
	pressDiffKey(v, "r") // a newer request replaces the first one
	runCompare(t, v, stale)
	if v.inner.PlainText() != before {
		t.Fatal("a stale result replaced the diff")
	}
}

func TestDiffViewerFailedRefreshKeepsDiff(t *testing.T) {
	t.Parallel()
	v := loadedViewer(t, compareSnap("a", map[int]string{5: "x"}))
	before := v.inner.PlainText()
	cmd := pressDiffKey(v, "ctrl+r")
	if cmd == nil {
		t.Fatal("C-r issued no read")
	}
	v.apply(compareResultMsg{view: v, seq: v.seq, err: errors.New("boom")})
	if v.inner.PlainText() != before {
		t.Fatal("a failed refresh replaced the diff")
	}
	if !strings.Contains(v.bar.flash, "boom") {
		t.Fatalf("flash = %q, want the error", v.bar.flash)
	}
}

func TestDiffViewerFirstReadFailure(t *testing.T) {
	t.Parallel()
	v := NewDiffViewer("", "", "", nil, nil)
	v.apply(compareResultMsg{seq: v.seq, err: errors.New("forbidden")})
	if !strings.Contains(v.inner.PlainText(), "Compare failed: forbidden") {
		t.Fatalf("content = %q", v.inner.PlainText())
	}
}

func manifestRow(id, ns string, obj map[string]any) *models.ObjectRow {
	row := models.NewObjectRow(id, []string{"/" + id}, []string{id}, compareGVR, ns, "web", models.WhiteStyle())
	row.SetViewCommand("kubectl get deployments.apps -n " + ns + " web -o yaml")
	return row.WithManifest("mba", func(context.Context) (*unstructured.Unstructured, error) {
		return &unstructured.Unstructured{Object: obj}, nil
	})
}

func compareApp(t *testing.T, left, right models.Item) *App {
	t.Helper()
	app := NewApp()
	model, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = model.(*App)
	ctx := t.Context()
	for _, p := range []struct {
		panel *Panel
		item  models.Item
	}{{app.leftPanel, left}, {app.rightPanel, right}} {
		id, _, _, _ := p.item.Columns()
		p.panel.UseFolder(ctx, true)
		p.panel.SetFolder(ctx, &mockFolderFB{items: []models.Item{p.item}}, false)
		p.panel.SelectByRowID(ctx, id)
		p.panel.RefreshFolder(ctx)
	}
	return app
}

func TestCtrlXCtrlDComparesPanels(t *testing.T) {
	app := compareApp(t,
		manifestRow("left", "a", compareObject("a", nil)),
		manifestRow("right", "b", compareObject("b", map[int]string{3: "x"})))

	model, _ := app.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	app = model.(*App)
	if app.modalManager.IsModalVisible() {
		t.Fatal("C-x alone opened a dialog")
	}
	model, _ = app.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	app = model.(*App)
	modal := app.modalManager.GetActiveModal()
	if modal == nil {
		t.Fatal("C-x C-d opened no dialog")
	}
	view, ok := modal.content.(*DiffViewer)
	if !ok {
		t.Fatalf("dialog content is %T", modal.content)
	}
	msg, ok := view.load(view.seq, view.want)().(compareResultMsg)
	if !ok || msg.err != nil {
		t.Fatalf("load = %+v", msg)
	}
	app.Update(msg)
	text := view.inner.PlainText()
	for _, want := range []string{
		"# left:    context mba, gvr apps/v1/deployments, namespace a, name web",
		"# right:   context mba, gvr apps/v1/deployments, namespace b, name web",
		"-  namespace: a",
		"+  namespace: b",
		"+    value: x",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	wantCmd := "diff -u <(kubectl get deployments.apps -n a web -o yaml) <(kubectl get deployments.apps -n b web -o yaml)"
	if view.bar.command != wantCmd {
		t.Errorf("command = %q, want %q", view.bar.command, wantCmd)
	}
}

func TestCtrlXCompareNeedsObjects(t *testing.T) {
	app := compareApp(t, mockItemFB{id: "plain"}, manifestRow("right", "b", compareObject("b", nil)))
	model, _ := app.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	model, _ = model.(*App).Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if model.(*App).modalManager.IsModalVisible() {
		t.Fatal("compare opened without an object in the left panel")
	}
}

func TestCtrlXOtherKeyIsDropped(t *testing.T) {
	app := compareApp(t,
		manifestRow("left", "a", compareObject("a", nil)),
		manifestRow("right", "b", compareObject("b", nil)))
	model, _ := app.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	model, _ = model.(*App).Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	app = model.(*App)
	if app.ctrlXPending {
		t.Fatal("prefix still pending")
	}
	model, _ = app.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if model.(*App).modalManager.IsModalVisible() {
		t.Fatal("C-d after a dropped C-x sequence opened compare")
	}
}
