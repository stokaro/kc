package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	gansi "charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/help"
	uistyles "github.com/stokaro/kc/internal/ui/styles"
)

// Like mc's help window, the text wraps at a fixed width however wide the
// terminal is, and the window around it is centered.
const (
	helpTextWidth = 76
	helpPadX      = 1 // blank columns between the frame and the text
	// helpWindowWidth is the widest help window: text, padding and frame.
	helpWindowWidth = helpTextWidth + 2*helpPadX + 2
)

// Link markers: a page's links reach glamour as their text between these
// two private-use runes, so the rendered lines say where each link went.
const (
	helpLinkOpen  = ""
	helpLinkClose = ""
)

// mc's help colors: a link is black on cyan, the selected link white on
// blue.
var (
	helpLinkStyle    = lipgloss.NewStyle().Background(lipgloss.Cyan).Foreground(lipgloss.Black)
	helpLinkSelStyle = lipgloss.NewStyle().Background(lipgloss.Blue).Foreground(lipgloss.BrightWhite)
)

// helpSpan is the part of a link on one rendered line: columns
// [start, end) of the text, padding not counted.
type helpSpan struct{ line, start, end int }

// helpLink is a link to another page; a link that wraps has a span per line.
type helpLink struct {
	target help.ID
	spans  []helpSpan
}

// helpPlace is a page in the Prev history, with where the reader was.
type helpPlace struct {
	page   help.ID
	scroll int
	focus  int
}

// HelpViewer shows the help pages in a window. Tab moves between links,
// Enter follows one, F3 goes back, F2 opens the index and F1 the page
// about the help itself.
type HelpViewer struct {
	page   help.ID
	back   []helpPlace
	md     string
	lines  []string
	links  []helpLink
	focus  int // index into links, -1 for none
	width  int // the content area inside the frame
	height int
	scroll int
	wrap   int // the text width lines were rendered for
	// close hides the window; F10 calls it (Esc is the modal's).
	close func() tea.Cmd
}

// NewHelpViewer returns a viewer showing page.
func NewHelpViewer(page help.ID) *HelpViewer {
	v := &HelpViewer{focus: -1}
	v.show(page)
	return v
}

// SetClose sets what F10 calls to close the window.
func (v *HelpViewer) SetClose(f func() tea.Cmd) { v.close = f }

// Init implements tea.Model.
func (v *HelpViewer) Init() tea.Cmd { return nil }

// Page returns the page shown.
func (v *HelpViewer) Page() help.ID { return v.page }

// Open shows page; F3 returns to the page shown before.
func (v *HelpViewer) Open(page help.ID) {
	if page == v.page {
		return
	}
	if v.page != "" {
		v.back = append(v.back, helpPlace{page: v.page, scroll: v.scroll, focus: v.focus})
	}
	v.show(page)
}

// Reset shows page with an empty history, as on a fresh F1.
func (v *HelpViewer) Reset(page help.ID) {
	v.back = nil
	v.page = ""
	v.show(page)
}

func (v *HelpViewer) show(page help.ID) {
	topic, ok := help.Lookup(page)
	if !ok {
		topic, _ = help.Lookup(help.Index)
		page = help.Index
	}
	v.page = page
	v.md = topic.Markdown
	v.scroll = 0
	v.wrap = 0
	v.render()
	v.focus = -1
	if len(v.links) > 0 {
		v.focus = 0
	}
}

// Back returns to the previous page; it reports false when there is none.
func (v *HelpViewer) Back() bool {
	if len(v.back) == 0 {
		return false
	}
	prev := v.back[len(v.back)-1]
	v.back = v.back[:len(v.back)-1]
	v.show(prev.page)
	v.scroll = prev.scroll
	v.focus = min(prev.focus, len(v.links)-1)
	v.clampScroll()
	return true
}

// SetDimensions applies the content area inside the frame.
func (v *HelpViewer) SetDimensions(w, h int) {
	v.width = max(0, w)
	v.height = max(0, h)
	v.render()
	v.clampScroll()
}

// TextWidth returns the width the text wraps at.
func (v *HelpViewer) TextWidth() int {
	if v.width <= 0 {
		return helpTextWidth
	}
	return max(1, min(helpTextWidth, v.width-2*helpPadX))
}

func (v *HelpViewer) render() {
	wrap := v.TextWidth()
	if wrap == v.wrap && v.lines != nil {
		return
	}
	v.wrap = wrap
	v.lines, v.links = renderHelpPage(v.md, wrap)
}

// helpGlamourStyle is the light glamour style on the dialog colors, with
// no margin: the viewer pads the text itself.
func helpGlamourStyle() gansi.StyleConfig {
	style := styles.LightStyleConfig
	bg := uistyles.ColorModalBg
	fg := uistyles.ColorModalFg
	style.Document.StylePrimitive.BackgroundColor = new(bg)
	style.Document.StylePrimitive.Color = new(fg)
	style.Document.Margin = new(uint(0))
	style.Paragraph.StylePrimitive.BackgroundColor = new(bg)
	style.Paragraph.StylePrimitive.Color = new(fg)
	style.List.StyleBlock.StylePrimitive.BackgroundColor = new(bg)
	style.List.StyleBlock.StylePrimitive.Color = new(fg)
	style.BlockQuote.StylePrimitive.BackgroundColor = new(bg)
	style.BlockQuote.StylePrimitive.Color = new(fg)
	// Section headings in bold without the "##" marks, as mc's help.
	for _, h := range []*gansi.StyleBlock{&style.H2, &style.H3, &style.H4, &style.H5, &style.H6} {
		h.StylePrimitive.Prefix = ""
		h.StylePrimitive.Bold = new(true)
		h.StylePrimitive.Color = new(uistyles.ColorModalTitle)
	}
	return style
}

// renderHelpPage renders md at width and returns its lines and links.
func renderHelpPage(md string, width int) ([]string, []helpLink) {
	src := markHelpLinks(strings.TrimSpace(md))
	out := src
	renderer, err := glamour.NewTermRenderer(glamour.WithStyles(helpGlamourStyle()), glamour.WithWordWrap(width))
	if err == nil {
		if r, rErr := renderer.Render(src); rErr == nil {
			out = r
		}
	}
	raw := strings.Split(strings.Trim(out, "\n"), "\n")
	return collectHelpLinks(raw, md)
}

// markHelpLinks replaces each link to a page by its text between the link
// markers, and any other link by its text. Code blocks stay as they are.
func markHelpLinks(md string) string {
	lines := strings.Split(md, "\n")
	fenced := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		lines[i] = help.ReplaceLinks(line, func(text, dest string) string {
			if _, ok := help.TargetOf(dest); ok {
				return helpLinkOpen + text + helpLinkClose
			}
			return text
		})
	}
	return strings.Join(lines, "\n")
}

// collectHelpLinks removes the link markers from the rendered lines and
// records where each link sits. md gives the targets, in the same order.
func collectHelpLinks(raw []string, md string) ([]string, []helpLink) {
	var targets []help.ID
	for _, l := range help.Links(md) {
		targets = append(targets, l.Target)
	}
	lines := make([]string, len(raw))
	var links []helpLink
	open := false
	for n, line := range raw {
		plain := ansi.Strip(line)
		col := 0
		spanStart := -1
		if open {
			spanStart = len(plain) - len(strings.TrimLeft(plain, " ")) // continue after the indent
			spanStart = ansi.StringWidth(plain[:spanStart])
		}
		rest := plain
		for rest != "" {
			i := strings.IndexAny(rest, helpLinkOpen+helpLinkClose)
			if i < 0 {
				col += ansi.StringWidth(rest)
				break
			}
			col += ansi.StringWidth(rest[:i])
			marker := rest[i : i+len(helpLinkOpen)]
			rest = rest[i+len(helpLinkOpen):]
			if marker == helpLinkOpen {
				open = true
				spanStart = col
				var target help.ID
				if len(links) < len(targets) {
					target = targets[len(links)]
				}
				links = append(links, helpLink{target: target})
				continue
			}
			if open && len(links) > 0 && spanStart >= 0 {
				last := &links[len(links)-1]
				last.spans = append(last.spans, helpSpan{line: n, start: spanStart, end: col})
			}
			open = false
			spanStart = -1
		}
		if open && len(links) > 0 && spanStart >= 0 && col > spanStart {
			last := &links[len(links)-1]
			last.spans = append(last.spans, helpSpan{line: n, start: spanStart, end: col})
		}
		line = strings.ReplaceAll(line, helpLinkOpen, "")
		lines[n] = strings.ReplaceAll(line, helpLinkClose, "")
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines, links
}

func (v *HelpViewer) clampScroll() {
	maxScroll := max(len(v.lines)-max(1, v.height), 0)
	v.scroll = clampInt(v.scroll, 0, maxScroll)
}

// Update handles the keys and the mouse.
func (v *HelpViewer) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.KeyMsg:
		return v, v.handleKey(m.String())
	case tea.MouseWheelMsg:
		switch m.Mouse().Button {
		case tea.MouseWheelUp:
			v.scrollBy(-1)
		case tea.MouseWheelDown:
			v.scrollBy(1)
		}
	case tea.MouseReleaseMsg:
		if m.Mouse().Button == tea.MouseLeft {
			mouse := m.Mouse()
			if i := v.linkAt(mouse.X-helpPadX, mouse.Y+v.scroll); i >= 0 {
				v.focus = i
				v.follow()
			}
		}
	}
	return v, nil
}

func (v *HelpViewer) handleKey(key string) tea.Cmd {
	switch key {
	case "up", "k":
		v.scrollBy(-1)
	case "down", "j":
		v.scrollBy(1)
	case "pgup", "ctrl+b", "b":
		v.scrollBy(-v.pageSize())
	case "pgdown", "ctrl+f", "space", " ":
		v.scrollBy(v.pageSize())
	case "home", "g":
		v.scroll = 0
	case "end", "G":
		v.scroll = len(v.lines)
		v.clampScroll()
	case "tab":
		v.moveFocus(1)
	case "shift+tab":
		v.moveFocus(-1)
	case "enter", "right", "l":
		v.follow()
	case "f3", "backspace", "left", "h":
		v.Back()
	case "f2":
		v.Open(help.Index)
	case "f1":
		v.Open(help.Help)
	case "f10", "q":
		if v.close != nil {
			return v.close()
		}
	}
	return nil
}

func (v *HelpViewer) follow() {
	if v.focus < 0 || v.focus >= len(v.links) || !v.linkVisible(v.focus) {
		return
	}
	if target := v.links[v.focus].target; target != "" {
		v.Open(target)
	}
}

// moveFocus selects the next or previous link and scrolls to it.
func (v *HelpViewer) moveFocus(delta int) {
	if len(v.links) == 0 {
		return
	}
	switch {
	case v.focus < 0 || !v.linkVisible(v.focus):
		// Start from the first link on the screen, as mc does.
		v.focus = v.firstVisibleLink(delta)
	default:
		v.focus = (v.focus + delta + len(v.links)) % len(v.links)
	}
	first := v.links[v.focus].spans
	if len(first) == 0 {
		return
	}
	line := first[0].line
	if line < v.scroll {
		v.scroll = line
	} else if h := max(1, v.height); line >= v.scroll+h {
		v.scroll = line - h + 1
	}
	v.clampScroll()
}

func (v *HelpViewer) firstVisibleLink(delta int) int {
	idx := -1
	for i := range v.links {
		if v.linkVisible(i) {
			idx = i
			if delta > 0 {
				break
			}
		}
	}
	if idx >= 0 {
		return idx
	}
	// No link on the screen: the next one below it, else the first.
	for i, l := range v.links {
		if len(l.spans) > 0 && l.spans[0].line >= v.scroll {
			return i
		}
	}
	return 0
}

func (v *HelpViewer) linkVisible(i int) bool {
	h := v.height
	if h <= 0 {
		return true
	}
	for _, s := range v.links[i].spans {
		if s.line >= v.scroll && s.line < v.scroll+h {
			return true
		}
	}
	return false
}

// linkAt returns the link at text column x of rendered line y, or -1.
func (v *HelpViewer) linkAt(x, y int) int {
	for i, l := range v.links {
		for _, s := range l.spans {
			if s.line == y && x >= s.start && x < s.end {
				return i
			}
		}
	}
	return -1
}

func (v *HelpViewer) pageSize() int { return max(1, v.height-1) }

func (v *HelpViewer) scrollBy(delta int) {
	v.scroll += delta
	v.clampScroll()
}

// View renders the visible lines with the links in their colors.
func (v *HelpViewer) View() tea.View {
	if v.width <= 0 || v.height <= 0 {
		return tea.NewView("")
	}
	wrap := v.TextWidth()
	pad := strings.Repeat(" ", helpPadX)
	right := max(0, v.width-wrap-helpPadX)
	buf := make([]string, v.height)
	for i := range v.height {
		n := v.scroll + i
		line := ""
		if n < len(v.lines) {
			line = v.styleLinks(n, v.lines[n])
		}
		buf[i] = wrapWithModalColors(pad + padTo(line, wrap) + strings.Repeat(" ", right))
	}
	return tea.NewView(strings.Join(buf, "\n"))
}

// styleLinks paints the links on line n.
func (v *HelpViewer) styleLinks(n int, line string) string {
	var spans []helpSpan
	var selected []bool
	for i, l := range v.links {
		for _, s := range l.spans {
			if s.line == n {
				spans = append(spans, s)
				selected = append(selected, i == v.focus)
			}
		}
	}
	if len(spans) == 0 {
		return line
	}
	var b strings.Builder
	col := 0
	for i, s := range spans {
		if s.start < col {
			continue
		}
		b.WriteString(ansi.Cut(line, col, s.start))
		style := helpLinkStyle
		if selected[i] {
			style = helpLinkSelStyle
		}
		b.WriteString(style.Render(ansi.Strip(ansi.Cut(line, s.start, s.end))))
		col = s.end
	}
	b.WriteString(ansi.Cut(line, col, ansi.StringWidth(line)))
	return b.String()
}

// padTo cuts or pads an ANSI line to width columns.
func padTo(line string, width int) string {
	w := ansi.StringWidth(line)
	if w > width {
		return ansi.Cut(line, 0, width)
	}
	return line + strings.Repeat(" ", width-w)
}

// FooterHints is the help window's own key bar, as in mc.
func (v *HelpViewer) FooterHints() []FooterHint {
	return []FooterHint{
		{Key: "F1", Label: "Help", Enabled: v.page != help.Help},
		{Key: "F2", Label: "Index", Enabled: v.page != help.Index},
		{Key: "F3", Label: "Prev", Enabled: len(v.back) > 0},
		{Key: "F10", Label: "Quit", Enabled: true},
	}
}

func wrapWithModalColors(s string) string {
	bgSeq := ansiBackgroundSeq(uistyles.ColorModalBg)
	fgSeq := ansiForegroundSeq(uistyles.ColorModalFg)
	prefix := bgSeq + fgSeq
	var b strings.Builder
	b.WriteString(prefix)
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				j++
				seq := s[i:j]
				b.WriteString(seq)
				if seq == "\x1b[0m" || seq == "\x1b[m" {
					b.WriteString(prefix)
				}
				i = j
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

func ansiBackgroundSeq(color string) string {
	if strings.HasPrefix(color, "#") {
		if rgb, ok := parseHexColor(color); ok {
			return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", rgb[0], rgb[1], rgb[2])
		}
	}
	return fmt.Sprintf("\x1b[48;5;%sm", color)
}

func ansiForegroundSeq(color string) string {
	if strings.HasPrefix(color, "#") {
		if rgb, ok := parseHexColor(color); ok {
			return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", rgb[0], rgb[1], rgb[2])
		}
	}
	return fmt.Sprintf("\x1b[38;5;%sm", color)
}

func parseHexColor(hex string) ([3]int, bool) {
	var rgb [3]int
	if len(hex) != 7 {
		return rgb, false
	}
	for i := range 3 {
		val, err := strconv.ParseInt(hex[1+i*2:3+i*2], 16, 0)
		if err != nil {
			return rgb, false
		}
		rgb[i] = int(val)
	}
	return rgb, true
}
