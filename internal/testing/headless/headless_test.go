package headless_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/stokaro/kc/internal/testing/kcenvtest"
)

const cleanExit = "KC_HEADLESS_EXIT_OK"

// TestHeadlessSession exercises the actual binaries, terminal protocol, and
// envtest connection. It is opt-in so ordinary unit tests never build tools or
// start an extra control plane. make test-headless supplies both binaries.
func TestHeadlessSession(t *testing.T) {
	if os.Getenv("KC_HEADLESS") != "1" {
		t.Skip("run make test-headless for the binary smoke test")
	}
	kc := binary(t, "KC_HEADLESS_KC_BINARY")
	driver := binary(t, "KC_HEADLESS_DRIVER_BINARY")
	cfg := kcenvtest.Start(t)
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Namespaces().Create(t.Context(), &corev1.Namespace{Name: "kc-smoke"}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	probe := &corev1.ConfigMap{Name: "headless-probe", Namespace: "kc-smoke"}
	if _, err := client.CoreV1().ConfigMaps("kc-smoke").Create(t.Context(), probe, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	kubeconfig := filepath.Join(home, "kubeconfig")
	kcfg := clientcmdapi.NewConfig()
	kcfg.Clusters["envtest"] = &clientcmdapi.Cluster{Server: cfg.Host, CertificateAuthorityData: cfg.CAData}
	kcfg.AuthInfos["envtest"] = &clientcmdapi.AuthInfo{ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData, Token: cfg.BearerToken}
	kcfg.Contexts["kc-smoke"] = &clientcmdapi.Context{Cluster: "envtest", AuthInfo: "envtest", Namespace: "kc-smoke"}
	kcfg.CurrentContext = "kc-smoke"
	if err := clientcmd.WriteToFile(*kcfg, kubeconfig); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(cancel)
	// #nosec G204 G702 -- make supplies the executable; arguments are fixed and the test creates the kubeconfig.
	cmd := exec.CommandContext(ctx, driver, "-cols", "120", "-rows", "30", "--", os.Args[0], "-test.run=^TestKCChild$", "-test.timeout=75s")
	cmd.Dir = home
	// Do not inherit user kubeconfigs, configuration, shell startup files, or
	// credential helpers. The child only sees this test's certificate identity.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "SHELL=/bin/sh",
		"XDG_CONFIG_HOME=" + home, "XDG_CACHE_HOME=" + home,
		"KUBECONFIG=" + kubeconfig, "KC_HEADLESS_KC_BINARY=" + kc,
		"KC_HEADLESS=1", "KC_HEADLESS_CHILD=1",
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close() }) // The driver may already have closed its input on exit.
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	finished := false
	t.Cleanup(func() {
		cancel()
		if !finished {
			_ = cmd.Wait() // Cancellation intentionally kills a stuck or failed session.
		}
		if t.Failed() {
			t.Logf("driver stderr: %s", stderr.String())
		}
	})
	s := session{t: t, in: in, out: bufio.NewReader(out), rows: 30}
	for range 3 { // Driver welcome: readiness, command, and supported commands.
		s.line()
	}
	ready := func(screen string) bool {
		lines := strings.Split(screen, "\n")
		footer := lines[len(lines)-1]
		return strings.Contains(screen, "kc-smoke") && strings.Contains(screen, "headless-probe") &&
			strings.Contains(footer, "Help") && strings.Contains(footer, "Quit")
	}
	s.until(ctx, "initial panels and function bar", ready)
	s.send("resize 100 24")
	if got := s.line(); got != "resized to 100x24" {
		t.Fatalf("resize response: %q", got)
	}
	s.rows = 24
	frame := s.until(ctx, "panels and function bar after resize", ready)
	for line := range strings.SplitSeq(frame, "\n") {
		if width := ansi.StringWidth(line); width > 100 {
			t.Fatalf("resized frame line is %d cells wide: %q", width, line)
		}
	}
	// The first F1 opens contextual help; the second opens its own help page.
	s.send("key f1\nkey f1")
	s.until(ctx, "help window", func(screen string) bool { return strings.Contains(screen, "Using the help") })
	s.send("key f10")
	s.until(ctx, "closed help", func(screen string) bool { return ready(screen) && !strings.Contains(screen, "Using the help") })
	s.send("key f10\nwait")
	if got := s.line(); got != "waiting for process to exit..." {
		t.Fatalf("wait response: %q", got)
	}
	if got := s.line(); got != "process exited" {
		t.Fatalf("child exit response: %q", got)
	}
	s.until(ctx, "successful kc exit", func(screen string) bool { return strings.Contains(screen, cleanExit) })
	s.send("exit")
	err = cmd.Wait()
	finished = true
	if err != nil {
		t.Fatalf("driver exit: %v", err)
	}
}

// TestKCChild preserves kc's exit status: the driver reports that a process
// exited but does not distinguish success from failure. The marker is printed
// on the restored terminal only after kc exits successfully.
func TestKCChild(t *testing.T) {
	if os.Getenv("KC_HEADLESS_CHILD") != "1" {
		t.Skip("only invoked inside the headless driver's terminal")
	}
	// #nosec G204 G702 -- the parent supplies its built binary and isolated envtest kubeconfig.
	cmd := exec.CommandContext(t.Context(), binary(t, "KC_HEADLESS_KC_BINARY"), "--kubeconfig", os.Getenv("KUBECONFIG"), "--context", "kc-smoke", "-n", "kc-smoke", "--readonly", "get", "configmaps")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("kc exit: %v", err)
	}
	if _, err := fmt.Fprintln(os.Stdout, cleanExit); err != nil {
		t.Fatal(err)
	}
}

func binary(t *testing.T, name string) string {
	t.Helper()
	path := os.Getenv(name)
	if !filepath.IsAbs(path) {
		t.Fatalf("%s must name an absolute binary path; use make test-headless", name)
	}
	// #nosec G703 -- make provides an absolute executable path; this only inspects it.
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("%s is not a regular executable file: %v", name, err)
	}
	return path
}

type session struct {
	t    *testing.T
	in   io.Writer
	out  *bufio.Reader
	rows int
}

func (s *session) send(command string) {
	s.t.Helper()
	if _, err := fmt.Fprintln(s.in, command); err != nil {
		s.t.Fatalf("send %q: %v", command, err)
	}
}

func (s *session) line() string {
	s.t.Helper()
	line, err := s.out.ReadString('\n')
	if err != nil {
		s.t.Fatalf("read driver output: %v (%q)", err, line)
	}
	// Commands without output leave adjacent REPL prompts before the next line.
	for strings.HasPrefix(line, "> ") {
		line = strings.TrimPrefix(line, "> ")
	}
	return strings.TrimSuffix(line, "\n")
}

func (s *session) until(ctx context.Context, want string, matches func(string) bool) string {
	s.t.Helper()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		s.send("screen plain")
		lines := make([]string, s.rows)
		for i := range lines {
			lines[i] = s.line()
		}
		screen := strings.Join(lines, "\n")
		if matches(screen) {
			return screen
		}
		select {
		case <-ctx.Done():
			s.t.Fatalf("waiting for %s: %v\nlast frame:\n%s", want, ctx.Err(), screen)
		case <-tick.C:
		}
	}
}
