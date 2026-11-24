package ui

// These tests drive App, whose navigation state is unexported, so they live
// in package ui like the other App tests.

import (
	"context"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/hotlist"
	"github.com/stokaro/kc/internal/models"
	modeltesting "github.com/stokaro/kc/internal/models/testing"
	navui "github.com/stokaro/kc/internal/navigation"
	"github.com/stokaro/kc/internal/table"
)

// jumpTree is root -> a -> b and root -> c. The rows of a appear only once
// aReady is set, like a list whose informer has not synced yet.
type jumpTree struct {
	root, a, b, c *models.BaseFolder
	aReady        atomic.Bool
}

func newJumpTree(aReadyAtStart bool) *jumpTree {
	style := models.WhiteStyle()
	tr := &jumpTree{}
	tr.aReady.Store(aReadyAtStart)
	tr.b = modeltesting.NewStaticFolder("a/b", nil, nil)
	tr.c = modeltesting.NewStaticFolder("c", nil, nil)
	tr.a = models.NewBaseFolder(models.Deps{}, []table.Column{{Title: " Name"}}, []string{"a"})
	tr.a.SetPopulate(func(context.Context) ([]table.Row, error) {
		if !tr.aReady.Load() {
			return nil, nil
		}
		return []table.Row{
			modeltesting.NewEnterableItem("b", []string{"b"}, []string{"a", "b"}, func() (models.Folder, error) { return tr.b, nil }, style),
		}, nil
	})
	tr.root = modeltesting.NewStaticFolder("/", nil, []table.Row{
		modeltesting.NewEnterableItem("a", []string{"a"}, []string{"a"}, func() (models.Folder, error) { return tr.a, nil }, style),
		modeltesting.NewEnterableItem("c", []string{"c"}, []string{"c"}, func() (models.Folder, error) { return tr.c, nil }, style),
	})
	return tr
}

// newJumpApp returns an App whose left panel shows tr.root and whose
// hotlist file is in a temporary directory.
func newJumpApp(t *testing.T, tr *jumpTree) *App {
	t.Helper()
	a := NewApp()
	t.Cleanup(a.cancel)
	a.hotlistPath = filepath.Join(t.TempDir(), hotlist.FileName)
	a.leftNav = navui.NewNavigator(tr.root)
	a.rightNav = navui.NewNavigator(modeltesting.NewStaticFolder("/", nil, nil))
	a.leftPanel.UseFolder(t.Context(), true)
	a.rightPanel.UseFolder(t.Context(), true)
	a.syncPanelWithNavigator(0)
	a.syncPanelWithNavigator(1)
	a.recordHistory(0)
	a.recordHistory(1)
	return a
}

// enter moves the active panel into the row id, as the user does.
func enter(t *testing.T, a *App, id string) {
	t.Helper()
	nav := a.navigatorFor(a.activePanel)
	item, ok := nav.Current().ItemByID(t.Context(), id)
	if !ok {
		t.Fatalf("row %q not in %v", id, nav.Current().Path())
	}
	next, err := item.(models.Enterable).Enter()
	if err != nil {
		t.Fatalf("enter %q: %v", id, err)
	}
	a.handleFolderNav(false, id, next)
}

// runJump runs cmd and feeds the jump retries it produces back into the
// app until the jump ends.
func runJump(t *testing.T, a *App, cmd tea.Cmd) {
	t.Helper()
	for range jumpAttempts + 1 {
		if cmd == nil {
			return
		}
		msg := cmd()
		jm, ok := msg.(pathJumpMsg)
		if !ok {
			return
		}
		cmd = a.continueJump(jm.jump)
	}
	t.Fatalf("jump did not end")
}

func leftSteps(a *App) []string { return a.leftNav.Steps() }

func TestHistoryBackForward(t *testing.T) {
	t.Parallel()
	tr := newJumpTree(true)
	a := newJumpApp(t, tr)
	enter(t, a, "a")
	enter(t, a, "b")

	steps := []struct {
		name string
		move func() tea.Cmd
		want []string
	}{
		{"back to a", a.historyBack, []string{"a"}},
		{"back to root", a.historyBack, nil},
		{"back at the oldest entry stays", a.historyBack, nil},
		{"forward to a", a.historyForward, []string{"a"}},
		{"forward to b", a.historyForward, []string{"a", "b"}},
		{"forward at the newest entry stays", a.historyForward, []string{"a", "b"}},
	}
	for _, s := range steps {
		runJump(t, a, s.move())
		if got := leftSteps(a); !slices.Equal(got, s.want) {
			t.Fatalf("%s: steps = %v, want %v", s.name, got, s.want)
		}
	}
	if got, want := a.leftPanel.GetCurrentPath(), "/a/b"; got != want {
		t.Fatalf("panel path = %q, want %q", got, want)
	}
}

func TestHistoryNavigatingAfterBackTruncates(t *testing.T) {
	t.Parallel()
	tr := newJumpTree(true)
	a := newJumpApp(t, tr)
	enter(t, a, "a")
	enter(t, a, "b")
	runJump(t, a, a.historyBack())
	runJump(t, a, a.historyBack())
	enter(t, a, "c")

	entries, cur := a.panelHistory(0).Entries()
	var got []string
	for _, e := range entries {
		got = append(got, e.Path)
	}
	if want := []string{"/", "/c"}; !slices.Equal(got, want) || cur != 1 {
		t.Fatalf("history = %v (cursor %d), want %v with the cursor on /c", got, cur, want)
	}
	if cmd := a.historyForward(); cmd != nil {
		t.Fatalf("forward after a new visit should do nothing")
	}
}

func TestJumpWaitsForRows(t *testing.T) {
	t.Parallel()
	tr := newJumpTree(false)
	a := newJumpApp(t, tr)
	target := navui.Location{Steps: []string{"a", "b"}, Path: "/a/b"}

	cmd := a.jumpTo(0, target, true)
	if cmd == nil {
		t.Fatalf("jump ended although the row of b is not there yet")
	}
	if got := leftSteps(a); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("while waiting, steps = %v, want [a]", got)
	}
	tr.aReady.Store(true)
	tr.a.Refresh()
	runJump(t, a, cmd)
	if got := leftSteps(a); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("steps = %v, want [a b]", got)
	}
	if cur, _ := a.panelHistory(0).Current(); !cur.Equal(target) {
		t.Fatalf("history current = %v, want the jump target", cur)
	}
}

func TestJumpGivesUpOnMissingRow(t *testing.T) {
	t.Parallel()
	tr := newJumpTree(true)
	a := newJumpApp(t, tr)
	j := pathJump{panel: 0, token: a.jumpToken[0], nav: a.leftNav, target: navui.Location{Steps: []string{"missing"}, Path: "/missing"}, attempt: jumpAttempts - 1}
	cmd := a.continueJump(j)
	if cmd == nil {
		t.Fatalf("a failed jump should report the error")
	}
	if msg, ok := cmd().(showToastMsg); !ok || msg.info || msg.text != "Cannot open /missing: missing is not there" {
		t.Fatalf("got %#v, want an error toast that names the missing row", msg)
	}
}

func TestJumpStopsWhenUserMoves(t *testing.T) {
	t.Parallel()
	tr := newJumpTree(false)
	a := newJumpApp(t, tr)
	cmd := a.jumpTo(0, navui.Location{Steps: []string{"a", "b"}}, true)
	if cmd == nil {
		t.Fatalf("expected the jump to wait")
	}
	a.handleFolderNav(true, "", nil) // the user goes back
	tr.aReady.Store(true)
	tr.a.Refresh()
	runJump(t, a, cmd)
	if got := leftSteps(a); got != nil {
		t.Fatalf("a canceled jump moved the panel to %v", got)
	}
}

func hotlistPicker(t *testing.T, a *App) *ScopePickerModel {
	t.Helper()
	modal := a.modalManager.modals[hotlistModalKey]
	if modal == nil || !modal.IsVisible() {
		t.Fatalf("hotlist is not open")
	}
	return modal.content.(*ScopePickerModel)
}

func TestHotlistAddRemovePersist(t *testing.T) {
	t.Parallel()
	tr := newJumpTree(true)
	a := newJumpApp(t, tr)
	enter(t, a, "a")

	a.Update(tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl})
	picker := hotlistPicker(t, a)
	picker.Update(tea.KeyPressMsg{Code: tea.KeyInsert})
	picker.Update(tea.KeyPressMsg{Code: tea.KeyInsert}) // a duplicate is not added
	saved, err := hotlist.Load(a.hotlistPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(saved) != 1 || !slices.Equal(saved[0].Steps, []string{"a"}) || saved[0].Path != "/a" {
		t.Fatalf("saved hotlist = %+v, want one entry for /a", saved)
	}

	// Go to the root and back to the entry through the dialog.
	a.hideModal()
	a.handleFolderNav(true, "", nil)
	a.Update(tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl})
	picker = hotlistPicker(t, a)
	_, cmd := picker.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	runJump(t, a, cmd)
	if got := leftSteps(a); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("after Enter, steps = %v, want [a]", got)
	}

	// A new App reads the saved entry.
	b := NewApp()
	t.Cleanup(b.cancel)
	b.hotlistPath = a.hotlistPath
	if cmd := b.loadHotlist(); cmd != nil {
		t.Fatalf("loadHotlist reported %v", cmd())
	}
	if len(b.hotlist) != 1 {
		t.Fatalf("reloaded hotlist has %d entries, want 1", len(b.hotlist))
	}

	a.Update(tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl})
	picker = hotlistPicker(t, a)
	picker.Update(tea.KeyPressMsg{Code: tea.KeyDelete})
	saved, err = hotlist.Load(a.hotlistPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(saved) != 0 || len(a.hotlist) != 0 {
		t.Fatalf("after Del the hotlist has %d saved and %d loaded entries, want none", len(saved), len(a.hotlist))
	}
}

func TestHistoryKeys(t *testing.T) {
	t.Parallel()
	tr := newJumpTree(true)
	a := newJumpApp(t, tr)
	enter(t, a, "a")

	_, cmd := a.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModAlt})
	runJump(t, a, cmd)
	if got := leftSteps(a); got != nil {
		t.Fatalf("after M-y, steps = %v, want root", got)
	}
	_, cmd = a.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModAlt})
	runJump(t, a, cmd)
	if got := leftSteps(a); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("after M-u, steps = %v, want [a]", got)
	}

	a.Update(tea.KeyPressMsg{Code: 'H', Mod: tea.ModAlt})
	modal := a.modalManager.modals[historyModalKey]
	if modal == nil || !modal.IsVisible() {
		t.Fatalf("M-H did not open the history list")
	}
	picker := modal.content.(*ScopePickerModel)
	if len(picker.choices) != 2 || picker.choices[0].Label != "/a" {
		t.Fatalf("history list = %+v, want /a first, then /", picker.choices)
	}
	picker.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	_, cmd = picker.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	runJump(t, a, cmd)
	if got := leftSteps(a); got != nil {
		t.Fatalf("after choosing /, steps = %v, want root", got)
	}
}

func TestSwapPanelsSwapsHistory(t *testing.T) {
	t.Parallel()
	tr := newJumpTree(true)
	a := newJumpApp(t, tr)
	enter(t, a, "a")
	left := a.panelHistory(0)
	right := a.panelHistory(1)
	a.swapPanels()
	if a.panelHistory(0) != right || a.panelHistory(1) != left {
		t.Fatalf("swapPanels did not swap the histories")
	}
}
