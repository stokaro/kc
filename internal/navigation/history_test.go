package navigation_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/stokaro/kc/internal/navigation"
)

func loc(steps ...string) navigation.Location {
	return navigation.Location{Context: "ctx", Namespace: "default", Steps: steps, Path: "/" + strings.Join(steps, "/")}
}

// op is one action on a History. kind is "record", "back", "forward" or
// "go"; for back, forward and go, want is the expected path ("" when the
// move is not possible).
type op struct {
	kind  string
	loc   navigation.Location
	index int
	want  string
}

func record(steps ...string) op { return op{kind: "record", loc: loc(steps...)} }
func back(want string) op       { return op{kind: "back", want: want} }
func forward(want string) op    { return op{kind: "forward", want: want} }
func goTo(i int, want string) op {
	return op{kind: "go", index: i, want: want}
}

func TestHistory(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		limit       int
		ops         []op
		wantPaths   []string
		wantCurrent string
	}{
		{
			name:        "empty history cannot move",
			ops:         []op{back(""), forward("")},
			wantPaths:   nil,
			wantCurrent: "",
		},
		{
			name:        "back and forward walk the list",
			ops:         []op{record(), record("pods"), record("pods", "web"), back("/pods"), back("/"), back(""), forward("/pods"), forward("/pods/web"), forward("")},
			wantPaths:   []string{"/", "/pods", "/pods/web"},
			wantCurrent: "/pods/web",
		},
		{
			name:        "recording after back drops the forward entries",
			ops:         []op{record(), record("pods"), record("pods", "web"), back("/pods"), back("/"), record("svc"), forward("")},
			wantPaths:   []string{"/", "/svc"},
			wantCurrent: "/svc",
		},
		{
			name:        "recording the current location again is a no-op",
			ops:         []op{record(), record("pods"), record("pods")},
			wantPaths:   []string{"/", "/pods"},
			wantCurrent: "/pods",
		},
		{
			name:        "a location is kept once and moves to the end",
			ops:         []op{record("a"), record("b"), record("c"), record("a"), back("/c"), back("/b"), back("")},
			wantPaths:   []string{"/b", "/c", "/a"},
			wantCurrent: "/b",
		},
		{
			name:        "the limit drops the oldest entries",
			limit:       2,
			ops:         []op{record("a"), record("b"), record("c"), back("/b"), back("")},
			wantPaths:   []string{"/b", "/c"},
			wantCurrent: "/b",
		},
		{
			name:        "go moves the cursor without truncating",
			ops:         []op{record("a"), record("b"), record("c"), goTo(0, "/a"), forward("/b"), goTo(5, "")},
			wantPaths:   []string{"/a", "/b", "/c"},
			wantCurrent: "/b",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := navigation.NewHistory(tc.limit)
			for i, o := range tc.ops {
				var got navigation.Location
				var ok bool
				switch o.kind {
				case "record":
					h.Record(o.loc)
					continue
				case "back":
					got, ok = h.Back()
				case "forward":
					got, ok = h.Forward()
				case "go":
					got, ok = h.Go(o.index)
				}
				if o.want == "" {
					if ok {
						t.Fatalf("op %d %s: moved to %q, want no move", i, o.kind, got.Path)
					}
					continue
				}
				if !ok || got.Path != o.want {
					t.Fatalf("op %d %s: got %q (ok=%v), want %q", i, o.kind, got.Path, ok, o.want)
				}
			}
			entries, _ := h.Entries()
			var paths []string
			for _, e := range entries {
				paths = append(paths, e.Path)
			}
			if !slices.Equal(paths, tc.wantPaths) {
				t.Fatalf("entries = %v, want %v", paths, tc.wantPaths)
			}
			cur, _ := h.Current()
			if cur.Path != tc.wantCurrent {
				t.Fatalf("current = %q, want %q", cur.Path, tc.wantCurrent)
			}
		})
	}
}

func TestHistoryZeroValue(t *testing.T) {
	t.Parallel()
	var h navigation.History
	for i := range navigation.DefaultHistoryLimit + 5 {
		h.Record(loc(string(rune('a'+i%26)), string(rune('0'+i/26))))
	}
	entries, idx := h.Entries()
	if len(entries) != navigation.DefaultHistoryLimit || idx != len(entries)-1 {
		t.Fatalf("len=%d idx=%d, want %d entries with the cursor on the last", len(entries), idx, navigation.DefaultHistoryLimit)
	}
}

func TestHistoryRecordCopiesSteps(t *testing.T) {
	t.Parallel()
	h := navigation.NewHistory(0)
	steps := []string{"pods"}
	h.Record(navigation.Location{Steps: steps})
	steps[0] = "changed"
	cur, _ := h.Current()
	if cur.Steps[0] != "pods" {
		t.Fatalf("history shares the caller's steps: %v", cur.Steps)
	}
}

func TestLocationEqualIgnoresPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		a, b navigation.Location
		want bool
	}{
		{"same", loc("pods"), loc("pods"), true},
		{"path differs", navigation.Location{Steps: []string{"x"}, Path: "/a"}, navigation.Location{Steps: []string{"x"}, Path: "/b"}, true},
		{"context differs", navigation.Location{Context: "a"}, navigation.Location{Context: "b"}, false},
		{"namespace differs", navigation.Location{Namespace: "a"}, navigation.Location{Namespace: "b"}, false},
		{"steps differ", loc("pods"), loc("svc"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.a.Equal(tc.b); got != tc.want {
				t.Fatalf("Equal = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLocationCommonSteps(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		a, b navigation.Location
		want int
	}{
		{"both root", loc(), loc(), 0},
		{"prefix", loc("pods"), loc("pods", "web"), 1},
		{"diverge", loc("pods", "web"), loc("pods", "db"), 1},
		{"unrelated", loc("pods"), loc("svc"), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.a.CommonSteps(tc.b); got != tc.want {
				t.Fatalf("CommonSteps = %d, want %d", got, tc.want)
			}
		})
	}
}
