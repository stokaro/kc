// Package textdiff computes line diffs and renders them in unified format.
//
// The diff is the linear-space variant of Myers' O(ND) algorithm ("An O(ND)
// Difference Algorithm and Its Variations", 1986, section 4b), so memory stays
// proportional to the input even when the two texts have nothing in common.
package textdiff

import (
	"fmt"
	"strings"
)

// Op is the kind of a line edit.
type Op int

const (
	// Equal keeps a line that both texts share.
	Equal Op = iota
	// Delete removes a line of the left text.
	Delete
	// Insert adds a line of the right text.
	Insert
)

// Edit is one line of a diff script.
type Edit struct {
	Op   Op
	Text string
}

// Lines returns a shortest edit script that turns a into b.
func Lines(a, b []string) []Edit {
	ids := make(map[string]int, len(a)+len(b))
	intern := func(lines []string) []int {
		out := make([]int, len(lines))
		for i, l := range lines {
			id, ok := ids[l]
			if !ok {
				id = len(ids)
				ids[l] = id
			}
			out[i] = id
		}
		return out
	}
	d := differ{a: intern(a), b: intern(b)}
	d.compare(0, len(a), 0, len(b))

	edits := make([]Edit, 0, len(a)+len(b)-len(d.matches))
	i, j := 0, 0
	for _, m := range append(d.matches, match{len(a), len(b)}) {
		for ; i < m.a; i++ {
			edits = append(edits, Edit{Op: Delete, Text: a[i]})
		}
		for ; j < m.b; j++ {
			edits = append(edits, Edit{Op: Insert, Text: b[j]})
		}
		if i < len(a) && j < len(b) {
			edits = append(edits, Edit{Op: Equal, Text: a[i]})
			i++
			j++
		}
	}
	return edits
}

type match struct{ a, b int }

type differ struct {
	a, b    []int
	matches []match // matched line pairs, in increasing order
}

// compare records the matches of a[aLo:aHi] against b[bLo:bHi].
func (d *differ) compare(aLo, aHi, bLo, bHi int) {
	for aLo < aHi && bLo < bHi && d.a[aLo] == d.b[bLo] {
		d.matches = append(d.matches, match{aLo, bLo})
		aLo++
		bLo++
	}
	suffix := 0
	for aHi > aLo && bHi > bLo && d.a[aHi-1] == d.b[bHi-1] {
		aHi--
		bHi--
		suffix++
	}
	// With the common ends removed and both sides non-empty, the edit
	// distance is at least 2, so both halves around the middle snake are
	// strictly smaller problems and the recursion ends.
	if aLo < aHi && bLo < bHi {
		sn := d.middleSnake(aLo, aHi, bLo, bHi)
		d.compare(aLo, sn.x, bLo, sn.y)
		for x, y := sn.x, sn.y; x < sn.u; x, y = x+1, y+1 {
			d.matches = append(d.matches, match{x, y})
		}
		d.compare(sn.u, aHi, sn.v, bHi)
	}
	for k := range suffix {
		d.matches = append(d.matches, match{aHi + k, bHi + k})
	}
}

// maxCost bounds the edit distance middleSnake searches for. Past it the
// search stops at the furthest point it reached and splits the problem
// there. The script then may be longer than the shortest one, but the time
// stays near-linear for large, mostly different inputs (Myers is O(ND)).
const maxCost = 2048

// snake is a run of matches from (x, y) to (u, v), in absolute coordinates.
// x == u for an empty snake.
type snake struct{ x, y, u, v int }

// snakeSearch is the state of one middleSnake search through
// a[aLo:aHi] x b[bLo:bHi].
type snakeSearch struct {
	d                  *differ
	aLo, aHi, bLo, bHi int
	n, m, delta, off   int
	odd                bool
	vf                 []int // furthest x on each forward diagonal
	vb                 []int // furthest distance from the end on each backward diagonal
}

// middleSnake finds the middle snake of an optimal path through
// a[aLo:aHi] x b[bLo:bHi].
func (d *differ) middleSnake(aLo, aHi, bLo, bHi int) snake {
	n, m := aHi-aLo, bHi-bLo
	maxD := (n + m + 1) / 2
	s := &snakeSearch{
		d: d, aLo: aLo, aHi: aHi, bLo: bLo, bHi: bHi,
		n: n, m: m, delta: n - m, off: maxD + 1,
		vf: make([]int, 2*maxD+3),
		vb: make([]int, 2*maxD+3),
	}
	s.odd = s.delta%2 != 0

	for rnd := 0; rnd <= maxD; rnd++ {
		if rnd > maxCost {
			return s.furthestForward(rnd - 1)
		}
		if sn, ok := s.forward(rnd); ok {
			return sn
		}
		if sn, ok := s.backward(rnd); ok {
			return sn
		}
	}
	// Unreachable: an optimal path has at most n+m edits.
	panic("textdiff: middle snake not found")
}

// forward runs round rnd of the forward search. It reports the snake where
// the forward and backward paths overlap, when they do.
func (s *snakeSearch) forward(rnd int) (snake, bool) {
	vf, vb, off := s.vf, s.vb, s.off
	for k := -rnd; k <= rnd; k += 2 {
		var px int
		if k == -rnd || (k != rnd && vf[off+k-1] < vf[off+k+1]) {
			px = vf[off+k+1]
		} else {
			px = vf[off+k-1] + 1
		}
		py := px - k
		sx, sy := px, py
		for px < s.n && py < s.m && s.d.a[s.aLo+px] == s.d.b[s.bLo+py] {
			px++
			py++
		}
		vf[off+k] = px
		if kr := s.delta - k; s.odd && kr >= -(rnd-1) && kr <= rnd-1 && px+vb[off+kr] >= s.n {
			return snake{s.aLo + sx, s.bLo + sy, s.aLo + px, s.bLo + py}, true
		}
	}
	return snake{}, false
}

// backward runs round rnd of the backward search, as forward does.
func (s *snakeSearch) backward(rnd int) (snake, bool) {
	vf, vb, off := s.vf, s.vb, s.off
	for k := -rnd; k <= rnd; k += 2 {
		var px int
		if k == -rnd || (k != rnd && vb[off+k-1] < vb[off+k+1]) {
			px = vb[off+k+1]
		} else {
			px = vb[off+k-1] + 1
		}
		py := px - k
		sx, sy := px, py
		for px < s.n && py < s.m && s.d.a[s.aHi-1-px] == s.d.b[s.bHi-1-py] {
			px++
			py++
		}
		vb[off+k] = px
		if kf := s.delta - k; !s.odd && kf >= -rnd && kf <= rnd && px+vf[off+kf] >= s.n {
			return snake{s.aLo + s.n - px, s.bLo + s.m - py, s.aLo + s.n - sx, s.bLo + s.m - sy}, true
		}
	}
	return snake{}, false
}

// furthestForward returns, as an empty snake, the point of the forward search
// after round rnd that is furthest from the start. It is neither the start
// nor the end, so both halves are smaller problems.
func (s *snakeSearch) furthestForward(rnd int) snake {
	best, bx, by := -1, 0, 0
	for k := -rnd; k <= rnd; k += 2 {
		px := min(s.vf[s.off+k], s.n)
		py := px - k
		if py < 0 || py > s.m {
			continue
		}
		if px+py > best {
			best, bx, by = px+py, px, py
		}
	}
	return snake{s.aLo + bx, s.bLo + by, s.aLo + bx, s.bLo + by}
}

// Hunk is a group of changes with the lines of context around them.
type Hunk struct {
	// LeftStart and RightStart are zero-based line indexes of the first
	// line of the hunk in each text.
	LeftStart, LeftLen   int
	RightStart, RightLen int
	Edits                []Edit
}

// Header returns the hunk's "@@ -l,s +l,s @@" line, as diff -u writes it.
func (h Hunk) Header() string {
	return fmt.Sprintf("@@ -%s +%s @@", rangeSpec(h.LeftStart, h.LeftLen), rangeSpec(h.RightStart, h.RightLen))
}

func rangeSpec(start, n int) string {
	switch n {
	case 0:
		return fmt.Sprintf("%d,0", start)
	case 1:
		return fmt.Sprintf("%d", start+1)
	default:
		return fmt.Sprintf("%d,%d", start+1, n)
	}
}

// Hunks groups an edit script into hunks with up to context unchanged lines
// before and after each change. Changes closer than 2*context lines share a
// hunk. An all-equal script has no hunks.
func Hunks(edits []Edit, context int) []Hunk {
	context = max(context, 0)
	var hunks []Hunk
	var cur *Hunk
	ai, bi := 0, 0   // line indexes before edits[i]
	lastChange := -1 // index in edits of the last change in cur
	for i, e := range edits {
		if e.Op != Equal {
			if cur == nil || i-lastChange-1 > 2*context {
				if cur != nil {
					closeHunk(cur, edits, lastChange, context)
					hunks = append(hunks, *cur)
				}
				lead := 0
				for lead < context && i-lead-1 >= 0 && edits[i-lead-1].Op == Equal {
					lead++
				}
				cur = &Hunk{LeftStart: ai - lead, RightStart: bi - lead}
				cur.Edits = append(cur.Edits, edits[i-lead:i]...)
			} else {
				cur.Edits = append(cur.Edits, edits[lastChange+1:i]...)
			}
			cur.Edits = append(cur.Edits, e)
			lastChange = i
		}
		switch e.Op {
		case Equal:
			ai++
			bi++
		case Delete:
			ai++
		case Insert:
			bi++
		}
	}
	if cur != nil {
		closeHunk(cur, edits, lastChange, context)
		hunks = append(hunks, *cur)
	}
	for i := range hunks {
		for _, e := range hunks[i].Edits {
			if e.Op != Insert {
				hunks[i].LeftLen++
			}
			if e.Op != Delete {
				hunks[i].RightLen++
			}
		}
	}
	return hunks
}

func closeHunk(h *Hunk, edits []Edit, lastChange, context int) {
	end := min(len(edits), lastChange+1+context)
	h.Edits = append(h.Edits, edits[lastChange+1:end]...)
}

// Unified is a rendered unified diff.
type Unified struct {
	// Text is the diff, one line per row, without a trailing newline.
	// It is empty when the inputs are equal.
	Text string
	// HunkLines holds the zero-based row of each hunk header in Text.
	HunkLines []int
}

// Equal reports whether the inputs had no differences.
func (u Unified) Equal() bool { return len(u.HunkLines) == 0 }

// UnifiedDiff renders the differences between a and b like diff -u, with
// "--- leftName" and "+++ rightName" headers and context lines around each
// change. A trailing newline at the end of either text is ignored.
func UnifiedDiff(leftName, rightName, a, b string, context int) Unified {
	hunks := Hunks(Lines(splitLines(a), splitLines(b)), context)
	if len(hunks) == 0 {
		return Unified{}
	}
	rows := []string{"--- " + leftName, "+++ " + rightName}
	var at []int
	for _, h := range hunks {
		at = append(at, len(rows))
		rows = append(rows, h.Header())
		for _, e := range h.Edits {
			rows = append(rows, prefix(e.Op)+e.Text)
		}
	}
	return Unified{Text: strings.Join(rows, "\n"), HunkLines: at}
}

func prefix(op Op) string {
	switch op {
	case Delete:
		return "-"
	case Insert:
		return "+"
	default:
		return " "
	}
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
