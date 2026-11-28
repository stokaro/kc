package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestShellRestartDelay(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		quickExits int
		want       time.Duration
		again      bool
	}{
		{0, 0, true},
		{1, 0, true},
		{2, time.Second, true},
		{3, 2 * time.Second, true},
		{4, 4 * time.Second, true},
		{5, 0, false},
		{9, 0, false},
	} {
		got, again := shellRestartDelay(tc.quickExits, time.Second)
		if got != tc.want || again != tc.again {
			t.Errorf("shellRestartDelay(%d) = %s, %v; want %s, %v", tc.quickExits, got, again, tc.want, tc.again)
		}
	}
}

// appLoop runs an app the way Bubble Tea does: each command in its own
// goroutine, each message back through Update on the test goroutine. It
// keeps the quits and toasts it saw.
type appLoop struct {
	t      *testing.T
	app    *App
	msgs   chan tea.Msg
	done   chan struct{}
	quits  int
	toasts []showToastMsg
}

func newAppLoop(t *testing.T, app *App) *appLoop {
	t.Helper()
	l := &appLoop{t: t, app: app, msgs: make(chan tea.Msg), done: make(chan struct{})}
	t.Cleanup(func() {
		close(l.done)
		_ = app.terminal.Close()
	})
	return l
}

func (l *appLoop) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		select {
		case l.msgs <- msg:
		case <-l.done:
		}
	}()
}

// until handles messages until cond holds, and fails after timeout.
func (l *appLoop) until(timeout time.Duration, what string, cond func() bool) {
	l.t.Helper()
	deadline := time.After(timeout)
	for !cond() {
		select {
		case msg := <-l.msgs:
			l.handle(msg)
		case <-deadline:
			l.t.Fatalf("after %s: %s did not happen", timeout, what)
		}
	}
}

// during handles messages for d.
func (l *appLoop) during(d time.Duration) {
	end := time.After(d)
	for {
		select {
		case msg := <-l.msgs:
			l.handle(msg)
		case <-end:
			return
		}
	}
}

func (l *appLoop) handle(msg tea.Msg) {
	switch msg := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			l.run(c)
		}
	case tea.QuitMsg:
		l.quits++
	case showToastMsg:
		l.toasts = append(l.toasts, msg)
	default:
		_, cmd := l.app.Update(msg)
		l.run(cmd)
	}
}

func (l *appLoop) screen() string {
	s, _ := l.app.terminal.terminalView()
	return s
}

// shellScript writes an executable shell for kc to start and returns its
// path; body runs in /bin/sh with $dir set to the script's directory.
func shellScript(t *testing.T, body string) (path, dir string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, "shell")
	// #nosec G306 -- the test shell must be executable.
	if err := os.WriteFile(path, []byte("#!/bin/sh\ndir="+dir+"\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path, dir
}

func countStarts(t *testing.T, dir string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "starts"))
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "\n")
}

// When the shell exits, kc starts a new one and keeps running; the
// notice says so (#375). On main, the exit quit kc.
func TestShellExitKeepsKcRunning(t *testing.T) {
	shell, dir := shellScript(t, `echo x >> "$dir/starts"
n=$(wc -l < "$dir/starts" | tr -d ' ')
echo "kc-shell-$n"
read line
exit 0
`)
	t.Setenv("SHELL", shell)
	app := execTestApp(t)
	app.terminal.shell.quickExit = 50 * time.Millisecond
	l := newAppLoop(t, app)
	l.run(app.terminal.Init())
	l.until(10*time.Second, "the first shell shows its line", func() bool { return strings.Contains(l.screen(), "kc-shell-1") })
	time.Sleep(100 * time.Millisecond) // past quickExit: a normal exit

	// The shell's read returns and the shell exits, as with exit or C-d.
	if err := app.terminal.terminal.GetEmulator().SendKey("\r"); err != nil {
		t.Fatal(err)
	}
	l.until(10*time.Second, "a second shell shows its line", func() bool { return strings.Contains(l.screen(), "kc-shell-2") })
	if l.quits != 0 {
		t.Fatalf("kc quit %d times when its shell exited", l.quits)
	}
	if len(l.toasts) != 1 || l.toasts[0].text != "The shell exited; kc started a new one." || !l.toasts[0].info {
		t.Fatalf("toasts %+v, want one notice of the restart", l.toasts)
	}
	if got := countStarts(t, dir); got != 2 {
		t.Fatalf("%d shells started, want 2", got)
	}
	// The new shell takes keys.
	if err := app.terminal.terminal.GetEmulator().SendKey("\r"); err != nil {
		t.Fatal(err)
	}
	l.until(10*time.Second, "a third shell", func() bool { return strings.Contains(l.screen(), "kc-shell-3") })
}

// A shell that exits at once does not spin: kc waits longer before each
// new one and stops after shellMaxQuickExits, and the shell line says
// why. On main, the first exit quit kc.
func TestShellExitingAtOnceDoesNotSpin(t *testing.T) {
	shell, dir := shellScript(t, `echo x >> "$dir/starts"
exit 3
`)
	t.Setenv("SHELL", shell)
	app := execTestApp(t)
	app.terminal.shell.backoff = 100 * time.Millisecond
	l := newAppLoop(t, app)
	start := time.Now()
	l.run(app.terminal.Init())
	l.until(10*time.Second, "kc stops starting the shell", app.terminal.ShellDown)
	l.until(10*time.Second, "kc stops starting the shell", func() bool { return app.terminal.shell.stopped })
	took := time.Since(start)
	// 0 + 0 + 100ms + 200ms + 400ms of delays before the fifth exit.
	if took < 700*time.Millisecond {
		t.Fatalf("kc started %d shells in %s; it did not wait between them", countStarts(t, dir), took)
	}
	l.during(time.Second)
	if got := countStarts(t, dir); got != shellMaxQuickExits {
		t.Fatalf("%d shells started, want %d and then none", got, shellMaxQuickExits)
	}
	if l.quits != 0 {
		t.Fatalf("kc quit %d times", l.quits)
	}
	want := "The shell " + shell + " exited at once (exit status 3), 5 times in a row; kc stopped starting it. C-o starts it again."
	if got := app.terminal.shell.statusText(); got != want {
		t.Fatalf("status %q, want %q", got, want)
	}
	// The shell line shows it, cut to the terminal's width.
	app.terminal.Update(tea.WindowSizeMsg{Width: 60, Height: 10})
	line := viewString(app.terminal.View())
	if strings.Contains(line, "\n") || lipgloss.Width(line) != 60 || !strings.HasPrefix(line, "The shell ") || !strings.HasSuffix(line, "…") {
		t.Fatalf("shell line %q, want the status cut to 60 columns", line)
	}
	if len(l.toasts) == 0 || l.toasts[len(l.toasts)-1].text != want || l.toasts[len(l.toasts)-1].info {
		t.Fatalf("toasts %+v, want the red stop notice last", l.toasts)
	}
	backoff := 0
	for _, toast := range l.toasts {
		if strings.Contains(toast.text, "kc starts a new one in") {
			backoff++
		}
	}
	if backoff != 3 {
		t.Fatalf("toasts %+v, want 3 notices of a delay", l.toasts)
	}

	// C-o starts it again, for one try.
	app.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if !app.showTerminal || app.terminal.ShellDown() {
		t.Fatal("C-o did not start a shell")
	}
}

// exit in the full-screen shell returns to the panels, where the notice
// shows; a message about an earlier shell changes nothing.
func TestShellExitInFullScreenReturnsToPanels(t *testing.T) {
	shell, _ := shellScript(t, "read line\n")
	t.Setenv("SHELL", shell)
	app := execTestApp(t)
	t.Cleanup(func() { _ = app.terminal.Close() })
	app.terminal.Init()
	app.toggleTerminal()
	gen := app.terminal.shell.gen
	app.terminal.shell.started = time.Now().Add(-time.Minute)

	if _, cmd := app.Update(shellExitedMsg{gen: gen - 1}); cmd != nil || !app.showTerminal {
		t.Fatal("a message about an earlier shell had an effect")
	}
	_, cmd := app.Update(shellExitedMsg{gen: gen, status: "exit status 1"})
	if app.showTerminal {
		t.Fatal("kc stayed in the full-screen terminal after its shell exited")
	}
	if app.terminal.shell.gen != gen+1 || app.terminal.ShellDown() {
		t.Fatalf("no new shell: start %d, down %v", app.terminal.shell.gen, app.terminal.ShellDown())
	}
	// Closed, the new shell's commands return at once instead of waiting
	// for its output and its end.
	_ = app.terminal.Close()
	toasts := collectMsgs[showToastMsg](cmd)
	if len(toasts) != 1 || toasts[0].text != "The shell exited (exit status 1); kc started a new one." {
		t.Fatalf("toasts %+v", toasts)
	}
	if collectMsgs[tea.QuitMsg](cmd) != nil {
		t.Fatal("kc quit")
	}
}
