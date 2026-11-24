package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/stokaro/kc/internal/ui/dashboard"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
	infowidget "github.com/stokaro/kc/internal/ui/panelcontent/info"
	"github.com/stokaro/kc/pkg/appconfig"
)

// TestInfoImageBlocksDoNotOverlap: the ID block of each Info panel stays
// in the 256-color range above the 16 theme colors, and overlaps neither
// the other panel's block nor the dashboard's IDs.
func TestInfoImageBlocksDoNotOverlap(t *testing.T) {
	t.Parallel()
	owner := make(map[int]string)
	claim := func(first, n int, who string) {
		for id := first; id < first+n; id++ {
			if id < 16 || id > 255 {
				t.Errorf("%s uses ID %d", who, id)
			}
			if prev, ok := owner[id]; ok {
				t.Errorf("ID %d is %s's and %s's", id, prev, who)
			}
			owner[id] = who
		}
	}
	claim(chartImageFirstID, 2*dashboard.ImageKeys, "the dashboard")
	for n := range 2 {
		first, ok := infoImageID(n)
		if !ok {
			t.Fatalf("no ID block for Info panel %d", n)
		}
		claim(int(first), 2*infowidget.ImageKeys, fmt.Sprintf("Info panel %d", n))
	}
	if _, ok := infoImageID(3); ok {
		t.Error("a fourth Info block reaches the dashboard's IDs")
	}
}

func graphicsApp(t *testing.T, mode string) *App {
	t.Helper()
	cfg := appconfig.Default()
	cfg.Charts.Graphics = mode
	return &App{ctx: t.Context(), cfg: cfg}
}

// TestInfoImagesFollowTheirPanel: each panel keeps its own set across
// calls, so a swap of the panels keeps their IDs apart; charts.graphics
// text gives Info no images.
func TestInfoImagesFollowTheirPanel(t *testing.T) {
	t.Parallel()
	a := graphicsApp(t, appconfig.ChartsGraphicsKitty)
	left, right := NewPanel("l"), NewPanel("r")
	l, r := a.infoImages(left), a.infoImages(right)
	if l == nil || r == nil || l == r {
		t.Fatalf("image sets %p and %p, want two", l, r)
	}
	a.leftPanel, a.rightPanel = right, left
	if a.infoImages(left) != l || a.infoImages(right) != r {
		t.Error("a panel got another image set")
	}
	if a.chartImages() == nil || a.chartImages() == l || a.chartImages() == r {
		t.Error("the dashboard shares a set with an Info panel")
	}
	if im := graphicsApp(t, appconfig.ChartsGraphicsText).infoImages(left); im != nil {
		t.Error("charts.graphics text gives Info images")
	}
}

// imageWidget is a panel widget that draws images: it records the
// terminal messages it gets and what its release deleted.
type imageWidget struct {
	stubWidget
	terminal []tea.Msg
	released int
	text     bool
}

func (w *imageWidget) Update(ctx context.Context, msg tea.Msg) (tea.Cmd, bool) {
	if m, ok := msg.(panelcontent.TerminalMsg); ok {
		w.terminal = append(w.terminal, m.Msg)
		return nil, true
	}
	return w.stubWidget.Update(ctx, msg)
}

func (w *imageWidget) ReleaseImages() string {
	w.released++
	return "\x1b_Ga=d,d=I,q=2,i=192\x1b\\"
}

func (w *imageWidget) View(context.Context, panelcontent.Frame) tea.View {
	return tea.NewView("picture \U0010EEEE")
}

func (w *imageWidget) TextView(context.Context, panelcontent.Frame) tea.View {
	w.text = true
	return tea.NewView("picture as text")
}

// TestLeavingAModeReleasesItsImages: switching a panel away from a mode
// whose widget draws images writes the delete commands.
func TestLeavingAModeReleasesItsImages(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	w := &imageWidget{}
	p := NewPanel("t")
	p.RegisterMode(PanelModeInfo, func(*Panel) panelcontent.Widget { return w })
	p.SetMode(ctx, PanelModeInfo)
	if w.released != 0 {
		t.Fatal("entering the mode released images")
	}
	out := rawOf(p.SetMode(ctx, PanelModeList))
	if w.released != 1 || !strings.Contains(out, "a=d") {
		t.Errorf("leaving the mode: %d releases, output %q", w.released, out)
	}
	// Setting the same mode again is no change.
	p.SetMode(ctx, PanelModeList)
	if w.released != 1 {
		t.Errorf("%d releases, want 1", w.released)
	}
}

// TestFrozenPanelKeepsText: C-x f takes the text view of a widget that
// draws images.
func TestFrozenPanelKeepsText(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	w := &imageWidget{}
	p := NewPanel("t")
	p.RegisterMode(PanelModeInfo, func(*Panel) panelcontent.Widget { return w })
	p.SetMode(ctx, PanelModeInfo)
	p.Render(ctx, 40, 10, false)
	p.Freeze(ctx, time.Now(), "")
	got := viewString(p.renderContentFocused(ctx, false))
	if !w.text || strings.ContainsRune(got, '\U0010EEEE') || !strings.Contains(got, "picture as text") {
		t.Errorf("frozen picture %q", got)
	}
}

// TestImageMessagesReachPanelsBehindADialog: a cell size report reaches
// the widgets of both panels while a dialog covers them.
func TestImageMessagesReachPanelsBehindADialog(t *testing.T) {
	t.Parallel()
	app := NewApp()
	t.Cleanup(app.cancel)
	model, _ := app.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	app = model.(*App)
	ctx := t.Context()
	left, right := &imageWidget{}, &imageWidget{}
	app.leftPanel.RegisterMode(PanelModeInfo, func(*Panel) panelcontent.Widget { return left })
	app.rightPanel.RegisterMode(PanelModeInfo, func(*Panel) panelcontent.Widget { return right })
	app.leftPanel.SetMode(ctx, PanelModeInfo)
	app.rightPanel.SetMode(ctx, PanelModeInfo)
	app.modalManager.Show("view_options")
	if !app.modalManager.IsModalVisible() {
		t.Fatal("no dialog open")
	}
	app.Update(uv.CellSizeEvent{Width: 16, Height: 34})
	for name, w := range map[string]*imageWidget{"left": left, "right": right} {
		if len(w.terminal) != 1 {
			t.Errorf("the %s panel got %d terminal messages, want 1", name, len(w.terminal))
		}
	}
}

// rawOf is what cmd and its batches write to the terminal.
func rawOf(cmd tea.Cmd) string {
	var b strings.Builder
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case tea.RawMsg:
			fmt.Fprint(&b, msg.Msg)
		}
	}
	return b.String()
}
