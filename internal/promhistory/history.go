package promhistory

import (
	"context"
	"math"
	"slices"
	"time"

	"golang.org/x/sync/errgroup"
)

// DefaultPoints is how many steps a history window is cut into: about one
// sparkline cell each in a half-screen panel.
const DefaultPoints = 60

// minStep is the shortest step: below the usual 15 to 30 second scrape
// interval more points only repeat samples.
const minStep = 15 * time.Second

// Point is one value of a series at one step.
type Point struct {
	At    time.Time
	Value float64
}

// History is the past usage of one workload.
type History struct {
	Target Target
	Source string
	// Start and End bound the window; points fall on Start + k*Step.
	Start, End time.Time
	Step       time.Duration
	// CPU is in cores, Memory in bytes. A step without a sample has no
	// point (a gap, never a zero). Nil means the backend has no series
	// for the target in the window.
	CPU, Memory []Point
	// Pods is how many pods the owner series tie to the target inside the
	// window, replaced and deleted ones included; PodsKnown is false when
	// that could not be read.
	Pods      int
	PodsKnown bool
	// NoOwners is true when the namespace has no kube_pod_owner series
	// now: kube-state-metrics is missing or not scraped, and pods cannot
	// be tied to the workload at all.
	NoOwners bool
	// Warnings are what the backend reported along with the results.
	Warnings []string
}

// Steps is how many points a complete series has.
func (h History) Steps() int {
	if h.Step <= 0 || !h.End.After(h.Start) {
		return 0
	}
	return int(h.End.Sub(h.Start)/h.Step) + 1
}

// Options shape one read.
type Options struct {
	Window time.Duration
	// Points is how many steps the window is cut into; 0 means
	// DefaultPoints.
	Points int
	// Now is the end of the window; zero means time.Now.
	Now time.Time
}

// window returns the aligned window: the end on a step boundary, so two
// reads within one step ask for the same points.
func (o Options) window() (start, end time.Time, step time.Duration) {
	points := o.Points
	if points <= 0 {
		points = DefaultPoints
	}
	step = max((o.Window / time.Duration(points)).Truncate(time.Second), minStep)
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	end = now.Truncate(step)
	return end.Add(-o.Window), end, step
}

// Fetch reads the history of t. It fails when the CPU or memory query
// fails; the pod count and the owner check only add detail. At most three
// queries run at once.
func (c *Client) Fetch(ctx context.Context, t Target, o Options) (History, error) {
	start, end, step := o.window()
	q, err := buildQueries(c.labels, t, end.Sub(start), step)
	if err != nil {
		return History{}, err
	}
	h := History{Target: t, Source: c.Source, Start: start, End: end, Step: step}
	var cpu, mem, pods []series
	var cpuWarn, memWarn, podsWarn []string
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		cpu, cpuWarn, err = c.queryRange(gctx, q.cpu, start, end, step)
		return err
	})
	g.Go(func() (err error) {
		mem, memWarn, err = c.queryRange(gctx, q.memory, start, end, step)
		return err
	})
	var podsErr error
	g.Go(func() error {
		pods, podsWarn, podsErr = c.query(gctx, q.pods, end)
		return nil
	})
	if err := g.Wait(); err != nil {
		return History{}, err
	}
	h.CPU, h.Memory = points(cpu), points(mem)
	if podsErr == nil {
		h.Pods, h.PodsKnown = int(scalar(pods)), true
	}
	h.Warnings = dedupe(cpuWarn, memWarn, podsWarn)
	if h.CPU == nil && h.Memory == nil {
		// Say why nothing came back: no owner series means no workload
		// history can be built, which differs from a workload without
		// pods in the window.
		if owners, _, err := c.query(ctx, q.owners, end); err == nil && scalar(owners) == 0 {
			h.NoOwners = true
		}
	}
	return h, nil
}

// points returns the samples of the single series, NaN and infinities
// dropped as gaps. Nil when there is no series.
func points(ss []series) []Point {
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

// scalar returns the value of a one-sample vector; 0 when empty (count()
// over nothing returns no series).
func scalar(ss []series) float64 {
	if len(ss) == 0 || len(ss[0].points) == 0 {
		return 0
	}
	return ss[0].points[0].value
}

func dedupe(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		for _, w := range l {
			if !slices.Contains(out, w) {
				out = append(out, w)
			}
		}
	}
	return out
}

// Missing returns how many steps of the window have no point in ps.
func (h History) Missing(ps []Point) int {
	return max(h.Steps()-len(h.indexes(ps)), 0)
}

// indexes maps each point to its step; points off the window are dropped.
func (h History) indexes(ps []Point) map[int]float64 {
	n := h.Steps()
	out := make(map[int]float64, len(ps))
	for _, p := range ps {
		i := int(math.Round(float64(p.At.Sub(h.Start)) / float64(h.Step)))
		if i >= 0 && i < n {
			out[i] = p.Value
		}
	}
	return out
}

// Buckets folds the window into width cells, oldest first, each the
// largest value of its steps. A cell without any point is NaN: a gap.
func (h History) Buckets(ps []Point, width int) []float64 {
	n := h.Steps()
	if width <= 0 || n == 0 {
		return nil
	}
	width = min(width, n)
	out := make([]float64, width)
	for i := range out {
		out[i] = math.NaN()
	}
	for i, v := range h.indexes(ps) {
		cell := i * width / n
		if math.IsNaN(out[cell]) || v > out[cell] {
			out[cell] = v
		}
	}
	return out
}
