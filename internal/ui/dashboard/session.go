package dashboard

import (
	"time"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/ui/chart"
)

const (
	// SessionRetention is how long the session history keeps samples.
	SessionRetention = time.Hour
	// sessionTargets bounds the targets with a session history; the one
	// sampled longest ago is dropped first.
	sessionTargets = 32
	// maxSessionSamples bounds one target's samples should usage be read
	// faster than polled.
	maxSessionSamples = 4 * int(SessionRetention/models.UsagePollInterval)
	// sessionHold is how long a sample stands for the chart columns after
	// it: one missed poll is jitter, two are a gap (ADR 0177).
	sessionHold = 2*models.UsagePollInterval + models.UsagePollInterval/2
)

// sample is one Metrics API reading of a target, with the requests,
// limits and allocatable of that moment.
type sample struct {
	at, readAt         time.Time
	cpu, mem           float64
	measured, expected int
	cpuRes, memRes     sampleRefs
}

// sampleRefs are the reference values of one resource at a sample. A
// limit is zero when one pod had none: then the sum is no ceiling.
type sampleRefs struct{ requests, limits, capacity float64 }

func refsOf(a models.ResourceAmount) sampleRefs {
	r := sampleRefs{requests: a.Requests, capacity: a.Capacity}
	if !a.LimitsPartial {
		r.limits = a.Limits
	}
	return r
}

// series is the session history of one target, oldest first.
type series struct {
	samples []sample
	touched time.Time
}

// session holds the Metrics API history the dashboard collected in this
// kc session, per target. It lives with the dashboard across closing and
// reopening, and is never written to disk.
type session struct {
	byKey map[string]*series
}

func newSession() *session { return &session{byKey: make(map[string]*series)} }

// add records a reading of key if it is new and measured. It returns
// whether it was added.
func (s *session) add(key string, a models.UsageAmounts) bool {
	if a.ReadAt.IsZero() || !a.CPU.UsedKnown {
		return false
	}
	h := s.byKey[key]
	if h == nil {
		s.evict()
		h = &series{}
		s.byKey[key] = h
	}
	h.touched = a.ReadAt
	if n := len(h.samples); n > 0 && !a.ReadAt.After(h.samples[n-1].readAt) {
		return false
	}
	h.samples = append(h.samples, sample{
		at: a.At, readAt: a.ReadAt, cpu: a.CPU.Used, mem: a.Memory.Used, measured: a.Measured, expected: a.Running,
		cpuRes: refsOf(a.CPU), memRes: refsOf(a.Memory),
	})
	cut := 0
	for cut < len(h.samples)-1 && a.At.Sub(h.samples[cut].at) > SessionRetention {
		cut++
	}
	cut = max(cut, len(h.samples)-maxSessionSamples)
	h.samples = h.samples[cut:]
	return true
}

func (s *session) evict() {
	if len(s.byKey) < sessionTargets {
		return
	}
	oldest := ""
	for k, h := range s.byKey {
		if oldest == "" || h.touched.Before(s.byKey[oldest].touched) {
			oldest = k
		}
	}
	delete(s.byKey, oldest)
}

// points returns key's CPU and memory samples.
func (s *session) points(key string) (cpu, mem []chart.Point) {
	h := s.byKey[key]
	if h == nil {
		return nil, nil
	}
	for _, x := range h.samples {
		cpu = append(cpu, chart.Point{At: x.at, V: x.cpu})
		mem = append(mem, chart.Point{At: x.at, V: x.mem})
	}
	return cpu, mem
}

// refPoints returns key's requests, limits and allocatable of one
// resource as they were at each sample.
func (s *session) refPoints(key string, memory bool) (requests, limits, capacity []chart.Point) {
	h := s.byKey[key]
	if h == nil {
		return nil, nil, nil
	}
	for _, x := range h.samples {
		r := x.cpuRes
		if memory {
			r = x.memRes
		}
		requests = append(requests, chart.Point{At: x.at, V: r.requests})
		limits = append(limits, chart.Point{At: x.at, V: r.limits})
		capacity = append(capacity, chart.Point{At: x.at, V: r.capacity})
	}
	return requests, limits, capacity
}

// kept returns the time of key's oldest and newest samples.
func (s *session) kept(key string) (first, last time.Time, ok bool) {
	h := s.byKey[key]
	if h == nil || len(h.samples) == 0 {
		return time.Time{}, time.Time{}, false
	}
	return h.samples[0].at, h.samples[len(h.samples)-1].at, true
}

// within returns the samples of key in [from, to].
func (s *session) within(key string, from, to time.Time) []sample {
	h := s.byKey[key]
	if h == nil {
		return nil
	}
	var out []sample
	for _, x := range h.samples {
		if !x.at.Before(from) && !x.at.After(to) {
			out = append(out, x)
		}
	}
	return out
}
