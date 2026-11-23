package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// stubWidget shows the name of the last item it was given.
type stubWidget struct {
	name string
	keys int
}

func (w *stubWidget) Init(context.Context) tea.Cmd { return nil }

func (w *stubWidget) Update(_ context.Context, msg tea.Msg) (tea.Cmd, bool) {
	if _, ok := msg.(tea.KeyMsg); ok {
		w.keys++
	}
	return nil, true
}
func (w *stubWidget) SetFocus(context.Context, bool) {}
func (w *stubWidget) Teardown(context.Context)       {}

func (w *stubWidget) View(context.Context, panelcontent.Frame) tea.View {
	return tea.NewView("showing " + w.name)
}

func (w *stubWidget) OnSelectionChanged(_ context.Context, sel panelcontent.Selection) tea.Cmd {
	if sel.Item != nil {
		w.name = selectionRowID(sel.Item)
	}
	return nil
}

// The picture is the one taken at freeze time even when the live widget
// behind it changes, it is cut to a smaller panel, and selections wait.
func TestFrozenPictureIsTheOldContent(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	stub := &stubWidget{}
	p := NewPanel("t")
	p.RegisterMode(PanelModeDescribe, func(*Panel) panelcontent.Widget { return stub })
	p.SetMode(ctx, PanelModeDescribe)
	p.Render(ctx, 40, 10, false)
	p.NotifySelection(ctx, panelcontent.Selection{ID: "web", Item: pinPod("web", "u1")})
	content := func() string { return strings.TrimSpace(viewString(p.renderContentFocused(ctx, false))) }
	if got := content(); got != "showing web" {
		t.Fatalf("live = %q", got)
	}
	p.Freeze(ctx, time.Now(), "")
	// The live widget moves on (a background update).
	stub.OnSelectionChanged(ctx, panelcontent.Selection{Item: pinPod("api", "u2")})
	if got := content(); got != "showing web" {
		t.Fatalf("frozen picture = %q, want the content at freeze time", got)
	}
	if got := p.frozenView(7, 2); got != "showing\n" {
		t.Fatalf("cut picture = %q", got)
	}
	p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if stub.keys != 0 {
		t.Fatal("a key reached the widget behind the picture")
	}
	// A selection does not reach the widget while frozen.
	p.NotifySelection(ctx, panelcontent.Selection{ID: "db", Item: pinPod("db", "u3")})
	if stub.name != "api" {
		t.Fatalf("a selection reached the widget behind the picture: %q", stub.name)
	}
	if missed := p.Thaw(); !missed {
		t.Fatal("Thaw did not report the selection that waited")
	}
	if got := content(); got != "showing api" {
		t.Fatalf("after thaw = %q, want the live widget", got)
	}
	p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if stub.keys != 1 {
		t.Fatal("keys do not reach the live widget after thawing")
	}
}
