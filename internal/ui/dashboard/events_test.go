package dashboard_test

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/investigation"
	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/ui/dashboard"
)

var (
	podsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	rsGVR   = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
	evGVR   = schema.GroupVersionResource{Version: "v1", Resource: "events"}
)

func event(name, typ, reason, kind string, obj models.ObjectRef, first, last time.Time, count int32) models.UsageEvent {
	return models.UsageEvent{
		Event:  models.ObjectRef{GVR: evGVR, Namespaced: true, Namespace: "shop", Name: name},
		Object: obj, Kind: kind, Type: typ, Reason: reason, Message: reason + " message",
		Count: count, First: first, Last: last, FirstKnown: true,
	}
}

// withEvents is measured() with a rollout 40 minutes ago, a repeated
// BackOff of web-1, and an Event about an object kc cannot open. The API
// server kept Events from 50 minutes ago.
func withEvents() models.UsageAmounts {
	a := measured()
	pod := models.ObjectRef{GVR: podsGVR, Namespaced: true, Namespace: "shop", Name: "web-1"}
	rs := models.ObjectRef{GVR: rsGVR, Namespaced: true, Namespace: "shop", Name: "web-abc"}
	a.Events = models.UsageEvents{
		Oldest: t0.Add(-50 * time.Minute),
		Items: []models.UsageEvent{
			event("e-scale", "Normal", "SuccessfulCreate", "ReplicaSet", rs, t0.Add(-40*time.Minute), t0.Add(-40*time.Minute), 1),
			event("e-custom", "Normal", "Synced", "Widget", models.ObjectRef{Namespaced: true, Namespace: "shop", Name: "w"}, t0.Add(-20*time.Minute), t0.Add(-20*time.Minute), 1),
			event("e-backoff", "Warning", "BackOff", "Pod", pod, t0.Add(-30*time.Minute), t0.Add(-2*time.Minute), 12),
		},
	}
	return a
}

// lane returns the lane of the Events widget and the line of labels
// under it.
func lane(t *testing.T, lines []string) (marks, labels string) {
	t.Helper()
	for i, l := range lines {
		if strings.Contains(l, "Events in") && i+2 < len(lines) {
			return lines[i+1], lines[i+2]
		}
	}
	t.Fatalf("no events widget:\n%s", strings.Join(lines, "\n"))
	return "", ""
}

// TestEventsLaneAndList: the lane marks a rollout and a warning at their
// times with their reasons written under them, and says where kc does not
// know what happened; the list is newest first in labeled columns.
func TestEventsLaneAndList(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: withEvents()}
	m := open(t, f, true, web, 150, 42)
	lines := screen(t, m, 150, 42)
	requireText(t, lines, "Events in the last 1 hour: 3", "the API server keeps them since 09:10:00",
		"Time", "Type", "Reason", "Count", "Message",
		"▲ Warning BackOff", "◆ Rollout SuccessfulCreate")
	marks, labels := lane(t, lines)
	for _, r := range []string{"▲", "◆"} {
		if !strings.Contains(marks, r) {
			t.Errorf("lane %q lacks %q", marks, r)
		}
	}
	for _, want := range []string{"before 09:10", "BackOff ×12", "SuccessfulCreate"} {
		if !strings.Contains(labels, want) {
			t.Errorf("lane labels %q lack %q", labels, want)
		}
	}
	// The hour shown starts at 09:00: the unknown first sixth is left of
	// the rollout.
	if strings.Index(labels, "before 09:10") > strings.Index(labels, "SuccessfulCreate") {
		t.Errorf("labels %q: want the unknown cells first, then the rollout", labels)
	}
	joined := strings.Join(lines, "\n")
	if strings.Index(joined, "▲ Warning BackOff") > strings.Index(joined, "◆ Rollout SuccessfulCreate") {
		t.Error("the list is not newest first")
	}
}

// TestEventsOfTheCursorCell: with the cursor, the list holds the Events
// of the cell only.
func TestEventsOfTheCursorCell(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: withEvents()}
	m := open(t, f, true, web, 150, 42)
	screen(t, m, 150, 42)
	// The newest cell is after the BackOff's last time, 09:58: empty.
	key(t, m, "left")
	requireText(t, screen(t, m, 150, 42), "Events in the cursor cell: 0")
	// Walk back into the BackOff's span; Synced at 09:40 stays out.
	var lines []string
	for range 10 {
		key(t, m, "left")
		if lines = screen(t, m, 150, 42); contains(lines, "Events in the cursor cell: 1") {
			break
		}
	}
	requireText(t, lines, "Events in the cursor cell: 1", "BackOff", "09:5")
	if contains(lines, "Synced message") {
		t.Error("an Event outside the cursor cell is listed")
	}
	key(t, m, "home")
	requireText(t, screen(t, m, 150, 42), "Events in the cursor cell: 0", "no Events in the cursor cell")
}

// TestOpenEventAndLogsAround: o opens the involved object with the Event;
// an object kc cannot open opens the Event itself; l asks for the logs of
// the pod around the Event's time.
func TestOpenEventAndLogsAround(t *testing.T) {
	t.Parallel()
	f := &fakes{now: t0, amounts: withEvents()}
	m := open(t, f, true, web, 100, 30)
	key(t, m, "shift+tab") // Events
	act := key(t, m, "o")
	if act.Open == nil || act.Open.Name != "web-1" || act.Open.GVR != podsGVR || act.Event == nil || act.Event.Reason != "BackOff" {
		t.Fatalf("o on BackOff = %+v", act)
	}
	act = key(t, m, "l")
	want := investigation.Window{Start: t0.Add(-7 * time.Minute), End: time.Time{}}
	if act.Logs == nil || act.Logs.Name != "web-1" || act.Window != want {
		t.Errorf("l on BackOff = %+v, want the pod from 09:53 to now", act)
	}
	key(t, m, "down")
	act = key(t, m, "enter")
	if act.Open == nil || act.Open.GVR != evGVR || act.Open.Name != "e-custom" {
		t.Errorf("Enter on an Event of an unknown kind = %+v, want the Event", act.Open)
	}
	if act = key(t, m, "l"); act.Logs != nil || !strings.Contains(act.Note, "has no logs") {
		t.Errorf("l on a Widget = %+v, want a note", act)
	}
	key(t, m, "down")
	act = key(t, m, "l")
	want = investigation.Window{Start: t0.Add(-45 * time.Minute), End: t0.Add(-35 * time.Minute)}
	if act.Logs == nil || act.Logs.GVR != rsGVR || act.Window != want {
		t.Errorf("l on the rollout = %+v, want the ReplicaSet around 09:20", act)
	}
}

// TestEventsUnavailableAndTooOld: missing Events say why; a range older
// than every kept Event says the API server keeps none that old.
func TestEventsUnavailableAndTooOld(t *testing.T) {
	t.Parallel()
	a := withEvents()
	a.Events = models.UsageEvents{Missing: "events is forbidden"}
	f := &fakes{now: t0, amounts: a}
	m := open(t, f, true, web, 100, 30)
	requireText(t, screen(t, m, 100, 30), "Events · cannot be read: events is forbidden")

	f2 := &fakes{now: t0, amounts: withEvents(), win: investigation.Window{Start: t0.Add(-3 * time.Hour), End: t0.Add(-2 * time.Hour)}}
	m2 := open(t, f2, true, web, 150, 42)
	lines := screen(t, m2, 150, 42)
	requireText(t, lines, "Events in the last 1 hour: 0", "the API server keeps no Events this old")
	if marks, labels := lane(t, lines); strings.ContainsAny(marks, "▲◆") || !strings.Contains(labels, "no Events kept before 09:10") {
		t.Errorf("lane %q / %q, want every cell unknown", marks, labels)
	}
}

var _ = dashboard.IsRollout
