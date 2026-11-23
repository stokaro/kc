package ui

// White-box: these tests drive the App's F1 path and read the help
// window's state (page, links, history), which the screen shows only as
// colors.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/help"
)

func helpTestApp(t *testing.T, width, height int) *App {
	t.Helper()
	a := NewApp()
	t.Cleanup(a.cancel)
	a.width, a.height = width, height
	return a
}

func helpPress(a *App, code rune) tea.Cmd {
	_, cmd := a.Update(tea.KeyPressMsg(tea.Key{Code: code}))
	return cmd
}

func helpType(a *App, s string) {
	for _, r := range s {
		a.Update(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
	}
}

func helpScreen(a *App) []string {
	return strings.Split(ansi.Strip(viewString(a.View())), "\n")
}

// helpBox finds the help window's frame on the screen: its first and last
// column and its top and bottom row.
func helpBox(t *testing.T, lines []string) (left, right, top, bottom int) {
	t.Helper()
	top = -1
	for y, line := range lines {
		r := []rune(line)
		title := strings.Index(line, "┤ Help ├")
		if title < 0 {
			title = strings.Index(line, " Help ")
		}
		if title < 0 || !strings.Contains(line, "┌") {
			continue
		}
		at := len([]rune(line[:title]))
		for x := at; x >= 0; x-- {
			if r[x] == '┌' {
				left = x
				break
			}
		}
		for x := at; x < len(r); x++ {
			if r[x] == '┐' {
				right = x
				break
			}
		}
		top = y
		break
	}
	if top < 0 {
		t.Fatalf("no help window on the screen:\n%s", strings.Join(lines, "\n"))
	}
	for y := top + 1; y < len(lines); y++ {
		if r := []rune(lines[y]); left < len(r) && r[left] == '└' {
			return left, right, top, y
		}
	}
	t.Fatal("the help window has no bottom")
	return left, right, top, bottom
}

func TestHelpWindowIsNarrowAndCenteredOnAWideScreen(t *testing.T) {
	t.Parallel()
	const width, height = 300, 50
	a := helpTestApp(t, width, height)
	a.showHelp(help.InvestigationWindow)
	lines := helpScreen(a)
	left, right, top, bottom := helpBox(t, lines)
	boxW := right - left + 1
	if boxW > helpWindowWidth {
		t.Fatalf("help window is %d columns wide, want at most %d", boxW, helpWindowWidth)
	}
	if lm, rm := left, width-1-right; lm-rm > 2 || rm-lm > 2 {
		t.Fatalf("help window not centered: %d columns left of it, %d right", lm, rm)
	}
	if bottom-top+1 < height/2 {
		t.Fatalf("help window is %d rows high on a %d-row screen", bottom-top+1, height)
	}
	if got := a.helpViewer.TextWidth(); got != helpTextWidth {
		t.Fatalf("text width = %d, want %d", got, helpTextWidth)
	}
	for n, line := range a.helpViewer.lines {
		if w := ansi.StringWidth(line); w > helpTextWidth {
			t.Fatalf("line %d is %d columns wide, want at most %d: %q", n, w, helpTextWidth, ansi.Strip(line))
		}
	}
	for y := top + 1; y < bottom; y++ {
		inner := []rune(lines[y])[left+1 : right]
		if text := strings.TrimRight(string(inner), " "); len([]rune(text)) > helpTextWidth+helpPadX {
			t.Fatalf("row %d of the window holds %d columns of text", y, len([]rune(text)))
		}
	}
	if !strings.Contains(lines[height-1], "Help") || !strings.Contains(lines[height-1], "Index") ||
		!strings.Contains(lines[height-1], "Prev") || !strings.Contains(lines[height-1], "Quit") {
		t.Fatalf("key bar = %q, want Help, Index, Prev and Quit", lines[height-1])
	}
}

func TestHelpWindowUsesANarrowScreen(t *testing.T) {
	t.Parallel()
	a := helpTestApp(t, 60, 24)
	a.showHelp(help.Index)
	left, right, _, _ := helpBox(t, helpScreen(a))
	if w := right - left + 1; w > 60 || w < 50 {
		t.Fatalf("help window is %d columns on a 60-column screen", w)
	}
	for n, line := range a.helpViewer.lines {
		if w := ansi.StringWidth(line); w > a.helpViewer.TextWidth() {
			t.Fatalf("line %d is %d columns wide, text width %d", n, w, a.helpViewer.TextWidth())
		}
	}
}

func TestHelpLinksPrevIndexAndQuit(t *testing.T) {
	t.Parallel()
	a := helpTestApp(t, 120, 40)
	a.showHelp(help.Index)
	viewString(a.View()) // lay out the window
	v := a.helpViewer
	if v.Page() != help.Index || len(v.links) == 0 {
		t.Fatalf("page %q with %d links", v.Page(), len(v.links))
	}
	helpPress(a, tea.KeyTab)
	target := v.links[v.focus].target
	helpPress(a, tea.KeyEnter)
	if v.Page() != target {
		t.Fatalf("Enter on a link to %q shows %q", target, v.Page())
	}
	helpPress(a, tea.KeyF3)
	if v.Page() != help.Index {
		t.Fatalf("F3 (Prev) shows %q, want the index", v.Page())
	}
	helpPress(a, tea.KeyF1)
	if v.Page() != help.Help {
		t.Fatalf("F1 in the help shows %q, want the page about the help", v.Page())
	}
	helpPress(a, tea.KeyF2)
	if v.Page() != help.Index {
		t.Fatalf("F2 shows %q, want the index", v.Page())
	}
	helpPress(a, tea.KeyF10)
	if a.modalManager.IsModalVisible() {
		t.Fatal("F10 did not close the help")
	}
}

func TestHelpLinkClick(t *testing.T) {
	t.Parallel()
	v := NewHelpViewer(help.Index)
	v.SetDimensions(helpTextWidth+2*helpPadX, 40)
	l := v.links[2]
	s := l.spans[0]
	v.Update(tea.MouseReleaseMsg(tea.Mouse{X: s.start + helpPadX, Y: s.line - v.scroll, Button: tea.MouseLeft}))
	if v.Page() != l.target {
		t.Fatalf("a click on the link to %q shows %q", l.target, v.Page())
	}
}

func TestHelpLinksAreColored(t *testing.T) {
	t.Parallel()
	v := NewHelpViewer(help.Index)
	v.SetDimensions(helpTextWidth+2*helpPadX, 60)
	screen := viewString(v.View())
	on := func(style lipgloss.Style) string {
		seq, _, _ := strings.Cut(style.Render("X"), "X")
		return seq
	}
	if !strings.Contains(screen, on(helpLinkSelStyle)) {
		t.Fatal("the selected link is not white on blue")
	}
	if !strings.Contains(screen, helpLinkStyle.Render("Panels and object lists")) {
		t.Fatal("a link is not black on cyan")
	}
}

// F1 in the investigation window dialog explains it; closing the help
// returns to the dialog with the text typed so far.
func TestF1InADialogOpensItsPageAndKeepsTheDialog(t *testing.T) {
	t.Parallel()
	a := helpTestApp(t, 160, 40)
	a.showWindowDialog()
	helpType(a, "15m")
	if !strings.Contains(helpScreen(a)[39], "Help") {
		t.Fatalf("the dialog's key bar has no F1 Help: %q", helpScreen(a)[39])
	}
	helpPress(a, tea.KeyF1)
	if a.modalManager.TopName() != helpModalName {
		t.Fatalf("top dialog = %q, want the help", a.modalManager.TopName())
	}
	if a.helpViewer.Page() != help.InvestigationWindow {
		t.Fatalf("F1 opened %q, want %q", a.helpViewer.Page(), help.InvestigationWindow)
	}
	if got := helpScreen(a); !strings.Contains(strings.Join(got, "\n"), "Investigation window") {
		t.Fatal("the help page does not show")
	}
	helpPress(a, tea.KeyEscape)
	if a.modalManager.TopName() != windowModalKey {
		t.Fatalf("after Esc the top dialog is %q, want the investigation window", a.modalManager.TopName())
	}
	input, ok := a.modalManager.modals[windowModalKey].content.(*TextInputModal)
	if !ok {
		t.Fatalf("content is %T", a.modalManager.modals[windowModalKey].content)
	}
	if got := input.Value(); !strings.Contains(got, "15m") {
		t.Fatalf("the dialog lost the typed text: %q", got)
	}
}

// Esc 1, the F1 fallback, works in a dialog too.
func TestEscOneInADialogOpensHelp(t *testing.T) {
	t.Parallel()
	mm := NewModalManager(nil)
	modal := NewModal("Filter", NewHelpViewer(help.Index))
	modal.SetCloseOnSingleEsc(false) // as a dialog with a text field
	mm.Register(rowFilterModal, modal)
	var opened help.ID
	mm.SetHelp(modalHelpPage, func(id help.ID) tea.Cmd { opened = id; return nil })
	mm.Show(rowFilterModal)
	mm.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	mm.Update(tea.KeyPressMsg(tea.Key{Code: '1', Text: "1"}))
	if opened != help.RowFilter {
		t.Fatalf("Esc 1 opened %q, want %q", opened, help.RowFilter)
	}
}

func TestF1InAPanelOpensTheModePage(t *testing.T) {
	t.Parallel()
	for _, mode := range []PanelViewMode{PanelModeList, PanelModeDescribe, PanelModeManifest, PanelModeLogs, PanelModeInfo} {
		t.Run(modeLabel(mode), func(t *testing.T) {
			t.Parallel()
			a, _ := pinApp(t, mode)
			a.activePanel = 1
			if mode == PanelModeList {
				a.activePanel = 0
			}
			helpPress(a, tea.KeyF1)
			if a.modalManager.TopName() != helpModalName {
				t.Fatalf("F1 opened %q", a.modalManager.TopName())
			}
			if got, want := a.helpViewer.Page(), modeHelpPages[mode]; got != want {
				t.Fatalf("F1 in a %s panel opened %q, want %q", modeLabel(mode), got, want)
			}
		})
	}
}

func TestF1InAFrozenPanel(t *testing.T) {
	t.Parallel()
	a, _ := pinApp(t, PanelModeInfo)
	a.activePanel = 1
	a.toggleFreeze()
	if a.rightPanel.freeze == nil {
		t.Skip("the Info panel did not freeze in this setup")
	}
	helpPress(a, tea.KeyF1)
	if a.helpViewer.Page() != help.ModeInfo || a.modalManager.TopName() != helpModalName {
		t.Fatalf("F1 in a frozen panel: top %q, page %q", a.modalManager.TopName(), a.helpViewer.Page())
	}
}
