package models

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/tablecache"
	"github.com/stokaro/kc/pkg/appconfig"
)

// The time columns of the server's Event table (kubectl get events). The
// server prints them relative to the moment it built the row, and the
// table cache keeps that text until the Event changes, so kc recomputes
// them from the Event's own timestamps (docs/adr/0278-event-recency.md).
var (
	eventLastSeenKey  = appconfig.ObjectsOrderKey("Last Seen")
	eventFirstSeenKey = appconfig.ObjectsOrderKey("First Seen")
)

// isEventGVR reports the two Event APIs, core/v1 and events.k8s.io/v1.
// The server prints both with the same table.
func isEventGVR(gvr schema.GroupVersionResource) bool {
	return gvr.Resource == "events" && (gvr.Group == "" || gvr.Group == "events.k8s.io")
}

// eventOccurrences returns when an Event happened first and last, as the
// server's Event table computes First Seen and Last Seen: first is
// firstTimestamp, else eventTime; last is series.lastObservedTime for a
// series, else lastTimestamp, else first. events.k8s.io/v1 names the old
// fields deprecatedFirstTimestamp and deprecatedLastTimestamp. A zero time
// means the Event does not say; it is never replaced by the creation time.
func eventOccurrences(u *unstructured.Unstructured) (first, last time.Time) {
	if u == nil {
		return time.Time{}, time.Time{}
	}
	first = eventTimeField(u, "firstTimestamp", "deprecatedFirstTimestamp")
	if first.IsZero() {
		first = eventTimeField(u, "eventTime")
	}
	if series, ok, _ := unstructured.NestedMap(u.Object, "series"); ok && series != nil {
		if s, ok := series["lastObservedTime"].(string); ok {
			last = parseEventTime(s)
		}
		return first, last
	}
	last = eventTimeField(u, "lastTimestamp", "deprecatedLastTimestamp")
	if last.IsZero() {
		last = first
	}
	return first, last
}

// eventTimeField returns the first of fields that holds a time.
func eventTimeField(u *unstructured.Unstructured, fields ...string) time.Time {
	for _, f := range fields {
		if s, ok, _ := unstructured.NestedString(u.Object, f); ok {
			if t := parseEventTime(s); !t.IsZero() {
				return t
			}
		}
	}
	return time.Time{}
}

// parseEventTime reads a metav1.Time or metav1.MicroTime value.
func parseEventTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// columnTimes holds, per server column index, the time each row's cell
// stands for: the cell shows the time since then, and sorting by the
// column compares these times. A zero time is a missing value.
type columnTimes map[int][]time.Time

// eventColumnTimes returns the Last Seen and First Seen times of the rows
// of an Event table, read from the Event objects. Other tables, and rows
// whose object is not in objs, get no times.
func eventColumnTimes(gvr schema.GroupVersionResource, items []tablecache.Row, cols []metav1.TableColumnDefinition, objs rowObjects) columnTimes {
	if !isEventGVR(gvr) || objs == nil {
		return nil
	}
	lastCol, firstCol := -1, -1
	for i, c := range cols {
		switch appconfig.ObjectsOrderKey(c.Name) {
		case eventLastSeenKey:
			lastCol = i
		case eventFirstSeenKey:
			firstCol = i
		}
	}
	if lastCol < 0 && firstCol < 0 {
		return nil
	}
	firsts := make([]time.Time, len(items))
	lasts := make([]time.Time, len(items))
	for i := range items {
		firsts[i], lasts[i] = eventOccurrences(objs.object(items[i].Namespace, rowName(&items[i])))
	}
	out := make(columnTimes, 2)
	if lastCol >= 0 {
		out[lastCol] = lasts
	}
	if firstCol >= 0 {
		out[firstCol] = firsts
	}
	return out
}

// nameColumn returns the index of the column that holds the object name:
// the one the server marks with format "name". The Event table puts Last
// Seen first and Name among the wide columns. A table without the mark
// has the name first, as most tables do.
func nameColumn(cols []metav1.TableColumnDefinition) int {
	for i, c := range cols {
		if c.Format == "name" {
			return i
		}
	}
	return 0
}
