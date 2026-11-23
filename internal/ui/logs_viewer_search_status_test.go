package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	corev1 "k8s.io/api/core/v1"

	"github.com/stokaro/kc/internal/logmux"
	"github.com/stokaro/kc/internal/logview"
	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/ui/viewer"
)

// footerText is the footer without styling and trailing padding.
func footerText(v *LogsViewer, width int) string {
	return strings.TrimRight(ansi.Strip(v.FooterStatus(width)), " ")
}

// TestLogsSearchKeepsTheStreamState applies a search to a stream that
// failed, ended or lost lines. The footer shows the search and still says
// what happened to the stream.
func TestLogsSearchKeepsTheStreamState(t *testing.T) {
	t.Parallel()
	text := "alpha\nbeta\nalpha again\n"
	tests := []struct {
		name   string
		end    error
		limits *logview.Limits
		want   []string
		absent []string
	}{
		{name: "failed", end: errors.New("gave up"), want: []string{"/alpha (1/2)", "(failed)"}},
		{name: "ended", want: []string{"/alpha (1/2)", "(ended)"}},
		{name: "dropped lines", limits: &logview.Limits{MaxRecords: 2}, want: []string{"/alpha", "older dropped", "(ended)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			open := func(models.LogsSpec) (io.ReadCloser, context.CancelFunc, error) {
				return &stuckEndReader{Reader: strings.NewReader(text), end: tt.end}, func() {}, nil
			}
			v := NewLogsViewer("t", testLogsSpec(), open, "")
			v.SetDimensions(100, 20)
			v.inner.Resize(100, 18)
			drain(t, v, v.Init())
			if tt.limits != nil {
				v.SetLimits(*tt.limits)
			}
			setViewerSearch(t, v.inner, "alpha")
			got := footerText(v, 100)
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("footer = %q, want %q in it", got, w)
				}
			}
		})
	}
}

// clearViewerSearch commits an empty search, which clears it.
func clearViewerSearch(w *viewer.Widget) {
	w.Update(tea.KeyPressMsg{Code: tea.KeyF7})
	for range 40 {
		w.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	w.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// stuckEndReader returns its text, then end (io.EOF when nil).
type stuckEndReader struct {
	io.Reader
	end error
}

func (r *stuckEndReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if errors.Is(err, io.EOF) && r.end != nil {
		err = r.end
	}
	return n, err
}

func (r *stuckEndReader) Close() error { return nil }

// TestLogsSearchKeepsMergedCoverage searches a merged log whose sources do
// not all stream. The coverage stays in the footer, also with no match,
// at a narrow width, and after the search is cleared.
func TestLogsSearchKeepsMergedCoverage(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		open := func(models.LogsSpec) (io.ReadCloser, context.CancelFunc, error) {
			mux := logmux.New(t.Context(), heldOpener, logmux.Options{MaxStreams: 1, LogOptions: corev1.PodLogOptions{Follow: true}})
			mux.SetTargets([]logmux.Target{muxTarget("a", "app"), muxTarget("b", "app")})
			src := &workloadLogsSource{
				Reader: logmux.NewReader(mux, func(l logmux.Line) string { return logmux.Format(l, false) }),
				mux:    mux,
				stop:   func() {},
			}
			return src, nil, nil
		}
		v := NewLogsViewer("logs:web/deployments/api", models.LogsSpec{Namespace: "web", Follow: true}, open, "")
		v.SetDimensions(100, 20)
		v.inner.Resize(100, 18)
		defer v.Close()
		cmd := v.Init()
		for range 2 {
			_, cmd = v.Update(cmd()) // opened, then the "+ a/app" notice
		}
		synctest.Wait()

		const coverage = "incomplete: 1 streaming, 1 waiting (limit 1)"
		if got := footerText(v, 100); !strings.Contains(got, coverage) {
			t.Fatalf("footer before the search = %q", got)
		}
		for _, query := range []string{"a/app", "no-such-text"} {
			clearViewerSearch(v.inner)
			setViewerSearch(t, v.inner, query)
			got := footerText(v, 100)
			if !strings.Contains(got, "/"+query) || !strings.Contains(got, coverage) {
				t.Fatalf("search %q: footer = %q, want the search and %q", query, got, coverage)
			}
		}
		// Narrow: the search is cut, the coverage stays whole.
		got := footerText(v, 60)
		if !strings.Contains(got, coverage) || lipgloss.Width(got) > 60 {
			t.Fatalf("narrow footer = %q (%d cells), want %q in 60 cells", got, lipgloss.Width(got), coverage)
		}
		clearViewerSearch(v.inner)
		if got := footerText(v, 100); strings.Contains(got, "/no-such-text") || !strings.Contains(got, coverage) {
			t.Fatalf("footer after clearing the search = %q", got)
		}
	})
}

func TestSearchFooterWidths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		search, alert string
		width         int
		want          string
	}{
		{name: "no alert", search: "/err (1/3)", width: 40, want: "/err (1/3)"},
		{name: "both fit", search: "/err (1/3)", alert: "(failed)", width: 40, want: "/err (1/3) | (failed)"},
		{name: "search cut first", search: "/a-long-search-text (1/3)", alert: "(failed)", width: 24, want: "/a-long-searc | (failed)"},
		{name: "alert alone when nothing fits", search: "/a-long-search-text (1/3)", alert: "incomplete: 1 streaming, 1 failed", width: 30, want: "incomplete: 1 streaming, 1 fai"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := searchFooter(tt.search, tt.alert, tt.width)
			if got != tt.want || lipgloss.Width(got) > tt.width {
				t.Fatalf("searchFooter = %q (%d cells), want %q", got, lipgloss.Width(got), tt.want)
			}
		})
	}
}
