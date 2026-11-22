package textdiff_test

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/stokaro/kc/internal/textdiff"
)

func TestUnifiedDiff(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		a, b  string
		ctx   int
		want  string
		hunks []int
	}{
		{
			name: "equal",
			a:    "a\nb\n",
			b:    "a\nb\n",
			ctx:  3,
		},
		{
			name: "both empty",
			ctx:  3,
		},
		{
			name:  "one changed line in the middle",
			a:     "1\n2\n3\n4\n5\n6\n7\n8\n9\n",
			b:     "1\n2\n3\n4\nX\n6\n7\n8\n9\n",
			ctx:   3,
			want:  "--- L\n+++ R\n@@ -2,7 +2,7 @@\n 2\n 3\n 4\n-5\n+X\n 6\n 7\n 8",
			hunks: []int{2},
		},
		{
			name:  "insert into empty",
			a:     "",
			b:     "x\ny\n",
			ctx:   3,
			want:  "--- L\n+++ R\n@@ -0,0 +1,2 @@\n+x\n+y",
			hunks: []int{2},
		},
		{
			name:  "delete everything",
			a:     "x\n",
			b:     "",
			ctx:   3,
			want:  "--- L\n+++ R\n@@ -1 +0,0 @@\n-x",
			hunks: []int{2},
		},
		{
			name:  "insert at the start",
			a:     "b\nc\n",
			b:     "a\nb\nc\n",
			ctx:   1,
			want:  "--- L\n+++ R\n@@ -1 +1,2 @@\n+a\n b",
			hunks: []int{2},
		},
		{
			name:  "distant changes make two hunks",
			a:     "a\n1\n2\n3\nb\n",
			b:     "A\n1\n2\n3\nB\n",
			ctx:   1,
			want:  "--- L\n+++ R\n@@ -1,2 +1,2 @@\n-a\n+A\n 1\n@@ -4,2 +4,2 @@\n 3\n-b\n+B",
			hunks: []int{2, 6},
		},
		{
			name:  "close changes share a hunk",
			a:     "a\n1\n2\nb\n",
			b:     "A\n1\n2\nB\n",
			ctx:   1,
			want:  "--- L\n+++ R\n@@ -1,4 +1,4 @@\n-a\n+A\n 1\n 2\n-b\n+B",
			hunks: []int{2},
		},
		{
			name:  "missing trailing newline is ignored",
			a:     "a\nb",
			b:     "a\nb\n",
			ctx:   3,
			want:  "",
			hunks: nil,
		},
		{
			name:  "zero context",
			a:     "a\nb\nc\n",
			b:     "a\nB\nc\n",
			ctx:   0,
			want:  "--- L\n+++ R\n@@ -2 +2 @@\n-b\n+B",
			hunks: []int{2},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := textdiff.UnifiedDiff("L", "R", tc.a, tc.b, tc.ctx)
			if got.Text != tc.want {
				t.Errorf("text:\n%s\nwant:\n%s", got.Text, tc.want)
			}
			if !slices.Equal(got.HunkLines, tc.hunks) {
				t.Errorf("hunk lines = %v, want %v", got.HunkLines, tc.hunks)
			}
			if got.Equal() != (tc.want == "") {
				t.Errorf("Equal() = %v", got.Equal())
			}
		})
	}
}

// TestLinesIsShortest checks the edit script on random inputs: it must turn
// a into b and be as short as the LCS allows.
func TestLinesIsShortest(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(1, 2)) // #nosec G404 -- seeded, reproducible test input
	gen := func() []string {
		n := rng.IntN(30)
		out := make([]string, n)
		for i := range out {
			out[i] = string('a' + rng.Int32N(4))
		}
		return out
	}
	for i := range 2000 {
		a, b := gen(), gen()
		edits := textdiff.Lines(a, b)
		var gotA, gotB []string
		changes := 0
		for _, e := range edits {
			if e.Op != textdiff.Insert {
				gotA = append(gotA, e.Text)
			}
			if e.Op != textdiff.Delete {
				gotB = append(gotB, e.Text)
			}
			if e.Op != textdiff.Equal {
				changes++
			}
		}
		if strings.Join(gotA, ",") != strings.Join(a, ",") || strings.Join(gotB, ",") != strings.Join(b, ",") {
			t.Fatalf("case %d: script does not rebuild the inputs\na=%v\nb=%v\nedits=%v", i, a, b, edits)
		}
		if want := len(a) + len(b) - 2*lcs(a, b); changes != want {
			t.Fatalf("case %d: %d changes, shortest is %d\na=%v\nb=%v", i, changes, want, a, b)
		}
	}
}

func TestLinesLargeDisjointInput(t *testing.T) {
	t.Parallel()
	a := make([]string, 20000)
	b := make([]string, 20000)
	for i := range a {
		a[i] = "a" + string(rune(i))
		b[i] = "b" + string(rune(i))
	}
	if got := len(textdiff.Lines(a, b)); got != 40000 {
		t.Fatalf("got %d edits, want 40000", got)
	}
}

// TestLinesPastCostLimit checks that a script cut short by the cost limit
// still turns a into b.
func TestLinesPastCostLimit(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(5, 6)) // #nosec G404 -- seeded, reproducible test input
	gen := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = string('a' + rng.Int32N(50))
		}
		return out
	}
	a, b := gen(6000), gen(6000)
	var gotA, gotB []string
	for _, e := range textdiff.Lines(a, b) {
		if e.Op != textdiff.Insert {
			gotA = append(gotA, e.Text)
		}
		if e.Op != textdiff.Delete {
			gotB = append(gotB, e.Text)
		}
	}
	if !slices.Equal(gotA, a) || !slices.Equal(gotB, b) {
		t.Fatal("script does not rebuild the inputs")
	}
}

func lcs(a, b []string) int {
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i, v := range slices.Backward(a) {
		for j := len(b) - 1; j >= 0; j-- {
			if v == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	return dp[0][0]
}

func BenchmarkLines(b *testing.B) {
	rng := rand.New(rand.NewPCG(3, 4)) // #nosec G404 -- seeded, reproducible test input
	left := make([]string, 2000)
	for i := range left {
		left[i] = string('a' + rng.Int32N(20))
	}
	right := append([]string(nil), left...)
	for range 50 {
		right[rng.IntN(len(right))] = "changed"
	}
	b.ReportAllocs()
	for b.Loop() {
		textdiff.Lines(left, right)
	}
}
