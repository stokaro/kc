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
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/taigrr/bubbleterm/emulator"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/stokaro/kc/internal/ui/kittygfx"
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

// maxSize bounds the columns and rows of the emulator, so that its size in
// pixels fits the PTY's window size fields.
const maxSize = 2000

type session struct {
	emu *emulator.Emulator
	// ptmx is the PTY's controlling side: the emulator reads the child's
	// output from it through an outputFilter and writes keys to it. tty is
	// the child's side until the child holds it.
	ptmx, tty *os.File
	gfx       *graphics
	filter    *outputFilter

	mu         sync.Mutex
	cols, rows int
	cmd        *exec.Cmd
	exited     bool
	exitCh     chan struct{}

	// caption is drawn over color snapshots at captionAt (top or bottom)
	// until it is cleared.
	caption   string
	captionAt string
}

// newSession opens a PTY of cols by rows cells and an emulator that reads
// it. The emulator gets the output through an outputFilter, which keeps the
// Kitty graphics commands for the color snapshots.
func newSession(cols, rows int) (*session, error) {
	if err := checkSize(cols, rows); err != nil {
		return nil, err
	}
	ptmx, tty, err := pty.Open()
	if err != nil {
		return nil, fmt.Errorf("open pty: %w", err)
	}
	s := &session{
		ptmx:      ptmx,
		tty:       tty,
		gfx:       newGraphics(colorCellWidth, colorCellHeight),
		cols:      cols,
		rows:      rows,
		exitCh:    make(chan struct{}),
		captionAt: captionBottom,
	}
	if err := s.setPTYSize(cols, rows); err != nil {
		_ = ptmx.Close()
		_ = tty.Close()
		return nil, err
	}
	s.filter = newOutputFilter(ptmx, ptmx, s.gfx, s.size)
	emu, err := emulator.NewFromPipes(cols, rows, s.filter, ptmx)
	if err != nil {
		_ = ptmx.Close()
		_ = tty.Close()
		_ = s.filter.Close()
		return nil, fmt.Errorf("create emulator: %w", err)
	}
	s.emu = emu
	return s, nil
}

func checkSize(cols, rows int) error {
	if cols <= 0 || rows <= 0 || cols > maxSize || rows > maxSize {
		return fmt.Errorf("invalid dimensions %dx%d", cols, rows)
	}
	return nil
}

// setPTYSize sets the PTY's window size, in cells and in the pixels of a
// color snapshot.
func (s *session) setPTYSize(cols, rows int) error {
	if err := checkSize(cols, rows); err != nil {
		return err
	}
	var ws pty.Winsize
	var err error
	if ws.Cols, err = toUint16(cols); err != nil {
		return err
	}
	if ws.Rows, err = toUint16(rows); err != nil {
		return err
	}
	if ws.X, err = toUint16(cols * colorCellWidth); err != nil {
		return err
	}
	if ws.Y, err = toUint16(rows * colorCellHeight); err != nil {
		return err
	}
	if err := pty.Setsize(s.ptmx, &ws); err != nil {
		return fmt.Errorf("set pty size: %w", err)
	}
	return nil
}

// toUint16 converts a PTY window size field. checkSize keeps the fields in
// range; the check here keeps the conversion safe on its own.
func toUint16(n int) (uint16, error) {
	if n < 0 || n > math.MaxUint16 {
		return 0, fmt.Errorf("window size %d out of range", n)
	}
	return uint16(n), nil
}

// size is the emulator's size in cells.
func (s *session) size() (cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}

// close stops the emulator, which closes the PTY, the filter's reading of
// it, and the child's side of the PTY if the child never took it.
func (s *session) close() {
	_ = s.emu.Close()
	_ = s.filter.Close()
	if s.tty != nil {
		_ = s.tty.Close()
	}
}

// terminalVars name the terminal kc runs in. The emulator here is none of
// those terminals, so the child must not see them: kc's charts.graphics:
// auto goes by them and draws text charts here. With charts.graphics: kitty
// kc sends images, and the color snapshots show them (kitty.go).
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

	// #nosec G204 G702 -- the driver runs the command line it was started with.
	cmd := exec.Command(command[0], command[1:]...)
	cols, rows := s.size()
	cmd.Env = childEnv(os.Environ(), cols, rows)
	if wd, err := os.Getwd(); err == nil {
		cmd.Dir = wd
	}
	// The PTY is the child's controlling terminal, in a session of its own,
	// so it gets SIGWINCH on a resize and SIGHUP when the PTY closes.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = s.tty, s.tty, s.tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start command: %w", err)
	}
	// Only the child holds its side now, so the emulator reads the end of
	// the output when the child exits.
	_ = s.tty.Close()
	s.tty = nil

	s.mu.Lock()
	s.cmd = cmd
	s.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		s.mu.Lock()
		s.exited = true
		s.mu.Unlock()
		close(s.exitCh)
	}()
	return nil
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

// resize resizes the emulator, then the PTY, which signals the child, so
// the child draws its new frame into a screen of the new size.
func (s *session) resize(cols, rows int) error {
	if err := checkSize(cols, rows); err != nil {
		return err
	}
	if err := s.emu.Resize(cols, rows); err != nil {
		return fmt.Errorf("resize: %w", err)
	}
	s.mu.Lock()
	s.cols, s.rows = cols, rows
	s.mu.Unlock()
	if err := s.setPTYSize(cols, rows); err != nil {
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
	fmt.Println("commands: help, key <token>, text <value>, enter, screen [ansi|plain], snapshot <file> [color], images, caption [text], caption-at <top|bottom>, mouse <button> <x> <y> <press|release|motion>, resize <cols> <rows>, wait, exit")
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
			// A combining mark, such as the diacritics of a Kitty
			// placeholder, belongs to the cell before it. The plain
			// snapshot draws no images, so a placeholder stays blank.
			if unicode.In(r, unicode.Mn, unicode.Me) {
				continue
			}
			if r != kittygfx.Placeholder {
				drawer.Dot = fixed.Point26_6{
					X: fixed.I(x),
					Y: fixed.I(rowIdx*snapshotCellHeight + baseline),
				}
				drawer.DrawString(string(r))
			}
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
	if err := png.Encode(file, img); err != nil {
		_ = file.Close() // Preserve the encoding error while releasing the file.
		return fmt.Errorf("encode png: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close snapshot: %w", err)
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
	defer session.close()

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
	case "images":
		s.cmdImages()
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
		err = colorSnapshotToPNG(s.emu.GetCells(), s.gfx, s.caption, s.captionAt, path)
	} else {
		err = snapshotToPNG(s.emu.GetScreen(), path)
	}
	if err != nil {
		fmt.Printf("error: %v\n", err)
	} else {
		fmt.Printf("saved snapshot: %s\n", path)
	}
}

// cmdImages lists the Kitty graphics images the child sent, which a color
// snapshot draws, and marks those the screen shows.
func (s *session) cmdImages() {
	lines := s.gfx.describe(shownImages(s.emu.GetCells()))
	if len(lines) == 0 {
		fmt.Println("no images")
		return
	}
	for _, l := range lines {
		fmt.Println(l)
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
