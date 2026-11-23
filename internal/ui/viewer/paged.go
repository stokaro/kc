package viewer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/overlay"
	"github.com/stokaro/kc/internal/pagedfile"
	uistyles "github.com/stokaro/kc/internal/ui/styles"
)

// PagedMsg is a message of a paged viewer: a block read, a search result
// or a redraw tick. The app hands it to PagedTarget even when a dialog
// covers the viewer.
type PagedMsg interface {
	PagedTarget() *PagedWidget
}

type pagedBlockMsg struct {
	w    *PagedWidget
	idx  int64
	data []byte
	size int64
	err  error
}

type pagedSearchMsg struct {
	w     *PagedWidget
	gen   int
	match pagedfile.Match
	err   error
}

type pagedScanMsg struct {
	w     *PagedWidget
	index []pagedfile.Counter
	total int64
	err   error
}

type pagedTickMsg struct{ w *PagedWidget }

func (m pagedBlockMsg) PagedTarget() *PagedWidget  { return m.w }
func (m pagedSearchMsg) PagedTarget() *PagedWidget { return m.w }
func (m pagedScanMsg) PagedTarget() *PagedWidget   { return m.w }
func (m pagedTickMsg) PagedTarget() *PagedWidget   { return m.w }

const (
	// pagedTickInterval redraws the progress of a search or a line count.
	pagedTickInterval = 150 * time.Millisecond
	// pagedMaxOps bounds the moves waiting for blocks.
	pagedMaxOps = 32
	// pagedSniffBytes is how much of the first block is checked for a NUL
	// byte, as for a local file.
	pagedSniffBytes = 8000
)

// pagedOp is a move that needs blocks: it changes the widget and returns
// nil, or returns the missing blocks and changes nothing.
type pagedOp func(w *PagedWidget) []int64

// PagedWidget shows a file too large to hold in memory. It keeps a bounded
// cache of blocks (pagedfile.Document), reads blocks with Cmds off the
// update loop, and shows a placeholder line where a block is still on its
// way. It shows plain text: no syntax highlighting.
type PagedWidget struct {
	meta  Metadata
	src   pagedfile.Source
	doc   *pagedfile.Document
	opts  pagedfile.Options
	theme string

	// spawn returns a Cmd that runs fn with a context that Close cancels,
	// and the cancel of that context alone.
	spawn  func(fn func(context.Context) tea.Msg) (tea.Cmd, context.CancelFunc)
	cancel context.CancelFunc
	closed bool

	width, height int
	wrapMode      bool
	hOffset       int
	// top is the start of the first visible line.
	top int64

	pending map[int64]bool
	failed  map[int64]error
	ops     []pagedOp
	binary  bool

	scanning     bool
	scanned      bool
	ticking      bool
	scanProgress *atomic.Int64

	searchDialog   *searchDialog
	searchQuery    string
	searchOpts     SearchOptions
	searchCompiled *compiledSearch
	searching      bool
	searchGen      int
	searchCancel   context.CancelFunc
	searchProgress *atomic.Int64
	active         pagedfile.Match
	hasActive      bool
	searchNote     string

	onEdit, onOptions, onClose func() tea.Cmd
}

// compiledSearch is a search expression with the match function the
// streaming search and the drawing share.
type compiledSearch struct {
	match pagedfile.MatchFunc
}

// NewPaged shows src, a file of size bytes. Reads run under a child of
// ctx; Close cancels them and closes src. Start returns the first reads.
func NewPaged(ctx context.Context, src pagedfile.Source, size int64, meta Metadata, opts pagedfile.Options) *PagedWidget {
	ctx, cancel := context.WithCancel(ctx)
	w := &PagedWidget{
		meta:    meta,
		src:     src,
		doc:     pagedfile.NewDocument(size, opts),
		opts:    opts,
		cancel:  cancel,
		pending: make(map[int64]bool),
		failed:  make(map[int64]error),
	}
	w.spawn = func(fn func(context.Context) tea.Msg) (tea.Cmd, context.CancelFunc) {
		child, stop := context.WithCancel(ctx)
		return func() tea.Msg { return fn(child) }, stop
	}
	return w
}

// Start reads the blocks of the first page.
func (w *PagedWidget) Start() tea.Cmd { return w.follow() }

// Close cancels pending reads and a running search and closes the source.
func (w *PagedWidget) Close() {
	if w.closed {
		return
	}
	w.closed = true
	w.cancel()
	_ = w.src.Close()
}

// Document returns the block cache, for tests and the status line.
func (w *PagedWidget) Document() *pagedfile.Document { return w.doc }

// Top returns the offset of the first visible line.
func (w *PagedWidget) Top() int64 { return w.top }

// SetCallbacks sets the F4, F2 and close handlers.
func (w *PagedWidget) SetCallbacks(onEdit, onOptions, onClose func() tea.Cmd) {
	w.onEdit, w.onOptions, w.onClose = onEdit, onOptions, onClose
}

// SetWrapMode wraps long lines instead of scrolling them sideways.
func (w *PagedWidget) SetWrapMode(on bool) {
	w.wrapMode = on
	if on {
		w.hOffset = 0
	}
}

// WrapMode reports whether lines wrap.
func (w *PagedWidget) WrapMode() bool { return w.wrapMode }

// SetTheme records the theme; paged text is not highlighted.
func (w *PagedWidget) SetTheme(theme string) { w.theme = theme }

// Theme returns the recorded theme.
func (w *PagedWidget) Theme() string { return w.theme }

// Metadata returns the file's metadata.
func (w *PagedWidget) Metadata() Metadata { return w.meta }

// SearchMode reports whether the search dialog is open.
func (w *PagedWidget) SearchMode() bool { return w.searchDialog != nil }

// Searching reports whether a search runs.
func (w *PagedWidget) Searching() bool { return w.searching }

// HasSearchMatches reports whether n and N have a search to repeat.
func (w *PagedWidget) HasSearchMatches() bool { return w.searchCompiled != nil }

// HandleEscape closes the search dialog or cancels a running search.
func (w *PagedWidget) HandleEscape() (tea.Cmd, bool) {
	switch {
	case w.searchDialog != nil:
		w.searchDialog = nil
		return nil, true
	case w.searching:
		w.stopSearch()
		w.searchNote = "search canceled"
		return nil, true
	}
	return nil, false
}

func (w *PagedWidget) page() int { return max(1, w.height) }

// Resize sets the size of the page.
func (w *PagedWidget) Resize(width, height int) { w.width, w.height = width, height }

// Update handles keys, the mouse wheel and the widget's own messages.
func (w *PagedWidget) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case PagedMsg:
		if m.PagedTarget() != w {
			return nil, false
		}
		return w.handleOwn(msg), true
	case tea.PasteMsg:
		if w.searchDialog != nil && w.searchDialog.focus == searchFocusInput {
			var cmd tea.Cmd
			w.searchDialog.input, cmd = w.searchDialog.input.Update(m)
			return cmd, true
		}
	case tea.KeyMsg:
		if w.searchDialog != nil {
			return w.handleSearchDialog(m), true
		}
		cmd, handled := w.handleKey(m.String())
		if handled {
			return tea.Batch(cmd, w.follow()), true
		}
	case tea.MouseWheelMsg:
		switch tea.Mouse(m).Button {
		case tea.MouseWheelUp:
			w.push(opUp(1))
			return w.follow(), true
		case tea.MouseWheelDown:
			w.push(opDown(1))
			return w.follow(), true
		}
	}
	return nil, false
}

func (w *PagedWidget) handleOwn(msg tea.Msg) tea.Cmd {
	if w.closed {
		return nil
	}
	switch m := msg.(type) {
	case pagedBlockMsg:
		delete(w.pending, m.idx)
		if m.err != nil {
			w.failed[m.idx] = m.err
			return w.follow()
		}
		w.doc.Put(m.idx, m.data, m.size)
		if m.idx == 0 && bytes.IndexByte(m.data[:min(len(m.data), pagedSniffBytes)], 0) >= 0 {
			w.binary = true
			w.stopSearch()
		}
		return w.follow()
	case pagedSearchMsg:
		if m.gen != w.searchGen || !w.searching {
			return nil
		}
		w.stopSearch()
		return w.searchDone(m.match, m.err)
	case pagedScanMsg:
		w.scanning = false
		if m.err == nil {
			w.scanned = true
			w.doc.AdoptIndex(m.index, m.total)
		}
		return nil
	case pagedTickMsg:
		w.ticking = false
		return w.tick()
	}
	return nil
}

// tick redraws the progress while a search or a line count runs. One tick
// is on its way at a time.
func (w *PagedWidget) tick() tea.Cmd {
	if w.ticking || (!w.searching && !w.scanning) {
		return nil
	}
	w.ticking = true
	return tea.Tick(pagedTickInterval, func(time.Time) tea.Msg { return pagedTickMsg{w: w} })
}

func (w *PagedWidget) handleKey(key string) (tea.Cmd, bool) {
	// A key retries the blocks that failed to read.
	clear(w.failed)
	switch key {
	case "up", "k":
		w.push(opUp(1))
	case "down", "j":
		w.push(opDown(1))
	case "pgup", "ctrl+b":
		w.push(opUp(w.page()))
	case "pgdown":
		w.push(opDown(w.page()))
	case "home", "g":
		w.ops = nil
		w.top, w.hOffset = 0, 0
	case "end", "G":
		return w.toEnd(), true
	case "left", "h":
		w.scrollSideways(-1)
	case "right", "l":
		w.scrollSideways(1)
	case "ctrl+a":
		w.hOffset = 0
	case "ctrl+e":
		w.hOffset = w.maxHorizontalOffset()
	case "ctrl+f", "f7", "/":
		if w.binary {
			return nil, true
		}
		w.searchDialog = newSearchDialog(w.searchQuery, w.searchOpts)
		return w.searchDialog.focusCmd(), true
	case "shift+f7", "n":
		return w.repeatSearch(!w.searchOpts.Backwards)
	case "N":
		return w.repeatSearch(w.searchOpts.Backwards)
	case "f2":
		return runCallback(w.onOptions)
	case "f3", "f10":
		return runCallback(w.onClose)
	case "f4":
		return runCallback(w.onEdit)
	default:
		return nil, false
	}
	return nil, true
}

// toEnd shows the last page. The last block is read again first, so a file
// that grew shows its new end. A local file is counted in the background,
// so the line numbers follow.
func (w *PagedWidget) toEnd() tea.Cmd {
	w.ops = nil
	w.hOffset = 0
	if size := w.doc.Size(); size > 0 && !w.pending[(size-1)/w.doc.BlockSize()] {
		w.doc.Drop((size - 1) / w.doc.BlockSize())
	}
	w.push(opEnd)
	return w.scan()
}

// scan counts the lines of a local file once, in the background.
func (w *PagedWidget) scan() tea.Cmd {
	if w.scanning || w.scanned || !pagedfile.IsLocal(w.src) {
		return nil
	}
	if _, ok := w.doc.Lines(); ok {
		return nil
	}
	w.scanning = true
	progress := new(atomic.Int64)
	w.scanProgress = progress
	src, opts := w.src, w.opts
	cmd, _ := w.spawn(func(ctx context.Context) tea.Msg {
		index, total, err := pagedfile.Scan(ctx, src, opts, progress)
		return pagedScanMsg{w: w, index: index, total: total, err: err}
	})
	return tea.Batch(cmd, w.tick())
}

func (w *PagedWidget) push(op pagedOp) {
	if len(w.ops) >= pagedMaxOps {
		return
	}
	w.ops = append(w.ops, op)
}

// follow runs the moves whose blocks are there and reads the blocks the
// next move and the visible lines need, and one block on either side.
func (w *PagedWidget) follow() tea.Cmd {
	if w.closed {
		return nil
	}
	var missing []int64
	for len(w.ops) > 0 {
		need := w.ops[0](w)
		if need == nil {
			w.ops = w.ops[1:]
			continue
		}
		if w.allFailed(need) {
			w.ops = w.ops[1:] // the move cannot complete; drop it
			continue
		}
		missing = need
		break
	}
	lines, need := w.visible()
	missing = append(missing, need...)
	cmd := w.read(missing)
	if len(missing) > 0 || len(lines) == 0 {
		return cmd
	}
	// Read ahead one block on either side of the page.
	bs := w.doc.BlockSize()
	var ahead []int64
	if first := w.top / bs; first > 0 {
		ahead = append(ahead, first-1)
	}
	if next := lines[len(lines)-1].off/bs + 1; next*bs < w.doc.Size() {
		ahead = append(ahead, next)
	}
	cached, _ := w.doc.CachedBlocks()
	ahead = slices.DeleteFunc(ahead, func(b int64) bool { return slices.Contains(cached, b) })
	return w.read(ahead)
}

func (w *PagedWidget) allFailed(blocks []int64) bool {
	for _, b := range blocks {
		if _, ok := w.failed[b]; !ok {
			return false
		}
	}
	return true
}

// read starts a read of each block that is not pending or failed. The
// document names a cached block as missing when it is shorter than the
// file now is, so cached blocks are read again too.
func (w *PagedWidget) read(blocks []int64) tea.Cmd {
	var cmds []tea.Cmd
	for _, b := range blocks {
		if w.pending[b] || w.failed[b] != nil {
			continue
		}
		w.pending[b] = true
		cmd, _ := w.spawn(w.readBlock(b))
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

func (w *PagedWidget) readBlock(b int64) func(context.Context) tea.Msg {
	src, bs := w.src, w.doc.BlockSize()
	return func(ctx context.Context) tea.Msg {
		buf := make([]byte, bs)
		n := 0
		var err error
		for n < len(buf) && err == nil {
			var k int
			k, err = src.ReadAt(ctx, buf[n:], b*bs+int64(n))
			n += k
			if k == 0 && err == nil {
				break
			}
		}
		if errors.Is(err, io.EOF) {
			err = nil
		}
		size, _ := src.Size()
		return pagedBlockMsg{w: w, idx: b, data: buf[:n], size: size, err: err}
	}
}

// visibleLine is a line of the page: its start and text, or a line whose
// block is still on its way (missing) or failed to read (err).
type visibleLine struct {
	off     int64
	text    string
	missing bool
	err     error
}

// visible returns the lines of the page and the blocks missing for them.
func (w *PagedWidget) visible() ([]visibleLine, []int64) {
	page := w.page()
	out := make([]visibleLine, 0, page)
	off := w.top
	for len(out) < page && off < w.doc.Size() {
		text, next, missing := w.doc.Line(off)
		if missing != nil {
			line := visibleLine{off: off, missing: true}
			for _, b := range missing {
				if err := w.failed[b]; err != nil {
					line.err = err
				}
			}
			return append(out, line), missing
		}
		out = append(out, visibleLine{off: off, text: string(text)})
		off = next
	}
	return out, nil
}

// Moves -----------------------------------------------------------------

func opDown(n int) pagedOp {
	return func(w *PagedWidget) []int64 {
		off := w.top
		for range n {
			_, next, missing := w.doc.Line(off)
			if missing != nil {
				return missing
			}
			if next >= w.doc.Size() {
				break
			}
			off = next
		}
		top, missing := w.clampTop(off)
		if missing != nil {
			return missing
		}
		w.top = top
		return nil
	}
}

func opUp(n int) pagedOp {
	return func(w *PagedWidget) []int64 {
		off := w.top
		for i := 0; i < n && off > 0; i++ {
			prev, missing := w.doc.Prev(off)
			if missing != nil {
				return missing
			}
			off = prev
		}
		w.top = off
		return nil
	}
}

func opEnd(w *PagedWidget) []int64 {
	top, missing := w.endTop()
	if missing != nil {
		return missing
	}
	w.top = top
	return nil
}

// opShow scrolls so the line at line is in the upper half of the page.
func opShow(line int64) pagedOp {
	return func(w *PagedWidget) []int64 {
		off := line
		for i := 0; i < w.page()/2 && off > 0; i++ {
			prev, missing := w.doc.Prev(off)
			if missing != nil {
				return missing
			}
			off = prev
		}
		top, missing := w.clampTop(off)
		if missing != nil {
			return missing
		}
		w.top = top
		w.hOffset = 0
		return nil
	}
}

// clampTop returns t, or the top of the last page when fewer than a page
// of lines follow t.
func (w *PagedWidget) clampTop(t int64) (int64, []int64) {
	off := t
	for range w.page() {
		if off >= w.doc.Size() {
			return w.endTop()
		}
		_, next, missing := w.doc.Line(off)
		if missing != nil {
			return 0, missing
		}
		off = next
	}
	return t, nil
}

// endTop returns the start of the first line of the last page.
func (w *PagedWidget) endTop() (int64, []int64) {
	off := w.doc.Size()
	for i := 0; i < w.page() && off > 0; i++ {
		prev, missing := w.doc.Prev(off)
		if missing != nil {
			return 0, missing
		}
		off = prev
	}
	return off, nil
}

func (w *PagedWidget) scrollSideways(delta int) {
	if w.wrapMode {
		w.hOffset = 0
		return
	}
	w.hOffset = min(max(0, w.hOffset+delta), w.maxHorizontalOffset())
}

func (w *PagedWidget) viewportWidth() int {
	if w.width <= 0 {
		return 1
	}
	if !w.wrapMode && w.width > 2 {
		return w.width - 2
	}
	return w.width
}

// maxHorizontalOffset is measured on the visible lines: the width of the
// whole file is not known.
func (w *PagedWidget) maxHorizontalOffset() int {
	if w.wrapMode {
		return 0
	}
	lines, _ := w.visible()
	widest := 0
	for _, l := range lines {
		widest = max(widest, lipgloss.Width(l.text))
	}
	return max(0, widest-w.viewportWidth())
}

// Search ----------------------------------------------------------------

func (w *PagedWidget) handleSearchDialog(key tea.KeyMsg) tea.Cmd {
	result, cmd := w.searchDialog.update(key)
	switch result {
	case searchDialogAccepted:
		return tea.Batch(cmd, w.commitSearch())
	case searchDialogCanceled:
		w.searchDialog = nil
	}
	return cmd
}

func (w *PagedWidget) commitSearch() tea.Cmd {
	d := w.searchDialog
	query := d.query()
	re, err := compileSearch(query, d.opts)
	if err != nil {
		d.err = err.Error()
		return nil
	}
	w.searchDialog = nil
	w.searchQuery, w.searchOpts = query, d.opts
	w.hasActive, w.searchNote = false, ""
	w.stopSearch()
	if re == nil {
		w.searchCompiled = nil
		return nil
	}
	w.searchCompiled = &compiledSearch{match: func(text []byte) [][2]int {
		if bytes.IndexByte(text, '\x1b') >= 0 {
			text = []byte(ansi.Strip(string(text)))
		}
		var out [][2]int
		for _, m := range re.FindAllIndex(text, -1) {
			if m[1] > m[0] {
				out = append(out, [2]int{m[0], m[1]})
			}
		}
		return out
	}}
	// Like the whole-file viewer: forward from the top of the page,
	// backward from its bottom.
	if w.searchOpts.Backwards {
		lines, _ := w.visible()
		from := w.top
		if len(lines) > 0 {
			from = lines[len(lines)-1].off
		}
		return w.startSearch(from, math.MaxInt, true)
	}
	return w.startSearch(w.top, 0, false)
}

// repeatSearch looks for the next match after the active one, forward or
// backward.
func (w *PagedWidget) repeatSearch(forward bool) (tea.Cmd, bool) {
	if w.searchCompiled == nil || w.binary {
		return nil, false
	}
	switch {
	case !w.hasActive && forward:
		return w.startSearch(w.top, 0, false), true
	case !w.hasActive:
		return w.startSearch(w.top, math.MaxInt, true), true
	case forward:
		return w.startSearch(w.active.Line, w.active.Start+1, false), true
	default:
		return w.startSearch(w.active.Line, w.active.Start, true), true
	}
}

func (w *PagedWidget) startSearch(line int64, col int, backwards bool) tea.Cmd {
	w.stopSearch()
	w.searchGen++
	gen := w.searchGen
	progress := new(atomic.Int64)
	w.searchProgress = progress
	w.searchNote = ""
	req := pagedfile.SearchRequest{
		Match: w.searchCompiled.match, Line: line, Col: col,
		Backwards: backwards, Wrap: true, Options: w.opts, Progress: progress,
	}
	src := w.src
	cmd, cancel := w.spawn(func(ctx context.Context) tea.Msg {
		m, err := pagedfile.Search(ctx, src, req)
		return pagedSearchMsg{w: w, gen: gen, match: m, err: err}
	})
	w.searchCancel = cancel
	w.searching = true
	return tea.Batch(cmd, w.tick())
}

func (w *PagedWidget) stopSearch() {
	if w.searchCancel != nil {
		w.searchCancel()
		w.searchCancel = nil
	}
	w.searching = false
}

func (w *PagedWidget) searchDone(m pagedfile.Match, err error) tea.Cmd {
	switch {
	case err == nil:
		w.active, w.hasActive = m, true
		w.ops = nil
		w.push(opShow(m.Line))
		return tea.Batch(w.follow(), w.scan())
	case errors.Is(err, pagedfile.ErrNoMatch):
		w.hasActive = false
		w.searchNote = "no match"
	case errors.Is(err, context.Canceled):
		w.searchNote = "search canceled"
	default:
		w.searchNote = "search failed: " + err.Error()
	}
	return nil
}

// Drawing ---------------------------------------------------------------

// View draws the page: the lines that are read, a placeholder where a
// block is on its way, and the search dialog on top.
func (w *PagedWidget) View(frame Frame) string {
	w.width, w.height = frame.Width, frame.Height
	contentW, contentH := max(1, frame.Width), max(1, frame.Height)
	rows := w.rows()
	style := uistyles.PanelContentStyle.Height(contentH).MaxHeight(contentH)
	if w.wrapMode {
		style = style.Width(contentW).MaxWidth(contentW)
	}
	if frame.Focused {
		style = style.Bold(true)
	}
	rendered := style.Render(strings.Join(rows, "\n"))
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

func (w *PagedWidget) rows() []string {
	viewWidth := w.viewportWidth()
	result := make([]string, w.page())
	if w.binary {
		name := w.meta.Filename
		if name == "" {
			name = w.meta.Title
		}
		result[0] = applyPanelBackground(name+" is a binary file; F5 saves it", w.width)
		for i := 1; i < len(result); i++ {
			result[i] = applyPanelBackground("", w.width)
		}
		return result
	}
	lines, _ := w.visible()
	for i := range result {
		if i >= len(lines) {
			result[i] = applyPanelBackground("", w.width)
			continue
		}
		line := lines[i]
		if line.missing {
			text := "~ reading..."
			if line.err != nil {
				text = "~ read failed: " + line.err.Error() + " (a key retries)"
			}
			result[i] = applyPanelBackground(placeholderStyle.Render(text), w.width)
			continue
		}
		matches := w.lineMatches(line)
		if w.wrapMode {
			result[i] = renderWrappedLine(line.text, matches)
			continue
		}
		result[i] = renderLineWithMatches(trimANSI(line.text, w.hOffset, viewWidth), viewWidth, matches, w.hOffset)
	}
	return result
}

var placeholderStyle = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)

// lineMatches finds the search matches of a visible line and marks the
// active one.
func (w *PagedWidget) lineMatches(line visibleLine) []displayMatch {
	if w.searchCompiled == nil {
		return nil
	}
	plain := line.text
	if strings.IndexByte(plain, '\x1b') >= 0 {
		plain = ansi.Strip(plain)
	}
	found := w.searchCompiled.match([]byte(line.text))
	out := make([]displayMatch, 0, len(found))
	prevEnd, prevWidth := 0, 0
	for _, m := range found {
		if m[0] < prevEnd || m[1] > len(plain) {
			continue
		}
		start := prevWidth + lipgloss.Width(plain[prevEnd:m[0]])
		end := start + lipgloss.Width(plain[m[0]:m[1]])
		prevEnd, prevWidth = m[1], end
		out = append(out, displayMatch{
			start:  start,
			end:    end,
			active: w.hasActive && w.active.Line == line.off && w.active.Start == m[0],
		})
	}
	return out
}

// StatusText describes the paged view for the footer: that it is paged,
// the file size, the position and a running search or line count.
func (w *PagedWidget) StatusText() string {
	// The mode comes first: a narrow footer cuts the end.
	parts := []string{"paged, " + humanBytes(w.doc.Size()) + ", highlighting off", w.positionText()}
	if w.scanning && w.scanProgress != nil {
		parts = append(parts, "counting lines "+percent(w.scanProgress.Load(), w.doc.Size()))
	}
	return strings.Join(parts, " · ")
}

func (w *PagedWidget) positionText() string {
	size := w.doc.Size()
	if size == 0 {
		return "empty"
	}
	if line, ok := w.doc.LineNumber(w.top); ok {
		if total, ok := w.doc.Lines(); ok {
			return fmt.Sprintf("line %d/%d", line+1, total)
		}
		return fmt.Sprintf("line %d", line+1)
	}
	// The lines above are not counted: show where the page starts in
	// the bytes of the file.
	return "~" + percent(w.top, size)
}

// FooterStatusText is the search state while a search ran, else "".
func (w *PagedWidget) FooterStatusText(width int) string {
	if width <= 0 || w.searchDialog != nil {
		return ""
	}
	summary := w.searchSummary()
	if summary == "" {
		return ""
	}
	return searchInputStyle.Width(width).Render(trimToWidth(summary, width))
}

func (w *PagedWidget) searchSummary() string {
	query := strings.TrimSpace(w.searchQuery)
	if query == "" || w.searchCompiled == nil {
		return ""
	}
	switch {
	case w.searching:
		done := int64(0)
		if w.searchProgress != nil {
			done = w.searchProgress.Load()
		}
		return fmt.Sprintf("/%s searching %s (Esc cancels)", query, percent(done, w.doc.Size()))
	case w.searchNote != "":
		return fmt.Sprintf("/%s (%s)", query, w.searchNote)
	case w.hasActive:
		if line, ok := w.doc.LineNumber(w.active.Line); ok {
			return fmt.Sprintf("/%s (line %d; n next, N previous)", query, line+1)
		}
		return fmt.Sprintf("/%s (at ~%s; n next, N previous)", query, percent(w.active.Line, w.doc.Size()))
	}
	return ""
}

func percent(n, total int64) string {
	if total <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%d%%", min(100, n*100/total))
}

// humanBytes formats a size like the panels do: 512 B, 1.5 KiB, 1.2 GiB.
func humanBytes(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	const unit = 1024
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}
