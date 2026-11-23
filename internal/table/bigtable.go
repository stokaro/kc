package table

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	lgtable "charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/x/ansi"
)

// GridMode controls how the table renders horizontally.
//   - ModeScroll: automatic column widths (lipgloss.table decides).
//   - ModeFit: constrain columns to fit the total width (no manual slicing).
type GridMode int

const (
	// ModeScroll uses automatic column widths.
	ModeScroll GridMode = iota
	// ModeFit constrains columns to fit the viewport width.
	ModeFit
)

const bigTableUpdateTimeout = 250 * time.Millisecond

// BigTable is a reusable Bubble Tea component that renders large, dynamic
// tables backed by a List provider using a single lipgloss.table.
// Only inner vertical separators are supported; no outside borders or underline.
type BigTable struct {
	mode GridMode
	w, h int

	cols []Column // base titles (ASCII)
	list List     // data provider

	desired []int // initial width hints

	// selection & windowing
	window    []Row               // rows currently rendered (order matches table rows)
	selected  map[string]struct{} // multi-select set by row ID
	top       int                 // absolute index of top row in provider
	cursor    int                 // absolute cursor index in provider
	focusedID string              // ID of the currently focused row (for stability across updates)
	windowTop int                 // absolute index backing the current window slice

	// cached rendered table (header + rows)
	bodyRow string

	styles Styles // external styles

	// Only inner vertical separators (no outside borders, no underline)
	bColumn bool

	// Horizontal scroll state for ModeScroll (character-based, ASCII-safe).
	xOff  int // horizontal offset in characters across the full row
	hStep int // step size for left/right navigation

	// Focus state: when unfocused, the selector highlight is hidden.
	// The outer component is responsible for routing keys to the focused table.
	focused bool
}

// Styles groups all externally configurable styles.
type Styles struct {
	Header   lipgloss.Style
	Selector lipgloss.Style
	Cell     lipgloss.Style
	Border   lipgloss.Style
	// Marked applies to rows toggled via C-t/Insert (multi-select).
	// It should not generally change the background; prefer fg/bold changes.
	Marked lipgloss.Style
}

// DefaultStyles returns a set of defaults for the table.
func DefaultStyles() Styles {
	return Styles{
		Header:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Green),
		Selector: lipgloss.NewStyle().Background(lipgloss.Cyan).Foreground(lipgloss.Black),
		Cell:     lipgloss.NewStyle(),
		Border:   lipgloss.NewStyle().Foreground(lipgloss.Yellow),
		Marked:   lipgloss.NewStyle().Foreground(lipgloss.Yellow).Bold(true),
	}
}

// SetStyles overrides the component styles.
func (m *BigTable) SetStyles(s Styles) { m.styles = s }

// NewBigTable constructs a table with the given columns, data provider and
// initial size (content width and height). Titles are plain text; runes count as one cell each (sort marks such as "↓").
func NewBigTable(cols []Column, list List, w, h int) BigTable {
	bt := BigTable{
		mode:      ModeScroll,
		w:         max(20, w),
		h:         max(6, h),
		list:      list,
		selected:  make(map[string]struct{}),
		top:       0,
		cursor:    0,
		focusedID: "",
		windowTop: 0,
		styles:    DefaultStyles(),
		bColumn:   false,
		xOff:      0,
		hStep:     4,
		focused:   true,
	}
	bt.setColumns(cols)
	return bt
}

// SetColumns replaces the columns and keeps the rows' state: the cursor
// stays on the same row at the same place in the window, and marked rows
// stay marked. The horizontal scroll goes back to the first column, since
// the old offset belongs to the old columns.
func (m *BigTable) SetColumns(ctx context.Context, cols []Column) {
	m.setColumns(cols)
	m.xOff = 0
	m.rebuildWindow(ctx)
}

func (m *BigTable) setColumns(cols []Column) {
	m.cols = append([]Column(nil), cols...)
	m.desired = make([]int, len(m.cols))
	for i := range m.cols {
		if m.cols[i].Width <= 0 {
			m.cols[i].Width = 14
		}
		m.desired[i] = m.cols[i].Width
	}
}

// SetHorizontalStep sets the number of characters to pan left/right in
// ModeScroll. Values < 1 are clamped to 1. Returns the receiver for chaining.
func (m *BigTable) SetHorizontalStep(step int) *BigTable {
	if step < 1 {
		step = 1
	}
	m.hStep = step
	return m
}

// SetSize updates the component size (content area). Width/height are clamped
// to sensible minimums and trigger a relayout.
func (m *BigTable) SetSize(ctx context.Context, w, h int) {
	if w < 20 {
		w = 20
	}
	if h < 6 {
		h = 6
	}
	if w == m.w && h == m.h {
		return
	}
	m.w, m.h = w, h
	m.applyMode(ctx)
}

// --- Border configuration (only inner verticals) ---

// BorderVertical toggles vertical separators between columns (inner verticals).
func (m *BigTable) BorderVertical(ctx context.Context, v bool) *BigTable {
	m.bColumn = v
	m.rebuildWindow(ctx)
	return m
}

// SetMode switches between ModeScroll and ModeFit and refreshes the view.
func (m *BigTable) SetMode(ctx context.Context, md GridMode) {
	if m.mode != md {
		m.mode = md
		m.applyMode(ctx)
	}
}

// ToggleMode flips the current GridMode.
func (m *BigTable) ToggleMode(ctx context.Context) {
	if m.mode == ModeScroll {
		m.SetMode(ctx, ModeFit)
	} else {
		m.SetMode(ctx, ModeScroll)
	}
}

// SetList swaps the data provider and repositions the cursor based on the
// previously focused row ID. If that row disappeared, the cursor moves to the
// next row, or previous when no next exists.
func (m *BigTable) SetList(ctx context.Context, list List) {
	m.list = list
	m.window = nil
	m.windowTop = m.top
	m.repositionOnDataChange(ctx)
	m.rebuildWindow(ctx)
}

// GetList returns the current data provider.
func (m *BigTable) GetList() List { return m.list }

// CurrentID returns the focused row ID, if any.
func (m *BigTable) CurrentID(ctx context.Context) (string, bool) {
	if row := m.list.Lines(ctx, m.cursor, 1); len(row) == 1 {
		id, _, _, ok := row[0].Columns()
		return id, ok
	}
	return "", false
}

// Select moves the focus to the row with the given ID, if present, and
// adjusts the window so the focused row is visible. Returns true if found.
func (m *BigTable) Select(ctx context.Context, id string) bool {
	n := m.list.Len(ctx)
	if n <= 0 {
		return false
	}
	// Find absolute index of the row with the given ID by scanning in chunks.
	step := 256
	found := -1
	for off := 0; off < n; {
		take := step
		if off+take > n {
			take = n - off
		}
		rows := m.list.Lines(ctx, off, take)
		for i, r := range rows {
			rid, _, _, ok := r.Columns()
			if ok && rid == id {
				found = off + i
				break
			}
		}
		if found >= 0 {
			break
		}
		off += take
	}
	if found < 0 {
		return false
	}
	m.cursor = found
	// Ensure the focused row is visible within the current window height.
	vis := m.bodyRowsHeight()
	if m.cursor < m.top {
		m.top = m.cursor
	} else if m.cursor >= m.top+vis {
		m.top = max(0, m.cursor-(vis-1))
	}
	m.rebuildWindow(ctx)
	return true
}

// Mark toggles selection state for a row ID without moving the cursor.
func (m *BigTable) Mark(ctx context.Context, id string, selected bool) bool {
	if id == "" {
		return false
	}
	if selected {
		m.selected[id] = struct{}{}
		return true
	}
	delete(m.selected, id)
	return true
}

// ClearMarks removes all multi-select marks.
func (m *BigTable) ClearMarks() {
	for id := range m.selected {
		delete(m.selected, id)
	}
}

// SelectedIDs returns the set of marked row IDs (multi-select state).
func (m *BigTable) SelectedIDs() []string {
	if len(m.selected) == 0 {
		return nil
	}
	out := make([]string, 0, len(m.selected))
	for id := range m.selected {
		out = append(out, id)
	}
	return out
}

// UpdateWithContext handles key navigation and selection toggling. Both
// returned commands are nil today; the pair keeps the signature batchable
// for external composition.
func (m *BigTable) UpdateWithContext(ctx context.Context, msg tea.Msg) (first, second tea.Cmd) {
	if v, ok := msg.(tea.KeyMsg); ok {
		m.handleKey(ctx, v.String())
	}
	return nil, nil
}

// handleKey applies one navigation or selection key.
func (m *BigTable) handleKey(ctx context.Context, key string) {
	switch key {
	case "ctrl+t", "insert":
		m.toggleCursorMark(ctx)
	case "up", "k":
		m.cursorUp(ctx)
	case "down", "j":
		m.cursorDown(ctx)
	case "pgup":
		m.pageUp(ctx)
	case "pgdown":
		m.pageDown(ctx)
	case "home":
		m.cursor = 0
		m.top = 0
		m.rebuildWindow(ctx)
	case "end":
		m.cursorEnd(ctx)
	case "left", "h":
		m.scrollLeft(ctx)
	case "right", "l":
		m.scrollRight(ctx)
	}
}

// toggleCursorMark toggles the mark of the cursor row, then moves the cursor
// down and keeps it visible.
func (m *BigTable) toggleCursorMark(ctx context.Context) {
	row := m.list.Lines(ctx, m.cursor, 1)
	if len(row) != 1 {
		return
	}
	id, _, _, _ := row[0].Columns()
	if _, ok := m.selected[id]; ok {
		delete(m.selected, id)
	} else {
		m.selected[id] = struct{}{}
	}
	if m.cursor+1 < m.list.Len(ctx) {
		m.cursor++
		vis := m.bodyRowsHeight()
		if m.cursor >= m.top+vis {
			m.top = max(0, m.cursor-(vis-1))
		}
	}
	m.rebuildWindow(ctx)
}

func (m *BigTable) cursorUp(ctx context.Context) {
	if m.cursor <= 0 {
		return
	}
	m.cursor--
	if m.cursor < m.top {
		m.top = m.cursor
	}
	m.rebuildWindow(ctx)
}

func (m *BigTable) cursorDown(ctx context.Context) {
	if m.cursor+1 >= m.list.Len(ctx) {
		return
	}
	m.cursor++
	vis := m.bodyRowsHeight()
	if m.cursor >= m.top+vis {
		m.top = m.cursor - (vis - 1)
	}
	m.rebuildWindow(ctx)
}

func (m *BigTable) pageUp(ctx context.Context) {
	vis := max(m.bodyRowsHeight(), 1)
	m.cursor = max(m.cursor-vis, 0)
	if m.cursor < m.top {
		m.top = m.cursor
	}
	m.rebuildWindow(ctx)
}

func (m *BigTable) pageDown(ctx context.Context) {
	vis := max(m.bodyRowsHeight(), 1)
	m.cursor += vis
	if m.cursor >= m.list.Len(ctx) {
		m.cursor = m.list.Len(ctx) - 1
	}
	if m.cursor >= m.top+vis {
		m.top = max(0, m.cursor-(vis-1))
	}
	m.rebuildWindow(ctx)
}

func (m *BigTable) cursorEnd(ctx context.Context) {
	n := m.list.Len(ctx)
	if n <= 0 {
		return
	}
	m.cursor = n - 1
	vis := m.bodyRowsHeight()
	m.top = max(0, n-vis)
	m.rebuildWindow(ctx)
}

func (m *BigTable) scrollLeft(ctx context.Context) {
	if m.mode != ModeScroll || m.xOff <= 0 {
		return
	}
	m.xOff = max(m.xOff-m.hStep, 0)
	m.rebuildWindow(ctx)
}

func (m *BigTable) scrollRight(ctx context.Context) {
	if m.mode != ModeScroll {
		return
	}
	// rebuildWindow clamps the offset to the total width.
	m.xOff = max(m.xOff+m.hStep, 0)
	m.rebuildWindow(ctx)
}

func (m *BigTable) Update(msg tea.Msg) (first, second tea.Cmd) {
	ctx, cancel := context.WithTimeout(context.Background(), bigTableUpdateTimeout)
	defer cancel()
	return m.UpdateWithContext(ctx, msg)
}

// View renders the component.
func (m *BigTable) View() tea.View { return tea.NewView(strings.TrimRight(m.bodyRow, "\n")) }

// no app header/help line inside BigTable; outer app should render it
// no BigTable footer; outer app should render any footers/help lines

// rebuildWindow sets the table rows to the current window [top:top+height)
// and positions the table cursor at (cursor-top), updating the width cache.
func (m *BigTable) rebuildWindow(ctx context.Context) {
	// rowsVisible already accounts for header and border lines.
	rowsVisible := m.bodyRowsHeight()
	m.clampViewport(m.list.Len(ctx), rowsVisible)

	// Render exactly the number of visible rows.
	m.ensureWindow(ctx, rowsVisible)

	// Single lipgloss table: headers + data rows; no outside borders or underline.
	t := lgtable.New().Wrap(false).Height(m.h).Width(m.w)
	// Use lipgloss.table column borders when enabled via m.bColumn.
	t = t.Border(lipgloss.NormalBorder()).BorderStyle(m.styles.Border)
	t = t.BorderTop(false).BorderBottom(false).BorderLeft(false).BorderRight(false)
	t = t.BorderHeader(false).BorderRow(false).BorderColumn(m.bColumn)

	// Fit mode is handled by lipgloss.table's Width; no manual slicing.
	var idx []int
	var headers []string
	var rows [][]string
	if m.mode == ModeScroll {
		idx, headers, rows = m.scrollContent()
	} else {
		idx, headers, rows = m.fitContent()
	}
	t = t.Headers(headers...)
	t = t.Rows(m.padRows(rows, len(idx), rowsVisible)...)
	t = t.StyleFunc(m.cellStyle(captureStylesSubset(m.window, idx)))

	m.bodyRow = m.highlightCursorLine(strings.TrimRight(t.Render(), "\n"))
	// Track focused ID for stability across updates.
	if row := m.list.Lines(ctx, m.cursor, 1); len(row) == 1 {
		id, _, _, ok := row[0].Columns()
		if ok {
			m.focusedID = id
		}
	}
}

// clampViewport keeps the cursor inside the n rows and the top row such that
// the cursor stays within the rowsVisible rows of the body.
func (m *BigTable) clampViewport(n, rowsVisible int) {
	n = max(n, 0)
	if m.cursor >= n {
		m.cursor = max(0, n-1)
	}
	m.top = min(m.top, max(0, n-rowsVisible), m.cursor)
	if m.cursor >= m.top+rowsVisible {
		m.top = max(0, m.cursor-(rowsVisible-1))
	}
}

// scrollContent returns the visible column indexes, the headers and the rows
// of ModeScroll: character-based horizontal panning over ASCII cell data.
func (m *BigTable) scrollContent() (idx []int, headers []string, rows [][]string) {
	idx, cuts, widths := m.slicePlanForScroll(m.xOff, m.w)
	base := m.computeScrollBaseWidths()
	// When the columns are narrower than the viewport, give the spare
	// room to one column (slackColumn). Otherwise lipgloss.table spreads
	// it evenly over all columns.
	if slack := m.w - scrollPlanWidth(widths); slack > 0 {
		if j := m.slackColumn(idx, cuts); j >= 0 {
			widths[j] += slack
			base = append([]int(nil), base...)
			base[idx[j]] += slack
		}
	}
	headerAlign := m.styles.Header.GetAlignHorizontal()
	headers = make([]string, len(idx))
	for i, col := range idx {
		// The title is placed within the visible part of the column. A
		// column wider than the viewport would otherwise center its title
		// beyond the right edge, out of sight.
		title := alignTitle(m.cols[col].Title, widths[i], headerAlign)
		h := titleSlicePad(title, 0, widths[i])
		if !m.bColumn && i < len(idx)-1 {
			h = ensureTrailingSpace(h)
		}
		headers[i] = h
	}
	return idx, headers, rowsToStringRowsSliced(m.window, idx, cuts, widths, base, !m.bColumn)
}

// fitContent returns the column indexes, the headers and the rows of
// ModeFit: all columns, and lipgloss.table handles the width.
func (m *BigTable) fitContent() (idx []int, headers []string, rows [][]string) {
	idx = m.visibleColumnsAll()
	headers = make([]string, len(idx))
	for i := range idx {
		h := m.cols[idx[i]].Title
		if !m.bColumn && i < len(idx)-1 {
			h = ensureTrailingSpace(h)
		}
		headers[i] = h
	}
	return idx, headers, rowsToStringRowsSubsetSep(m.window, idx, !m.bColumn)
}

// cellStyle returns the lipgloss.table style function: the header style, the
// row's own cell styles, and the cursor and mark overlays.
func (m *BigTable) cellStyle(stylesPerRow [][]*lipgloss.Style) func(row, col int) lipgloss.Style {
	selected := m.selected
	return func(row, col int) lipgloss.Style {
		if row == lgtable.HeaderRow {
			return m.styles.Header
		}
		if row < 0 || row >= len(stylesPerRow) {
			return m.styles.Cell
		}
		st := m.styles.Cell
		if col < len(stylesPerRow[row]) && stylesPerRow[row][col] != nil {
			st = stylesPerRow[row][col].Inherit(st)
		}
		id, _, _, _ := m.window[row].Columns()
		// Row-level overlays
		if m.focused && row == (m.cursor-m.top) {
			st = m.styles.Selector.Inherit(st)
		}
		if _, ok := selected[id]; ok {
			st = m.styles.Marked.Inherit(st)
		}
		return st
	}
}

// padRows appends empty rows up to the body height when column separators are
// enabled, so the separators run down to the bottom of the viewport like in a
// file manager panel instead of stopping after the last data row.
func (m *BigTable) padRows(rows [][]string, cols, rowsVisible int) [][]string {
	if !m.bColumn || cols == 0 {
		return rows
	}
	for len(rows) < rowsVisible {
		rows = append(rows, make([]string, cols))
	}
	return rows
}

// highlightCursorLine restyles the whole cursor line, column separators
// included, with the selector style. lipgloss.table styles borders with a single
// style for all rows, so without this the separators break the cursor bar.
func (m *BigTable) highlightCursorLine(body string) string {
	if !m.focused {
		return body
	}
	row := m.cursor - m.top
	if row < 0 || row >= len(m.window) {
		return body
	}
	lines := strings.Split(body, "\n")
	line := row + 1 // the header occupies the first line
	if line >= len(lines) {
		return body
	}
	st := m.styles.Selector
	if id, _, _, _ := m.window[row].Columns(); id != "" {
		if _, ok := m.selected[id]; ok {
			st = m.styles.Marked.Inherit(st)
		}
	}
	lines[line] = st.Render(ansi.Strip(lines[line]))
	return strings.Join(lines, "\n")
}

func (m *BigTable) ensureWindow(ctx context.Context, rowsVisible int) {
	total := m.list.Len(ctx)
	targetSize := clampWindowSize(total, m.top, rowsVisible)
	if targetSize == 0 {
		m.window = nil
		m.windowTop = m.top
		return
	}
	prevTarget := clampWindowSize(total, m.windowTop, rowsVisible)
	if len(m.window) == targetSize && m.windowTop == m.top {
		return
	}
	if len(m.window) == prevTarget && prevTarget > 0 {
		if m.top == m.windowTop+1 && m.tryShiftDown(ctx, targetSize) {
			return
		}
		if m.windowTop == m.top+1 && m.tryShiftUp(ctx, targetSize) {
			return
		}
	}
	m.window = m.list.Lines(ctx, m.top, rowsVisible)
	m.windowTop = m.top
}

func (m *BigTable) tryShiftDown(ctx context.Context, targetSize int) bool {
	if len(m.window) == 0 {
		return false
	}
	trimmed := m.window[1:]
	if len(trimmed) == 0 {
		return false
	}
	if len(trimmed) >= targetSize {
		m.window = trimmed[len(trimmed)-targetSize:]
		m.windowTop = m.top
		return true
	}
	last := trimmed[len(trimmed)-1]
	id, _, _, ok := last.Columns()
	if !ok {
		return false
	}
	need := max(targetSize-len(trimmed), 0)
	if need == 0 {
		m.window = trimmed
		m.windowTop = m.top
		return true
	}
	extra := m.list.Below(ctx, id, need)
	if len(extra) < need {
		return false
	}
	m.window = slices.Concat(trimmed, extra)
	m.windowTop = m.top
	return true
}

func (m *BigTable) tryShiftUp(ctx context.Context, targetSize int) bool {
	if len(m.window) == 0 {
		return false
	}
	prev := m.window
	anchor := prev[0]
	id, _, _, ok := anchor.Columns()
	if !ok {
		return false
	}
	keep := min(max(targetSize-1, 0), len(prev))
	remain := prev[:keep]
	if len(remain) >= targetSize {
		m.window = remain[len(remain)-targetSize:]
		m.windowTop = m.top
		return true
	}
	need := targetSize - len(remain)
	extra := m.list.Above(ctx, id, need)
	if len(extra) < need {
		return false
	}
	m.window = slices.Concat(extra, remain)
	if len(m.window) > targetSize {
		m.window = m.window[len(m.window)-targetSize:]
	}
	m.windowTop = m.top
	return true
}

func clampWindowSize(total, top, rowsVisible int) int {
	if rowsVisible <= 0 || total <= 0 || top >= total {
		return 0
	}
	size := rowsVisible
	if remaining := total - top; remaining < size {
		size = remaining
	}
	if size < 0 {
		return 0
	}
	return size
}

func (m *BigTable) applyMode(ctx context.Context) { m.rebuildWindow(ctx) }

// Refresh forces a re-render of the current window without changing state.
func (m *BigTable) Refresh(ctx context.Context) { m.rebuildWindow(ctx) }

// Focus sets the table as focused (selector visible on the focused row).
func (m *BigTable) Focus(ctx context.Context) { m.focused = true; m.rebuildWindow(ctx) }

// Blur sets the table as unfocused (selector hidden).
func (m *BigTable) Blur(ctx context.Context) { m.focused = false; m.rebuildWindow(ctx) }

// SetFocused toggles focus explicitly and returns the receiver for chaining.
func (m *BigTable) SetFocused(ctx context.Context, v bool) *BigTable {
	if m.focused != v {
		m.focused = v
		m.rebuildWindow(ctx)
	}
	return m
}

// Focused reports whether the table is focused.
func (m *BigTable) Focused() bool { return m.focused }

// VisibleRowID returns the row ID at the given zero-based visible row index
// within the current window (data rows only; header is not counted). It
// returns ok=false when the index is out of bounds.
func (m *BigTable) VisibleRowID(i int) (string, bool) {
	if i < 0 || i >= len(m.window) {
		return "", false
	}
	id, _, _, ok := m.window[i].Columns()
	return id, ok
}

// bodyRowsHeight returns the number of data rows visible within the viewport
// after subtracting sticky header lines.
func (m *BigTable) bodyRowsHeight() int {
	// Reserve exactly one line for the table header; the rest is body space.
	rows := max(m.h-1, 1)
	return rows
}

// (no custom column width allocator; Fit relies on lipgloss.table sizing)

// (no custom interleaved separator columns)

// captureStyles extracts per-cell styles for visible rows to use in StyleFunc.
func captureStylesSubset(rows []Row, idx []int) [][]*lipgloss.Style {
	out := make([][]*lipgloss.Style, len(rows))
	for i := range rows {
		_, _, styles, _ := rows[i].Columns()
		row := make([]*lipgloss.Style, len(idx))
		for j, k := range idx {
			if k < len(styles) {
				row[j] = styles[k]
			} else {
				row[j] = nil
			}
		}
		out[i] = row
	}
	return out
}

// visibleColumnsAll returns all column indices in order.
func (m *BigTable) visibleColumnsAll() []int {
	n := len(m.cols)
	if n == 0 {
		return nil
	}
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	return idx
}

// slicePlanForScroll computes which columns and which horizontal slices of those
// columns should be rendered to display a viewport of width vw starting at xOff.
// Returns the column indices, per-column cut offsets, and per-column visible widths.
func (m *BigTable) slicePlanForScroll(xOff, vw int) (idx, cuts, widths []int) {
	n := len(m.cols)
	if n == 0 || vw <= 0 {
		return nil, nil, nil
	}
	// Compute dynamic base widths from current header titles and visible rows.
	base := m.computeScrollBaseWidths()
	// Always budget 1 column between visible columns: either a space (when
	// not using lipgloss borders) or the border glyph width (when enabled).
	// The lipgloss table will render border columns when BorderColumn(true),
	// so we must include their width in our horizontal plan.
	const sep = 1
	xOff = clampScrollOffset(xOff, scrollPlanWidth(base), vw)
	m.xOff = xOff // keep clamped value

	remaining := vw
	pos := 0 // running position across full line including separators

	for i := 0; i < n && remaining > 0; i++ {
		if i > 0 {
			var stop bool
			pos, remaining, stop = scrollSeparator(pos, remaining, xOff, sep, len(widths))
			if stop {
				break
			}
		}

		w := base[i]

		// Skip columns fully left of the viewport.
		if pos+w <= xOff {
			pos += w
			continue
		}

		// Determine starting cut within this column and how much to take.
		cut := 0
		if xOff > pos {
			cut = xOff - pos
		}
		avail := w - cut
		if avail <= 0 {
			continue
		}
		take := min(avail, remaining)
		idx = append(idx, i)
		cuts = append(cuts, cut)
		widths = append(widths, take)
		remaining -= take
		pos += w
	}

	if len(idx) == 0 {
		// Nothing visible, force showing the last column tail.
		i := n - 1
		w := base[i]
		cut := 0
		if w > vw {
			cut = w - vw
		}
		return []int{i}, []int{cut}, []int{min(vw, w)}
	}
	return idx, cuts, widths
}

// clampScrollOffset limits a horizontal offset to [0, total-vw], and to 0
// when the whole line of total columns fits into vw.
func clampScrollOffset(xOff, total, vw int) int {
	if total <= vw {
		return 0
	}
	return max(0, min(xOff, total-vw))
}

// scrollSeparator accounts for the separator before a column. Left of the
// viewport it moves pos past the separator when xOff is beyond it. Inside
// the viewport, after the first visible column (shown counts the visible
// columns so far), the separator uses sep columns of the remaining width.
// stop is true when no width is left.
func scrollSeparator(pos, remaining, xOff, sep, shown int) (newPos, newRemaining int, stop bool) {
	if pos < xOff {
		if pos+sep <= xOff {
			pos += sep
		}
		return pos, remaining, false
	}
	if shown == 0 {
		return pos, remaining, false
	}
	if remaining == 0 {
		return pos, remaining, true
	}
	remaining -= sep
	return pos, remaining, remaining <= 0
}

// scrollPlanWidth returns the rendered width of a scroll plan: the visible
// column widths plus one separator column between each pair.
func scrollPlanWidth(widths []int) int {
	total := 0
	for i, w := range widths {
		total += w
		if i > 0 {
			total++
		}
	}
	return total
}

// computeScrollBaseWidths returns per-column widths based on current header
// titles and the visible rows window (ASCII lengths). This adapts to the data
// being shown in the viewport without scanning the entire dataset.
func (m *BigTable) computeScrollBaseWidths() []int {
	n := len(m.cols)
	if n == 0 {
		return nil
	}
	w := make([]int, n)
	// Start from header title widths.
	for i := range n {
		if m.cols[i].Title != "" {
			if l := utf8.RuneCountInString(m.cols[i].Title); l > w[i] {
				w[i] = l
			}
		}
	}
	// Include visible rows.
	for _, r := range m.window {
		_, cells, _, _ := r.Columns()
		for i := range n {
			var s string
			if i < len(cells) {
				s = cells[i]
			}
			if l := len(s); l > w[i] {
				w[i] = l
			}
		}
	}
	// Ensure a sensible minimum width of 1 per column.
	for i := range n {
		if w[i] < 1 {
			w[i] = 1
		}
	}
	m.shrinkToViewport(w)
	return w
}

// shrinkToViewport narrows the columns until the line fits the viewport,
// in this order:
//
//  1. a column with a MinWidth that is wider than capWidth narrows to it,
//     so one long name cannot take most of the line;
//  2. Fill columns narrow, down to their floor (fillFloor);
//  3. the columns with a MinWidth narrow, the widest first, down to it.
//
// A line that is still too wide pans.
func (m *BigTable) shrinkToViewport(w []int) {
	excess := scrollPlanWidth(w) - m.w
	if excess <= 0 {
		return
	}
	capWidth := m.w * 2 / 5
	excess = m.shrinkWidest(w, excess, func(i int) int {
		if m.cols[i].Fill || m.cols[i].MinWidth <= 0 {
			return w[i]
		}
		return max(capWidth, m.cols[i].MinWidth)
	})
	excess = m.shrinkWidest(w, excess, func(i int) int {
		if !m.cols[i].Fill {
			return w[i]
		}
		return m.fillFloor(i)
	})
	m.shrinkWidest(w, excess, func(i int) int {
		if m.cols[i].Fill || m.cols[i].MinWidth <= 0 {
			return w[i]
		}
		return m.cols[i].MinWidth
	})
}

// shrinkWidest takes up to excess cells from the columns, one at a time
// from the widest column above its floor, and returns the excess left.
func (m *BigTable) shrinkWidest(w []int, excess int, floor func(i int) int) int {
	for excess > 0 {
		widest := -1
		for i := range w {
			if w[i] > floor(i) && (widest < 0 || w[i] > w[widest]) {
				widest = i
			}
		}
		if widest < 0 {
			return excess
		}
		w[widest]--
		excess--
	}
	return 0
}

// fillFloor is the narrowest a Fill column gets: its MinWidth, or the
// width of its title.
func (m *BigTable) fillFloor(i int) int {
	if m.cols[i].MinWidth > 0 {
		return m.cols[i].MinWidth
	}
	return max(1, utf8.RuneCountInString(m.cols[i].Title))
}

// slackColumn returns the plan position of the column that takes the
// spare width of a line narrower than the viewport: the first Fill column,
// else the first column, like the Name column of a file manager. It
// returns -1 when that column is cut by horizontal panning.
func (m *BigTable) slackColumn(idx, cuts []int) int {
	for j, col := range idx {
		if m.cols[col].Fill {
			if cuts[j] != 0 {
				return -1
			}
			return j
		}
	}
	if len(idx) > 0 && idx[0] == 0 && cuts[0] == 0 {
		return 0
	}
	return -1
}

func asciiSlicePad(s string, start, width int) string {
	if width <= 0 {
		return ""
	}
	if start < 0 {
		start = 0
	}
	// ASCII only: byte indexing is safe and equals rune count
	if start >= len(s) {
		// pad entirely
		if width <= 0 {
			return ""
		}
		return strings.Repeat(" ", width)
	}
	end := min(start+width, len(s))
	out := s[start:end]
	if len(out) < width {
		out += strings.Repeat(" ", width-len(out))
	}
	return out
}

// ellipsize ends the slice s of a longer cell in "…" within width cells.
// A cell that is not ASCII after all may be cut inside a rune; the broken
// bytes are dropped and the cell is padded back to width.
func ellipsize(s string, width int) string {
	out := strings.ToValidUTF8(s[:len(s)-1], "") + "…"
	return out + strings.Repeat(" ", max(0, width-ansi.StringWidth(out)))
}

// rowsToStringRowsSliced slices the cells of the visible columns. full are
// the columns' widths in the plan: a cell longer than its column's width
// ends in "…" where the column ends.
func rowsToStringRowsSliced(rows []Row, idx, cuts, widths, full []int, spaceSep bool) [][]string {
	out := make([][]string, len(rows))
	for i := range rows {
		_, cells, _, _ := rows[i].Columns()
		row := make([]string, len(idx))
		for j, k := range idx {
			var cell string
			if k < len(cells) {
				cell = cells[k]
			}
			s := asciiSlicePad(cell, cuts[j], widths[j])
			if k < len(full) && len(cell) > full[k] && widths[j] > 0 && cuts[j]+widths[j] >= full[k] {
				s = ellipsize(s, widths[j])
			}
			if spaceSep && j < len(idx)-1 {
				s = ensureTrailingSpace(s)
			}
			row[j] = s
		}
		out[i] = row
	}
	return out
}

// rowsToStringRowsSubsetSep builds a subset and optionally appends a space
// separator to all but the last column.
func rowsToStringRowsSubsetSep(rows []Row, idx []int, spaceSep bool) [][]string {
	out := make([][]string, len(rows))
	for i := range rows {
		_, cells, _, _ := rows[i].Columns()
		row := make([]string, len(idx))
		for j, k := range idx {
			var cell string
			if k < len(cells) {
				cell = cells[k]
			}
			if spaceSep && j < len(idx)-1 {
				cell = ensureTrailingSpace(cell)
			}
			row[j] = cell
		}
		out[i] = row
	}
	return out
}

// titleSlicePad is asciiSlicePad by runes, for titles that carry a sort mark
// such as "AGE↓". The marks are one cell wide.
func titleSlicePad(s string, start, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	start = min(max(start, 0), len(r))
	r = r[start:min(len(r), start+width)]
	return string(r) + strings.Repeat(" ", width-len(r))
}

// alignTitle is alignASCII counting runes.
func alignTitle(s string, width int, pos lipgloss.Position) string {
	gap := width - utf8.RuneCountInString(s)
	if gap <= 0 {
		return s
	}
	return alignASCII(s, len(s)+gap, pos)
}

// alignASCII pads s to width according to the horizontal position (left,
// center or right). Strings that are already wide enough are returned as-is.
func alignASCII(s string, width int, pos lipgloss.Position) string {
	gap := width - len(s)
	if gap <= 0 {
		return s
	}
	left := 0
	switch pos {
	case lipgloss.Center:
		left = gap / 2
	case lipgloss.Right:
		left = gap
	}
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", gap-left)
}

func ensureTrailingSpace(s string) string {
	if len(s) == 0 || s[len(s)-1] != ' ' {
		return s + " "
	}
	return s
}

// repositionOnDataChange keeps the cursor stable by ID. If the previous
// focused row vanished, move to the next row; if none, to the previous; else
// clamp to bounds.
func (m *BigTable) repositionOnDataChange(ctx context.Context) {
	n := m.list.Len(ctx)
	if n <= 0 {
		m.cursor, m.top, m.focusedID = 0, 0, ""
		m.selected = make(map[string]struct{})
		return
	}
	// Prune selection for rows that disappeared.
	for id := range m.selected {
		if _, _, ok := m.list.Find(ctx, id); !ok {
			delete(m.selected, id)
		}
	}

	if m.focusedID == "" {
		if m.cursor >= n {
			m.cursor = n - 1
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
		return
	}
	if idx, _, ok := m.list.Find(ctx, m.focusedID); ok {
		m.cursor = idx
		return
	}
	if below := m.list.Below(ctx, m.focusedID, 1); len(below) > 0 {
		if id, _, _, ok := below[0].Columns(); ok {
			if idx, _, ok := m.list.Find(ctx, id); ok {
				m.cursor = idx
				m.focusedID = id
				return
			}
		}
	}
	if above := m.list.Above(ctx, m.focusedID, 1); len(above) > 0 {
		if id, _, _, ok := above[len(above)-1].Columns(); ok {
			if idx, _, ok := m.list.Find(ctx, id); ok {
				m.cursor = idx
				m.focusedID = id
				return
			}
		}
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}
