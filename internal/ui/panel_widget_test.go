package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	models "github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

func TestPanelModeSwitchesToManifest(t *testing.T) {
	panel := NewPanel("test")
	ctx := t.Context()
	panel.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	panel.SetMode(ctx, PanelModeManifest)
	if panel.Mode() != PanelModeManifest {
		t.Fatalf("expected manifest mode, got %v", panel.Mode())
	}
	view := viewString(panel.View())
	if !strings.Contains(view, "Select a resource") {
		t.Fatalf("expected manifest placeholder, got %q", view)
	}
	widget := panel.ensureActiveWidget(ctx)
	provider, ok := widget.(panelcontent.FrameInfoProvider)
	if !ok {
		t.Fatalf("manifest widget does not implement FrameInfoProvider")
	}
	info := provider.FrameInfo(ctx, panelcontent.FrameInfoRequest{Width: panel.width})
	if !info.SuppressFooter {
		t.Fatalf("expected manifest to suppress footer")
	}
	if info.FooterStatus != "" {
		t.Fatalf("expected manifest footer status to be empty without a selection, got %q", info.FooterStatus)
	}
}

func TestNextPanelModeCycles(t *testing.T) {
	modes := PanelModeOrder()
	for i := range modes {
		next := NextPanelMode(modes[i])
		if next == modes[i] {
			t.Fatalf("mode did not advance for %v", modes[i])
		}
	}
}

type selectionSpyWidget struct {
	panelcontent.Widget
	events []panelcontent.Selection
}

func (s *selectionSpyWidget) Init(context.Context) tea.Cmd                    { return nil }
func (s *selectionSpyWidget) Update(context.Context, tea.Msg) (tea.Cmd, bool) { return nil, false }
func (s *selectionSpyWidget) View(context.Context, panelcontent.Frame) tea.View {
	return tea.NewView("")
}
func (s *selectionSpyWidget) SetFocus(context.Context, bool) {}
func (s *selectionSpyWidget) Teardown(context.Context)       {}
func (s *selectionSpyWidget) OnSelectionChanged(_ context.Context, sel panelcontent.Selection) tea.Cmd {
	s.events = append(s.events, sel)
	return nil
}

func TestPanelDescribeModeReplaysSelection(t *testing.T) {
	ctx := t.Context()
	panel := NewPanel("test")
	panel.Update(tea.WindowSizeMsg{Width: 20, Height: 5})
	spy := &selectionSpyWidget{}
	panel.RegisterMode(PanelModeDescribe, func(*Panel) PanelWidget { return spy })
	item := models.NewSimpleItem("foo", []string{"/foo"}, nil, models.WhiteStyle())
	panel.SetMode(ctx, PanelModeDescribe)
	panel.NotifySelection(ctx, panelcontent.Selection{ID: "foo", Item: item})
	if len(spy.events) == 0 {
		t.Fatalf("expected describe widget to receive selection replay")
	}
	got := spy.events[len(spy.events)-1]
	if got.ID != "foo" {
		t.Fatalf("expected selection ID foo, got %s", got.ID)
	}
	if got.Item == nil {
		t.Fatalf("expected selection item to be populated")
	}
}
