package ui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	models "github.com/sttts/kc/internal/models"
	panelcontent "github.com/sttts/kc/internal/ui/panelcontent"
)

// contentSpyWidget stands in for a Describe or Manifest widget: it records
// selections, refresh requests and background results.
type contentSpyWidget struct {
	selectionSpyWidget
	refreshes  int
	background []tea.Msg
	ran        *bool // set when the command from OnSelectionChanged runs
}

type spyBackgroundMsg struct{}

func (spyBackgroundMsg) WidgetBackground() {}

func (s *contentSpyWidget) OnSelectionChanged(ctx context.Context, sel panelcontent.Selection) tea.Cmd {
	s.selectionSpyWidget.OnSelectionChanged(ctx, sel)
	ran := s.ran
	return func() tea.Msg {
		if ran != nil {
			*ran = true
		}
		return nil
	}
}

func (s *contentSpyWidget) Refresh(context.Context) tea.Cmd {
	s.refreshes++
	return nil
}

func (s *contentSpyWidget) Update(_ context.Context, msg tea.Msg) (tea.Cmd, bool) {
	if _, ok := msg.(panelcontent.BackgroundMsg); ok {
		s.background = append(s.background, msg)
		return nil, true
	}
	return nil, false
}

// runCmd executes cmd and every command of nested batches.
func runCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			runCmd(c)
		}
	}
}

func TestHiddenListSelectionDoesNotReachDescribe(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	panel := NewPanel("test")
	spy := &contentSpyWidget{}
	panel.RegisterMode(PanelModeDescribe, func(*Panel) PanelWidget { return spy })
	panel.SetMode(ctx, PanelModeDescribe)
	before := len(spy.events)

	// RefreshFolder on the hidden list reports its own selection with Force.
	item := models.NewSimpleItem("own", []string{"own"}, nil, models.WhiteStyle())
	panel.widgetSelectionChanged(ctx, panelcontent.Selection{ID: "own", Item: item, Force: true})

	if len(spy.events) != before {
		t.Fatalf("the hidden list's selection replaced what Describe shows: %#v", spy.events[before:])
	}
}

func TestInactiveWidgetGetsNoSelectionButGetsResults(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	panel := NewPanel("test")
	spy := &contentSpyWidget{}
	panel.RegisterMode(PanelModeManifest, func(*Panel) PanelWidget { return spy })
	panel.SetMode(ctx, PanelModeManifest)
	panel.SetMode(ctx, PanelModeList)
	before := len(spy.events)

	item := models.NewSimpleItem("foo", []string{"foo"}, nil, models.WhiteStyle())
	panel.NotifySelection(ctx, panelcontent.Selection{ID: "foo", Item: item})
	if len(spy.events) != before {
		t.Fatalf("an inactive widget must not load for selections")
	}

	panel.DeliverBackground(ctx, spyBackgroundMsg{})
	if len(spy.background) != 1 {
		t.Fatalf("expected the inactive widget to receive its background result, got %d", len(spy.background))
	}
}

func TestSelectionChangeRunsOtherPanelLoad(t *testing.T) {
	t.Parallel()
	app := NewApp()
	defer app.cancel()
	ran := false
	spy := &contentSpyWidget{ran: &ran}
	app.rightPanel.RegisterMode(PanelModeDescribe, func(*Panel) PanelWidget { return spy })
	app.rightPanel.SetMode(t.Context(), PanelModeDescribe)

	item := models.NewSimpleItem("foo", []string{"foo"}, nil, models.WhiteStyle())
	_, cmd := app.Update(PanelSelectionChangedMsg{
		Panel:     app.leftPanel,
		Selection: panelcontent.Selection{ID: "foo", Item: item},
	})
	runCmd(cmd)
	if !ran {
		t.Fatalf("the load command of the other panel's widget was dropped")
	}
}

func TestFolderDirtyRefreshesContentWidgets(t *testing.T) {
	t.Parallel()
	app := NewApp()
	defer app.cancel()
	spy := &contentSpyWidget{}
	app.rightPanel.RegisterMode(PanelModeDescribe, func(*Panel) PanelWidget { return spy })
	app.rightPanel.SetMode(t.Context(), PanelModeDescribe)

	app.Update(FolderDirtyMsg{PanelIdx: 0})
	if spy.refreshes != 1 {
		t.Fatalf("expected one refresh request after the left folder changed, got %d", spy.refreshes)
	}
}

func TestBackgroundResultsPassDialogs(t *testing.T) {
	t.Parallel()
	if !passesModal(spyBackgroundMsg{}) {
		t.Fatalf("background results must reach widgets behind a dialog")
	}
	app := NewApp()
	defer app.cancel()
	spy := &contentSpyWidget{}
	app.leftPanel.RegisterMode(PanelModeManifest, func(*Panel) PanelWidget { return spy })
	app.leftPanel.SetMode(t.Context(), PanelModeManifest)
	app.showTerminal = true // the default route skips panels then

	app.Update(spyBackgroundMsg{})
	if len(spy.background) != 1 {
		t.Fatalf("expected the result to reach the widget, got %d", len(spy.background))
	}
}
