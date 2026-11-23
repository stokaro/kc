package diagnostic_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stokaro/kc/internal/diagnostic"
)

// The test binary is the fake tool: with KC_DIAG_FAKE set it acts as the
// mode names and exits, so the runner is tested on a real process.
func TestMain(m *testing.M) {
	if mode := os.Getenv("KC_DIAG_FAKE"); mode != "" {
		os.Exit(fakeTool(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeTool(mode string, args []string) int {
	switch mode {
	case "echo-args":
		for _, a := range args {
			fmt.Printf("[%s]\n", a)
		}
		fmt.Printf("env=%s\n", os.Getenv("KC_DIAG_PROBE"))
		return 0
	case "fail":
		fmt.Fprintln(os.Stderr, "krr: cannot reach Prometheus")
		return 3
	case "sleep":
		time.Sleep(time.Minute)
		return 0
	case "flood":
		fmt.Print(strings.Repeat("x", 4096))
		return 0
	}
	return 99
}

func fake(t *testing.T, mode string, args ...string) diagnostic.Process {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return diagnostic.Process{
		Argv: append([]string{exe}, args...),
		Env:  append(os.Environ(), "KC_DIAG_FAKE="+mode, "KC_DIAG_PROBE=from-kc"),
	}
}

func TestRunPassesArgumentsWithoutAShell(t *testing.T) {
	t.Parallel()
	hostile := []string{"web; rm -rf /", "$(id)", "`id`", "a b", "--context=x", "'quoted'", ""}
	res := diagnostic.Run(t.Context(), fake(t, "echo-args", hostile...))
	if res.Outcome != diagnostic.Finished {
		t.Fatalf("outcome = %v (%v), stderr %q", res.Outcome, res.Err, res.Stderr)
	}
	var want strings.Builder
	for _, a := range hostile {
		fmt.Fprintf(&want, "[%s]\n", a)
	}
	want.WriteString("env=from-kc\n")
	if got := string(res.Stdout); got != want.String() {
		t.Fatalf("stdout = %q, want %q", got, want.String())
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d", res.ExitCode)
	}
}

func TestRunOutcomes(t *testing.T) {
	t.Parallel()
	t.Run("failed", func(t *testing.T) {
		t.Parallel()
		res := diagnostic.Run(t.Context(), fake(t, "fail"))
		if res.Outcome != diagnostic.Failed || res.ExitCode != 3 {
			t.Fatalf("outcome = %v exit %d, want failed exit 3", res.Outcome, res.ExitCode)
		}
		if !strings.Contains(res.Err.Error(), "exit status 3") || !strings.Contains(res.Stderr, "cannot reach Prometheus") {
			t.Fatalf("err %v, stderr %q", res.Err, res.Stderr)
		}
		if res.Stdout != nil {
			t.Fatalf("a failed run keeps no output to parse")
		}
	})
	t.Run("timed out", func(t *testing.T) {
		t.Parallel()
		p := fake(t, "sleep")
		p.Timeout = 200 * time.Millisecond
		start := time.Now()
		res := diagnostic.Run(t.Context(), p)
		if res.Outcome != diagnostic.TimedOut {
			t.Fatalf("outcome = %v (%v), want timed out", res.Outcome, res.Err)
		}
		if took := time.Since(start); took > 20*time.Second {
			t.Fatalf("the stopped tool took %s to go away", took)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		p := fake(t, "sleep")
		p.Timeout = time.Minute
		time.AfterFunc(200*time.Millisecond, cancel)
		res := diagnostic.Run(ctx, p)
		if res.Outcome != diagnostic.Canceled {
			t.Fatalf("outcome = %v (%v), want canceled", res.Outcome, res.Err)
		}
	})
	t.Run("unavailable", func(t *testing.T) {
		t.Parallel()
		res := diagnostic.Run(t.Context(), diagnostic.Process{Argv: []string{"kc-no-such-tool-198"}})
		if res.Outcome != diagnostic.Unavailable {
			t.Fatalf("outcome = %v, want unavailable", res.Outcome)
		}
		if !strings.Contains(res.Err.Error(), "kc-no-such-tool-198 is not installed or not in PATH") {
			t.Fatalf("err = %v", res.Err)
		}
	})
	t.Run("too much output", func(t *testing.T) {
		t.Parallel()
		p := fake(t, "flood")
		p.MaxOutput = 1 << 10
		res := diagnostic.Run(t.Context(), p)
		if res.Outcome != diagnostic.Failed || !strings.Contains(res.Err.Error(), "output is larger") {
			t.Fatalf("outcome = %v (%v), want failed on output size", res.Outcome, res.Err)
		}
	})
}

func TestOutcomeStringsAreDistinct(t *testing.T) {
	t.Parallel()
	seen := make(map[string]bool)
	for _, o := range []diagnostic.Outcome{diagnostic.Finished, diagnostic.Unavailable, diagnostic.TimedOut, diagnostic.Canceled, diagnostic.Failed} {
		if seen[o.String()] {
			t.Fatalf("outcome %q is not distinct", o)
		}
		seen[o.String()] = true
	}
}
