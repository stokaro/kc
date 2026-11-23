package ui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/nodeaction"
)

// refusal is what PrepareDrain returns for a node with two pods that keep
// data in an emptyDir and an unmanaged pod.
func refusal() *nodeaction.RefusedError {
	const local = "Pods with local storage (use --delete-emptydir-data to override)"
	return &nodeaction.RefusedError{Pods: []nodeaction.RefusedPod{
		{Node: "a", Namespace: "shop", Name: "loadgen-1", Message: local, Override: nodeaction.OverrideDeleteEmptyDirData},
		{Node: "a", Namespace: "shop", Name: "loadgen-2", Message: local, Override: nodeaction.OverrideDeleteEmptyDirData},
		{Node: "a", Namespace: "ops", Name: "debug", Message: "cannot delete Pods that declare no controller (use --force to override)", Override: nodeaction.OverrideForce},
	}}
}

// lineIndex returns the index of the first line containing s, or -1.
func lineIndex(lines []string, s string) int {
	return slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, s) })
}

func formLines(form *drainOptionsModel) []string {
	return strings.Split(ansi.Strip(form.View().Content), "\n")
}

// A refused drain keeps the options as typed, lists each pod under the
// box that lets the drain go past it, and moves the focus to the first
// such box. Ticking it clears its pods, and Enter checks again with the new
// choice.
func TestDrainOptionsShowRefusedPods(t *testing.T) {
	form := newDrainOptionsModel(nodeTarget{nodes: []string{"a"}}, nodeaction.DefaultDrainOptions())
	// Grace 30, then Drain.
	msg, ok := pressNode(t, form, "down", "down", "backspace", "backspace", "3", "0", "up", "up", "enter").(nodeDrainOptionsMsg)
	if !ok || !msg.ok || msg.form != form || !form.checking {
		t.Fatalf("Drain = %#v (checking %v), want a check of the pods", msg, form.checking)
	}
	if got := pressNode(t, form, "up", "space"); got != nil || form.focus != drainFieldOK {
		t.Fatalf("keys while checking: msg %#v, focus %d; the form must take no input", got, form.focus)
	}
	form.SetDimensions(70, 30)
	if lineIndex(formLines(form), "Reading the pods on the node") < 0 {
		t.Errorf("the form does not say it reads the pods:\n%s", strings.Join(formLines(form), "\n"))
	}

	form.checked(refusal())
	if form.checking || form.focus != drainFieldDeleteEmptyDir {
		t.Fatalf("after the refusal: checking %v, focus %d, want the emptyDir box", form.checking, form.focus)
	}
	lines := formLines(form)
	text := strings.Join(lines, "\n")
	box, force := lineIndex(lines, "Delete emptyDir data"), lineIndex(lines, "Force:")
	if box < 0 || lineIndex(lines, "! Pods with local storage:") != box+1 || lineIndex(lines, "shop/loadgen-1") != box+2 || lineIndex(lines, "shop/loadgen-2") != box+3 {
		t.Errorf("the emptyDir pods are not under their box:\n%s", text)
	}
	if force < 0 || lineIndex(lines, "! cannot delete Pods that declare no controller:") != force+1 || lineIndex(lines, "ops/debug") != force+2 {
		t.Errorf("the unmanaged pod is not under Force:\n%s", text)
	}
	for _, want := range []string{"Grace period (s, -1 = pod's own): 30", "kubectl drain refuses these pods. Nothing changed.", "Tick the option above them"} {
		if lineIndex(lines, want) < 0 {
			t.Errorf("view lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "(use --") {
		t.Errorf("the box above names the choice; the flag hint must not repeat it:\n%s", text)
	}

	// Space ticks the focused box and drops its pods; the Force pod stays.
	pressNode(t, form, "space")
	lines = formLines(form)
	if lineIndex(lines, "loadgen-1") >= 0 || lineIndex(lines, "ops/debug") < 0 {
		t.Errorf("after ticking emptyDir:\n%s", strings.Join(lines, "\n"))
	}
	msg, ok = pressNode(t, form, "enter").(nodeDrainOptionsMsg)
	if !ok || !msg.ok || !msg.opts.DeleteEmptyDirData || msg.opts.GracePeriodSeconds != 30 || form.refused != nil {
		t.Fatalf("second Drain = %#v, refused %v; want the kept options with emptyDir data deleted", msg, form.refused)
	}
}

// A pod no option helps is listed above the buttons, and the focus goes to
// Cancel. Any other error is shown as the form's error.
func TestDrainOptionsOtherRefusals(t *testing.T) {
	form := newDrainOptionsModel(nodeTarget{nodes: []string{"a"}}, nodeaction.DefaultDrainOptions())
	pressNode(t, form, "enter")
	form.checked(&nodeaction.RefusedError{Pods: []nodeaction.RefusedPod{{Node: "a", Namespace: "kube-system", Name: "agent-x", Message: "reading DaemonSet kube-system/agent: forbidden"}}})
	form.SetDimensions(90, 20)
	view := ansi.Strip(form.View().Content)
	if form.focus != drainFieldCancel || !strings.Contains(view, "! kube-system/agent-x: reading DaemonSet kube-system/agent: forbidden") || strings.Contains(view, "Tick the option") {
		t.Errorf("focus %d, view:\n%s", form.focus, view)
	}

	form = newDrainOptionsModel(nodeTarget{nodes: []string{"a"}}, nodeaction.DefaultDrainOptions())
	pressNode(t, form, "enter")
	form.checked(errors.New("listing pods on node a: timeout"))
	form.SetDimensions(70, 20)
	if view := ansi.Strip(form.View().Content); !strings.Contains(view, "! listing pods on node a: timeout") || form.checking {
		t.Errorf("view does not show the error:\n%s", view)
	}
}

// The App keeps the options open on a refusal and grows their window,
// drops a result that arrives after the options were closed, and opens
// the red dialog for a prepared drain.
func TestDrainPreparedKeepsOptionsOnRefusal(t *testing.T) {
	app, _ := readOnlyTestApp(t, true)
	target := nodeTarget{nodes: []string{"a"}}
	app.showDrainOptions(target)
	form, ok := app.modalManager.modals[nodeOptionsModal].content.(*drainOptionsModel)
	if !ok {
		t.Fatalf("options content = %T", app.modalManager.modals[nodeOptionsModal].content)
	}
	before := form.height
	msg, ok := pressNode(t, form, "enter").(nodeDrainOptionsMsg)
	if !ok {
		t.Fatalf("Enter = %#v", msg)
	}
	_ = app.handleNodeActionMsg(nodeDrainPreparedMsg{err: refusal(), form: form})
	if app.modalManager.TopName() != nodeOptionsModal {
		t.Fatalf("top dialog = %q after a refusal, want the options", app.modalManager.TopName())
	}
	if form.height <= before {
		t.Errorf("form height %d, was %d: the window must grow for the pods", form.height, before)
	}

	app.hideModalName(nodeOptionsModal)
	_ = app.handleNodeActionMsg(nodeDrainPreparedMsg{action: &nodeaction.DrainAction{}, form: form})
	if app.modalManager.IsModalVisible() {
		t.Fatalf("a drain prepared after the options closed opened %q", app.modalManager.TopName())
	}

	app.showDrainOptions(target)
	form = app.modalManager.modals[nodeOptionsModal].content.(*drainOptionsModel)
	_ = app.handleNodeActionMsg(nodeDrainPreparedMsg{action: &nodeaction.DrainAction{}, form: form})
	if app.modalManager.TopName() != nodeConfirmModal {
		t.Fatalf("top dialog = %q for a prepared drain, want the red confirmation", app.modalManager.TopName())
	}
}
