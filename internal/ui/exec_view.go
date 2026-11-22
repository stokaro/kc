package ui

import (
	"context"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	bubbleterm "github.com/taigrr/bubbleterm"

	"github.com/stokaro/kc/internal/podexec"
	uistyles "github.com/stokaro/kc/internal/ui/styles"
)

// execStreamFunc connects an interactive remote command to stdin and
// stdout and blocks until it ends. podexec.Stream is the usual one.
type execStreamFunc func(ctx context.Context, stdin io.Reader, stdout io.Writer, sizes *podexec.SizeQueue) error

// execEndedMsg reports that the stream of execView id ended.
type execEndedMsg struct {
	id  int
	err error
}

// execView shows an interactive exec stream full screen: a terminal
// emulator fed by pipes instead of a local PTY, and one status line. It
// knows nothing about pods, so a container shell can use it as well.
type execView struct {
	id      int
	status  string
	term    *bubbleterm.Model
	sizes   *podexec.SizeQueue
	stdinR  *io.PipeReader
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	cancel  context.CancelFunc
	width   int
	height  int
}

// execViewSeq numbers exec views so a late end message of a closed view
// cannot close a newer one. Only the update goroutine touches it.
var execViewSeq int

func newExecView(width, height int, status string) (*execView, error) {
	width, height = max(1, width), max(2, height)
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	term, err := bubbleterm.NewWithPipes(width, height-1, stdoutR, stdinW)
	if err != nil {
		_ = stdinR.Close()
		_ = stdoutW.Close()
		return nil, err
	}
	term.SetAutoPoll(true)
	execViewSeq++
	return &execView{
		id:      execViewSeq,
		status:  status,
		term:    term,
		sizes:   podexec.NewSizeQueue(width, height-1),
		stdinR:  stdinR,
		stdoutR: stdoutR,
		stdoutW: stdoutW,
		width:   width,
		height:  height,
	}, nil
}

// start runs stream in the background. The returned command delivers
// execEndedMsg when the stream ends for any reason.
func (v *execView) start(ctx context.Context, stream execStreamFunc) tea.Cmd {
	ctx, v.cancel = context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		err := stream(ctx, v.stdinR, v.stdoutW, v.sizes)
		// End the emulator's read loop and the stream's stdin copy.
		_ = v.stdoutW.Close()
		_ = v.stdinR.Close()
		v.sizes.Close()
		done <- err
	}()
	id := v.id
	wait := func() tea.Msg { return execEndedMsg{id: id, err: <-done} }
	return tea.Batch(v.term.Init(), wait)
}

// stop ends the stream; execEndedMsg follows.
func (v *execView) stop() {
	if v.cancel != nil {
		v.cancel()
	}
}

// close stops the stream and releases the emulator. Closing the read end
// of stdout fails a write the stream is blocked in, so the stream sees
// the canceled context even when nobody reads its output any more.
func (v *execView) close() {
	v.stop()
	_ = v.stdoutR.CloseWithError(io.ErrClosedPipe)
	_ = v.term.Close()
}

func (v *execView) resize(width, height int) tea.Cmd {
	width, height = max(1, width), max(2, height)
	if width == v.width && height == v.height {
		return nil
	}
	v.width, v.height = width, height
	v.sizes.Push(width, height-1)
	model, cmd := v.term.Update(tea.WindowSizeMsg{Width: width, Height: height - 1})
	v.term = updatedModel(v.term, model)
	return cmd
}

// update hands keys, pastes and emulator messages to the terminal.
// The emulator ignores messages of other emulators.
func (v *execView) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		return writeNow(v.term.SendInput(pasteInput(msg.Content)))
	case tea.KeyMsg:
		model, cmd := v.term.Update(msg)
		v.term = updatedModel(v.term, model)
		return writeNow(cmd)
	case tea.MouseMsg:
		return nil
	}
	model, cmd := v.term.Update(msg)
	v.term = updatedModel(v.term, model)
	return cmd
}

// render returns the screen and the cursor.
func (v *execView) render() (string, *tea.Cursor) {
	content := viewString(v.term.View())
	lines := strings.Split(content, "\n")
	rows := v.height - 1
	if len(lines) > rows {
		lines = lines[:rows]
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	bar := lipgloss.NewStyle().
		Background(lipgloss.Color(uistyles.ColorDangerBg)).
		Foreground(lipgloss.Color(uistyles.ColorDangerFg)).
		Width(v.width).
		Render(truncateStringToWidth(" "+v.status, v.width))
	return strings.Join(append(lines, bar), "\n"), emulatorCursor(v.term)
}
