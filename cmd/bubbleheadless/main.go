package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/taigrr/bubbleterm/emulator"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

const (
	defaultCols         = 120
	defaultRows         = 40
	snapshotCellWidth   = 8
	snapshotCellHeight  = 16
	snapshotBaselinePad = 4
)

var keyAliases = map[string]string{
	"enter":     "\r",
	"return":    "\r",
	"tab":       "\t",
	"backspace": "\b",
	"delete":    "\x7f",
	"esc":       "\x1b",
	"escape":    "\x1b",
	"space":     " ",
	"up":        "\x1b[A",
	"down":      "\x1b[B",
	"right":     "\x1b[C",
	"left":      "\x1b[D",
	"home":      "\x1b[H",
	"end":       "\x1b[F",
	"pageup":    "\x1b[5~",
	"pagedown":  "\x1b[6~",
	"insert":    "\x1b[2~",
	"f1":        "\x1bOP",
	"f2":        "\x1bOQ",
	"f3":        "\x1bOR",
	"f4":        "\x1bOS",
	"f5":        "\x1b[15~",
	"f6":        "\x1b[17~",
	"f7":        "\x1b[18~",
	"f8":        "\x1b[19~",
	"f9":        "\x1b[20~",
	"f10":       "\x1b[21~",
	"f11":       "\x1b[23~",
	"f12":       "\x1b[24~",
	"ctrl+a":    "\x01",
	"ctrl+b":    "\x02",
	"ctrl+c":    "\x03",
	"ctrl+d":    "\x04",
	"ctrl+e":    "\x05",
	"ctrl+f":    "\x06",
	"ctrl+g":    "\x07",
	"ctrl+h":    "\x08",
	"ctrl+i":    "\t",
	"ctrl+j":    "\n",
	"ctrl+k":    "\x0b",
	"ctrl+l":    "\x0c",
	"ctrl+m":    "\r",
	"ctrl+n":    "\x0e",
	"ctrl+o":    "\x0f",
	"ctrl+p":    "\x10",
	"ctrl+q":    "\x11",
	"ctrl+r":    "\x12",
	"ctrl+s":    "\x13",
	"ctrl+t":    "\x14",
	"ctrl+u":    "\x15",
	"ctrl+v":    "\x16",
	"ctrl+w":    "\x17",
	"ctrl+x":    "\x18",
	"ctrl+y":    "\x19",
	"ctrl+z":    "\x1a",
}

type session struct {
	mu     sync.Mutex
	emu    *emulator.Emulator
	cmd    *exec.Cmd
	exited bool
	exitCh chan struct{}

	// caption is drawn over color snapshots at captionAt (top or bottom)
	// until it is cleared.
	caption   string
	captionAt string
}

func newSession(cols, rows int) (*session, error) {
	if cols <= 0 || rows <= 0 {
		return nil, fmt.Errorf("invalid dimensions %dx%d", cols, rows)
	}

	emu, err := emulator.New(cols, rows)
	if err != nil {
		return nil, fmt.Errorf("create emulator: %w", err)
	}

	s := &session{
		emu:       emu,
		exitCh:    make(chan struct{}),
		captionAt: captionBottom,
	}
	emu.SetOnExit(func(string) {
		s.mu.Lock()
		s.exited = true
		s.mu.Unlock()
		close(s.exitCh)
	})
	return s, nil
}

// terminalVars name the terminal kc runs in. The emulator here is none of
// those terminals and draws no images, so the child must not see them:
// with them, kc's charts.graphics: auto would send Kitty graphics and
// draw placeholders in place of the text charts.
var terminalVars = []string{
	"TERM_PROGRAM", "TERM_PROGRAM_VERSION", "LC_TERMINAL", "LC_TERMINAL_VERSION",
	"KITTY_WINDOW_ID", "TMUX", "TMUX_PANE",
}

// childEnv is the environment of the command: environ without the
// variables of the outer terminal, with the emulator's size and TERM.
func childEnv(environ []string, w, h int) []string {
	env := make([]string, 0, len(environ)+3)
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if name == "TERM" || name == "COLUMNS" || name == "LINES" || slices.Contains(terminalVars, name) {
			continue
		}
		env = append(env, kv)
	}
	return append(env, fmt.Sprintf("COLUMNS=%d", w), fmt.Sprintf("LINES=%d", h), "TERM=xterm-256color")
}

func (s *session) startCommand(command []string) error {
	if len(command) == 0 {
		return errors.New("missing command to run")
	}

	// #nosec G204 -- the driver runs the command line it was started with.
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Env = childEnv(os.Environ(), s.emuWidth(), s.emuHeight())
	if wd, err := os.Getwd(); err == nil {
		cmd.Dir = wd
	}

	if err := s.emu.StartCommand(cmd); err != nil {
		return fmt.Errorf("start command: %w", err)
	}

	s.mu.Lock()
	s.cmd = cmd
	s.mu.Unlock()

	return nil
}

func (s *session) emuWidth() int {
	frame := s.emu.GetScreen()
	maxWidth := 0
	for _, row := range frame.Rows {
		w := ansi.StringWidth(ansi.Strip(row))
		if w > maxWidth {
			maxWidth = w
		}
	}
	if maxWidth == 0 {
		maxWidth = defaultCols
	}
	return maxWidth
}

func (s *session) emuHeight() int {
	frame := s.emu.GetScreen()
	if len(frame.Rows) == 0 {
		return defaultRows
	}
	return len(frame.Rows)
}

func (s *session) ensureRunning() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exited {
		return errors.New("process has exited")
	}
	return nil
}

func (s *session) sendKey(input string) error {
	if err := s.ensureRunning(); err != nil {
		return err
	}
	if input == "" {
		return errors.New("empty key input")
	}
	if _, err := s.emu.Write([]byte(input)); err != nil {
		return fmt.Errorf("send key: %w", err)
	}
	return nil
}

func (s *session) sendMouse(button, x, y int, pressed, motion bool) error {
	if err := s.ensureRunning(); err != nil {
		return err
	}
	if motion {
		button = -1
	}
	if err := s.emu.SendMouse(button, x, y, pressed); err != nil {
		return fmt.Errorf("send mouse: %w", err)
	}
	return nil
}

func (s *session) resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("invalid dimensions %dx%d", cols, rows)
	}
	if err := s.emu.Resize(cols, rows); err != nil {
		return fmt.Errorf("resize: %w", err)
	}
	return nil
}

func (s *session) terminate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.cmd.Process != nil {
		if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			return fmt.Errorf("terminate: %w", err)
		}
	}
	return nil
}

func printWelcome(command []string, cols, rows int) {
	fmt.Printf("bubbleheadless ready (cols=%d rows=%d)\n", cols, rows)
	fmt.Printf("running command: %s\n", strings.Join(command, " "))
	fmt.Println("commands: help, key <token>, text <value>, enter, screen [ansi|plain], snapshot <file> [color], caption [text], caption-at <top|bottom>, mouse <button> <x> <y> <press|release|motion>, resize <cols> <rows>, wait, exit")
}

func parseKeyToken(token string) (string, error) {
	token = strings.ToLower(token)
	if v, ok := keyAliases[token]; ok {
		return v, nil
	}
	if strings.HasPrefix(token, "0x") {
		// Parse as 32 bits so the conversion to rune cannot overflow, then
		// reject values that are not Unicode code points.
		parsed, err := strconv.ParseUint(token[2:], 16, 32)
		if err != nil || parsed > utf8.MaxRune || !utf8.ValidRune(rune(parsed)) {
			return "", fmt.Errorf("invalid hex token %q", token)
		}
		return string(rune(parsed)), nil
	}
	if strings.HasPrefix(token, "\\x") && len(token) == 4 {
		parsed, err := strconv.ParseUint(token[2:], 16, 8)
		if err != nil {
			return "", fmt.Errorf("invalid byte token %q", token)
		}
		return string(byte(parsed)), nil
	}
	if len(token) == 1 {
		return token, nil
	}
	return "", fmt.Errorf("unknown key token %q", token)
}

func snapshotToPNG(frame emulator.EmittedFrame, path string) error {
	stripped := make([]string, len(frame.Rows))
	maxWidth := 0
	for i, row := range frame.Rows {
		clean := ansi.Strip(row)
		clean = strings.ReplaceAll(clean, "\t", "    ")
		stripped[i] = clean
		width := ansi.StringWidth(clean)
		if width > maxWidth {
			maxWidth = width
		}
	}
	if maxWidth == 0 {
		maxWidth = 1
	}
	height := len(stripped)
	if height == 0 {
		stripped = []string{""}
		height = 1
	}

	imgWidth := maxWidth * snapshotCellWidth
	imgHeight := height * snapshotCellHeight

	img := image.NewRGBA(image.Rect(0, 0, imgWidth, imgHeight))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)

	drawer := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(color.Black),
		Face: basicfont.Face7x13,
	}
	baseline := snapshotCellHeight - snapshotBaselinePad

	for rowIdx, line := range stripped {
		x := 0
		for _, r := range line {
			drawer.Dot = fixed.Point26_6{
				X: fixed.I(x),
				Y: fixed.I(rowIdx*snapshotCellHeight + baseline),
			}
			drawer.DrawString(string(r))
			x += snapshotCellWidth
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directories: %w", err)
	}

	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create snapshot: %w", err)
	}
	defer file.Close()

	if err := png.Encode(file, img); err != nil {
		return fmt.Errorf("encode png: %w", err)
	}
	return nil
}

func main() {
	cols := flag.Int("cols", defaultCols, "terminal columns")
	rows := flag.Int("rows", defaultRows, "terminal rows")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: bubbleheadless [flags] -- <command> [args...]")
		os.Exit(2)
	}

	if err := run(args, *cols, *rows); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run starts the command in an emulator of cols x rows and reads driver
// commands from stdin until the input ends or an exit command arrives.
func run(args []string, cols, rows int) error {
	session, err := newSession(cols, rows)
	if err != nil {
		return fmt.Errorf("init session: %w", err)
	}
	defer session.emu.Close()

	if err := session.resize(cols, rows); err != nil {
		return fmt.Errorf("resize emulator: %w", err)
	}

	if err := session.startCommand(args); err != nil {
		return fmt.Errorf("start command: %w", err)
	}

	printWelcome(args, cols, rows)

	input := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !input.Scan() {
			break
		}
		line := strings.TrimSpace(input.Text())
		if line == "" {
			continue
		}
		if session.handleLine(line, func() { printWelcome(args, cols, rows) }) {
			return nil
		}
	}

	if err := input.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "input error: %v\n", err)
	}
	return nil
}

// handleLine runs one driver command. It reports true when the driver
// should stop.
func (s *session) handleLine(line string, help func()) bool {
	parts := strings.Fields(line)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case "help":
		help()
	case "key":
		s.cmdKey(parts)
	case "text":
		s.cmdText(line, parts)
	case "enter":
		if err := s.sendKey("\r"); err != nil {
			fmt.Printf("error: %v\n", err)
		}
	case "screen":
		s.cmdScreen(parts)
	case "snapshot":
		s.cmdSnapshot(parts)
	case "caption":
		s.caption = strings.TrimSpace(strings.TrimPrefix(line, parts[0]))
	case "caption-at":
		s.cmdCaptionAt(parts)
	case "mouse":
		s.cmdMouse(parts)
	case "resize":
		s.cmdResize(parts)
	case "wait":
		fmt.Println("waiting for process to exit...")
		<-s.exitCh
		fmt.Println("process exited")
	case "exit", "quit":
		if err := s.terminate(); err != nil {
			fmt.Printf("error terminating command: %v\n", err)
		}
		select {
		case <-s.exitCh:
		case <-time.After(500 * time.Millisecond):
		}
		return true
	default:
		fmt.Printf("unknown command %q (type 'help' for help)\n", cmd)
	}
	return false
}

func (s *session) cmdText(line string, parts []string) {
	if len(parts) < 2 {
		fmt.Println("usage: text <string>")
		return
	}
	payload := strings.TrimPrefix(line, parts[0]+" ")
	if err := s.sendKey(payload); err != nil {
		fmt.Printf("error: %v\n", err)
	}
}

func (s *session) cmdCaptionAt(parts []string) {
	if len(parts) != 2 || (parts[1] != captionTop && parts[1] != captionBottom) {
		fmt.Println("usage: caption-at <top|bottom>")
		return
	}
	s.captionAt = parts[1]
}

func (s *session) cmdKey(parts []string) {
	if len(parts) < 2 {
		fmt.Println("usage: key <token>")
		return
	}
	keyInput, err := parseKeyToken(parts[1])
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}
	if err := s.sendKey(keyInput); err != nil {
		fmt.Printf("error: %v\n", err)
	}
}

func (s *session) cmdScreen(parts []string) {
	mode := "plain"
	if len(parts) > 1 {
		mode = strings.ToLower(parts[1])
	}
	frame := s.emu.GetScreen()
	switch mode {
	case "ansi":
		for _, row := range frame.Rows {
			fmt.Println(row)
		}
	default:
		for _, row := range frame.Rows {
			fmt.Println(ansi.Strip(row))
		}
	}
}

func (s *session) cmdSnapshot(parts []string) {
	if len(parts) < 2 || len(parts) > 3 || (len(parts) == 3 && parts[2] != "color") {
		fmt.Println("usage: snapshot <path> [color]")
		return
	}
	path := parts[1]
	if !strings.Contains(path, string(os.PathSeparator)) {
		path = filepath.Join("snapshots", path)
	}
	var err error
	if len(parts) == 3 {
		err = colorSnapshotToPNG(s.emu.GetCells(), s.caption, s.captionAt, path)
	} else {
		err = snapshotToPNG(s.emu.GetScreen(), path)
	}
	if err != nil {
		fmt.Printf("error: %v\n", err)
	} else {
		fmt.Printf("saved snapshot: %s\n", path)
	}
}

func (s *session) cmdMouse(parts []string) {
	if len(parts) < 5 {
		fmt.Println("usage: mouse <button> <x> <y> <press|release|motion>")
		return
	}
	button, err := strconv.Atoi(parts[1])
	if err != nil {
		fmt.Printf("invalid button: %v\n", err)
		return
	}
	x, err := strconv.Atoi(parts[2])
	if err != nil {
		fmt.Printf("invalid x: %v\n", err)
		return
	}
	y, err := strconv.Atoi(parts[3])
	if err != nil {
		fmt.Printf("invalid y: %v\n", err)
		return
	}
	state := strings.ToLower(parts[4])
	pressed := state == "press"
	motion := state == "motion"
	if state != "press" && state != "release" && state != "motion" {
		fmt.Println("state must be press, release, or motion")
		return
	}
	if err := s.sendMouse(button, x, y, pressed, motion); err != nil {
		fmt.Printf("error: %v\n", err)
	}
}

func (s *session) cmdResize(parts []string) {
	if len(parts) < 3 {
		fmt.Println("usage: resize <cols> <rows>")
		return
	}
	colsVal, err := strconv.Atoi(parts[1])
	if err != nil {
		fmt.Printf("invalid cols: %v\n", err)
		return
	}
	rowsVal, err := strconv.Atoi(parts[2])
	if err != nil {
		fmt.Printf("invalid rows: %v\n", err)
		return
	}
	if err := s.resize(colsVal, rowsVal); err != nil {
		fmt.Printf("error: %v\n", err)
	} else {
		fmt.Printf("resized to %dx%d\n", colsVal, rowsVal)
	}
}
