package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/help"
	"github.com/stokaro/kc/internal/overlay"
	uistyles "github.com/stokaro/kc/internal/ui/styles"
	"github.com/stokaro/kc/pkg/appconfig"
)

// Modal represents a modal dialog
type Modal struct {
	title            string
	content          tea.Model
	width            int
	height           int
	visible          bool
	onClose          func() tea.Cmd
	escPressed       bool
	closeOnSingleEsc bool
	// closeOnEscTimeout closes the modal when a lone Esc is not followed by a
	// digit (Esc+digit emulates F-keys) before the Esc timeout expires.
	closeOnEscTimeout bool
	// Windowed overlay support
	windowed       bool
	danger         bool // red: the dialog confirms a change to the cluster
	winWidth       int
	winHeight      int
	background     string        // full-screen base to overlay on when windowed
	backgroundFunc func() string // dynamic background provider
	windowOffsetX  int
	windowOffsetY  int
	windowHasPos   bool
	contentOffsetX int
	contentOffsetY int
	// drawn is where the last View put the window, frame included; zero
	// when the modal is not windowed.
	drawn          ModalWindow
	footerHotspots []footerHotspot
	footerCursor   *tea.Cursor
	mode           ModalMode
	escTimeout     time.Duration
	// name and manager are set by ModalManager.Register; F1 asks the
	// manager for the help page of this dialog.
	name    string
	manager *ModalManager
}

type modalEscapeHandler interface {
	HandleModalEscape(tea.KeyMsg) (bool, tea.Cmd)
}

// ModalMode selects the rendering style for a modal.
type ModalMode int

const (
	// ModalModeStacked renders the grey stacked modal overlay.
	ModalModeStacked ModalMode = iota
	// ModalModeFullscreen renders the blue fullscreen modal used by the viewer.
	ModalModeFullscreen
)

// ModalFooterHints allows content to contribute footer key hints
// rendered next to the default "Esc Close".
type ModalFooterHints interface {
	// FooterHints returns a list of key,label pairs to render in the footer.
	FooterHints() []FooterHint
}

// ModalFooterStatus allows content to append a free-form status string to the footer.
type ModalFooterStatus interface {
	FooterStatus(width int) string
}

// ModalFooterCursor allows content to expose a real cursor within the footer status area.
type ModalFooterCursor interface {
	FooterCursor(width int) *tea.Cursor
}

// ModalRules is content with separator rules across its whole width. A
// windowed Modal joins them to the frame with tees (├───┤), as mc does.
type ModalRules interface {
	// RuleRows are the content rows of the rules in the last View.
	RuleRows() []int
}

// joinContentRules joins the rules of content, when it reports any, to
// the frame box.
func joinContentRules(content tea.Model, box string) string {
	if rules, ok := content.(ModalRules); ok {
		return joinRules(box, rules.RuleRows())
	}
	return box
}

// joinRules replaces the side borders of the given content rows of box
// (the frame without its top line) with tees.
func joinRules(box string, rows []int) string {
	if len(rows) == 0 {
		return box
	}
	lines := strings.Split(box, "\n")
	for _, r := range rows {
		// The last line is the bottom border.
		if r < 0 || r >= len(lines)-1 {
			continue
		}
		l := lines[r]
		first, last := strings.Index(l, "│"), strings.LastIndex(l, "│")
		if first < 0 || first == last {
			continue
		}
		lines[r] = l[:first] + "├" + l[first+len("│"):last] + "┤" + l[last+len("│"):]
	}
	return strings.Join(lines, "\n")
}

// FooterHint describes a function key footer entry.
type FooterHint struct {
	Key     string
	Label   string
	Enabled bool
}

// Init initializes the modal
func (m *Modal) Init() tea.Cmd {
	return m.content.Init()
}

// NewModal creates a new modal dialog
func NewModal(title string, content tea.Model) *Modal {
	return &Modal{
		title:            title,
		content:          content,
		visible:          false,
		closeOnSingleEsc: true,
		mode:             ModalModeStacked,
		escTimeout:       defaultEscTimeout,
	}
}

// SetContent replaces the content model inside the modal.
func (m *Modal) SetContent(content tea.Model) { m.content = content }

// Show shows the modal
func (m *Modal) Show() {
	m.visible = true
}

// Hide hides the modal
func (m *Modal) Hide() {
	m.visible = false
}

// IsVisible returns true if the modal is visible
func (m *Modal) IsVisible() bool {
	return m.visible
}

// SetDimensions sets the modal dimensions
func (m *Modal) SetDimensions(width, height int) {
	m.width = width
	m.height = height
}

// SetOnClose sets the callback for when the modal is closed
func (m *Modal) SetOnClose(callback func() tea.Cmd) {
	m.onClose = callback
}

// SetCloseOnSingleEsc controls whether a lone Esc closes the modal. Esc+digit
// handling remains active regardless.
func (m *Modal) SetCloseOnSingleEsc(v bool) { m.closeOnSingleEsc = v }

// SetCloseOnEscTimeout makes a lone Esc close the modal once the Esc timeout
// expires, keeping Esc+digit available as F-key emulation. It only matters
// when closeOnSingleEsc is false.
func (m *Modal) SetCloseOnEscTimeout(v bool) { m.closeOnEscTimeout = v }

// SetEscTimeout configures the double-ESC timeout window.
func (m *Modal) SetEscTimeout(timeout time.Duration) {
	if timeout <= 0 {
		timeout = defaultEscTimeout
	}
	m.escTimeout = timeout
}

// SetWindowed configures the modal to render as a centered window of the given
// size over the provided background (full-screen base). If bg is empty, the
// window is centered over a blank backdrop.
func (m *Modal) SetWindowed(winW, winH int, bg string) {
	m.windowed = true
	m.winWidth, m.winHeight = winW, winH
	m.background = bg
	m.contentOffsetX = 0
	m.contentOffsetY = 0
	m.windowHasPos = false
}

// SetWindowedBackgroundProvider sets a function to produce the background
// view dynamically each render (e.g., for live preview under dialogs).
func (m *Modal) SetWindowedBackgroundProvider(f func() string) { m.backgroundFunc = f }

// SetWindowOffset positions the window relative to the main view's origin.
// Only applies when windowed=true.
func (m *Modal) SetWindowOffset(offsetX, offsetY int) {
	m.windowOffsetX = offsetX
	m.windowOffsetY = offsetY
	m.windowHasPos = true
}

// ModalWindow is the position and size of a windowed modal.
type ModalWindow struct {
	X, Y          int
	Width, Height int
}

// WindowPosition returns the configured window offset and size when windowed.
func (m *Modal) WindowPosition() (ModalWindow, bool) {
	if !m.windowed {
		return ModalWindow{}, false
	}
	return ModalWindow{X: m.windowOffsetX, Y: m.windowOffsetY, Width: m.winWidth, Height: m.winHeight}, true
}

// SetDanger draws the dialog in red, for confirming changes to the cluster.
func (m *Modal) SetDanger(on bool) { m.danger = on }

// SetMode configures the modal presentation style.
func (m *Modal) SetMode(mode ModalMode) { m.mode = mode }

// escAltKey turns a plain character typed after Esc into the matching Meta
// key: "a" becomes "alt+a". Keys with modifiers other than Shift, and keys
// that are not a single character, are left alone.
func escAltKey(msg tea.KeyMsg) (tea.KeyPressMsg, bool) {
	press, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return tea.KeyPressMsg{}, false
	}
	key := press.Key()
	if key.Mod&^tea.ModShift != 0 {
		return tea.KeyPressMsg{}, false
	}
	runes := []rune(key.Text)
	if len(runes) != 1 || !unicode.IsPrint(runes[0]) || unicode.IsSpace(runes[0]) {
		return tea.KeyPressMsg{}, false
	}
	return tea.KeyPressMsg(tea.Key{Code: runes[0], Mod: tea.ModAlt}), true
}

// Update handles messages and updates the modal state
//
//nolint:gocognit,gocyclo // the modal key and mouse switch, one case per message type; splitting it is a larger refactor than the lint baseline (#128)
func (m *Modal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !m.visible {
		return m, nil
	}

	var cmd tea.Cmd
	var cmds []tea.Cmd
	var rerouted tea.Msg

	// Handle modal-specific keys
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			if handler, ok := m.content.(modalEscapeHandler); ok {
				if handled, hcmd := handler.HandleModalEscape(msg); handled {
					if hcmd != nil {
						cmds = append(cmds, hcmd)
					}
					return m, tea.Batch(cmds...)
				}
			}
			// Double-ESC always closes, regardless of closeOnSingleEsc.
			if m.escPressed {
				m.escPressed = false
				m.Hide()
				if m.onClose != nil {
					cmd = m.onClose()
					cmds = append(cmds, cmd)
				}
				return m, tea.Batch(cmds...)
			}
			// Single ESC: close immediately only when enabled
			if m.closeOnSingleEsc {
				m.Hide()
				if m.onClose != nil {
					cmd = m.onClose()
					cmds = append(cmds, cmd)
				}
				return m, tea.Batch(cmds...)
			}
			// Otherwise arm ESC sequence to allow ESC ESC close
			m.escPressed = true
			return m, tea.Tick(m.escTimeout, func(time.Time) tea.Msg { return EscTimeoutMsg{} })
		case "1", "2", "3", "4", "5", "6", "7", "8", "9", "0":
			if m.escPressed {
				if fk, ok := functionKeyFromDigit(msg.String()); ok {
					m.escPressed = false
					rerouted = tea.KeyPressMsg(tea.Key{Code: fk})
					break
				}
				// Unhandled number: cancel esc sequence
				m.escPressed = false
				return m, nil
			}
		default:
			// Esc <char> is the fallback for M-<char> (Esc c copies the
			// command, Esc a the output), as on the panels.
			if m.escPressed {
				m.escPressed = false
				if alt, ok := escAltKey(msg); ok {
					rerouted = alt
				}
			}
		}
	case tea.MouseMsg:
		if m.wheelOutsideWindow(msg) {
			return m, nil
		}
		if model, cmd, handled := m.handleFooterMouse(msg); handled {
			return model, cmd
		}
	case EscTimeoutMsg:
		if m.escPressed {
			// Timeout expired without second ESC; cancel sequence.
			m.escPressed = false
			if m.closeOnEscTimeout {
				m.Hide()
				if m.onClose != nil {
					return m, m.onClose()
				}
				return m, nil
			}
			// For closeOnSingleEsc=false, do nothing (wait for explicit action)
			// For true, single ESC already closed immediately above.
			return m, nil
		}
	}

	// Update the content
	updateMsg := msg
	if rerouted != nil {
		updateMsg = rerouted
	}
	if mm, ok := msg.(tea.MouseMsg); ok {
		updateMsg = shiftMouseMsg(mm, m.contentOffsetX, m.contentOffsetY)
	}
	if key, ok := updateMsg.(tea.KeyMsg); ok && key.String() == "f1" {
		if page := m.helpPage(); page != "" && m.manager.openHelp != nil {
			return m, tea.Batch(append(cmds, m.manager.openHelp(page))...)
		}
	}
	model, cmd := m.content.Update(updateMsg)
	m.content = model
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

// helpPage returns the help page F1 opens over this dialog, or "" when the
// dialog has none (the help window itself).
func (m *Modal) helpPage() help.ID {
	if m.manager == nil || m.manager.helpPage == nil {
		return ""
	}
	return m.manager.helpPage(m.name)
}

// titleGrowth returns how much wider a window of width winW gets so that
// its title fits: the title, its padding, a line glyph on each side and the
// corners. It grows up to the screen width less the dialog padding; a title
// that still does not fit is cut (fitFrameTitle).
func titleGrowth(title string, winW, screenW int) int {
	need := lipgloss.Width(title) + 6
	if need <= winW {
		return 0
	}
	return max(0, min(need, screenW-2*dialogPadX)-winW)
}

// fitFrameTitle cuts title with "…" so that, with its padding and a line
// glyph on each side, it fits a top border of available columns.
func fitFrameTitle(title string, available int) string {
	room := available - 4
	if room < 1 {
		return ""
	}
	return ansi.Truncate(title, room, "…")
}

// View renders the modal and optionally returns a cursor when the content
// exposes one (e.g., viewer search input).
//
//nolint:gocognit,gocyclo,nestif // renders the modal modes (windowed, fullscreen, danger) in one pass; splitting it is a larger refactor than the lint baseline (#128)
func (m *Modal) View() tea.View {
	if !m.visible {
		return tea.NewView("")
	}
	m.footerHotspots = m.footerHotspots[:0]
	m.footerCursor = nil
	var contentCur *tea.Cursor
	var inner string
	// Fullscreen modal styled like panel
	// Reserve 1 line for overlay header and 1 terminal line for the function key bar outside the frame.
	// The framed box below has total height (m.height-2); its interior height is (m.height-2) - bottom border (1) = m.height-3.
	contentW := max(1, m.width-2)
	contentH := max(1, m.height-3)
	fullscreen := m.mode == ModalModeFullscreen
	if fullscreen {
		contentW = m.width
		// Viewers do not draw a bottom border; give them one extra row of
		// interior height so content reaches the footer line.
		contentH = max(1, m.height-2)
	}
	m.contentOffsetX = 0
	m.contentOffsetY = 0
	m.drawn = ModalWindow{}

	if m.windowed {
		// Render background (use provided base or blank)
		base := m.background
		if m.backgroundFunc != nil {
			if s := m.backgroundFunc(); s != "" {
				base = s
			}
		}
		if base == "" {
			base = lipgloss.NewStyle().Width(m.width).Height(m.height).Render("")
		}

		// Compute inner dimensions and render content
		bgWidth, bgHeight := lipgloss.Size(base)
		if bgWidth == 0 {
			bgWidth = m.width
		}
		if bgHeight == 0 {
			bgHeight = m.height
		}
		availableHeight := max(0, bgHeight-1) // leave room for footer outside
		winW := min(m.winWidth, bgWidth)
		if winW <= 0 {
			winW = bgWidth
		}
		grow := titleGrowth(m.title, winW, bgWidth)
		winW += grow
		winH := max(1, min(m.winHeight, max(1, availableHeight)))
		innerW := max(1, winW-2)
		innerH := max(1, winH-2)
		windowOffsetX := 0
		if bgWidth > winW {
			windowOffsetX = (bgWidth - winW) / 2
		}
		windowOffsetY := 0
		if availableHeight > winH {
			windowOffsetY = (availableHeight - winH) / 2
		}
		if m.windowHasPos {
			// A window widened for its title keeps its center.
			windowOffsetX = m.windowOffsetX - grow/2
			windowOffsetY = m.windowOffsetY
		}
		// Like mc, the frame sits inside a padded window that casts a
		// shadow; drop the padding when the screen is too small for it.
		padX, padY := dialogPadX, dialogPadY
		if winW+2*padX > bgWidth {
			padX = 0
		}
		if winH+2*padY > availableHeight {
			padY = 0
		}
		windowOffsetX = clampInt(windowOffsetX, padX, max(padX, bgWidth-winW-padX))
		windowOffsetY = clampInt(windowOffsetY, padY, max(padY, availableHeight-winH-padY))
		m.contentOffsetX = windowOffsetX + 1
		m.contentOffsetY = windowOffsetY + 1
		m.drawn = ModalWindow{X: windowOffsetX, Y: windowOffsetY, Width: winW, Height: winH}
		if setter, ok := m.content.(interface{ SetDimensions(int, int) }); ok {
			setter.SetDimensions(innerW, innerH)
		}
		inner, contentCur = renderModelView(m.content)

		frameBg := lipgloss.Color(uistyles.ColorModalBg)
		frameFg := lipgloss.Color(uistyles.ColorModalFg)
		titleFg := lipgloss.Color(uistyles.ColorModalTitle)
		if m.danger {
			frameBg = lipgloss.Color(uistyles.ColorDangerBg)
			frameFg = lipgloss.Color(uistyles.ColorDangerFg)
			titleFg = frameFg
		}
		boxStyle := lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(frameFg).
			BorderBackground(frameBg).
			Background(frameBg).
			Width(winW).
			Height(winH)

		labelStyle := lipgloss.NewStyle().
			Foreground(titleFg).
			Bold(m.danger).
			Background(frameBg).
			Padding(0, 1)
		border := boxStyle.GetBorderStyle()
		topBorderStyler := lipgloss.NewStyle().
			Foreground(boxStyle.GetBorderTopForeground()).
			Background(boxStyle.GetBorderTopBackground()).
			Render
		// In windowed settings dialogs we keep corners; viewers don't use windowed mode.
		topLeft := topBorderStyler(border.TopLeft)
		topRight := topBorderStyler(border.TopRight)
		available := winW - lipgloss.Width(topLeft+topRight)
		label := labelStyle.Render(fitFrameTitle(m.title, available))
		lw := lipgloss.Width(label)
		var top string
		if lw >= available {
			gap := strings.Repeat(border.Top, max(0, available-lw))
			top = topLeft + label + topBorderStyler(gap) + topRight
		} else {
			total := available - lw
			left := total / 2
			right := total - left
			top = topLeft + topBorderStyler(strings.Repeat(border.Top, left)) + label + topBorderStyler(strings.Repeat(border.Top, right)) + topRight
		}
		// Render inner content using modal palette.
		inner = lipgloss.NewStyle().
			Background(frameBg).
			Foreground(frameFg).
			Width(innerW).
			Height(innerH).
			Render(inner)

		winBottom := boxStyle.
			BorderTop(false).
			Width(winW).
			Height(winH - 1).
			Render(inner)
		winBottom = joinContentRules(m.content, winBottom)
		winFrame := padDialog(top+"\n"+winBottom, winW, padX, padY, frameBg)

		// Compose window over background (centered)
		composed := overlay.Composite(
			winFrame,
			base,
			overlay.Left, overlay.Top,
			windowOffsetX-padX, windowOffsetY-padY,
		)
		if padX > 0 || padY > 0 {
			composed = castShadow(composed, base, windowOffsetX-padX, windowOffsetY-padY, winW+2*padX, winH+2*padY)
		}
		bgLines := strings.Split(composed, "\n")
		// Footer line
		footer := m.buildFooter(false)
		if m.height > 0 {
			if len(bgLines) < m.height {
				for len(bgLines) < m.height {
					bgLines = append(bgLines, "")
				}
			}
			bgLines[m.height-1] = uistyles.FunctionKeyBarStyle.Width(m.width).Render(footer)
		}
		composed = strings.Join(bgLines, "\n")
		// Return composed screen without forcing a global background color so
		// the 2-line terminal retains its original styling.
		cursor := m.contentCursorForView(contentCur)
		if cursor == nil {
			cursor = m.footerCursorForView()
		}
		return viewWithCursor(
			lipgloss.NewStyle().
				Width(m.width).
				Height(m.height).
				Render(composed),
			cursor,
		)
	}
	if setter, ok := m.content.(interface{ SetDimensions(int, int) }); ok {
		setter.SetDimensions(contentW, contentH)
	}
	inner, contentCur = renderModelView(m.content)
	// Build frame with overlay title (match focused panel style)
	modalBg := lipgloss.Color(uistyles.ColorModalBg)
	modalFg := lipgloss.Color(uistyles.ColorModalFg)
	if fullscreen {
		modalBg = lipgloss.Blue
		modalFg = lipgloss.White
	}
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(modalFg).
		BorderBackground(modalBg).
		Background(modalBg)

	labelStyle := lipgloss.NewStyle().
		Foreground(modalFg).
		Background(lipgloss.Color(uistyles.ColorModalSelBg)).
		Padding(0, 1)
	if fullscreen {
		labelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Black).
			Background(lipgloss.Color(uistyles.ColorModalBg)).
			Padding(0, 1)
	}
	border := boxStyle.GetBorderStyle()
	topBorderStyler := lipgloss.NewStyle().
		Foreground(boxStyle.GetBorderTopForeground()).
		Background(boxStyle.GetBorderTopBackground()).
		Render

	// For viewers, do not show corner glyphs; use '-' for the entire top line.
	// For viewers, use the horizontal border rune for the entire top line,
	// including the ends, instead of corner glyphs.
	topRune := border.Top
	tl := border.TopLeft
	tr := border.TopRight
	if fullscreen {
		tl, tr = topRune, topRune
	}
	topLeft := topBorderStyler(tl)
	topRight := topBorderStyler(tr)
	available := m.width - lipgloss.Width(topLeft+topRight)
	label := labelStyle.Render(fitFrameTitle(m.title, available))
	lw := lipgloss.Width(label)
	var top string
	if lw >= available {
		gap := strings.Repeat(string(topRune), max(0, available-lw))
		top = topLeft + label + topBorderStyler(gap) + topRight
	} else {
		total := available - lw
		left := total / 2
		right := total - left
		top = topLeft + topBorderStyler(strings.Repeat(string(topRune), left)) + label + topBorderStyler(strings.Repeat(string(topRune), right)) + topRight
	}

	// Box content under header (no footer inside the frame)
	bottomSty := boxStyle.
		BorderTop(false).
		Width(m.width).
		// Reserve one terminal line for the footer outside the frame
		Height(m.height - 2)
	if fullscreen {
		// Remove vertical borders and bottom border for viewers.
		bottomSty = bottomSty.BorderLeft(false).BorderRight(false).BorderBottom(false)
	}
	bottom := bottomSty.Render(inner)
	if fullscreen {
		m.contentOffsetX = 0
	} else {
		m.contentOffsetX = 1
	}
	m.contentOffsetY = 1

	// Replace bottom corners to T junction at the top border of bottom
	lines := strings.Split(bottom, "\n")
	frame := top + "\n" + strings.Join(lines, "\n")
	// Function key bar outside the frame
	footer := m.buildFooter(true)
	footerLine := uistyles.FunctionKeyBarStyle.Width(m.width).Render(footer)
	cursor := m.contentCursorForView(contentCur)
	if cursor == nil {
		cursor = m.footerCursorForView()
	}
	return viewWithCursor(lipgloss.JoinVertical(lipgloss.Left, frame, footerLine), cursor)
}

func renderModelView(model tea.Model) (string, *tea.Cursor) {
	if model == nil {
		return "", nil
	}
	view := model.View()
	return viewString(view), view.Cursor
}

func viewWithCursor(content string, cursor *tea.Cursor) tea.View {
	view := tea.NewView(content)
	view.Cursor = cursor
	return view
}

func viewString(view tea.View) string {
	return view.Content
}

// wheelOutsideWindow reports whether msg is a wheel event away from the
// window of a windowed modal. Such events are dropped, so a list dialog
// scrolls only while the pointer is over it, as in mc, and the wheel never
// reaches the panels under the dialog. Fullscreen modals take every wheel
// event.
func (m *Modal) wheelOutsideWindow(msg tea.MouseMsg) bool {
	if _, ok := msg.(tea.MouseWheelMsg); !ok || !m.windowed {
		return false
	}
	p, w := msg.Mouse(), m.drawn
	return p.X < w.X || p.X >= w.X+w.Width || p.Y < w.Y || p.Y >= w.Y+w.Height
}

func shiftMouseMsg(msg tea.MouseMsg, dx, dy int) tea.MouseMsg {
	if dx == 0 && dy == 0 {
		return msg
	}
	mouse := msg.Mouse()
	mouse.X -= dx
	mouse.Y -= dy
	switch msg.(type) {
	case tea.MouseClickMsg:
		return tea.MouseClickMsg(mouse)
	case tea.MouseReleaseMsg:
		return tea.MouseReleaseMsg(mouse)
	case tea.MouseWheelMsg:
		return tea.MouseWheelMsg(mouse)
	case tea.MouseMotionMsg:
		return tea.MouseMotionMsg(mouse)
	default:
		return msg
	}
}

type footerHotspot struct {
	key     string
	start   int
	end     int
	enabled bool
}

func (m *Modal) handleFooterMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd, bool) {
	mouse := msg.Mouse()
	if mouse.Button != tea.MouseLeft {
		return nil, nil, false
	}
	if mouse.Y != m.height-1 {
		return nil, nil, false
	}
	// Swallow clicks on footer to prevent fallthrough to content.
	if _, ok := msg.(tea.MouseClickMsg); ok {
		return m, nil, true
	}
	if _, ok := msg.(tea.MouseReleaseMsg); !ok {
		return m, nil, true
	}
	key, enabled := m.keyAtFooterColumn(mouse.X)
	if key == "" || !enabled {
		return m, nil, true
	}
	if km, ok := keyMsgForLabel(key); ok {
		model, cmd := m.Update(km)
		return updatedModel(m, model), cmd, true
	}
	return m, nil, true
}

func (m *Modal) keyAtFooterColumn(x int) (string, bool) {
	for _, hs := range m.footerHotspots {
		if x >= hs.start && x < hs.end {
			return hs.key, hs.enabled
		}
	}
	return "", false
}

// buildFooter renders the modal's key bar in the layout of the panel function
// bar. Footers with function keys (F1..F10) get ten numbered slots like Midnight
// Commander; other footers get one slot per hint. Slots are spread over the full
// width unless a footer status (e.g. the search input) needs the space.
func (m *Modal) buildFooter(includeEsc bool) string {
	m.footerCursor = nil
	var hints []FooterHint
	if provider, ok := m.content.(ModalFooterHints); ok {
		for _, hint := range provider.FooterHints() {
			if hint.Key != "" && hint.Label != "" {
				hints = append(hints, hint)
			}
		}
	}
	numbered := false
	hasEsc := false
	for _, hint := range hints {
		if functionKeyNumber(hint.Key) > 0 {
			numbered = true
		}
		if strings.EqualFold(hint.Key, "esc") {
			hasEsc = true
		}
	}

	// "F1 Help" in the key bar of every dialog that has a help page, where
	// mc shows it. A content hint for F1 wins.
	withHelp := m.helpPage() != ""
	for _, hint := range hints {
		if functionKeyNumber(hint.Key) == 1 {
			withHelp = false
		}
	}

	var keys []functionKey
	var hotkeys []string
	if numbered {
		keys = make([]functionKey, 10)
		hotkeys = make([]string, 10)
		for i := range keys {
			keys[i] = functionKey{Key: fmt.Sprintf("%2d", i+1)}
			hotkeys[i] = fmt.Sprintf("F%d", i+1)
		}
		if withHelp {
			keys[0].Label, keys[0].Enabled = "Help", true
		}
	} else {
		if withHelp {
			keys = append(keys, functionKey{Key: " F1", Label: "Help", Enabled: true})
			hotkeys = append(hotkeys, "F1")
		}
		if includeEsc && m.closeOnSingleEsc && !hasEsc {
			keys = append(keys, functionKey{Key: " Esc", Label: "Close", Enabled: true})
			hotkeys = append(hotkeys, "Esc")
		}
	}
	for _, hint := range hints {
		if n := functionKeyNumber(hint.Key); n > 0 {
			keys[n-1].Label = hint.Label
			keys[n-1].Enabled = hint.Enabled
			continue
		}
		keys = append(keys, functionKey{Key: " " + hint.Key, Label: hint.Label, Enabled: hint.Enabled})
		hotkeys = append(hotkeys, hint.Key)
	}
	return m.renderFooterSlots(keys, hotkeys, numbered)
}

// footerStatus returns the content's footer status and the width it
// takes, or "" and 0 when the content has none. naturalTotal is the width
// the keys need.
func (m *Modal) footerStatus(naturalTotal int, numbered bool) (status string, width int) {
	statusProvider, ok := m.content.(ModalFooterStatus)
	if !ok {
		return "", 0
	}
	if numbered {
		// Give the status only the room its text needs (up to a third of
		// the line) so the numbered slots stay readable.
		width = m.width / 3
		if probe := statusProvider.FooterStatus(width); probe != "" {
			width = min(width, lipgloss.Width(strings.TrimRight(ansi.Strip(probe), " "))+1)
		}
	} else {
		width = m.width - naturalTotal - 1
	}
	if width > 0 {
		status = statusProvider.FooterStatus(width)
	}
	if strings.TrimSpace(ansi.Strip(status)) == "" {
		return "", 0
	}
	return status, width
}

func (m *Modal) renderFooterSlots(keys []functionKey, hotkeys []string, numbered bool) string {
	natural := make([]int, len(keys))
	naturalTotal := 0
	for i, k := range keys {
		natural[i] = lipgloss.Width(k.Key) + lipgloss.Width(k.Label) + 1
		naturalTotal += natural[i]
	}

	status, statusWidth := m.footerStatus(naturalTotal, numbered)

	barWidth := m.width
	var starts []int
	switch {
	case statusWidth == 0:
		starts = functionKeySlotStarts(len(keys), barWidth)
	case numbered:
		barWidth = max(0, m.width-statusWidth-1)
		starts = functionKeySlotStarts(len(keys), barWidth)
	default:
		barWidth = min(naturalTotal, m.width)
		starts = make([]int, len(keys)+1)
		for i, w := range natural {
			starts[i+1] = min(barWidth, starts[i]+w)
		}
	}

	for i, k := range keys {
		m.footerHotspots = append(m.footerHotspots, footerHotspot{
			key:     hotkeys[i],
			start:   starts[i],
			end:     starts[i+1],
			enabled: k.Enabled && strings.TrimSpace(k.Label) != "",
		})
	}
	footer := renderFunctionKeySlotsAt(keys, starts)
	if status == "" {
		return footer
	}
	start := barWidth + 1
	m.footerHotspots = append(m.footerHotspots, footerHotspot{
		start: start,
		end:   start + lipgloss.Width(status),
	})
	if cursorProvider, ok := m.content.(ModalFooterCursor); ok {
		if cur := cursorProvider.FooterCursor(statusWidth); cur != nil {
			clone := tea.NewCursor(start+cur.X, cur.Y)
			clone.Blink = cur.Blink
			clone.Color = cur.Color
			clone.Shape = cur.Shape
			m.footerCursor = clone
		}
	}
	return footer + " " + status
}

// functionKeyNumber returns n for "F<n>" with n in 1..10, or 0.
func functionKeyNumber(key string) int {
	if len(key) < 2 || (key[0] != 'F' && key[0] != 'f') {
		return 0
	}
	n, err := strconv.Atoi(key[1:])
	if err != nil || n < 1 || n > 10 {
		return 0
	}
	return n
}

func clampInt(v, minVal, maxVal int) int {
	if v < minVal {
		return minVal
	}
	if v > maxVal {
		return maxVal
	}
	return v
}

// keyModsForLabels returns the modifiers named in a key label such as
// "ctrl+alt+x"; unknown names are ignored.
func keyModsForLabels(names []string) tea.KeyMod {
	mod := tea.KeyMod(0)
	for _, name := range names {
		switch strings.ToLower(name) {
		case "ctrl", "control":
			mod |= tea.ModCtrl
		case "alt":
			mod |= tea.ModAlt
		case "shift":
			mod |= tea.ModShift
		case "meta":
			mod |= tea.ModMeta
		case "super":
			mod |= tea.ModSuper
		case "hyper":
			mod |= tea.ModHyper
		}
	}
	return mod
}

func keyMsgForLabel(label string) (tea.KeyMsg, bool) {
	if label == "" {
		return nil, false
	}
	lower := strings.ToLower(label)
	switch lower {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEsc}, true
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}, true
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}, true
	}
	if strings.HasPrefix(lower, "f") && len(label) > 1 {
		if n, err := strconv.Atoi(label[1:]); err == nil {
			if key, ok := functionKeyConstant(n); ok {
				return tea.KeyPressMsg{Code: key}, true
			}
		}
	}
	if strings.Contains(lower, "+") {
		parts := strings.Split(label, "+")
		if len(parts) < 2 {
			return nil, false
		}
		mod := keyModsForLabels(parts[:len(parts)-1])
		finalPart := parts[len(parts)-1]
		finalLower := strings.ToLower(finalPart)
		switch finalLower {
		case "esc":
			return tea.KeyPressMsg{Code: tea.KeyEsc, Mod: mod}, true
		case "enter":
			return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: mod}, true
		}
		runes := []rune(finalLower)
		if len(runes) == 1 {
			return tea.KeyPressMsg{Code: runes[0], Text: string(runes[0]), Mod: mod}, true
		}
		return nil, false
	}
	return nil, false
}

func functionKeyFromDigit(d string) (rune, bool) {
	switch d {
	case "1":
		return tea.KeyF1, true
	case "2":
		return tea.KeyF2, true
	case "3":
		return tea.KeyF3, true
	case "4":
		return tea.KeyF4, true
	case "5":
		return tea.KeyF5, true
	case "6":
		return tea.KeyF6, true
	case "7":
		return tea.KeyF7, true
	case "8":
		return tea.KeyF8, true
	case "9":
		return tea.KeyF9, true
	case "0":
		return tea.KeyF10, true
	default:
		return 0, false
	}
}

func (m *Modal) footerCursorForView() *tea.Cursor {
	if m.footerCursor == nil || m.height <= 0 {
		return nil
	}
	cur := tea.NewCursor(m.footerCursor.X, m.height-1)
	cur.Blink = m.footerCursor.Blink
	cur.Color = m.footerCursor.Color
	cur.Shape = m.footerCursor.Shape
	return cur
}

func (m *Modal) contentCursorForView(cur *tea.Cursor) *tea.Cursor {
	if cur == nil || m.height <= 0 {
		return nil
	}
	x := cur.X + m.contentOffsetX
	y := cur.Y + m.contentOffsetY
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	c := tea.NewCursor(x, y)
	c.Blink = cur.Blink
	c.Color = cur.Color
	c.Shape = cur.Shape
	return c
}

// functionKeyCodes are the codes of F1 to F24, in order.
var functionKeyCodes = [...]rune{
	tea.KeyF1, tea.KeyF2, tea.KeyF3, tea.KeyF4, tea.KeyF5, tea.KeyF6,
	tea.KeyF7, tea.KeyF8, tea.KeyF9, tea.KeyF10, tea.KeyF11, tea.KeyF12,
	tea.KeyF13, tea.KeyF14, tea.KeyF15, tea.KeyF16, tea.KeyF17, tea.KeyF18,
	tea.KeyF19, tea.KeyF20, tea.KeyF21, tea.KeyF22, tea.KeyF23, tea.KeyF24,
}

func functionKeyConstant(n int) (rune, bool) {
	if n < 1 || n > len(functionKeyCodes) {
		return 0, false
	}
	return functionKeyCodes[n-1], true
}

// ModalManager manages multiple modals
type ModalManager struct {
	modals map[string]*Modal
	stack  []string // modal name stack; top-most is last
	cfg    *appconfig.Config
	// helpPage names the help page of a dialog, "" for none; openHelp
	// shows a page over the open dialogs. See SetHelp.
	helpPage func(name string) help.ID
	openHelp func(help.ID) tea.Cmd
}

// SetHelp makes F1 in a dialog open its help page: page names the page of
// a registered dialog ("" for none) and open shows it.
func (mm *ModalManager) SetHelp(page func(name string) help.ID, open func(help.ID) tea.Cmd) {
	mm.helpPage = page
	mm.openHelp = open
}

// TopName returns the name of the dialog on top, or "".
func (mm *ModalManager) TopName() string {
	mm.dropHidden()
	if len(mm.stack) == 0 {
		return ""
	}
	return mm.stack[len(mm.stack)-1]
}

// Init initializes the modal manager
func (mm *ModalManager) Init() tea.Cmd {
	return nil
}

// NewModalManager creates a new modal manager
func NewModalManager(cfg *appconfig.Config) *ModalManager {
	if cfg == nil {
		cfg = appconfig.Default()
	}
	return &ModalManager{
		modals: make(map[string]*Modal),
		stack:  make([]string, 0),
		cfg:    cfg,
	}
}

// Register registers a modal
func (mm *ModalManager) Register(name string, modal *Modal) {
	modal.SetEscTimeout(mm.cfg.Input.EscTimeout.Duration)
	modal.name = name
	modal.manager = mm
	mm.modals[name] = modal
}

// SyncEscTimeout reapplies the configured ESC timeout to all managed modals.
func (mm *ModalManager) SyncEscTimeout() {
	timeout := mm.cfg.Input.EscTimeout.Duration
	if timeout <= 0 {
		timeout = defaultEscTimeout
	}
	for _, modal := range mm.modals {
		modal.SetEscTimeout(timeout)
	}
}

// Show shows a modal by name
func (mm *ModalManager) Show(name string) {
	if modal, exists := mm.modals[name]; exists {
		// If already on top, just ensure visible
		if len(mm.stack) > 0 && mm.stack[len(mm.stack)-1] == name {
			modal.Show()
			return
		}
		modal.Show()
		mm.stack = append(mm.stack, name)
	}
}

// Hide hides the active modal
func (mm *ModalManager) Hide() {
	if len(mm.stack) > 0 {
		top := mm.stack[len(mm.stack)-1]
		if modal, exists := mm.modals[top]; exists {
			modal.Hide()
		}
		mm.stack = mm.stack[:len(mm.stack)-1]
	}
}

// HideName hides a specific modal by name regardless of position.
func (mm *ModalManager) HideName(name string) {
	if modal, exists := mm.modals[name]; exists {
		modal.Hide()
	}
	for i := len(mm.stack) - 1; i >= 0; i-- {
		if mm.stack[i] == name {
			mm.stack = append(mm.stack[:i], mm.stack[i+1:]...)
			break
		}
	}
}

// IsModalVisible returns true if any modal is visible
func (mm *ModalManager) IsModalVisible() bool {
	mm.dropHidden()
	return len(mm.stack) > 0
}

// dropHidden removes dialogs that closed themselves (Esc Esc, the Esc
// timeout) from the top of the stack. Left there, they would keep taking
// every key and click while nothing is shown.
func (mm *ModalManager) dropHidden() {
	for len(mm.stack) > 0 {
		top, ok := mm.modals[mm.stack[len(mm.stack)-1]]
		if ok && top.visible {
			return
		}
		mm.stack = mm.stack[:len(mm.stack)-1]
	}
}

// GetActiveModal returns the active modal
func (mm *ModalManager) GetActiveModal() *Modal {
	mm.dropHidden()
	if len(mm.stack) > 0 {
		name := mm.stack[len(mm.stack)-1]
		return mm.modals[name]
	}
	return nil
}

// Update handles messages and updates the modal manager
func (mm *ModalManager) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if len(mm.stack) > 0 {
		name := mm.stack[len(mm.stack)-1]
		if modal, exists := mm.modals[name]; exists {
			model, cmd := modal.Update(msg)
			mm.modals[name] = updatedModel(mm.modals[name], model)
			// If the modal was closed externally, pop it safely
			if !modal.IsVisible() && len(mm.stack) > 0 && mm.stack[len(mm.stack)-1] == name {
				mm.stack = mm.stack[:len(mm.stack)-1]
			}
			return mm, cmd
		}
	}
	return mm, nil
}

// View renders the active modal (if any) and returns its view.
func (mm *ModalManager) View() tea.View {
	if len(mm.stack) > 0 {
		name := mm.stack[len(mm.stack)-1]
		if modal, exists := mm.modals[name]; exists {
			return modal.View()
		}
	}
	return tea.NewView("")
}

// sliceANSIByColumns returns a substring by visible columns, ignoring ANSI
// escape sequences for counting. It preserves escape sequences in the slice
// and terminates with a reset to avoid leaking attributes.
// sliceANSIColsRaw returns a raw ANSI slice by visible columns without
// appending a reset; useful for composing segments.
func sliceANSIColsRaw(s string, start, width int) string {
	if start < 0 {
		start = 0
	}
	if width <= 0 {
		return ""
	}
	var b strings.Builder
	col := 0
	collecting := false
	for i := 0; i < len(s); {
		if i < len(s) && s[i] == 0x1b { // ESC
			// CSI ... 'm'
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) && s[j] != 'm' {
					j++
				}
				if j < len(s) {
					j++
				}
			}
			if collecting {
				b.WriteString(s[i:j])
			}
			i = j
			continue
		}
		if col >= start && col < start+width {
			b.WriteByte(s[i])
			collecting = true
		}
		col++
		if col >= start+width {
			break
		}
		i++
	}
	return b.String()
}
