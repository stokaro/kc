package logs_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/stokaro/kc/internal/logmux"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
	logswidget "github.com/stokaro/kc/internal/ui/panelcontent/logs"
)

// TestJSONLinesShowFormatted checks that the panel shows a JSON line as the
// logs viewer's formatted view does, and other lines as they are.
func TestJSONLinesShowFormatted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		all  bool
		line string
		want string
	}{
		{
			name: "json line",
			line: `{"level":"error","msg":"payment declined","order":4988}`,
			want: "ERROR payment declined order=4988",
		},
		{
			name: "text line",
			line: "GET /products 200",
			want: "GET /products 200",
		},
		{
			name: "json line of all containers keeps its source",
			all:  true,
			line: logmux.StyledPrefix(logmux.Target{Pod: "pod-a", Container: "app"}) + ` {"level":"warn","msg":"slow upstream"}`,
			want: "[pod-a/app] WARN  slow upstream",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := newFakeSource(map[string]*chanBody{"pod-a": newChanBody(tc.line)})
			w := newWidget(t, src)
			if tc.all {
				if cmd := w.SetSettings(logswidget.Settings{Container: logswidget.ContainerAll}); cmd != nil {
					t.Fatal("without a target nothing is opened")
				}
			}
			update(t, w, run(t, open(t, w, podItem("ns", "pod-a"))))
			got := screen(t, w)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("panel shows\n%s\nwant a line with %q", got, tc.want)
			}
			if strings.Contains(got, `"msg"`) {
				t.Fatalf("panel shows the raw JSON:\n%s", got)
			}
		})
	}
}

// TestExpandedSessionGetsTheLinesAsRead checks that F3 hands the viewer the
// original JSON lines, not the formatted text, so the viewer can parse,
// filter and show them raw.
func TestExpandedSessionGetsTheLinesAsRead(t *testing.T) {
	t.Parallel()
	const line = `{"level":"error","msg":"payment declined"}`
	src := newFakeSource(map[string]*chanBody{"pod-a": newChanBody(line + "\nplain")})
	w := newWidget(t, src)
	update(t, w, run(t, open(t, w, podItem("ns", "pod-a"))))
	sess, err := w.Follow()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sess.Lines, []string{line, "plain"}) {
		t.Fatalf("Lines = %q, want the lines as read", sess.Lines)
	}
}

// TestFormattedLinesKeepThePanelBackground checks that a colored source
// prefix, which ends with a full reset, does not leave the rest of the
// line on the terminal's background.
func TestFormattedLinesKeepThePanelBackground(t *testing.T) {
	t.Parallel()
	const blue = "\x1b[44m"
	line := logmux.StyledPrefix(logmux.Target{Pod: "pod-a", Container: "app"}) + ` {"level":"info","msg":"ok"}`
	src := newFakeSource(map[string]*chanBody{"pod-a": newChanBody(line)})
	w := newWidget(t, src)
	if cmd := w.SetSettings(logswidget.Settings{Container: logswidget.ContainerAll}); cmd != nil {
		t.Fatal("without a target nothing is opened")
	}
	update(t, w, run(t, open(t, w, podItem("ns", "pod-a"))))
	view := w.View(t.Context(), panelcontent.Frame{Size: panelcontent.Size{Width: 80, Height: 10}}).Content
	first, _, _ := strings.Cut(view, "\n")
	for _, reset := range []string{"\x1b[m", "\x1b[0m"} {
		rest := first
		for {
			_, after, found := strings.Cut(rest, reset)
			if !found {
				break
			}
			if strings.TrimSpace(stripSGR(after)) != "" && !strings.HasPrefix(after, blue) {
				t.Fatalf("a reset inside the line is not followed by the background: %q", first)
			}
			rest = after
		}
	}
}

// stripSGR drops the SGR sequences, to tell whether text follows.
func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
