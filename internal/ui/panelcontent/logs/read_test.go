package logs

import (
	"bufio"
	"strings"
	"testing"

	"github.com/stokaro/kc/internal/logview"
)

// TestReadLinesBoundsLongLines checks that a line longer than the limit is
// cut while it is read, and that the line after it is intact.
func TestReadLinesBoundsLongLines(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("z", logview.ReadLimit+100)
	r := bufio.NewReader(strings.NewReader(long + "\nafter\n"))
	var lines []string
	for len(lines) < 2 {
		got, err := readLines(r, maxChunkLines)
		if err != nil {
			t.Fatalf("readLines: %v", err)
		}
		lines = append(lines, got...)
	}
	if len(lines) != 2 || lines[1] != "after" {
		t.Fatalf("got %d lines, the last %q", len(lines), lines[len(lines)-1])
	}
	if !logview.IsTruncated(lines[0]) || len(lines[0]) > logview.ReadLimit+100 {
		t.Fatalf("long line kept %d bytes, truncated %v", len(lines[0]), logview.IsTruncated(lines[0]))
	}
}
