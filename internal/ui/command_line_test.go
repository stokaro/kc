package ui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestRenderCommandLine(t *testing.T) {
	line := renderCommandLine("kubectl get pods -n web", 80)
	plain := ansi.Strip(line)
	if lipgloss.Width(line) != 80 {
		t.Fatalf("width = %d, want 80", lipgloss.Width(line))
	}
	if !strings.HasPrefix(plain, " Command: kubectl get pods -n web") || !strings.HasSuffix(plain, "📋 Copy M-c ") {
		t.Fatalf("unexpected line %q", plain)
	}

	long := renderCommandLine("kubectl get "+strings.Repeat("x", 100), 60)
	if lipgloss.Width(long) != 60 || !strings.Contains(ansi.Strip(long), "…") {
		t.Fatalf("long command should be truncated to the width: %q", ansi.Strip(long))
	}

	if plain := ansi.Strip(renderCommandLine("", 40)); strings.Contains(plain, "M-c") {
		t.Fatalf("no copy hint without a command: %q", plain)
	}
}

func TestOSC52Sequence(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	plain := ansi.SetSystemClipboard("kubectl get pods")
	if got := osc52Sequence("kubectl get pods", env(nil)); got != plain {
		t.Fatalf("plain terminal: got %q", got)
	}
	tmux := osc52Sequence("kubectl get pods", env(map[string]string{"TMUX": "/tmp/tmux-1/default,1,0"}))
	if !strings.HasPrefix(tmux, plain) || !strings.Contains(tmux, "\x1bPtmux;") {
		t.Fatalf("tmux: expected plain plus passthrough, got %q", tmux)
	}
	screen := osc52Sequence("x", env(map[string]string{"TERM": "screen-256color", "STY": "1.pts"}))
	if !strings.Contains(screen, "\x1bP") {
		t.Fatalf("screen: expected passthrough, got %q", screen)
	}
}

func TestIsSSHSession(t *testing.T) {
	if isSSHSession(func(string) string { return "" }) {
		t.Fatalf("no SSH variables means a local session")
	}
	if !isSSHSession(func(k string) string {
		if k == "SSH_CONNECTION" {
			return "1.2.3.4 5 6.7.8.9 22"
		}
		return ""
	}) {
		t.Fatalf("SSH_CONNECTION means an SSH session")
	}
}

func TestPlanCopy(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	have := func(names ...string) func(string) (string, error) {
		return func(n string) (string, error) {
			if slices.Contains(names, n) {
				return "/usr/bin/" + n, nil
			}
			return "", errors.New("not found")
		}
	}
	tests := []struct {
		name     string
		env      map[string]string
		tools    []string
		wantTool string
		osc52    bool
		tmux     bool
	}{
		{"local mac", nil, []string{"pbcopy"}, "pbcopy", false, false},
		{"local without tool", nil, nil, "", true, false},
		{"ssh ignores remote tool", map[string]string{"SSH_TTY": "/dev/pts/1"}, []string{"xclip"}, "", true, false},
		{"ssh in tmux", map[string]string{"SSH_TTY": "/dev/pts/1", "TMUX": "x"}, nil, "", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := planCopy(env(tt.env), have(tt.tools...))
			gotTool := ""
			if p.tool != nil {
				gotTool = p.tool[0]
			}
			if gotTool != tt.wantTool || p.osc52 != tt.osc52 || p.tmux != tt.tmux {
				t.Fatalf("plan = %+v", p)
			}
		})
	}
}
