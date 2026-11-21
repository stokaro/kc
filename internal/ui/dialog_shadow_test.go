package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestPadDialog(t *testing.T) {
	out := padDialog("┌┐\n└┘", 2, 2, 1, lipgloss.Color("7"))
	lines := strings.Split(ansi.Strip(out), "\n")
	want := []string{"      ", "  ┌┐  ", "  └┘  ", "      "}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("padded = %q", lines)
	}
}

// The shadow keeps the text under it, two columns right and one row below.
func TestCastShadowKeepsUnderlyingText(t *testing.T) {
	base := strings.Join([]string{"abcdefgh", "ijklmnop", "qrstuvwx", "yz012345"}, "\n")
	screen := strings.Join([]string{"abcdefgh", "iWWWWnop", "qWWWWvwx", "yz012345"}, "\n")
	out := castShadow(screen, base, 1, 1, 4, 2)
	if got := ansi.Strip(out); got != screen {
		t.Fatalf("text changed:\n%s", got)
	}
	lines := strings.Split(out, "\n")
	if lines[0] != "abcdefgh" || lines[1] != "iWWWWnop" {
		t.Fatalf("rows above the shadow must not change: %q", lines[:2])
	}
	if !strings.Contains(lines[2], shadowStyle.Render("vw")) {
		t.Fatalf("right shadow missing: %q", lines[2])
	}
	if !strings.Contains(lines[3], shadowStyle.Render("1234")) {
		t.Fatalf("bottom shadow missing: %q", lines[3])
	}
}
