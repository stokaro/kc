package ui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/manifestdiff"
	"github.com/stokaro/kc/internal/ui/viewer"
)

// compareSnapshot holds the two manifests a comparison was computed from.
// It is read once and kept until an explicit refresh, so the diff does not
// move while the user reads it. It is never modified after it is built, so
// commands may render it on other goroutines.
type compareSnapshot struct {
	left, right manifestdiff.Side
}

// compareResultMsg carries a finished read or render back to its viewer.
type compareResultMsg struct {
	view *DiffViewer
	seq  int
	// snap is set when the manifests were read again; nil when only the
	// view options changed.
	snap   *compareSnapshot
	opts   manifestdiff.Options
	result manifestdiff.Result
	err    error
}

// renderComparison computes the diff of snap off the UI goroutine.
func renderComparison(view *DiffViewer, seq int, snap *compareSnapshot, opts manifestdiff.Options) tea.Cmd {
	return func() tea.Msg {
		res, err := manifestdiff.Compare(snap.left, snap.right, opts)
		return compareResultMsg{view: view, seq: seq, opts: opts, result: res, err: err}
	}
}

// DiffViewer shows the unified diff of two manifests. It is read-only;
// search, scrolling and copying work as in the other viewers.
type DiffViewer struct {
	inner  *viewer.Widget
	bar    viewerCommandBar
	width  int
	height int

	onOptions func() tea.Cmd
	onClose   func() tea.Cmd
	// load reads both manifests again and renders them with opts.
	load func(seq int, opts manifestdiff.Options) tea.Cmd

	leftCmd, rightCmd string // kubectl commands that print each manifest

	seq     int                  // id of the newest request; older results are dropped
	want    manifestdiff.Options // options of the newest request
	shown   manifestdiff.Options // options of the displayed result
	snap    *compareSnapshot
	result  manifestdiff.Result
	loaded  bool
	current int // index of the change last jumped to, -1 for none

	// headerRows is how many rows wrapping the header added above the
	// diff; the change rows of result move down by as many. laidOut is
	// the width the text was wrapped for.
	headerRows int
	laidOut    int
}

// NewDiffViewer creates the viewer in its loading state. leftCmd and
// rightCmd are the kubectl commands that print the two manifests.
func NewDiffViewer(theme, leftCmd, rightCmd string, load func(seq int, opts manifestdiff.Options) tea.Cmd, onClose func() tea.Cmd) *DiffViewer {
	v := &DiffViewer{
		inner:    viewer.New(theme),
		onClose:  onClose,
		load:     load,
		leftCmd:  leftCmd,
		rightCmd: rightCmd,
		current:  -1,
	}
	v.inner.SetCallbacks(nil, nil, onClose)
	v.inner.SetContent("Reading both manifests...", viewer.Metadata{Title: "compare", Language: "diff"})
	v.bar.command = compareCommand(leftCmd, rightCmd, false)
	return v
}

// Start issues the first read.
func (v *DiffViewer) Start() tea.Cmd { return v.request(nil, v.want) }

func (v *DiffViewer) request(snap *compareSnapshot, opts manifestdiff.Options) tea.Cmd {
	v.seq++
	v.want = opts
	if snap == nil {
		if v.load == nil {
			return nil
		}
		return v.load(v.seq, opts)
	}
	return renderComparison(v, v.seq, snap, opts)
}

// apply shows a finished result. A failed refresh keeps the previous diff
// on screen and reports the error on the command line.
func (v *DiffViewer) apply(msg compareResultMsg) tea.Cmd {
	if msg.seq != v.seq {
		return nil
	}
	if msg.err != nil {
		if !v.loaded {
			v.inner.SetContent("Compare failed: "+msg.err.Error(), viewer.Metadata{Title: "compare", Language: "diff"})
		}
		v.want = v.shown
		return v.flash("Compare failed: "+msg.err.Error(), true)
	}
	if msg.snap != nil {
		v.snap = msg.snap
	}
	v.result = msg.result
	v.shown = msg.opts
	v.loaded = true
	v.current = -1
	v.layout()
	v.bar.command = compareCommand(v.leftCmd, v.rightCmd, msg.opts.Mode == manifestdiff.ModeRaw)
	return nil
}

// layout shows the result with its header wrapped to the viewer's width.
func (v *DiffViewer) layout() {
	text, added := wrapCompareHeader(v.result.Text, v.textWidth(v.width))
	v.headerRows, v.laidOut = added, v.textWidth(v.width)
	v.inner.SetContent(text, viewer.Metadata{Title: "compare", Language: "diff", Filename: "compare.diff"})
}

// wrapCompareHeader wraps the "# name:   value" lines above the first empty
// line of a comparison to width, at the ", " between the parts of a
// value. The continuation rows start with "#" and line up with the value.
// It returns the text and how many rows it added. A part longer than the
// width stays whole.
func wrapCompareHeader(text string, width int) (string, int) {
	if width <= 0 {
		return text, 0
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	added := 0
	for i, line := range lines {
		if line == "" {
			out = append(out, lines[i:]...)
			break
		}
		wrapped := wrapHeaderLine(line, width)
		added += len(wrapped) - 1
		out = append(out, wrapped...)
	}
	return strings.Join(out, "\n"), added
}

func wrapHeaderLine(line string, width int) []string {
	colon := strings.Index(line, ":")
	if ansi.StringWidth(line) <= width || !strings.HasPrefix(line, "# ") || colon < 0 {
		return []string{line}
	}
	col := colon + 1
	for col < len(line) && line[col] == ' ' {
		col++
	}
	indent := "#" + strings.Repeat(" ", col-1)
	parts := strings.Split(line[col:], ", ")
	var out []string
	cur := line[:col] + parts[0]
	for i, p := range parts[1:] {
		// A row that more parts follow ends with a comma.
		comma := 0
		if i < len(parts)-2 {
			comma = 1
		}
		if ansi.StringWidth(cur)+len(", ")+ansi.StringWidth(p)+comma <= width {
			cur += ", " + p
			continue
		}
		out = append(out, cur+",")
		cur = indent + p
	}
	out = append(out, cur)
	// A part wider than the screen, such as a long file path, is cut into
	// pieces under the value.
	var rows []string
	for _, row := range out {
		rows = append(rows, splitHeaderRow(row, indent, width)...)
	}
	return rows
}

// splitHeaderRow cuts row into pieces of at most width; the pieces after
// the first start with indent. It leaves row whole when indent leaves no
// room.
func splitHeaderRow(row, indent string, width int) []string {
	room := width - len(indent)
	if ansi.StringWidth(row) <= width || room < 10 {
		return []string{row}
	}
	r := []rune(row)
	out := []string{string(r[:width])}
	for r = r[width:]; len(r) > room; r = r[room:] {
		out = append(out, indent+string(r[:room]))
	}
	return append(out, indent+string(r))
}

func (v *DiffViewer) flash(text string, isErr bool) tea.Cmd {
	cmd, _ := v.bar.handle(copyResultMsg{text: text, error: isErr}, v.inner, v.width, v.height)
	return cmd
}

// compareCommand is the shell command that prints the same comparison of
// the raw manifests.
func compareCommand(leftCmd, rightCmd string, raw bool) string {
	if leftCmd == "" || rightCmd == "" {
		return ""
	}
	if raw {
		leftCmd += " --show-managed-fields"
		rightCmd += " --show-managed-fields"
	}
	return fmt.Sprintf("diff -u <(%s) <(%s)", leftCmd, rightCmd)
}

func (v *DiffViewer) Init() tea.Cmd { return nil }

// SetDimensions sizes the viewer. A new width wraps the header again and
// keeps the same diff row at the top.
func (v *DiffViewer) SetDimensions(w, h int) {
	v.width, v.height = w, h
	if v.loaded && v.textWidth(w) != v.laidOut {
		v.relayout()
	}
}

// textWidth is the width of the shown text in a viewer w columns wide; 0
// before the viewer has a size, which leaves the header unwrapped.
func (v *DiffViewer) textWidth(w int) int {
	if w <= 0 {
		return 0
	}
	return v.inner.TextWidth(w)
}

// relayout wraps the header again and keeps the same diff row on top.
func (v *DiffViewer) relayout() {
	top := v.inner.Offset() - v.headerRows
	v.layout()
	if top > 0 {
		v.inner.ScrollToLine(top + v.headerRows)
	}
}

func (v *DiffViewer) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if cmd, handled := v.bar.handle(msg, v.inner, v.width, v.height); handled {
		return v, cmd
	}
	if key, ok := msg.(tea.KeyMsg); ok && !v.inner.SearchMode() {
		if cmd, handled := v.handleKey(key.String()); handled {
			return v, cmd
		}
	}
	if cmd, handled := v.inner.Update(msg); handled {
		return v, cmd
	}
	return v, nil
}

// handleKey takes the keys of mc's diff viewer: n, Space and Enter go to the
// next change, p and Backspace to the previous one.
func (v *DiffViewer) handleKey(key string) (tea.Cmd, bool) {
	switch key {
	case "n", "space", " ", "enter":
		v.jump(1)
		return nil, true
	case "p", "backspace":
		v.jump(-1)
		return nil, true
	case "r":
		if v.snap == nil {
			return nil, true
		}
		opts := v.want
		if opts.Mode == manifestdiff.ModeRaw {
			opts.Mode = manifestdiff.ModeConfig
		} else {
			opts.Mode = manifestdiff.ModeRaw
		}
		return v.request(v.snap, opts), true
	case "s":
		if v.snap == nil {
			return nil, true
		}
		opts := v.want
		opts.Mode = manifestdiff.ModeConfig
		opts.IncludeStatus = !opts.IncludeStatus
		return v.request(v.snap, opts), true
	case "ctrl+r":
		return v.request(nil, v.want), true
	}
	return nil, false
}

// jump scrolls to the next (dir > 0) or previous change. It counts from the
// change it jumped to last while that is still on or below the top line, so
// changes on the last page, which cannot scroll to the top, are reachable.
func (v *DiffViewer) jump(dir int) {
	lines := v.result.ChangeLines
	if !v.loaded || len(lines) == 0 {
		return
	}
	// The change rows of the result count without the wrapped header.
	anchor := v.inner.Offset() - v.headerRows
	if v.current >= 0 && lines[v.current] >= anchor {
		anchor = lines[v.current]
	}
	next := -1
	if dir > 0 {
		for i, l := range lines {
			if l > anchor {
				next = i
				break
			}
		}
	} else {
		for i, line := range slices.Backward(lines) {
			if line < anchor {
				next = i
				break
			}
		}
	}
	if next < 0 {
		return
	}
	v.current = next
	v.inner.ScrollToLine(lines[next] + v.headerRows)
}

func (v *DiffViewer) View() tea.View {
	if v.height <= 0 || v.width <= 0 {
		return tea.NewView("")
	}
	return tea.NewView(renderViewerWithBar(v.inner, &v.bar, v.width, v.height))
}

func (v *DiffViewer) FooterHints() []FooterHint {
	if v.inner.SearchMode() {
		return []FooterHint{
			{Key: "Enter", Label: "OK", Enabled: true},
			{Key: "Tab", Label: "Next field", Enabled: true},
			{Key: "Space", Label: "Toggle", Enabled: true},
			{Key: "Esc", Label: "Cancel", Enabled: true},
		}
	}
	hasChanges := v.loaded && len(v.result.ChangeLines) > 0
	view := "Raw"
	if v.shown.Mode == manifestdiff.ModeRaw {
		view = "Config"
	}
	// The footer has room for few letter keys; p (previous change) and s
	// (status) are documented in the README.
	return append([]FooterHint{
		{Key: "F2", Label: "Options", Enabled: v.onOptions != nil},
		{Key: "F3", Label: "Close", Enabled: v.onClose != nil},
		{Key: "F7", Label: "Search", Enabled: true},
		{Key: "n", Label: "Next", Enabled: hasChanges},
		{Key: "r", Label: view, Enabled: v.snap != nil},
		{Key: "C-r", Label: "Reload", Enabled: true},
	}, viewerCopyHints(v.bar.command != "")...)
}

// FooterStatus shows the search summary, or which change is in view.
func (v *DiffViewer) FooterStatus(width int) string {
	if s := v.inner.FooterStatusText(width); s != "" {
		return s
	}
	if !v.loaded || width <= 0 {
		return ""
	}
	n := len(v.result.ChangeLines)
	var s string
	switch {
	case n == 0:
		s = "no changes"
	case n == 1 && v.current < 0:
		s = "1 change"
	case v.current < 0:
		s = fmt.Sprintf("%d changes", n)
	default:
		s = fmt.Sprintf("change %d/%d", v.current+1, n)
	}
	return truncateStringToWidth(s, width)
}

func (v *DiffViewer) FooterCursor(width int) *tea.Cursor { return v.inner.FooterCursor(width) }

func (v *DiffViewer) HandleModalEscape(key tea.KeyMsg) (bool, tea.Cmd) {
	if key.String() != "esc" {
		return false, nil
	}
	if cmd, handled := v.inner.HandleEscape(); handled {
		return true, cmd
	}
	if v.inner.HasSelection() {
		v.inner.ClearSelection()
		return true, nil
	}
	return false, nil
}

func (v *DiffViewer) SetTheme(theme string) {
	v.inner.SetTheme(theme)
}

func (v *DiffViewer) Theme() string { return v.inner.Theme() }

// SetWrapMode turns wrapping on or off; the header is wrapped again for
// the width the viewer then shows.
func (v *DiffViewer) SetWrapMode(on bool) {
	v.inner.SetWrapMode(on)
	if v.loaded {
		v.relayout()
	}
}

func (v *DiffViewer) WrapMode() bool { return v.inner.WrapMode() }

func (v *DiffViewer) SetOnOptions(fn func() tea.Cmd) {
	v.onOptions = fn
	v.inner.SetCallbacks(nil, v.onOptions, v.onClose)
}
