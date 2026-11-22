package logmux_test

import (
	"bufio"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"

	corev1 "k8s.io/api/core/v1"

	"github.com/stokaro/kc/internal/logmux"
	"github.com/stokaro/kc/internal/logview"
)

// TestMuxTruncatesLongLines checks that the mux keeps at most
// logview.MaxLineBytes of a line, so its line buffer stays bounded, and
// that the merged line still fits the viewers' read limit.
func TestMuxTruncatesLongLines(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		op := newFakeOpener()
		m := logmux.New(t.Context(), op.open, logmux.Options{LogOptions: corev1.PodLogOptions{Follow: true}})
		defer m.Close()
		r := bufio.NewReader(logmux.NewReader(m, func(l logmux.Line) string { return logmux.Format(l, false) }))
		m.SetTargets([]logmux.Target{target("api-1", "app")})
		synctest.Wait()
		if line, err := r.ReadString('\n'); err != nil || !strings.Contains(line, "[kc] + api-1/app") {
			t.Fatalf("start notice = %q, %v", line, err)
		}

		s := op.stream(t, "api-1/app")
		s.lines <- strings.Repeat("q", logview.MaxLineBytes*3)
		s.lines <- "after"
		long, err := logview.ReadLine(r, logview.ReadLimit)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if !logview.IsTruncated(strings.TrimSuffix(long, "\n")) || len(long) > logview.ReadLimit {
			t.Fatalf("merged long line has %d bytes, truncated %v", len(long), logview.IsTruncated(strings.TrimSuffix(long, "\n")))
		}
		// The notice is the mux's, with the original size: the viewer's
		// read did not cut the line again.
		if want := fmt.Sprintf("line truncated: %d bytes", logview.MaxLineBytes*3); !strings.HasPrefix(long, "[api-1/app] qqq") || !strings.Contains(long, want) {
			t.Fatalf("merged long line starts %q and ends %q, want %q", long[:20], long[len(long)-60:], want)
		}
		if next, err := r.ReadString('\n'); err != nil || next != "[api-1/app] after\n" {
			t.Fatalf("next line = %q, %v", next, err)
		}
	})
}
