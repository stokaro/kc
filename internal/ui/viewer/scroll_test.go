package viewer_test

import (
	"strings"
	"testing"

	"github.com/stokaro/kc/internal/ui/viewer"
)

func TestScrollToLine(t *testing.T) {
	t.Parallel()
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "line"
	}
	tests := []struct {
		name string
		line int
		want int
	}{
		{"top", 0, 0},
		{"middle", 7, 7},
		{"clamped to the last page", 18, 15},
		{"negative", -3, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := viewer.New("")
			w.SetPlainMode(true) // one display line per text line
			w.SetContent(strings.Join(lines, "\n"), viewer.Metadata{})
			w.View(viewer.Frame{Width: 20, Height: 5})
			w.ScrollToLine(tc.line)
			if got := w.Offset(); got != tc.want {
				t.Fatalf("Offset() = %d, want %d", got, tc.want)
			}
		})
	}
}
