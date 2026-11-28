package ui

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
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
	frame = startupFrame(app)
	if strings.Contains(frame, "Enter opens the panels") {
		t.Errorf("the panels screen still shows the error:\n%s", frame)
	}
	// The left panel says why it lists nothing, in its first column below
	// the header. Before, it showed an empty list.
	want := "│ Cannot reach " + strings.TrimPrefix(server, "https://") + ": connection refused"
	if !slices.ContainsFunc(strings.Split(frame, "\n"), func(l string) bool { return strings.HasPrefix(l, want) }) {
		t.Errorf("no line of the panels screen starts with %q:\n%s", want, frame)
	}
}

// A start context kc cannot build a client for opens panels that name the
// context and the reason, in the lists and in Info. Before, the lists said
// "No items" and Info "Not connected to a cluster."
func TestStartupWithoutAClientNamesTheReason(t *testing.T) {
	useStartKubeconfig(t, refusedServer(t))
	// A CA file that does not exist: the client cannot be built.
	path := os.Getenv("KUBECONFIG")
	kcfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	missingCA := filepath.Join(filepath.Dir(path), "missing-ca.crt")
	kcfg.Clusters["remote"].InsecureSkipTLSVerify = false
	kcfg.Clusters["remote"].CertificateAuthority = missingCA
	if err := clientcmd.WriteToFile(*kcfg, path); err != nil {
		t.Fatal(err)
	}
	app := newStartApp(t)
	if err := app.beginConfigAndStartup(t.Context()); err != nil {
		t.Fatalf("startup: %v", err)
	}
	app.Init()
	app.Update(tea.WindowSizeMsg{Width: 200, Height: 30})
	msg := app.connectStartup(app.startup)()
	if msg.acquireErr == nil {
		t.Fatalf("building a client with a missing CA file succeeded (discovery error: %v)", msg.discoveryErr)
	}
	app.Update(msg)
	if _, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil || app.startup != nil {
		t.Fatal("Enter on the error did not open the panels")
	}
	frame := startupFrame(app)
	left := leftPanelText(frame)
	for _, want := range []string{"Cannot connect to staging:", missingCA, hintPickContext} {
		if !strings.Contains(left, strings.ReplaceAll(want, " ", "")) {
			t.Errorf("the left panel does not say %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "No items") {
		t.Errorf("the panels say No items:\n%s", frame)
	}
	if got := app.notConnectedHint(); !strings.Contains(got, "Cannot connect to staging:") || !strings.Contains(got, missingCA) {
		t.Errorf("Info's hint = %q, want the context and the missing file", got)
	}
}

// leftPanelText is the text of the left panel in frame without spaces,
// so that a check reads a wrapped line as one.
func leftPanelText(frame string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(frame, "\n") {
		_, rest, ok := strings.Cut(line, "│")
		if !ok {
			continue
		}
		inner, _, _ := strings.Cut(rest, "│")
		b.WriteString(strings.ReplaceAll(inner, " ", ""))
	}
	return b.String()
}

// Without a kubeconfig the lists say which file kc looked for. Before,
// Run printed a warning to the terminal the UI then covered, and the
// panels said "No items".
func TestStartupWithoutKubeconfigSaysWhichFile(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, home string) (kubeconfigEnv string)
		want  func(home, env string) string
	}{
		{
			name:  "KUBECONFIG names a missing file",
			setup: func(_ *testing.T, home string) string { return filepath.Join(home, "missing") },
			want:  func(_, env string) string { return "No kubeconfig: $KUBECONFIG=" + env + " not found" },
		},
		{
			name:  "no KUBECONFIG and no ~/.kube",
			setup: func(*testing.T, string) string { return "" },
			want: func(home, _ string) string {
				return "No kubeconfig: " + filepath.Join(home, ".kube", "config") + " not found"
			},
		},
		{
			name: "a kubeconfig without contexts",
			setup: func(t *testing.T, home string) string {
				path := filepath.Join(home, "empty")
				if err := clientcmd.WriteToFile(*clientcmdapi.NewConfig(), path); err != nil {
					t.Fatal(err)
				}
				return path
			},
			want: func(_, env string) string { return "No contexts in $KUBECONFIG=" + env },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			env := tt.setup(t, home)
			t.Setenv("KUBECONFIG", env)
			t.Setenv("HOME", home)
			t.Setenv("SHELL", "/bin/sh")
			app := newStartApp(t)
			err := app.beginConfigAndStartup(t.Context())
			if _, ok := errors.AsType[*noKubeconfigError](err); !ok {
				t.Fatalf("startup error = %v (%T), want a missing kubeconfig", err, err)
			}
			want := tt.want(home, env)
			if err.Error() != want {
				t.Errorf("startup error = %q, want %q", err, want)
			}
			// As Run does with the error.
			app.showStartProblem(err, "")
			app.Init()
			app.Update(tea.WindowSizeMsg{Width: 200, Height: 30})
			frame := startupFrame(app)
			if strings.Contains(frame, "No items") {
				t.Errorf("the panels say No items:\n%s", frame)
			}
			if !strings.Contains(leftPanelText(frame), strings.ReplaceAll(want, " ", "")) {
				t.Errorf("the left panel does not say %q:\n%s", want, frame)
			}
			if got := app.notConnectedHint(); !strings.Contains(got, want) {
				t.Errorf("Info's hint = %q, want it to name %q", got, want)
			}
		})
	}
}
