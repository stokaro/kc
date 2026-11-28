package ui

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// settle runs cmd as the program loop would and hands the App back the
// messages that move a follower: panel content loads, path walk and jump
// retries and explicit selection reports. Other messages (timers, toasts,
// the wait for folder events) are dropped. A command that blocks is left
// behind once nothing has arrived for a while and no panel is loading.
func settle(t *testing.T, app *App, cmd tea.Cmd) {
	t.Helper()
	results := make(chan tea.Msg)
	done := make(chan struct{})
	defer close(done)
	outstanding := 0
	start := func(c tea.Cmd) {
		if c == nil {
			return
		}
		outstanding++
		go func() {
			msg := c()
			select {
			case results <- msg:
			case <-done:
			}
		}()
	}
	start(cmd)
	deadline := time.Now().Add(30 * time.Second)
	for outstanding > 0 {
		if time.Now().After(deadline) {
			t.Fatal("the commands did not settle in 30s")
		}
		select {
		case msg := <-results:
			outstanding--
			switch m := msg.(type) {
			case tea.BatchMsg:
				for _, c := range m {
					start(c)
				}
			case panelcontent.BackgroundMsg, panelWalkMsg, pathJumpMsg, PanelSelectionChangedMsg:
				_, next := app.Update(m)
				start(next)
			}
		case <-time.After(time.Second):
			if !panelsLoading(t.Context(), app) {
				return
			}
		}
	}
}

// panelsLoading reports whether a panel's active widget waits for its
// content.
func panelsLoading(ctx context.Context, app *App) bool {
	for idx := range 2 {
		p := app.panelByIndex(idx)
		if p == nil {
			continue
		}
		if fi, ok := p.ensureActiveWidget(ctx).(panelcontent.FrameInfoProvider); ok &&
			fi.FrameInfo(ctx, panelcontent.FrameInfoRequest{Width: 40}).HeaderStatus == panelcontent.LoadingStatus {
			return true
		}
	}
	return false
}

// sendKey sends key to the App and settles what it starts.
func sendKey(t *testing.T, app *App, key tea.KeyPressMsg) {
	t.Helper()
	_, cmd := app.Update(key)
	settle(t, app, cmd)
}

// shellCd types line on the shell line and presses Enter, which moves the
// active panel (ADR 0040).
func shellCd(t *testing.T, app *App, line string) {
	t.Helper()
	typeOnShellLine(app, line)
	sendKey(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
}

// followerText is the right panel as the App draws it, frame included,
// without colors.
func followerText(t *testing.T, app *App) string {
	t.Helper()
	p := app.rightPanel
	if p.frameWidth <= 0 || p.frameHeight <= 0 {
		t.Fatalf("the right panel has no frame size (%dx%d)", p.frameWidth, p.frameHeight)
	}
	return ansi.Strip(p.Render(t.Context(), p.frameWidth, p.frameHeight, false))
}

// followerPlaceholder is what a follower in mode shows with nothing to show.
func followerPlaceholder(mode PanelViewMode) string {
	if mode == PanelModeManifest {
		return "Select a resource to view its manifest."
	}
	return "Select a resource to describe."
}

// showsObject reports whether text is mode's output for ns/name: the Name
// and Namespace lines of kubectl describe, or the metadata of a manifest.
func showsObject(mode PanelViewMode, text, ns, name string) bool {
	nameRE, nsRE := `Name:\s+`, `Namespace:\s+`
	if mode == PanelModeManifest {
		nameRE, nsRE = `\bname: `, `\bnamespace: `
	}
	return regexp.MustCompile(nameRE+regexp.QuoteMeta(name)+`\b`).MatchString(text) &&
		regexp.MustCompile(nsRE+regexp.QuoteMeta(ns)+`\b`).MatchString(text)
}

// assertFollowsLeft fails unless the right panel, in mode, shows the row
// under the left panel's cursor: the object read from the left panel's
// connection, or nothing when the row is not an object. Its frame must
// name the left panel's context and namespace, and an object shown must
// be in that namespace. It returns the object's name, "" for none.
func assertFollowsLeft(t *testing.T, app *App, mode PanelViewMode) string {
	t.Helper()
	ctx := t.Context()
	conn, ns := app.ownConn(0), app.panelScope[0]
	if s := app.rightPanel.scope; s.Context != conn.name() || s.Namespace != ns {
		t.Fatalf("the follower's frame names %s : %s, want the left panel's %s : %s", s.Context, s.Namespace, conn.name(), ns)
	}
	text := followerText(t, app)
	if !strings.Contains(text, " "+conn.name()+" ") || !strings.Contains(text, " "+namespaceScopeLabel(ns)+" ") {
		t.Fatalf("the follower's frame does not read %s : %s:\n%s", conn.name(), ns, text)
	}
	sel := app.rightPanel.CurrentSelection(ctx)
	item, _ := app.leftPanel.SelectedNavItem(ctx)
	obj, isObj := item.(models.ObjectItem)
	if !isObj {
		if obj, ok := sel.Item.(models.ObjectItem); ok {
			t.Fatalf("the left cursor is on %v, not an object, but the follower keeps %s/%s", item, obj.Namespace(), obj.Name())
		}
		if !strings.Contains(text, followerPlaceholder(mode)) {
			t.Fatalf("the left cursor is on %v, not an object; the follower shows:\n%s", item, text)
		}
		return ""
	}
	if obj.Namespace() != ns {
		t.Fatalf("the left row %s/%s is outside the frame's namespace %s", obj.Namespace(), obj.Name(), ns)
	}
	got, ok := sel.Item.(models.ObjectItem)
	if !ok || got.GVR() != obj.GVR() || got.Namespace() != obj.Namespace() || got.Name() != obj.Name() {
		t.Fatalf("the follower's selection is %v, want %s/%s", sel.Item, obj.Namespace(), obj.Name())
	}
	if sel.Source != conn.connID() {
		t.Fatalf("the follower reads %s/%s from connection %q, want the left panel's %q", obj.Namespace(), obj.Name(), sel.Source, conn.connID())
	}
	if !showsObject(mode, text, obj.Namespace(), obj.Name()) {
		t.Fatalf("the follower does not show %s/%s:\n%s", obj.Namespace(), obj.Name(), text)
	}
	return obj.Name()
}
