package ui

import (
	"strings"
	"testing"

	"github.com/stokaro/kc/internal/logmux"
	"github.com/stokaro/kc/internal/models"
)

// The widget test (viewer.TestAppendStyledLines) covers search and theme
// changes; this checks that a styled viewer shows the colored prefix and
// keeps the stripped text for copy.
func TestLogsViewerStyledLines(t *testing.T) {
	line := logmux.Format(logmux.Line{Target: logmux.Target{Pod: "api-1", Container: "app"}, Text: "hello"}, true)
	if !strings.Contains(line, "\x1b[") {
		t.Fatalf("formatted line %q has no styling", line)
	}

	v := NewLogsViewer("logs:test", models.LogsSpec{}, nil, "dracula")
	v.SetStyledLines(true)
	v.SetDimensions(80, 10)
	v.Update(logStreamMsg{viewer: v, lines: []string{line}})

	if text := v.inner.PlainText(); !strings.HasSuffix(text, "[api-1/app] hello") {
		t.Fatalf("viewer text %q, want the stripped line", text)
	}
	if view := v.View().Content; !strings.Contains(view, "api-1/app") {
		t.Fatalf("view does not show the line: %q", view)
	}
}
