package ui

import (
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/go-logr/logr"
	bubbleterm "github.com/taigrr/bubbleterm"
)

// Terminal represents the bottom terminal component
type Terminal struct {
	// Bubbleterm terminal emulator
	terminal *bubbleterm.Model
	// Terminal state
	width  int
	height int
	// Display state
	showPanels bool // Whether panels are visible
	// Input tracking
	hasTyped bool // Whether user has typed since last command
	line     shellLine
	// The shell's lifecycle (shell_restart.go): kc starts a new shell
	// when one ends, and backs off when shells exit at once (#375).
	shell shellLife
	// blurred keeps Blur over a restart: a new emulator starts focused.
	blurred bool
	// Whether the PTY app has enabled mouse tracking (ESC[?1000h/1002h/1003h/1006h/1015h)
	ptyWantsMouse bool
	log           logr.Logger
	env           []string
	// cursor is the emulator cursor captured when the last frame arrived.
	// The emulator cursor is live while the text is the last frame, so a
	// live cursor can run ahead of the text (a blank cell shows first).
	cursor     *tea.Cursor
	haveCursor bool
}

// NewTerminal creates a new terminal instance
func NewTerminal() *Terminal {
	return &Terminal{
		width:      80,
		height:     24,
		showPanels: true, // Start with panels visible
		hasTyped:   false,
		shell:      shellLife{now: time.Now, quickExit: shellQuickExit, backoff: shellBackoff},
	}
}

// Init starts the shell.
func (t *Terminal) Init() tea.Cmd { return t.startShell() }

// shellCommand is the user's shell with the environment kc gives it. It is
// built for each start, so a new shell gets the KUBECONFIG of the active
// panel at that time.
func (t *Terminal) shellCommand() (*exec.Cmd, string) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash" // fallback
	}
	// #nosec G204 G702 -- the embedded terminal runs the user's own $SHELL.
	cmd := exec.Command(shell)
	env := t.env
	if len(env) == 0 {
		env = os.Environ()
	}
	cmd.Env = env
	return cmd, shell
}

// SetEnv overrides the environment used when spawning the PTY shell.
func (t *Terminal) SetEnv(env []string) { t.env = append([]string(nil), env...) }

// Update handles messages and updates the terminal state
func (t *Terminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Handle window resize events
	if msg, ok := msg.(tea.WindowSizeMsg); ok {
		msg.Width = max(1, msg.Width)
		msg.Height = max(1, msg.Height)

		t.width = msg.Width
		t.height = msg.Height
		if t.terminal != nil {
			model, cmd := t.terminal.Update(msg)
			t.terminal = updatedModel(t.terminal, model)
			return t, cmd
		}
		return t, nil
	}

	// Always update bubbleterm first to check process status
	//nolint:nestif // paste, mouse and key input to the PTY differ only in small steps
	if t.terminal != nil {
		if paste, ok := msg.(tea.PasteMsg); ok {
			if t.showPanels || t.hasTyped {
				t.hasTyped = true
				t.line.paste(paste.Content)
			} else {
				t.line.invalidate()
			}
			return t, writeNow(t.terminal.SendInput(pasteInput(paste.Content)))
		}
		// Swallow mouse events while panels are visible to avoid sending
		// mouse escape sequences into the PTY (which shows up as garbage
		// in the 2-line terminal view). In fullscreen terminal mode, let
		// mouse events pass through to bubbleterm only if the app requested it.
		// In fullscreen mode bubbleterm and the app in the terminal decide
		// whether to act on mouse events.
		if _, ok := msg.(tea.MouseMsg); ok && t.showPanels {
			// Panels visible → ignore mouse for PTY; panels handle mouse themselves
			return t, nil
		}
		model, cmd := t.terminal.Update(translateKeyForTerminal(msg))
		t.terminal = updatedModel(t.terminal, model)
		if _, isKey := msg.(tea.KeyMsg); isKey {
			t.trackTyped(msg)
			return t, writeNow(cmd)
		}
		if _, isMouse := msg.(tea.MouseMsg); !isMouse {
			// A poll result (or other emulator message): take the cursor
			// that belongs to this frame.
			t.snapshotCursor()
		}

		return t, cmd
	}

	return t, nil
}

// SetPTYWantsMouse allows wiring a detector that toggles whether the PTY app
// requested mouse events (e.g., by parsing ESC[?1000h/l sequences from the
// app's stdout). TODO: hook into bubbleterm emulator output to call this.
func (t *Terminal) SetPTYWantsMouse(on bool) { t.ptyWantsMouse = on }

// View renders the terminal
func (t *Terminal) View() tea.View {
	if t.terminal == nil {
		// No shell runs: the status says why, on the one shell line under
		// the panels or wrapped across the full-screen terminal.
		if t.showPanels {
			view, cur := t.renderInlineViewWithCursor()
			return viewWithCursor(view, cur)
		}
		return viewWithCursor(ansi.Wrap(t.shell.statusText(), max(1, t.width), ""), nil)
	}

	if t.showPanels {
		view, cur := t.renderInlineViewWithCursor()
		return viewWithCursor(view, cur)
	}

	terminalView, cursor := t.terminalView()
	return viewWithCursor(terminalView, cursor)
}

// inlineTerminalLines is the height of the shell area shown under the panels.
// Like the Midnight Commander command line it is a single line: the line with
// the shell cursor.
const inlineTerminalLines = 1

// renderInlineViewWithCursor renders the inline shell area and returns its cursor.
func (t *Terminal) renderInlineViewWithCursor() (string, *tea.Cursor) {
	if t.terminal == nil {
		// No shell runs: say why, and when the next one starts, on one
		// line cut to the terminal's width. It is not padded: before the
		// first resize the width is a guess, and the app pads the line.
		return ansi.Truncate(t.shell.statusText(), max(1, t.width), "…"), nil
	}

	terminalView, termCur := t.terminalView()
	return renderInlineFrom(terminalView, termCur, t.width, inlineTerminalLines)
}

func (t *Terminal) terminalView() (string, *tea.Cursor) {
	if t.terminal == nil {
		return "", nil
	}
	switch term := any(t.terminal).(type) {
	case interface{ View() tea.View }:
		view := term.View()
		if t.haveCursor {
			return viewString(view), cloneCursor(t.cursor)
		}
		return viewString(view), emulatorCursor(t.terminal)
	case interface{ View() (string, *tea.Cursor) }:
		return term.View()
	case interface{ View() string }:
		return term.View(), nil
	default:
		return "", nil
	}
}

// renderInlineFrom cuts height lines out of a full terminal view. With a
// cursor it shows the cursor line and the lines above it, and moves the cursor
// to the last line; without one it shows the last lines of the view. The
// result always has exactly height lines rendered to width.
func renderInlineFrom(terminalView string, termCur *tea.Cursor, width, height int) (string, *tea.Cursor) {
	if height < 1 {
		height = 1
	}
	terminalLines := strings.Split(terminalView, "\n")
	render := func(lines []string) string {
		return lipgloss.NewStyle().
			Width(width).
			Height(height).
			Render(strings.Join(lines, "\n"))
	}

	if termCur == nil {
		start := max(0, len(terminalLines)-height)
		lines := append([]string(nil), terminalLines[start:]...)
		for len(lines) < height {
			lines = append(lines, "")
		}
		return render(lines), nil
	}

	y := clampInt(termCur.Y, 0, len(terminalLines)-1)
	start := max(0, y+1-height)
	lines := append([]string(nil), terminalLines[start:y+1]...)
	for len(lines) < height {
		lines = append([]string{""}, lines...)
	}
	cur := *termCur
	cur.Y = height - 1
	cur.Blink = true
	return render(lines), &cur
}

// snapshotCursor stores the emulator cursor for the frame just applied.
func (t *Terminal) snapshotCursor() {
	if t.terminal == nil {
		return
	}
	t.cursor = emulatorCursor(t.terminal)
	t.haveCursor = true
}

// writeNow runs a bubbleterm input command at once. bubbleterm returns PTY
// writes as tea.Cmds, and Bubble Tea runs commands concurrently, so fast
// typing could reach the shell out of order ("echo" became "ehco"). The
// write is a short PTY write; an error message is still delivered.
func writeNow(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	return func() tea.Msg { return msg }
}

// emulatorCursor reads the cursor of a bubbleterm model, nil when hidden.
func emulatorCursor(term *bubbleterm.Model) *tea.Cursor {
	if term == nil || term.GetEmulator() == nil {
		return nil
	}
	pos, show := term.GetEmulator().Cursor()
	if !show {
		return nil
	}
	return tea.NewCursor(pos.X, pos.Y)
}

func cloneCursor(c *tea.Cursor) *tea.Cursor {
	if c == nil {
		return nil
	}
	cp := *c
	return &cp
}

// trackTyped records whether the user typed into the inline shell line.
func (t *Terminal) trackTyped(msg tea.Msg) {
	if press, ok := msg.(tea.KeyPressMsg); ok {
		switch {
		case press.String() == "enter", press.String() == "ctrl+c":
			// Both end the shell line, in the full-screen terminal too.
			t.line.reset()
			t.hasTyped = false
			return
		case t.showPanels || t.hasTyped:
			// Text typed under the panels is still on the shell line in
			// the full-screen terminal until Enter or C-c.
			t.line.key(press)
		default:
			// Otherwise a key in the full-screen terminal may go to
			// another program (after Enter, say); kc stops following.
			t.line.invalidate()
		}
	}
	if !t.showPanels {
		return
	}
	if key, ok := msg.(tea.KeyMsg); ok {
		// Enter runs the command and gives the keys back to the panels.
		t.hasTyped = key.String() != "enter"
	}
}

// pasteInput prepares pasted text for the PTY. Multi-line text is wrapped in
// bracketed paste markers so a shell does not run each line as it arrives;
// shells with line editing (zsh, bash 5.1+, fish) enable bracketed paste.
// Single-line text is sent as is, which is safe either way.
func pasteInput(content string) string {
	if !strings.ContainsAny(content, "\r\n") {
		return content
	}
	content = strings.ReplaceAll(content, "\r\n", "\r")
	content = strings.ReplaceAll(content, "\n", "\r")
	return "\x1b[200~" + content + "\x1b[201~"
}

// translateKeyForTerminal returns the message unchanged: Bubble Tea v2 key
// messages reach the terminal as they are.
func translateKeyForTerminal(msg tea.Msg) tea.Msg {
	return msg
}

// SetShowPanels sets whether panels are visible
func (t *Terminal) SetShowPanels(show bool) {
	t.showPanels = show
	// Ensure proper focus when switching modes
	if !show {
		// When going to fullscreen mode, focus the terminal
		t.Focus()
	}
	// Terminal dimensions are managed by the app through WindowSizeMsg forwarding
}

// Focus sets focus on the terminal
func (t *Terminal) Focus() {
	t.blurred = false
	if t.terminal != nil {
		t.terminal.Focus()
		// Also ensure the terminal is focused by calling it again
		// This helps with some focus issues
		t.terminal.Focus()
	}
}

// Blur removes focus from the terminal
func (t *Terminal) Blur() {
	t.blurred = true
	if t.terminal != nil {
		t.terminal.Blur()
	}
}

// Focused returns whether the terminal is focused
func (t *Terminal) Focused() bool {
	if t.terminal != nil {
		return t.terminal.Focused()
	}
	return false
}

// SendInput sends input to the terminal
func (t *Terminal) SendInput(input string) {
	if t.terminal != nil {
		t.terminal.SendInput(input)
	}
}

// HasInput returns whether the terminal has non-empty input. When kc knows
// the shell line, an erased or blank line counts as empty; otherwise any key
// typed since the last Enter or C-c counts.
func (t *Terminal) HasInput() bool {
	if !t.hasTyped {
		return false
	}
	if line, ok := t.line.current(); ok {
		return strings.TrimSpace(line) != ""
	}
	return true
}

// ClearTyped resets the typed flag (used to return focus to panels).
func (t *Terminal) ClearTyped() { t.hasTyped = false }

// ShellLine returns the text typed on the shell line since the last
// command, and false when kc cannot know it (see shellLine).
func (t *Terminal) ShellLine() (string, bool) { return t.line.current() }

// DiscardLine clears the shell's input line with C-u (kill the line in
// bash, zsh and fish with the cursor at the end) and returns the focus to
// the panels.
func (t *Terminal) DiscardLine() tea.Cmd {
	t.line.reset()
	t.hasTyped = false
	if t.terminal == nil {
		return nil
	}
	return writeNow(t.terminal.SendInput("\x15"))
}

// SetLogger installs a logger for terminal lifecycle events.
func (t *Terminal) SetLogger(log logr.Logger) { t.log = log }

// Close shuts down the underlying emulator/PTY.
func (t *Terminal) Close() error {
	if t == nil || t.terminal == nil {
		return nil
	}
	return t.terminal.Close()
}
