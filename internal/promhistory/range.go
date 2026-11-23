package promhistory

import (
	"context"
	"fmt"
	"math"
	"time"
)

// MaxPoints bounds the steps of one range read, whatever the width of the
// chart that asks: a wide terminal must not multiply the samples the
// server evaluates.
const MaxPoints = 480

// Range is the time axis of one read: points fall on Start + k*Step, up
// to End.
type Range struct {
	Start, End time.Time
	Step       time.Duration
}

// RangeFor returns the range that shows start..end in cells columns, one
// step per column: the step is the span divided by the columns, in whole
// seconds and at least minStep, and both ends are aligned to it so that
// two reads within one step ask for the same points. cells above
// MaxPoints is cut to MaxPoints.
func RangeFor(start, end time.Time, cells int) Range {
	cells = max(1, min(cells, MaxPoints))
	span := max(end.Sub(start), minStep)
	step := max(ceilDuration(span/time.Duration(cells)+time.Duration(min(span%time.Duration(cells), 1)), time.Second), minStep)
	e := end.Truncate(step)
	n := max(ceilDuration(e.Sub(start), step)/step, 1)
	return Range{Start: e.Add(-n * step), End: e, Step: step}
}

// ceilDuration rounds d up to a multiple of unit.
func ceilDuration(d, unit time.Duration) time.Duration {
	if r := d % unit; r != 0 {
		return d + unit - r
	}
	return d
}

// Steps is how many points a complete series of r has.
func (r Range) Steps() int {
	if r.Step <= 0 || r.End.Before(r.Start) {
		return 0
	}
	return int(r.End.Sub(r.Start)/r.Step) + 1
}

// Valid reports whether r can be asked for.
func (r Range) Valid() bool { return r.Step > 0 && r.End.After(r.Start) }

// key is r in a form fit for a cache key.
func (r Range) key() string {
	return fmt.Sprintf("%d:%d:%d", r.Start.UnixMilli(), r.End.UnixMilli(), r.Step.Milliseconds())
}

// rangeSeries runs a range query that sums or counts to at most one
// series and returns its finite points.
func (c *Client) rangeSeries(ctx context.Context, expr string, r Range) ([]Point, []string, error) {
	ss, warnings, err := c.queryRange(ctx, expr, r.Start, r.End, r.Step)
	if err != nil {
		return nil, warnings, err
	}
	if len(ss) > 1 {
		return nil, warnings, fmt.Errorf("prometheus %s: %d series where one was expected", c.Source, len(ss))
	}
	return finitePoints(ss), warnings, nil
}

// finitePoints is points without dropping the empty case to nil: an empty
// matrix is no series, nil.
func finitePoints(ss []series) []Point {
	if len(ss) == 0 {
		return nil
	}
	out := make([]Point, 0, len(ss[0].points))
	for _, p := range ss[0].points {
		if math.IsNaN(p.value) || math.IsInf(p.value, 0) {
			continue
		}
		out = append(out, Point{At: p.at, Value: p.value})
	}
	return out
}
