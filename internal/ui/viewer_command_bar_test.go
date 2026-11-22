package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sttts/kc/internal/ui/viewer"
)

func TestViewerCommandBar(t *testing.T) {
	v := NewTextViewer("cm", "a: 1\nb: 2", "yaml", "", "cm.yaml", "", nil, nil, nil)
	v.SetCommand("kubectl get configmap cm -n web -o yaml")
	v.SetDimensions(80, 10)

	lines := strings.Split(ansi.Strip(viewString(v.View())), "\n")
	if len(lines) != 10 || !strings.HasPrefix(lines[9], " Command: kubectl get configmap cm -n web -o yaml") {
		t.Fatalf("last line should be the command line, got %q", lines[len(lines)-1])
	}

	req := func(cmd tea.Cmd) viewer.CopyRequestMsg {
		t.Helper()
		if cmd == nil {
			t.Fatalf("expected a copy request")
		}
		return cmd().(viewer.CopyRequestMsg)
	}
	_, cmd := v.Update(tea.KeyPressMsg{Code: 'a', Mod: tea.ModAlt})
	if r := req(cmd); r.Text != "a: 1\nb: 2" || r.What != "output" {
		t.Fatalf("M-a = %#v", r)
	}
	_, cmd = v.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModAlt})
	if r := req(cmd); r.Text != "kubectl get configmap cm -n web -o yaml" {
		t.Fatalf("M-c = %#v", r)
	}
	_, cmd = v.Update(tea.MouseReleaseMsg(tea.Mouse{X: 78, Y: 9, Button: tea.MouseLeft}))
	if r := req(cmd); r.What != "command" {
		t.Fatalf("click on the copy button = %#v", r)
	}

	// The confirmation replaces the command line until it expires.
	v.Update(copyResultMsg{text: "Copied output via pbcopy"})
	if last := lastLine(v); !strings.Contains(last, "Copied output via pbcopy") {
		t.Fatalf("flash not shown: %q", last)
	}
	v.Update(viewerFlashExpiredMsg{seq: v.bar.flashSeq})
	if last := lastLine(v); !strings.Contains(last, "Command:") {
		t.Fatalf("flash should expire: %q", last)
	}
}

func lastLine(v *TextViewer) string {
	lines := strings.Split(ansi.Strip(viewString(v.View())), "\n")
	return lines[len(lines)-1]
}

func TestCopyWhat(t *testing.T) {
	if got := copyWhat(viewer.CopyRequestMsg{Text: "a\nb\nc", What: "selection"}); got != "selection (3 lines)" {
		t.Fatalf("got %q", got)
	}
	if got := copyWhat(viewer.CopyRequestMsg{Text: "héllo", What: "command"}); got != "command (5 chars)" {
		t.Fatalf("got %q", got)
	}
}
