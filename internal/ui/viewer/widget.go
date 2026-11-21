package viewer

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	chroma "github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/sttts/kc/internal/overlay"
	uistyles "github.com/sttts/kc/internal/ui/styles"
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

	raw          string
	rawLines     []string
	lines        []string
	theme        string
	plain        bool
	wrapMode     bool
	maxLineWidth int

	offset  int
	hOffset int

	onEdit    func() tea.Cmd
	onOptions func() tea.Cmd
	onClose   func() tea.Cmd

	searchDialog     *searchDialog
	searchQuery      string
	searchOpts       SearchOptions
	searchRe         *regexp.Regexp
	searchErr        string
	searchMatches    []matchLocation
	searchMatchIndex map[int][]int
	activeMatch      int
}

type matchLocation struct {
	line  int
	start int
	end   int
}

type displayMatch struct {
	start  int
	end    int
	active bool
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
	w.raw = text
	w.metadata = meta
	w.rawLines = strings.Split(text, "\n")
	w.updateMaxLineWidth()
	if w.plain {
		w.lines = append([]string{}, w.rawLines...)
	} else {
		w.highlight()
	}
	w.offset = 0
	w.hOffset = 0
	w.clearSearchState()
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
	w.updateMaxLineWidth()
	appendText := strings.Join(lines, "\n")
	if w.raw == "" {
		w.raw = appendText
	} else {
		if !strings.HasSuffix(w.raw, "\n") {
			w.raw += "\n"
		}
		w.raw += appendText
	}
	if w.plain {
		if len(w.lines) == 0 {
			w.lines = make([]string, 0, len(lines))
		}
		w.lines = append(w.lines, lines...)
	} else {
		w.highlight()
	}
	w.extendSearchMatches(start, lines)
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
	case tea.KeyMsg:
		if w.searchDialog != nil {
			return w.handleSearchDialog(m), true
		}
		if cmd, handled := w.handleKey(m.String()); handled {
			return cmd, true
		}
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
	style := uistyles.PanelContentStyle.Copy().Height(contentH).MaxHeight(contentH)
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
func (w *Widget) Position() (current int, total int) {
	total = len(w.lines)
	if total == 0 {
		return 0, 0
	}
	end := w.offset + w.page()
	if end > total {
		end = total
	}
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
	w.offset = len(w.lines) - page
	if w.offset < 0 {
		w.offset = 0
	}
}

// ScrollIndicators reports whether additional content exists above or below.
func (w *Widget) ScrollIndicators() (bool, bool) {
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
	return searchInputStyle.Copy().Width(width).Render(trimmed)
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
	return fmt.Sprintf("/%s (%d/%d)", trimmed, idx, len(w.searchMatches))
}

func (w *Widget) highlight() {
	if w.raw == "" {
		w.lines = []string{""}
		return
	}
	ensureCustomStyles()
	lexer := w.selectLexer()
	iterator, err := lexer.Tokenise(nil, w.raw)
	if err != nil {
		w.lines = strings.Split(w.raw, "\n")
		return
	}
	var rendered string
	if w.theme == "turbo-pascal" {
		rendered = highlightFallback(iterator)
	} else {
		style := styles.Get(w.theme)
		if style == nil {
			style = styles.Monokai
		}
		rendered = renderTokensWithStyle(style, iterator)
	}
	rendered = strings.TrimRight(rendered, "\n")
	w.lines = strings.Split(rendered, "\n")
	if len(w.lines) == 0 {
		w.lines = []string{""}
	}
}

func (w *Widget) selectLexer() chroma.Lexer {
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
	if analyser := lexers.Analyse(w.raw); analyser != nil {
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
	case "ctrl+f":
		if cmd := w.beginSearch(); cmd != nil {
			return cmd, true
		}
		return nil, true
	case "home", "g":
		w.offset = 0
	case "end", "G":
		w.offset = max(0, len(w.lines)-w.page())
	case "ctrl+a":
		w.hOffset = 0
		w.clampHorizontalOffset()
	case "ctrl+e":
		w.hOffset = w.maxHorizontalOffset()
	case "f7", "/":
		if cmd := w.beginSearch(); cmd != nil {
			return cmd, true
		}
		return nil, true
	case "f2":
		if w.onOptions != nil {
			return w.onOptions(), true
		}
		return nil, false
	case "f3":
		// F3 toggles the viewer like in Midnight Commander: it closes it.
		if w.onClose != nil {
			return w.onClose(), true
		}
		return nil, false
	case "shift+f7", "n":
		if w.nextMatch() {
			return nil, true
		}
	case "N":
		if w.prevMatch() {
			return nil, true
		}
	case "f4":
		if w.onEdit != nil {
			return w.onEdit(), true
		}
		return nil, false
	case "f10":
		if w.onClose != nil {
			return w.onClose(), true
		}
		return nil, false
	default:
		return nil, false
	}
	return nil, true
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
	w.searchMatches = nil
	w.activeMatch = -1
	if w.searchMatchIndex == nil {
		w.searchMatchIndex = make(map[int][]int)
	}
	for k := range w.searchMatchIndex {
		delete(w.searchMatchIndex, k)
	}
}

func (w *Widget) hasSearchQuery() bool {
	return w.searchRe != nil
}

func (w *Widget) alignToEnd() {
	start := w.offset
	end := min(len(w.rawLines), w.offset+w.page())
	maxLen := 0
	for i := start; i < end; i++ {
		if length := runeWidth(w.rawLines[i]); length > maxLen {
			maxLen = length
		}
	}
	if maxLen > w.width {
		w.hOffset = maxLen - w.width
	} else {
		w.hOffset = 0
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
	w.searchMatches = w.searchMatches[:0]
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
		for i := len(w.searchMatches) - 1; i >= 0; i-- {
			if w.searchMatches[i].line <= bottom {
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
	for _, m := range re.FindAllStringIndex(line, -1) {
		if m[1] <= m[0] {
			continue // ignore empty matches (e.g. "a*")
		}
		startWidth := lipgloss.Width(line[:m[0]])
		loc := matchLocation{
			line:  lineIdx,
			start: startWidth,
			end:   startWidth + lipgloss.Width(line[m[0]:m[1]]),
		}
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
	target := line - page/2
	if target < 0 {
		target = 0
	}
	maxScroll := max(0, len(w.lines)-page)
	if target > maxScroll {
		target = maxScroll
	}
	w.offset = target
	if w.offset < 0 {
		w.offset = 0
	}
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
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
		if strings.HasPrefix(line, bg) {
			return strings.TrimPrefix(line, bg)
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

func renderWrappedLine(line string, matches []displayMatch) string {
	const (
		baseBg    = "\033[44m"
		matchBg   = "\033[45m"
		activeBg  = "\033[43m"
		resetCode = "\033[0m"
	)
	line = trimBackgroundPrefix(strings.TrimSuffix(line, resetCode))
	var b strings.Builder
	b.WriteString(baseBg)
	currentBg := baseBg
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
			if currentBg != baseBg {
				b.WriteString(baseBg)
				currentBg = baseBg
			}
			matchIdx++
		}
		if matchIdx < len(matches) && displayPos >= matches[matchIdx].start && displayPos < matches[matchIdx].end {
			desired := matchBg
			if matches[matchIdx].active {
				desired = activeBg
			}
			if desired != currentBg {
				b.WriteString(desired)
				currentBg = desired
			}
		} else if currentBg != baseBg {
			b.WriteString(baseBg)
			currentBg = baseBg
		}
		b.WriteString(seg)
		displayPos += runeWidth(seg)
		i += size
	}
	if currentBg != baseBg {
		b.WriteString(baseBg)
	}
	b.WriteString(resetCode)
	return b.String()
}
func applyHighlightedBackground(line string, width int, matches []displayMatch) string {
	const (
		baseBg    = "\033[44m"
		matchBg   = "\033[45m"
		activeBg  = "\033[43m"
		resetCode = "\033[0m"
	)
	line = trimBackgroundPrefix(strings.TrimSuffix(line, resetCode))
	line = padLine(line, width)
	var b strings.Builder
	b.WriteString(baseBg)
	currentBg := baseBg
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
			if currentBg != baseBg {
				b.WriteString(baseBg)
				currentBg = baseBg
			}
			matchIdx++
		}
		if matchIdx < len(matches) && displayPos >= matches[matchIdx].start && displayPos < matches[matchIdx].end {
			desired := matchBg
			if matches[matchIdx].active {
				desired = activeBg
			}
			if desired != currentBg {
				b.WriteString(desired)
				currentBg = desired
			}
		} else if currentBg != baseBg {
			b.WriteString(baseBg)
			currentBg = baseBg
		}
		b.WriteString(seg)
		displayPos += runeWidth(seg)
		i += size
	}
	if currentBg != baseBg {
		b.WriteString(baseBg)
	}
	b.WriteString(resetCode)
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
