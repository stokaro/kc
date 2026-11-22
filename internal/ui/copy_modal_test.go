package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func keyPress(code rune, text string, mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Text: text, Mod: mod}
}

func TestCopyToLocalModelConfirm(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	model := NewCopyToLocalModel()
	model.SetDimensions(80, 12)
	model.Configure("logs/foo", target)
	model.FocusPath()
	_, cmd := model.Update(keyPress(tea.KeyEnter, "", 0))
	if cmd == nil {
		t.Fatalf("expected command on enter")
	}
	msg := cmd()
	res, ok := msg.(CopyToLocalResultMsg)
	if !ok {
		t.Fatalf("expected CopyToLocalResultMsg, got %T", msg)
	}
	if !res.Confirm || !res.Close || res.Path != target {
		t.Fatalf("expected confirm+close, got %+v", res)
	}
}

func TestCopyToLocalModelValidationFailure(t *testing.T) {
	model := NewCopyToLocalModel()
	model.SetDimensions(70, 10)
	model.Configure("logs/foo", "/unlikely-dir/out.txt")
	model.FocusPath()
	_, cmd := model.Update(keyPress(tea.KeyEnter, "", 0))
	if cmd != nil {
		t.Fatalf("expected nil command on invalid path")
	}
	if model.Error() == "" {
		t.Fatalf("expected validation error")
	}
}

// An existing file is replaced only with the overwrite option on; M-w
// toggles it from the input line.
func TestCopyToLocalModelOverwriteOption(t *testing.T) {
	target := filepath.Join(t.TempDir(), "out.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	model := NewCopyToLocalModel()
	model.SetDimensions(70, 10)
	model.Configure("pods/web", target)
	model.FocusPath()
	if _, cmd := model.Update(keyPress(tea.KeyEnter, "", 0)); cmd != nil {
		t.Fatalf("Enter replaced an existing file without the option")
	}
	if !strings.Contains(model.Error(), "exists") {
		t.Fatalf("error = %q", model.Error())
	}
	model.Update(keyPress('w', "", tea.ModAlt))
	if model.Error() != "" {
		t.Fatalf("toggling the option keeps the error %q", model.Error())
	}
	_, cmd := model.Update(keyPress(tea.KeyEnter, "", 0))
	if cmd == nil {
		t.Fatalf("Enter with the option on did nothing")
	}
	if res := cmd().(CopyToLocalResultMsg); !res.Confirm || res.Path != target {
		t.Fatalf("result = %+v", res)
	}
}

func TestCopyToLocalModelRendersMcLayout(t *testing.T) {
	model := NewCopyToLocalModel()
	model.SetDimensions(50, 7)
	model.Configure("pods/web", "/tmp/web.yaml")
	model.FocusPath()
	got := ansi.Strip(viewString(model.View()))
	want := strings.Join([]string{
		` Copy "pods/web" to:                              `,
		` /tmp/web.yaml                                    `,
		`                                                  `,
		`──────────────────────────────────────────────────`,
		` [ ] Overwrite an existing file                   `,
		`──────────────────────────────────────────────────`,
		`               [< OK >] [ Cancel ]                `,
	}, "\n")
	if got != want {
		t.Fatalf("view:\n%s\nwant:\n%s", got, want)
	}
	if rows := model.RuleRows(); len(rows) != 2 || rows[0] != 3 || rows[1] != 5 {
		t.Fatalf("rule rows = %v", rows)
	}
}
