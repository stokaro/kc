package viewer

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	chroma "github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/overlay"
	uistyles "github.com/stokaro/kc/internal/ui/styles"
)

const (
	ansiDisableWrap = "\x1b[?7l"
	ansiEnableWrap  = "\x1b[?7h"
)

var searchInputStyle = lipgloss.NewStyle().
	Background(lipgloss.Color("#d7d7d7")).
	Foreground(lipgloss.Black)

// Metadata describes the highlighted content.
type Metadata struct {
	Title    string
	Language string
	MimeType string
	Filename string
}

// Frame describes the drawing area granted to the viewer.
type Frame struct {
	Width   int
	Height  int
	Focused bool
}

// Widget renders highlighted text with scrolling support.
type Widget struct {
	metadata Metadata

	width  int
	height int

	raw      string
	rawLines []string
	lines    []string
	// styled holds the shown form of rawLines in plain mode once
	// AppendStyledLines was used; nil otherwise.
	styled       []string
	theme        string
	plain        bool
	wrapMode     bool
	maxLineWidth int

	offset  int
	hOffset int

	onEdit    func() tea.Cmd
	onOptions func() tea.Cmd
	onClose   func() tea.Cmd

	searchDialog  *searchDialog
	searchQuery   string
	searchOpts    SearchOptions
	searchRe      *regexp.Regexp
	searchMatches []matchLocation
	// searchCapped is set when a search found more than maxSearchMatches;
	// later matches are not recorded.
	searchCapped     bool
	searchMatchIndex map[int][]int
	activeMatch      int

	sel selection
}

type matchLocation struct {
	line  int
	start int
	end   int
}

type displayMatch struct {
	start    int
	end      int
	active   bool
	selected bool // mouse selection, drawn on cyan
}

// New constructs a viewer widget with an optional theme name.
func New(theme string) *Widget {
	if theme == "" {
		theme = "dracula"
	}
	return &Widget{
		theme:            theme,
		searchMatchIndex: make(map[int][]int),
		activeMatch:      -1,
	}
}

// SetContent replaces the text body and metadata, re-running syntax highlighting.
func (w *Widget) SetContent(text string, meta Metadata) {
	if meta.MimeType == BinaryMIME {
		text = BinaryPreview([]byte(text))
		meta.Language = "plaintext"
	}
	w.raw = text
	w.metadata = meta
	w.rawLines = strings.Split(text, "\n")
	w.styled = nil
	w.updateMaxLineWidth()
	if w.plain {
		w.lines = append(make([]string, 0), w.rawLines...)
	} else {
		w.highlight()
	}
	w.offset = 0
	w.hOffset = 0
	w.sel = selection{}
	w.clearSearchState()
}

// UpdateContent replaces the text like SetContent, for a newer version of the
// same document: the scroll position and a committed search stay. A mouse
// selection is dropped because its positions may no longer match the text.
func (w *Widget) UpdateContent(text string, meta Metadata) {
	offset, hOffset := w.offset, w.hOffset
	w.raw = text
	w.metadata = meta
	w.rawLines = strings.Split(text, "\n")
	w.updateMaxLineWidth()
	if w.plain {
		w.lines = append(make([]string, 0), w.rawLines...)
	} else {
		w.highlight()
	}
	w.sel = selection{}
	if w.searchRe != nil {
		w.updateSearchMatches()
	}
	w.offset = offset
	w.scrollBy(0)
	w.hOffset = hOffset
	w.clampHorizontalOffset()
}

// SetPlainMode toggles syntax highlighting; when plain mode is enabled, content
// updates avoid chroma re-rendering so we can append lines cheaply (logs view).
func (w *Widget) SetPlainMode(on bool) {
	w.plain = on
}

// SetWrapMode toggles whether lines wrap within the viewport or scroll horizontally.
func (w *Widget) SetWrapMode(on bool) {
	if w.wrapMode == on {
		return
	}
	w.wrapMode = on
	if on {
		w.hOffset = 0
	}
	w.clampHorizontalOffset()
}

// WrapMode reports whether wrapping is enabled.
func (w *Widget) WrapMode() bool { return w.wrapMode }

// SetTheme updates the chroma theme name and re-highlights the content.
func (w *Widget) SetTheme(theme string) {
	if theme == "" || theme == w.theme {
		return
	}
	w.theme = theme
	w.highlight()
}

// Theme returns the active chroma theme.
func (w *Widget) Theme() string { return w.theme }

// RawLines returns a copy of the text lines, without styling.
func (w *Widget) RawLines() []string { return append([]string(nil), w.rawLines...) }

// AppendLines appends raw text lines (used by logs viewer). In highlighted mode
// this forces a full re-render; in plain mode we simply extend the backing
// slices so appending remains cheap.
func (w *Widget) AppendLines(lines []string) {
	if len(lines) == 0 {
		return
	}
	if len(w.rawLines) == 0 {
		w.rawLines = make([]string, 0, len(lines))
	}
	start := len(w.rawLines)
	w.rawLines = append(w.rawLines, lines...)
	// Lines only grow the widest line; measuring the new ones keeps an
	// append independent of what is already shown.
	for _, line := range lines {
		w.maxLineWidth = max(w.maxLineWidth, runeWidth(line))
	}
	w.clampHorizontalOffset()
	if w.plain {
		// Plain mode never highlights, so it does not keep the joined
		// text: rebuilding it on every append would copy the whole log.
		w.raw = ""
	} else {
		appendText := strings.Join(lines, "\n")
		if w.raw == "" {
			w.raw = appendText
		} else {
			if !strings.HasSuffix(w.raw, "\n") {
				w.raw += "\n"
			}
			w.raw += appendText
		}
	}
	if w.plain {
		if len(w.lines) == 0 {
			w.lines = make([]string, 0, len(lines))
		}
		w.lines = append(w.lines, lines...)
		if w.styled != nil {
			w.styled = append(w.styled, lines...)
		}
	} else {
		w.highlight()
	}
	w.extendSearchMatches(start, lines)
}

// SetLines replaces the body with lines and keeps the metadata, the search
// query and the scroll position (clamped to the new length). The logs viewer
// uses it to show the same log in another format or filter without losing
// the search.
func (w *Widget) SetLines(lines []string) {
	w.rawLines = append([]string(nil), lines...)
	w.styled = nil
	w.raw = ""
	w.updateMaxLineWidth()
	if w.plain {
		w.lines = append([]string(nil), lines...)
	} else {
		w.raw = strings.Join(lines, "\n")
		w.highlight()
	}
	w.offset = min(w.offset, max(0, len(w.lines)-w.page()))
	w.sel = selection{}
	w.searchMatchIndex = make(map[int][]int)
	w.searchMatches, w.searchCapped = w.searchMatches[:0], false
	w.activeMatch = -1
	if w.searchRe == nil {
		return
	}
	for idx, line := range w.rawLines {
		w.appendMatchesForLine(idx, line, w.searchRe)
	}
	if len(w.searchMatches) > 0 {
		w.activeMatch = w.firstMatchFromView()
	}
}

// AppendStyledLines appends lines whose shown form differs from their text
// only by ANSI styling, such as the colored prefix of a merged log. Search,
// selection and copy work on text; plain mode shows styled. Outside plain
// mode it is AppendLines(text).
func (w *Widget) AppendStyledLines(text, styled []string) {
	if !w.plain || len(text) != len(styled) {
		w.AppendLines(text)
		return
	}
	if w.styled == nil {
		w.styled = append(make([]string, 0, len(w.rawLines)+len(text)), w.rawLines...)
	}
	n := len(w.lines)
	w.AppendLines(text)
	// AppendLines extended lines and styled with text; show styled instead.
	copy(w.lines[n:], styled)
	copy(w.styled[len(w.styled)-len(styled):], styled)
}

// SetCallbacks configures optional edit/theme/close handlers.
func (w *Widget) SetCallbacks(onEdit, onTheme, onClose func() tea.Cmd) {
	w.onEdit = onEdit
	w.onOptions = onTheme
	w.onClose = onClose
}

// Resize updates the viewport size.
func (w *Widget) Resize(width, height int) {
	w.width = width
	w.height = height
	w.clampHorizontalOffset()
}

// Update processes key/mouse input. Returns a command when an action is triggered.
func (w *Widget) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		w.Resize(max(1, m.Width), max(1, m.Height))
		return nil, true
	case tea.PasteMsg:
		if w.searchDialog != nil && w.searchDialog.focus == searchFocusInput {
			var cmd tea.Cmd
			w.searchDialog.input, cmd = w.searchDialog.input.Update(m)
			return cmd, true
		}
		return nil, false
	case tea.KeyMsg:
		if w.searchDialog != nil {
			return w.handleSearchDialog(m), true
		}
		if cmd, handled := w.handleKey(m.String()); handled {
			return cmd, true
		}
	case tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg:
		if w.searchDialog != nil {
			return nil, false
		}
		return w.handleMouseSelection(m.(tea.MouseMsg))
	case tea.MouseWheelMsg:
		mouse := tea.Mouse(m)
		switch mouse.Button {
		case tea.MouseWheelUp:
			w.scrollBy(-1)
			return nil, true
		case tea.MouseWheelDown:
			w.scrollBy(1)
			return nil, true
		}
	}
	return nil, false
}

// View renders the highlighted text inside the frame.
func (w *Widget) View(frame Frame) string {
	w.Resize(frame.Width, frame.Height)
	lines := w.visibleLines()
	if len(lines) == 0 {
		lines = []string{""}
	}
	body := strings.Join(lines, "\n")
	contentW := max(1, frame.Width)
	contentH := max(1, frame.Height)
	style := uistyles.PanelContentStyle.Height(contentH).MaxHeight(contentH)
	if w.wrapMode {
		style = style.Width(contentW).MaxWidth(contentW)
	}
	if frame.Focused {
		style = style.Bold(true)
	}
	rendered := style.Render(body)
	if w.searchDialog != nil {
		dialog := w.searchDialog.view(contentW)
		dw, dh := lipgloss.Size(dialog)
		rendered = overlay.Composite(dialog, rendered, overlay.Left, overlay.Top, max(0, (contentW-dw)/2), max(0, (contentH-dh)/2))
	}
	if !w.wrapMode {
		rendered = ansiDisableWrap + rendered + ansiEnableWrap
	}
	return rendered
}

// Footer returns viewer status (title/position) rendered into a footer row.
func (w *Widget) Footer(width int) string {
	info := w.metadata.Title
	if info == "" {
		info = w.metadata.Filename
	}
	if info == "" {
		info = "Manifest"
	}
	status := fmt.Sprintf("%s • %d/%d", info, min(len(w.lines), w.offset+w.page()), len(w.lines))
	return lipgloss.NewStyle().Width(max(1, width)).Render(status)
}

// Metadata returns the current content metadata.
func (w *Widget) Metadata() Metadata { return w.metadata }

// Position returns the one-based index of the last visible line and total lines.
func (w *Widget) Position() (current, total int) {
	total = len(w.lines)
	if total == 0 {
		return 0, 0
	}
	end := min(w.offset+w.page(), total)
	if end <= 0 {
		end = 1
	}
	return end, total
}

// HasSearchMatches reports whether a committed search has any matches.
func (w *Widget) HasSearchMatches() bool {
	return len(w.searchMatches) > 0
}

// AtEnd reports whether the viewport currently shows the latest lines.
func (w *Widget) AtEnd() bool {
	if len(w.lines) <= w.page() {
		return true
	}
	return w.offset+w.page() >= len(w.lines)
}

// ScrollToEnd jumps the viewport to the most recent lines.
func (w *Widget) ScrollToEnd() {
	page := w.page()
	if page <= 0 {
		page = 1
	}
	if len(w.lines) <= page {
		w.offset = 0
		return
	}
	w.offset = max(len(w.lines)-page, 0)
}

// ScrollIndicators reports whether additional content exists above or below.
func (w *Widget) ScrollIndicators() (above, below bool) {
	if len(w.lines) == 0 {
		return false, false
	}
	return w.offset > 0, w.offset+w.page() < len(w.lines)
}

// SearchMode reports whether the search dialog is open.
func (w *Widget) SearchMode() bool {
	return w.searchDialog != nil
}

// HandleEscape consumes Escape presses for active inline modes (search).
func (w *Widget) HandleEscape() (tea.Cmd, bool) {
	if w.searchDialog != nil {
		w.cancelSearch()
		return nil, true
	}
	return nil, false
}

// SearchSummary is the applied search and the match in focus, for a footer
// that shows it next to other state, for example "/timeout (2/7)". It is
// empty while the search dialog is open or no search is applied.
func (w *Widget) SearchSummary() string {
	if w.searchDialog != nil {
		return ""
	}
	return w.searchSummary()
}

// FooterStatusText describes the active search prompt or summary for footer display.
func (w *Widget) FooterStatusText(width int) string {
	if width <= 0 {
		return ""
	}
	if w.searchDialog != nil {
		return ""
	}
	summary := w.searchSummary()
	if summary == "" {
		return ""
	}
	trimmed := trimToWidth(summary, width)
	return searchInputStyle.Width(width).Render(trimmed)
}

// FooterCursor returns nil: the search string is edited in the search dialog,
// which draws its own cursor.
func (w *Widget) FooterCursor(width int) *tea.Cursor {
	return nil
}

func (w *Widget) searchSummary() string {
	trimmed := strings.TrimSpace(w.searchQuery)
	if trimmed == "" {
		return ""
	}
	if len(w.searchMatches) == 0 {
		return fmt.Sprintf("/%s (no match)", trimmed)
	}
	idx := w.activeMatch + 1
	if idx <= 0 {
		idx = 1
	}
	if w.searchCapped {
		return fmt.Sprintf("/%s (%d/%d+)", trimmed, idx, len(w.searchMatches))
	}
	return fmt.Sprintf("/%s (%d/%d)", trimmed, idx, len(w.searchMatches))
}

func (w *Widget) highlight() {
	text := w.text()
	if text == "" {
		w.lines = []string{""}
		return
	}
	if w.plain && w.styled != nil {
		w.lines = append([]string(nil), w.styled...)
		return
	}
	ensureCustomStyles()
	lexer := w.selectLexer(text)
	iterator, err := lexer.Tokenise(nil, text)
	if err != nil {
		w.lines = strings.Split(text, "\n")
		return
	}
	var rendered string
	if w.theme == "turbo-pascal" {
		rendered = highlightFallback(iterator)
	} else {
		style := styles.Get(w.theme)
		if style == nil {
			style = styles.Get("monokai")
		}
		rendered = renderTokensWithStyle(style, iterator)
	}
	rendered = strings.TrimRight(rendered, "\n")
	w.lines = strings.Split(rendered, "\n")
	if len(w.lines) == 0 {
		w.lines = []string{""}
	}
}

// text returns the whole body. Plain mode does not keep it joined (see
// AppendLines) and joins the lines when it is needed.
func (w *Widget) text() string {
	if w.plain && w.raw == "" {
		return strings.Join(w.rawLines, "\n")
	}
	return w.raw
}

func (w *Widget) selectLexer(text string) chroma.Lexer {
	if w.metadata.Language != "" {
		if lex := lexers.Get(w.metadata.Language); lex != nil {
			return lex
		}
	}
	if w.metadata.MimeType != "" {
		if lex := lexers.MatchMimeType(w.metadata.MimeType); lex != nil {
			return lex
		}
	}
	if w.metadata.Filename != "" {
		if lex := lexers.Match(w.metadata.Filename); lex != nil {
			return lex
		}
	}
	if analyser := lexers.Analyse(text); analyser != nil {
		return analyser
	}
	return lexers.Fallback
}

func (w *Widget) handleKey(key string) (tea.Cmd, bool) {
	switch key {
	case "up", "k":
		w.scrollBy(-1)
	case "down", "j":
		w.scrollBy(1)
	case "left", "h":
		w.adjustHorizontal(-1)
	case "right", "l":
		w.adjustHorizontal(1)
	case "pgup", "ctrl+b":
		w.scrollBy(-w.page())
	case "pgdown":
		w.scrollBy(w.page())
	case "ctrl+f", "f7", "/":
		return w.beginSearch(), true
	case "home", "g":
		w.offset = 0
	case "end", "G":
		w.offset = max(0, len(w.lines)-w.page())
	case "ctrl+a":
		w.hOffset = 0
		w.clampHorizontalOffset()
	case "ctrl+e":
		w.hOffset = w.maxHorizontalOffset()
	case "f2":
		return runCallback(w.onOptions)
	case "f3", "f10":
		// F3 toggles the viewer like in Midnight Commander: it closes it.
		return runCallback(w.onClose)
	case "shift+f7", "n":
		if w.nextMatch() {
			return nil, true
		}
	case "N":
		if w.prevMatch() {
			return nil, true
		}
	case "f4":
		return runCallback(w.onEdit)
	default:
		return nil, false
	}
	return nil, true
}

// runCallback runs an optional key callback. The key is handled only when
// the callback is set.
func runCallback(fn func() tea.Cmd) (tea.Cmd, bool) {
	if fn == nil {
		return nil, false
	}
	return fn(), true
}

func (w *Widget) handleSearchDialog(key tea.KeyMsg) tea.Cmd {
	result, cmd := w.searchDialog.update(key)
	switch result {
	case searchDialogAccepted:
		w.commitSearch()
	case searchDialogCanceled:
		w.cancelSearch()
	}
	return cmd
}

func (w *Widget) scrollBy(delta int) {
	w.offset += delta
	if w.offset < 0 {
		w.offset = 0
	}
	maxScroll := max(0, len(w.lines)-w.page())
	if w.offset > maxScroll {
		w.offset = maxScroll
	}
}

func (w *Widget) beginSearch() tea.Cmd {
	w.searchDialog = newSearchDialog(w.searchQuery, w.searchOpts)
	return w.searchDialog.focusCmd()
}

// commitSearch applies the dialog's query and options. An invalid regular
// expression keeps the dialog open and shows the error in it.
func (w *Widget) commitSearch() {
	d := w.searchDialog
	query := d.query()
	re, err := compileSearch(query, d.opts)
	if err != nil {
		d.err = err.Error()
		return
	}
	w.searchDialog = nil
	w.searchQuery = query
	w.searchOpts = d.opts
	w.searchRe = re
	w.updateSearchMatches()
}

func (w *Widget) cancelSearch() {
	w.searchDialog = nil
}

// nextMatch moves to the next match in the search direction.
func (w *Widget) nextMatch() bool {
	if w.searchOpts.Backwards {
		return w.previousMatch()
	}
	return w.advanceMatch()
}

// prevMatch moves to the next match against the search direction.
func (w *Widget) prevMatch() bool {
	if w.searchOpts.Backwards {
		return w.advanceMatch()
	}
	return w.previousMatch()
}

func (w *Widget) clearSearchState() {
	w.searchDialog = nil
	w.searchQuery = ""
	w.searchRe = nil
	w.searchMatches, w.searchCapped = nil, false
	w.activeMatch = -1
	if w.searchMatchIndex == nil {
		w.searchMatchIndex = make(map[int][]int)
	}
	for k := range w.searchMatchIndex {
		delete(w.searchMatchIndex, k)
	}
}

func (w *Widget) adjustHorizontal(delta int) {
	if delta == 0 || w.wrapMode {
		w.hOffset = 0
		return
	}
	w.hOffset += delta
	w.clampHorizontalOffset()
}

func (w *Widget) clampHorizontalOffset() {
	if w.wrapMode {
		w.hOffset = 0
		return
	}
	if w.hOffset < 0 {
		w.hOffset = 0
	}
	maxOff := w.maxHorizontalOffset()
	if w.hOffset > maxOff {
		w.hOffset = maxOff
	}
}

func (w *Widget) maxHorizontalOffset() int {
	if w.wrapMode || w.width <= 0 {
		return 0
	}
	effective := w.viewportWidth()
	if w.maxLineWidth <= effective {
		return 0
	}
	return w.maxLineWidth - effective
}

func (w *Widget) updateMaxLineWidth() {
	maxWidth := 0
	for _, line := range w.rawLines {
		if lw := runeWidth(line); lw > maxWidth {
			maxWidth = lw
		}
	}
	w.maxLineWidth = maxWidth
	w.clampHorizontalOffset()
}

func (w *Widget) viewportWidth() int {
	width := w.width
	if width <= 0 {
		return 1
	}
	if !w.wrapMode && width > 2 {
		return width - 2
	}
	return width
}

func (w *Widget) updateSearchMatches() {
	w.searchMatchIndex = make(map[int][]int)
	w.searchMatches, w.searchCapped = w.searchMatches[:0], false
	w.activeMatch = -1
	if w.searchRe == nil {
		return
	}
	for idx, line := range w.rawLines {
		w.appendMatchesForLine(idx, line, w.searchRe)
	}
	if len(w.searchMatches) > 0 {
		w.activeMatch = w.firstMatchFromView()
		w.scrollToMatch(w.activeMatch)
	}
}

// firstMatchFromView picks the match to start at, like mc: searching forward
// starts at the top of the view, searching backwards at its bottom; both wrap.
func (w *Widget) firstMatchFromView() int {
	if w.searchOpts.Backwards {
		bottom := w.offset + w.page() - 1
		for i, v := range slices.Backward(w.searchMatches) {
			if v.line <= bottom {
				return i
			}
		}
		return len(w.searchMatches) - 1
	}
	for i, m := range w.searchMatches {
		if m.line >= w.offset {
			return i
		}
	}
	return 0
}

func (w *Widget) extendSearchMatches(startLine int, lines []string) {
	if w.searchRe == nil || len(lines) == 0 {
		return
	}
	if w.searchMatchIndex == nil {
		w.searchMatchIndex = make(map[int][]int)
	}
	for i, line := range lines {
		w.appendMatchesForLine(startLine+i, line, w.searchRe)
	}
}

func (w *Widget) appendMatchesForLine(lineIdx int, line string, re *regexp.Regexp) {
	if re == nil {
		return
	}
	room := maxSearchMatches - len(w.searchMatches)
	if room <= 0 {
		w.searchCapped = true
		return
	}
	// Match the text as shown: color codes (the logs viewer's level colors,
	// a container's own colors) must neither match nor split a match.
	if strings.IndexByte(line, '\x1b') >= 0 {
		line = ansi.Strip(line)
	}
	// Widths are summed from the previous match on, so a long line with
	// many matches is measured once rather than once per match.
	prevEnd, prevWidth := 0, 0
	for _, m := range re.FindAllStringIndex(line, room+1) {
		if m[1] <= m[0] {
			continue // ignore empty matches (e.g. "a*")
		}
		if len(w.searchMatches) >= maxSearchMatches {
			w.searchCapped = true
			return
		}
		startWidth := prevWidth + lipgloss.Width(line[prevEnd:m[0]])
		loc := matchLocation{
			line:  lineIdx,
			start: startWidth,
			end:   startWidth + lipgloss.Width(line[m[0]:m[1]]),
		}
		prevEnd, prevWidth = m[1], loc.end
		w.searchMatches = append(w.searchMatches, loc)
		w.searchMatchIndex[lineIdx] = append(w.searchMatchIndex[lineIdx], len(w.searchMatches)-1)
	}
}

func (w *Widget) advanceMatch() bool {
	if len(w.searchMatches) == 0 {
		return false
	}
	if w.activeMatch < 0 {
		w.activeMatch = 0
	} else {
		w.activeMatch = (w.activeMatch + 1) % len(w.searchMatches)
	}
	w.scrollToMatch(w.activeMatch)
	return true
}

func (w *Widget) previousMatch() bool {
	if len(w.searchMatches) == 0 {
		return false
	}
	if w.activeMatch < 0 {
		w.activeMatch = 0
	} else {
		w.activeMatch--
		if w.activeMatch < 0 {
			w.activeMatch = len(w.searchMatches) - 1
		}
	}
	w.scrollToMatch(w.activeMatch)
	return true
}

func (w *Widget) scrollToMatch(matchIdx int) {
	if matchIdx < 0 || matchIdx >= len(w.searchMatches) {
		return
	}
	line := w.searchMatches[matchIdx].line
	page := w.page()
	target := max(line-page/2, 0)
	maxScroll := max(0, len(w.lines)-page)
	if target > maxScroll {
		target = maxScroll
	}
	w.offset = max(target, 0)
	w.hOffset = 0
}

func (w *Widget) visibleLines() []string {
	if len(w.lines) == 0 {
		return []string{applyPanelBackground("", w.width)}
	}
	viewWidth := w.viewportWidth()
	start := w.offset
	end := min(len(w.lines), start+w.page())
	segment := w.lines[start:end]
	result := make([]string, w.page())
	for i := range result {
		if i < len(segment) {
			lineIdx := start + i
			matches := w.lineMatches(lineIdx)
			if from, to, ok := w.selectionRange(lineIdx); ok {
				matches = withSelection(matches, from, to)
			}
			if w.wrapMode {
				result[i] = renderWrappedLine(segment[i], matches)
			} else {
				line := trimANSI(segment[i], w.hOffset, viewWidth)
				result[i] = renderLineWithMatches(line, viewWidth, matches, w.hOffset)
			}
		} else {
			result[i] = applyPanelBackground("", w.width)
		}
	}
	return result
}

func (w *Widget) lineMatches(lineIdx int) []displayMatch {
	if w.searchMatchIndex == nil {
		return nil
	}
	indexes, ok := w.searchMatchIndex[lineIdx]
	if !ok || len(indexes) == 0 {
		return nil
	}
	matches := make([]displayMatch, 0, len(indexes))
	activeIdx := -1
	if w.activeMatch >= 0 && w.activeMatch < len(w.searchMatches) {
		activeIdx = w.activeMatch
	}
	for _, idx := range indexes {
		if idx < 0 || idx >= len(w.searchMatches) {
			continue
		}
		loc := w.searchMatches[idx]
		matches = append(matches, displayMatch{
			start:  loc.start,
			end:    loc.end,
			active: idx == activeIdx,
		})
	}
	return matches
}

// FocusLine returns the line the user points at: the active search match,
// else the last line while the view follows the end, else the top line of
// the view. ok is false when there are no lines.
func (w *Widget) FocusLine() (line int, ok bool) {
	if len(w.lines) == 0 {
		return 0, false
	}
	if w.activeMatch >= 0 && w.activeMatch < len(w.searchMatches) {
		return w.searchMatches[w.activeMatch].line, true
	}
	if w.AtEnd() {
		return len(w.lines) - 1, true
	}
	return min(w.offset, len(w.lines)-1), true
}

func (w *Widget) page() int {
	if w.height <= 0 {
		return 1
	}
	return w.height
}

func trimANSI(s string, start, width int) string {
	if width <= 0 {
		return ""
	}
	return sliceANSI(s, start, width)
}

// ANSI helpers --------------------------------------------------------------

func sliceANSI(s string, start, width int) string {
	raw := sliceANSIColsRaw(s, start, width)
	if !strings.HasSuffix(raw, "\x1b[0m") {
		raw += "\x1b[0m"
	}
	return raw
}

func trimToWidth(text string, width int) string {
	if width <= 0 {
		return ""
	}
	display := lipgloss.Width(text)
	if display <= width {
		if display < width {
			text += strings.Repeat(" ", width-display)
		}
		return text
	}
	if width <= 1 {
		return "…"
	}
	limit := width - 1
	var b strings.Builder
	current := 0
	for _, r := range text {
		rw := runeWidth(string(r))
		if current+rw > limit {
			break
		}
		b.WriteRune(r)
		current += rw
	}
	trimmed := b.String() + "…"
	padding := width - lipgloss.Width(trimmed)
	if padding > 0 {
		trimmed += strings.Repeat(" ", padding)
	}
	return trimmed
}

func sliceANSIColsRaw(s string, start, width int) string {
	if start < 0 {
		start = 0
	}
	if width <= 0 {
		return ""
	}
	var b strings.Builder
	col := 0
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				b.WriteString(s[i : j+1])
				i = j + 1
				continue
			}
		}
		if col >= start && col < start+width {
			b.WriteByte(s[i])
		}
		col++
		if col >= start+width {
			break
		}
		i++
	}
	return b.String()
}

func runeWidth(s string) int {
	return lipgloss.Width(s)
}

func applyPanelBackground(line string, width int) string {
	return applyBackground(line, width, "\033[44m")
}

func applyBackground(line string, width int, bg string) string {
	const reset = "\033[0m"
	if width <= 0 {
		return bg + reset
	}
	line = padLine(strings.TrimSuffix(line, reset), width)
	line = sliceANSI(line, 0, width)
	line = bg + line + reset
	return line
}

func trimBackgroundPrefix(line string) string {
	backgrounds := []string{"\033[44m", "\033[45m", "\033[43m"}
	for _, bg := range backgrounds {
		if after, ok := strings.CutPrefix(line, bg); ok {
			return after
		}
	}
	return line
}

func renderLineWithMatches(line string, width int, matches []displayMatch, hOffset int) string {
	adjusted := adjustMatches(matches, hOffset, width)
	if len(adjusted) == 0 {
		return applyPanelBackground(line, width)
	}
	return applyHighlightedBackground(line, width, adjusted)
}

// Background codes of the viewer lines and their search matches.
const (
	lineBaseBg     = "\033[44m"
	lineMatchBg    = "\033[45m"
	lineActiveBg   = "\033[43m"
	lineSelectedBg = "\033[46m"
	lineResetCode  = "\033[0m"
)

func renderWrappedLine(line string, matches []displayMatch) string {
	line = trimBackgroundPrefix(strings.TrimSuffix(line, lineResetCode))
	return paintMatches(line, matches)
}

func applyHighlightedBackground(line string, width int, matches []displayMatch) string {
	line = trimBackgroundPrefix(strings.TrimSuffix(line, lineResetCode))
	line = padLine(line, width)
	return paintMatches(line, matches)
}

// paintMatches puts line on the panel background and the matches, sorted
// display column ranges, on their match backgrounds.
func paintMatches(line string, matches []displayMatch) string {
	var b strings.Builder
	b.WriteString(lineBaseBg)
	currentBg := lineBaseBg
	displayPos := 0
	matchIdx := 0
	for i := 0; i < len(line); {
		if seq := ansiSequenceLength(line[i:]); seq > 0 {
			b.WriteString(line[i : i+seq])
			i += seq
			continue
		}
		_, size := utf8.DecodeRuneInString(line[i:])
		seg := line[i : i+size]
		for matchIdx < len(matches) && displayPos >= matches[matchIdx].end {
			if currentBg != lineBaseBg {
				b.WriteString(lineBaseBg)
				currentBg = lineBaseBg
			}
			matchIdx++
		}
		if matchIdx < len(matches) && displayPos >= matches[matchIdx].start && displayPos < matches[matchIdx].end {
			desired := lineMatchBg
			if matches[matchIdx].active {
				desired = lineActiveBg
			}
			if matches[matchIdx].selected {
				desired = lineSelectedBg
			}
			if desired != currentBg {
				b.WriteString(desired)
				currentBg = desired
			}
		} else if currentBg != lineBaseBg {
			b.WriteString(lineBaseBg)
			currentBg = lineBaseBg
		}
		b.WriteString(seg)
		displayPos += runeWidth(seg)
		i += size
	}
	if currentBg != lineBaseBg {
		b.WriteString(lineBaseBg)
	}
	b.WriteString(lineResetCode)
	return b.String()
}

func adjustMatches(matches []displayMatch, offset, width int) []displayMatch {
	if len(matches) == 0 || width <= 0 {
		return nil
	}
	end := offset + width
	out := make([]displayMatch, 0, len(matches))
	for _, m := range matches {
		if m.end <= offset {
			continue
		}
		if m.start >= end {
			break
		}
		adj := m
		if adj.start < offset {
			adj.start = offset
		}
		if adj.end > end {
			adj.end = end
		}
		adj.start -= offset
		adj.end -= offset
		if adj.start < adj.end {
			out = append(out, adj)
		}
	}
	return out
}

func padLine(line string, width int) string {
	line = trimBackgroundPrefix(line)
	displayWidth := lipgloss.Width(line)
	if displayWidth < width {
		line += strings.Repeat(" ", width-displayWidth)
	}
	return line
}

func ansiSequenceLength(s string) int {
	if len(s) < 2 || s[0] != 0x1b || s[1] != '[' {
		return 0
	}
	j := 2
	for j < len(s) && s[j] != 'm' {
		j++
	}
	if j < len(s) {
		return j + 1
	}
	return len(s)
}
