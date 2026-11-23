package dashboard_test

import (
	"strings"
	"testing"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/promhistory"
)

// raisedRequest is a Prometheus usage whose CPU request was 0.5 until
// the middle of r and 1 after, and whose limit was 2 throughout but for
// three steps where a container had none.
func raisedRequest(s promhistory.Scope, r promhistory.Range) promhistory.Usage {
	u := steadyUsage(s, r, 0.25)
	res := &promhistory.Resources{}
	mid := r.Start.Add(r.End.Sub(r.Start) / 2)
	i := 0
	for at := r.Start; !at.After(r.End); at = at.Add(r.Step) {
		req, limited := 0.5, 2.0
		if !at.Before(mid) {
			req = 1
		}
		if i >= 3 && i <= 5 {
			limited = 1
		}
		pt := func(v float64) promhistory.Point { return promhistory.Point{At: at, Value: v} }
		res.Containers = append(res.Containers, pt(2))
		res.CPU.Requests = append(res.CPU.Requests, pt(req))
		res.CPU.Requested = append(res.CPU.Requested, pt(2))
		res.CPU.Limits = append(res.CPU.Limits, pt(2))
		res.CPU.Limited = append(res.CPU.Limited, pt(limited))
		res.Memory.Requests = append(res.Memory.Requests, pt(1<<30))
		res.Memory.Requested = append(res.Memory.Requested, pt(2))
		i++
	}
	u.Resources = res
	return u
}

// TestRequestsFromHistoryNotToday: with kube-state-metrics in Prometheus
// the request at a past cursor is the request then, never today's spec
// (3 cores here); a step where one container had no limit has no limit.
func TestRequestsFromHistoryNotToday(t *testing.T) {
	t.Parallel()
	a := measured()
	a.CPU.Requests, a.CPU.Limits = 3, 4
	f := &fakes{now: t0, amounts: a, usage: func(s promhistory.Scope, r promhistory.Range) (promhistory.Usage, error) {
		return raisedRequest(s, r), nil
	}}
	m := open(t, f, true, web, 240, 42)
	key(t, m, "home")
	requireText(t, screen(t, m, 240, 42), "█ usage 0.25 (highest)  ┄ request 0.5 (50% used)  ┄ limit 2 ↑ above")
	key(t, m, "end")
	lines := screen(t, m, 240, 42)
	requireText(t, lines, "█ usage 0.25 (highest)  ┄ request 1 (25% used) ↑ above  ┄ limit 2 ↑ above")
	for _, l := range lines {
		if strings.Contains(l, "request 3") || strings.Contains(l, "limit 4") {
			t.Fatalf("today's spec drawn over the past: %q", l)
		}
	}
	// Walk the cells: one shows the request without a limit.
	unlimited := false
	key(t, m, "home")
	for range 20 {
		if l := screen(t, m, 240, 42); contains(l, "request 0.5 (50% used)  ┄ limit missing on some pods") {
			unlimited = true
			break
		}
		key(t, m, "right")
	}
	if !unlimited {
		t.Error("no cell showed the step where a container had no limit")
	}
}

// TestNoResourcesHistorySaysWhy: Prometheus without kube-state-metrics
// container series draws no request or limit lines and says why; a failed
// read names its error.
func TestNoResourcesHistorySaysWhy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  string
		want string
	}{
		{name: "no series", want: "no request or limit history: no kube-state-metrics"},
		{name: "failed read", err: "prometheus monitoring/prometheus: two series for sum/cpu", want: "no request or limit history: prometheus monitoring"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakes{now: t0, amounts: measured(), usage: func(s promhistory.Scope, r promhistory.Range) (promhistory.Usage, error) {
				u := steadyUsage(s, r, 0.25)
				u.ResourcesErr = tc.err
				return u, nil
			}}
			m := open(t, f, true, web, 240, 42)
			lines := screen(t, m, 240, 42)
			requireText(t, lines, tc.want)
			for _, l := range lines {
				if strings.Contains(l, "│") && strings.Contains(l, "┄") {
					t.Fatalf("a reference line is drawn without a history: %q", l)
				}
			}
		})
	}
}

// TestSessionRequestsAsSampled: without Prometheus, the request at a
// past cursor is the one the session sampled then.
func TestSessionRequestsAsSampled(t *testing.T) {
	t.Parallel()
	a := measured()
	a.CPU.Requests = 0.5
	f := &fakes{now: t0, amounts: a}
	m := open(t, f, false, web, 150, 42)
	ticks(t, f, m, 30, models.UsagePollInterval)
	f.mu.Lock()
	f.amounts.CPU.Requests = 1.5
	f.mu.Unlock()
	ticks(t, f, m, 30, models.UsagePollInterval)
	screen(t, m, 150, 42) // lay out the cells before moving the cursor
	key(t, m, "end")
	requireText(t, screen(t, m, 150, 42), "request 1.5")
	seen := false
	for range 60 {
		key(t, m, "left")
		if contains(screen(t, m, 150, 42), "usage 0.4 (highest)  ┄ request 0.5") {
			seen = true
			break
		}
	}
	if !seen {
		t.Error("no past cell showed the request sampled then")
	}
}
