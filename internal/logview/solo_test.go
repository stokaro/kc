package logview_test

import (
	"slices"
	"testing"

	"github.com/stokaro/kc/internal/logview"
)

// TestLogSoloHidesWithoutDropping checks that Solo shows one source's
// records, combines with the query, keeps the other records within the
// limits, and shows them again when cleared.
func TestLogSoloHidesWithoutDropping(t *testing.T) {
	t.Parallel()
	app := logview.Source{Pod: "p", Container: "app"}
	side := logview.Source{Pod: "p", Container: "side"}
	l := logview.NewLog(logview.Format{})
	l.Append(app, "a1 ok")
	l.Append(side, "s1 ok")

	l.SetSolo(app, true)
	if got, on := l.Solo(); !on || got != app {
		t.Fatalf("Solo() = %v, %v; want p/app", got, on)
	}
	shown, _ := l.Append(side, "s2")
	if len(shown) != 0 {
		t.Fatalf("a record of another source was shown: %q", shown)
	}
	if shown, _ := l.Append(app, "a2"); len(shown) != 1 {
		t.Fatalf("a record of the solo source returned %q", shown)
	}
	if got := l.Originals(); !slices.Equal(got, []string{"a1 ok", "a2"}) {
		t.Fatalf("Originals() = %q under solo", got)
	}
	if l.Len() != 4 || l.Shown() != 2 {
		t.Fatalf("Len %d Shown %d, want 4 kept and 2 shown", l.Len(), l.Shown())
	}

	l.SetQuery(logview.MustParseQuery("ok"))
	if got := l.Originals(); !slices.Equal(got, []string{"a1 ok"}) {
		t.Fatalf("solo and query give %q", got)
	}

	l.SetSolo(logview.Source{}, false)
	if got := l.Originals(); !slices.Equal(got, []string{"a1 ok", "s1 ok"}) {
		t.Fatalf("clearing solo gives %q, want the query alone", got)
	}
	l.SetQuery(nil)
	if got := l.Originals(); !slices.Equal(got, []string{"a1 ok", "s1 ok", "s2", "a2"}) {
		t.Fatalf("Originals() = %q, want every record back", got)
	}
}
