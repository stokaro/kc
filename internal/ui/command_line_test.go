package ui

import (
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
	if !strings.HasPrefix(plain, " Command: kubectl get pods -n web") || !strings.HasSuffix(plain, "Copy Alt+C ") {
		t.Fatalf("unexpected line %q", plain)
	}

	long := renderCommandLine("kubectl get "+strings.Repeat("x", 100), 60)
	if lipgloss.Width(long) != 60 || !strings.Contains(ansi.Strip(long), "…") {
		t.Fatalf("long command should be truncated to the width: %q", ansi.Strip(long))
	}

	if plain := ansi.Strip(renderCommandLine("", 40)); strings.Contains(plain, "Alt+C") {
		t.Fatalf("no copy hint without a command: %q", plain)
	}
}
