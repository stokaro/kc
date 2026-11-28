package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func press(code rune, text string, mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Text: text, Mod: mod}
}

func TestNamespaceCreateModelEnterConfirm(t *testing.T) {
	model := NewNamespaceCreateModel()
	_, _ = model.Update(press('t', "t", 0))
	_, _ = model.Update(press('e', "e", 0))
	_, _ = model.Update(press('s', "s", 0))
	_, _ = model.Update(press('t', "t", 0))
	_, cmd := model.Update(press(tea.KeyEnter, "", 0))
	if cmd == nil {
		t.Fatalf("expected command on enter")
	}
	msg := cmd()
	result, ok := msg.(NamespaceCreateResultMsg)
	if !ok {
		t.Fatalf("expected NamespaceCreateResultMsg, got %T", msg)
	}
	if !result.Confirm || !result.Close || result.Name != "test" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestNamespaceCreateModelValidation(t *testing.T) {
	model := NewNamespaceCreateModel()
	_, cmd := model.Update(press(tea.KeyEnter, "", 0))
	if cmd != nil {
		t.Fatalf("expected nil command when name empty")
	}
	if model.modal.Error() == "" {
		t.Fatalf("expected validation error")
	}
}

func TestNamespaceCreateModelEscCancels(t *testing.T) {
	model := NewNamespaceCreateModel()
	_, cmd := model.Update(press(tea.KeyEsc, "", 0))
	if cmd == nil {
		t.Fatalf("expected command on esc")
	}
	msg := cmd()
	res, ok := msg.(NamespaceCreateResultMsg)
	if !ok {
		t.Fatalf("expected NamespaceCreateResultMsg, got %T", msg)
	}
	if res.Confirm || !res.Close {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// F7 in /namespaces opens a red dialog that names the context the
// namespace goes to and its read-only mode, as the other dialogs that
// change the cluster do. Enter creates the namespace: one step (#374).
func TestCreateNamespaceDialogNamesTheContext(t *testing.T) {
	app, panel := readOnlyTestApp(t, true)
	// The right panel shows another context; the dialog names the left one.
	useTestCluster(app, testContext("kind-dev"), nil, 1)
	if cmd := app.createNamespaceForPanel(panel); isRefusal(cmd) {
		t.Fatal("F7 refused in a writable context")
	}
	modal := app.modalManager.GetActiveModal()
	if app.modalManager.TopName() != "namespace_create" || modal == nil {
		t.Fatalf("F7 opened %q, want the namespace dialog", app.modalManager.TopName())
	}
	if !modal.danger {
		t.Fatal("the dialog that creates a namespace is not red")
	}
	view := ansi.Strip(modal.View().Content)
	rest := view
	for _, want := range []string{"Create Namespace", "Context:    eu-prod", "Read-only:  off", "Namespace", "[< Create >]"} {
		i := strings.Index(rest, want)
		if i < 0 {
			t.Fatalf("dialog has no %q after the line before:\n%s", want, view)
		}
		rest = rest[i+len(want):]
	}
	if strings.Contains(view, "kind-dev") {
		t.Fatalf("the dialog names the other panel's context:\n%s", view)
	}

	var res *NamespaceCreateResultMsg
	for _, msg := range pressDialog(t, app, append(textKeys("new-ns"), tea.KeyPressMsg{Code: tea.KeyEnter})...) {
		if r, ok := msg.(NamespaceCreateResultMsg); ok {
			res = &r
		}
	}
	if res == nil || !res.Confirm || res.Name != "new-ns" {
		t.Fatalf("Enter after the name answered %+v, want a create of new-ns", res)
	}
}

// In the red dialog a letter on a button does not press it, as in the
// other red dialogs: "c" after Tab types nothing and creates nothing.
func TestCreateNamespaceDialogLettersNeedMeta(t *testing.T) {
	t.Parallel()
	model := NewNamespaceCreateModel()
	model.SetTarget("kind-dev", false)
	for _, r := range "web" {
		model.Update(press(r, string(r), 0))
	}
	model.Update(press(tea.KeyTab, "", 0))
	model.Update(press(tea.KeyTab, "", 0)) // Cancel
	if _, cmd := model.Update(press('c', "c", 0)); cmd != nil {
		if res, ok := cmd().(NamespaceCreateResultMsg); ok {
			t.Fatalf("a plain c answered %+v", res)
		}
	}
	_, cmd := model.Update(press('c', "", tea.ModAlt))
	if cmd == nil {
		t.Fatal("M-c did not answer")
	}
	if res, ok := cmd().(NamespaceCreateResultMsg); !ok || !res.Confirm || res.Name != "web" {
		t.Fatalf("M-c answered %+v, want a create of web", cmd())
	}
}
