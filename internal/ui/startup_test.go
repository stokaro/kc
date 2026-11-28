package ui

import (
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// stalledServer accepts connections and never answers, like an API server
// whose process is stopped. It returns the server URL.
func stalledServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu    sync.Mutex
		conns []net.Conn
	)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return "https://" + l.Addr().String()
}

// refusedServer is a server URL whose port refuses connections.
func refusedServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return "https://" + addr
}

// useStartKubeconfig points KUBECONFIG and HOME at a temporary directory
// with one context, staging, on server.
func useStartKubeconfig(t *testing.T, server string) {
	t.Helper()
	home := t.TempDir()
	kcfg := clientcmdapi.NewConfig()
	kcfg.Clusters["remote"] = &clientcmdapi.Cluster{Server: server, InsecureSkipTLSVerify: true}
	kcfg.AuthInfos["user"] = &clientcmdapi.AuthInfo{Token: "test-token"}
	kcfg.Contexts["staging"] = &clientcmdapi.Context{Cluster: "remote", AuthInfo: "user", Namespace: "default"}
	kcfg.CurrentContext = "staging"
	path := filepath.Join(home, "kubeconfig")
	if err := clientcmd.WriteToFile(*kcfg, path); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/sh")
}

// newStartApp is an App with Run's cleanup.
func newStartApp(t *testing.T) *App {
	t.Helper()
	app := NewApp()
	t.Cleanup(func() {
		app.releasePanelConns()
		if app.clPool != nil {
			app.clPool.Stop()
		}
		_ = app.terminal.Close()
		app.closeKubeconfigFiles()
		app.cancel()
	})
	return app
}

// within runs fn and fails the test when it takes longer than limit.
func within(t *testing.T, limit time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("%s took longer than %v; the server never answers", what, limit)
	}
}

// startupFrame is the frame View draws, without escape sequences.
func startupFrame(a *App) string { return ansi.Strip(a.View().Content) }

// kc draws before it connects. Before, Run read API discovery of the start
// context before the program started, so a server that accepts connections
// and never answers left the screen empty for minutes, and only C-c got
// out.
func TestStartupDrawsBeforeTheServerAnswers(t *testing.T) {
	server := stalledServer(t)
	useStartKubeconfig(t, server)
	app := newStartApp(t)

	// Everything Run does before the program starts, then Init and the
	// first frame.
	within(t, time.Second, "picking the start context", func() {
		if err := app.beginConfigAndStartup(t.Context()); err != nil {
			t.Errorf("startup: %v", err)
		}
	})
	within(t, time.Second, "Init", func() { app.Init() })
	app.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	var frame string
	within(t, time.Second, "drawing the first frame", func() { frame = startupFrame(app) })
	want := "Connecting to staging (" + server + ")… 0s · F10 quits"
	if !strings.Contains(frame, want) {
		t.Fatalf("first frame lacks %q:\n%s", want, frame)
	}

	// The time spent counts up on the tick.
	app.startup.since = app.startup.since.Add(-12 * time.Second)
	if _, cmd := app.Update(startupTickMsg{}); cmd == nil {
		t.Error("the tick while connecting did not ask for the next one")
	}
	if frame := startupFrame(app); !strings.Contains(frame, ")… 12s · F10 quits") {
		t.Errorf("after 12s the frame reads:\n%s", frame)
	}

	// Other keys have nothing to act on yet.
	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyDown}, {Code: tea.KeyEnter}, {Code: tea.KeyF1}, {Code: 'q', Text: "q"}} {
		if _, cmd := app.Update(key); cmd != nil || app.startup == nil {
			t.Errorf("%s while connecting returned a command or left the connecting screen", key)
		}
	}
	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyF10}, {Code: 'c', Mod: tea.ModCtrl}} {
		_, cmd := app.Update(key)
		if cmd == nil {
			t.Fatalf("%s while connecting did nothing, want quit", key)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%s while connecting did not quit", key)
		}
	}
}

// A failed connection stays on the screen with its error. Enter opens the
// panels on the cluster anyway, as kc did before when discovery failed.
func TestStartupShowsTheConnectError(t *testing.T) {
	server := refusedServer(t)
	useStartKubeconfig(t, server)
	app := newStartApp(t)
	if err := app.beginConfigAndStartup(t.Context()); err != nil {
		t.Fatalf("startup: %v", err)
	}
	app.Init()
	app.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	var msg startupConnectedMsg
	within(t, 30*time.Second, "connecting to a port that refuses connections", func() {
		msg = app.connectStartup(app.startup)()
	})
	if msg.err() == nil {
		t.Fatal("connecting to a port that refuses connections succeeded")
	}
	if _, cmd := app.Update(msg); cmd != nil {
		t.Error("a failed connection returned a command; want the error on the screen")
	}
	frame := startupFrame(app)
	for _, want := range []string{"Cannot connect to staging (" + server + "):", "connection refused", "Enter opens the panels · F10 quits"} {
		if !strings.Contains(frame, want) {
			t.Errorf("error frame lacks %q:\n%s", want, frame)
		}
	}
	if _, cmd := app.Update(startupTickMsg{}); cmd != nil {
		t.Error("the tick after the error asked for another one")
	}

	if _, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil || app.startup != nil {
		t.Fatal("Enter on the error did not open the panels")
	}
	if got := app.ownConn(0).name(); got != "staging" {
		t.Errorf("the left panel opened on %q, want staging", got)
	}
	if frame := startupFrame(app); strings.Contains(frame, "Cannot connect") {
		t.Errorf("the panels screen still shows the error:\n%s", frame)
	}
}
