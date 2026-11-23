package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestWrapCompareHeader(t *testing.T) {
	t.Parallel()
	const left = "# left:    context local file /tmp/kc-shots/manifests/web.yaml, gvr apps/v1/deployments, namespace shop, name web, sha256 5909f4a9c91a"
	const diff = "-      - image: nginx:1.28-alpine, a diff line that is longer than the width and has commas, so it must stay as it is"
	text := strings.Join([]string{left, "# view:    configuration", "", "--- left/x", diff}, "\n")
	tests := []struct {
		name  string
		width int
		want  []string
	}{
		{name: "wide enough", width: 200, want: strings.Split(text, "\n")},
		{name: "no width yet", width: 0, want: strings.Split(text, "\n")},
		{name: "wrapped at the parts", width: 81, want: []string{
			"# left:    context local file /tmp/kc-shots/manifests/web.yaml,",
			"#          gvr apps/v1/deployments, namespace shop, name web, sha256 5909f4a9c91a",
			"# view:    configuration", "", "--- left/x", diff,
		}},
		{name: "a part wider than the screen is cut", width: 40, want: []string{
			"# left:    context local file /tmp/kc-sh",
			"#          ots/manifests/web.yaml,",
			"#          gvr apps/v1/deployments,",
			"#          namespace shop, name web,",
			"#          sha256 5909f4a9c91a",
			"# view:    configuration", "", "--- left/x", diff,
		}},
		{name: "no room beside the label leaves the parts whole", width: 18, want: []string{
			"# left:    context local file /tmp/kc-shots/manifests/web.yaml,",
			"#          gvr apps/v1/deployments,",
			"#          namespace shop,",
			"#          name web,",
			"#          sha256 5909f4a9c91a",
			"# view:    configuration", "", "--- left/x", diff,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, added := wrapCompareHeader(text, tt.width)
			if got != strings.Join(tt.want, "\n") {
				t.Errorf("wrapped:\n%s\nwant:\n%s", got, strings.Join(tt.want, "\n"))
			}
			if want := len(tt.want) - len(strings.Split(text, "\n")); added != want {
				t.Errorf("added = %d, want %d", added, want)
			}
		})
	}
}

// The viewer wraps the header to its width, so "# left:" no longer runs off
// the screen; n still lands on the hunk headers, and a resize wraps again
// and keeps the diff row at the top.
func TestDiffViewerWrapsHeader(t *testing.T) {
	t.Parallel()
	snap := compareSnap("a", map[int]string{2: "x", 30: "y"})
	snap.left.Context = "local file /tmp/kc-shots/manifests/web.yaml"
	snap.left.Revision = "sha256 5909f4a9c91a, modified 2026-11-23T10:00:00Z"
	v := loadedViewer(t, snap)
	v.SetDimensions(70, 12)

	rows := strings.Split(v.inner.PlainText(), "\n")
	header := 0
	for ; header < len(rows) && rows[header] != ""; header++ {
		if w := ansi.StringWidth(rows[header]); w > 70 {
			t.Errorf("header row %d is %d wide: %q", header, w, rows[header])
		}
	}
	if v.headerRows == 0 || !strings.HasPrefix(rows[1], "#          ") {
		t.Fatalf("header not wrapped (added %d):\n%s", v.headerRows, strings.Join(rows[:header], "\n"))
	}

	for i, at := range v.result.ChangeLines {
		if row := rows[at+v.headerRows]; !strings.HasPrefix(row, "@@ ") {
			t.Fatalf("change %d is on row %q", i, row)
		}
	}
	pressDiffKey(v, "n")
	if top := rows[v.inner.Offset()]; v.current != 0 || !strings.HasPrefix(top, "@@ ") {
		t.Fatalf("n: top row %q, current %d", top, v.current)
	}

	// A wider screen wraps less; the same diff row stays on top.
	want, narrow := rows[v.inner.Offset()], v.headerRows
	v.SetDimensions(140, 12)
	rows = strings.Split(v.inner.PlainText(), "\n")
	if top := rows[v.inner.Offset()]; top != want {
		t.Errorf("after the resize the top row is %q, want %q", top, want)
	}
	if v.headerRows >= narrow {
		t.Errorf("header rows added at 140 columns = %d, at 70 = %d", v.headerRows, narrow)
	}
}
