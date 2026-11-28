package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/models"
	navui "github.com/stokaro/kc/internal/navigation"
)

func typeKeys(l *shellLine, s string) {
	for _, r := range s {
		l.key(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func TestShellLine(t *testing.T) {
	t.Parallel()
	backspace := tea.KeyPressMsg{Code: tea.KeyBackspace}
	tests := []struct {
		name   string
		run    func(l *shellLine)
		want   string
		wantOK bool
	}{
		{name: "fresh line", run: func(*shellLine) {}, want: "", wantOK: true},
		{name: "typed", run: func(l *shellLine) { typeKeys(l, "cd pods") }, want: "cd pods", wantOK: true},
		{name: "shifted letters", run: func(l *shellLine) {
			l.key(tea.KeyPressMsg{Code: 'a', Text: "A", Mod: tea.ModShift})
		}, want: "A", wantOK: true},
		{name: "backspace", run: func(l *shellLine) { typeKeys(l, "cd podx"); l.key(backspace); typeKeys(l, "s") }, want: "cd pods", wantOK: true},
		{name: "backspace on empty", run: func(l *shellLine) { l.key(backspace); typeKeys(l, "cd") }, want: "cd", wantOK: true},
		{name: "C-u clears", run: func(l *shellLine) {
			typeKeys(l, "rm -rf")
			l.key(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
			typeKeys(l, "cd -")
		}, want: "cd -", wantOK: true},
		{name: "single-line paste", run: func(l *shellLine) { typeKeys(l, "cd "); l.paste("deploy/web") }, want: "cd deploy/web", wantOK: true},
		{name: "multi-line paste", run: func(l *shellLine) { l.paste("cd a\ncd b") }},
		{name: "tab completion", run: func(l *shellLine) { typeKeys(l, "cd po"); l.key(tea.KeyPressMsg{Code: tea.KeyTab}) }},
		{name: "cursor move", run: func(l *shellLine) { typeKeys(l, "cd po"); l.key(tea.KeyPressMsg{Code: tea.KeyLeft}) }},
		{name: "alt key", run: func(l *shellLine) {
			typeKeys(l, "cd po")
			l.key(tea.KeyPressMsg{Code: 'b', Text: "b", Mod: tea.ModAlt})
		}},
		{name: "enter starts a fresh line", run: func(l *shellLine) {
			l.key(tea.KeyPressMsg{Code: tea.KeyTab})
			l.key(tea.KeyPressMsg{Code: tea.KeyEnter})
			typeKeys(l, "cd x")
		}, want: "cd x", wantOK: true},
		{name: "keys that add no text keep an empty line", run: func(l *shellLine) {
			for _, r := range "abdefgkl" {
				l.key(tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl})
			}
			typeKeys(l, "cd")
		}, want: "cd", wantOK: true},
		{name: "C-e after text", run: func(l *shellLine) {
			typeKeys(l, "cd")
			l.key(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
		}},
		{name: "Delete on an empty line", run: func(l *shellLine) { l.key(tea.KeyPressMsg{Code: tea.KeyDelete}) }},
		{name: "C-Right on an empty line", run: func(l *shellLine) {
			l.key(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModCtrl})
		}},
		{name: "C-y on an empty line", run: func(l *shellLine) { l.key(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl}) }},
		{name: "C-c starts a fresh line", run: func(l *shellLine) {
			l.invalidate()
			l.key(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		}, want: "", wantOK: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var l shellLine
			tc.run(&l)
			got, ok := l.current()
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("current() = %q, %v; want %q, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// cdApp builds an App whose left panel shows root, without a cluster.
func cdApp(t *testing.T, root models.Folder) *App {
	t.Helper()
	a := NewApp()
	t.Cleanup(a.cancel)
	ctx := t.Context()
	a.leftNav = navui.NewNavigator(root)
	a.rightNav = navui.NewNavigator(newLateFolder())
	a.leftPanel.UseFolder(ctx, true)
	a.leftPanel.SetFolder(ctx, root, false)
	a.leftPanel.SetCurrentPath(a.navigatorPath(a.leftNav))
	return a
}

// typeOnShellLine stands in for the keys reaching the shell; the test App
// has no PTY, so Terminal.Update does not record them.
func typeOnShellLine(a *App, s string) {
	a.terminal.hasTyped = true
	a.terminal.line.reset()
	a.terminal.line.paste(s)
}

func TestShellCdMovesActivePanel(t *testing.T) {
	root := newLateFolder()
	web := newLateFolder("web")
	web.add(plainRow("logs", "logs"))
	a := cdApp(t, root)
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}

	typeOnShellLine(a, "cd web")
	_, cmd := a.handleKeyMsg(enter, nil)
	if cmd == nil {
		t.Fatalf("cd returned no command; want a wait tick")
	}
	if a.terminal.HasInput() {
		t.Fatalf("shell line still marked as typed after cd")
	}
	if got := a.leftPanel.GetCurrentPath(); got != "/" {
		t.Fatalf("panel moved before the row existed: %s", got)
	}
	// The row arrives; the next tick finishes the walk.
	root.add(enterRow("web", "web", web))
	if cmd := a.continuePanelNavigation(0, a.panelNavGen); cmd != nil {
		t.Fatalf("walk still waiting after the row appeared")
	}
	if got := a.leftPanel.GetCurrentPath(); got != "/web" {
		t.Fatalf("panel path = %s, want /web", got)
	}

	typeOnShellLine(a, "cd -")
	a.handleKeyMsg(enter, nil)
	if got := a.leftPanel.GetCurrentPath(); got != "/" {
		t.Fatalf("cd - went to %s, want /", got)
	}
	typeOnShellLine(a, "cd -")
	a.handleKeyMsg(enter, nil)
	if got := a.leftPanel.GetCurrentPath(); got != "/web" {
		t.Fatalf("second cd - went to %s, want /web", got)
	}
}

// A cd lands in the panel history, so M-, after it returns to where the
// panel was and the history window lists it.
func TestShellCdIsRecordedInHistory(t *testing.T) {
	root := newLateFolder()
	web := newLateFolder("web")
	web.add(plainRow("logs", "logs"))
	root.add(enterRow("web", "web", web))
	a := cdApp(t, root)
	a.recordHistory(0)

	typeOnShellLine(a, "cd web")
	a.handleKeyMsg(tea.KeyPressMsg{Code: tea.KeyEnter}, nil)
	if got := a.leftPanel.GetCurrentPath(); got != "/web" {
		t.Fatalf("panel path = %s, want /web", got)
	}
	entries, cur := a.panelHistory(0).Entries()
	if len(entries) != 2 || cur != 1 {
		t.Fatalf("history has %d entries at %d after cd, want 2 at 1: %v", len(entries), cur, entries)
	}
	if got := entries[1].Path; got != "/web" {
		t.Errorf("history entry after cd = %q, want /web", got)
	}
}

func TestShellLineOtherCommandsReachTheShell(t *testing.T) {
	a := cdApp(t, newLateFolder())
	typeOnShellLine(a, "ls -l")
	if _, ok := a.interceptShellCd(); ok {
		t.Fatalf("ls was intercepted")
	}
	a.terminal.line.invalidate()
	if _, ok := a.interceptShellCd(); ok {
		t.Fatalf("an unknown line was intercepted")
	}
}

func TestNavigatePanelNewWalkReplacesOld(t *testing.T) {
	root := newLateFolder()
	a := cdApp(t, root)
	var results []error
	record := func(err error) tea.Cmd { results = append(results, err); return nil }
	if cmd := a.NavigatePanel(0, PanelPath{Elems: names("late")}, record); cmd == nil {
		t.Fatalf("first walk did not wait")
	}
	first := a.panelNavGen
	b := newLateFolder("b")
	root.add(enterRow("b", "b", b))
	a.NavigatePanel(0, PanelPath{Elems: names("b")}, record)
	// A tick of the old walk is ignored.
	if cmd := a.continuePanelNavigation(0, first); cmd != nil {
		t.Fatalf("stale tick advanced a walk")
	}
	if len(results) != 1 || results[0] != nil {
		t.Fatalf("results = %v, want one success", results)
	}
	if got := a.leftPanel.GetCurrentPath(); got != "/b" {
		t.Fatalf("panel path = %s, want /b", got)
	}
}
