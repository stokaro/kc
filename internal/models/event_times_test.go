package models

import (
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/tablecache"
	"github.com/stokaro/kc/pkg/appconfig"
)

// eventTableCols are the columns of the server's Event table: Last Seen
// first, Name among the wide columns and marked by its format.
var eventTableCols = []metav1.TableColumnDefinition{
	{Name: "Last Seen", Type: "string"},
	{Name: "Type", Type: "string"},
	{Name: "Reason", Type: "string"},
	{Name: "Object", Type: "string"},
	{Name: "Message", Type: "string"},
	{Name: "First Seen", Type: "string", Priority: 1},
	{Name: "Name", Type: "string", Format: "name", Priority: 1},
}

var eventTestNow = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

// eventRow is an Event table row with its cached cells: lastSeen as the
// server printed it when the row arrived.
func eventRow(ns, name, lastSeen, firstSeen string) tablecache.Row {
	var r tablecache.Row
	r.Namespace, r.Name = ns, name
	r.Cells = []any{lastSeen, "Warning", "BackOff", "pod/web", "restarting", firstSeen, name}
	return r
}

// eventObject is the Event behind a row, with lastTimestamp and
// firstTimestamp ago before eventTestNow.
func eventObject(ns, name string, last, first time.Duration) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Event",
		"metadata":       map[string]any{"namespace": ns, "name": name},
		"firstTimestamp": eventTestNow.Add(-first).Format(time.RFC3339),
		"lastTimestamp":  eventTestNow.Add(-last).Format(time.RFC3339),
	}}
	return u
}

func TestEventOccurrences(t *testing.T) {
	t.Parallel()
	at := func(s string) time.Time { return parseEventTime(s) }
	tests := []struct {
		name        string
		obj         map[string]any
		first, last string
	}{
		{name: "first and last timestamps",
			obj:   map[string]any{"firstTimestamp": "2026-11-23T10:00:00Z", "lastTimestamp": "2026-11-23T11:00:00Z"},
			first: "2026-11-23T10:00:00Z", last: "2026-11-23T11:00:00Z"},
		{name: "series wins over lastTimestamp",
			obj: map[string]any{"firstTimestamp": "2026-11-23T10:00:00Z", "lastTimestamp": "2026-11-23T10:30:00Z",
				"series": map[string]any{"count": int64(7), "lastObservedTime": "2026-11-23T11:59:00.123456Z"}},
			first: "2026-11-23T10:00:00Z", last: "2026-11-23T11:59:00.123456Z"},
		{name: "events.k8s.io fields and eventTime",
			obj:   map[string]any{"eventTime": "2026-11-23T09:00:00.000001Z", "deprecatedLastTimestamp": "2026-11-23T09:30:00Z"},
			first: "2026-11-23T09:00:00.000001Z", last: "2026-11-23T09:30:00Z"},
		{name: "only eventTime: last is first",
			obj:   map[string]any{"eventTime": "2026-11-23T09:00:00.5Z"},
			first: "2026-11-23T09:00:00.5Z", last: "2026-11-23T09:00:00.5Z"},
		{name: "no times stay missing, creation is not used",
			obj: map[string]any{"metadata": map[string]any{"creationTimestamp": "2026-11-23T08:00:00Z"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			first, last := eventOccurrences(&unstructured.Unstructured{Object: tt.obj})
			if !first.Equal(at(tt.first)) || !last.Equal(at(tt.last)) {
				t.Fatalf("occurrences = %v, %v, want %q, %q", first, last, tt.first, tt.last)
			}
		})
	}
}

// TestEventTableSortsByRecency sorts an Event table by Last Seen. The
// cached cells disagree with the Events: "old" arrived when it was 5s
// old and is now an hour old.
func TestEventTableSortsByRecency(t *testing.T) {
	t.Parallel()
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "events"}
	items := []tablecache.Row{
		eventRow("a", "a.old", "5s", "5s"),
		eventRow("b", "z.recent", "20m", "2h"),
		eventRow("a", "m.unknown", "<unknown>", "<unknown>"),
	}
	objs := rowObjects{
		"a/a.old":    eventObject("a", "a.old", time.Hour, time.Hour),
		"b/z.recent": eventObject("b", "z.recent", 5*time.Second, 2*time.Hour),
		"a/m.unknown": &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"namespace": "a", "name": "m.unknown"}}},
	}
	times := eventColumnTimes(gvr, items, eventTableCols, objs)
	tests := []struct {
		order  string
		layout namespaceLayout
		want   []string
		mark   string
	}{
		{order: "last-seen", layout: oneNamespace, want: []string{"z.recent", "a.old", "m.unknown"}, mark: "last-seen"},
		{order: "-last-seen", layout: oneNamespace, want: []string{"a.old", "z.recent", "m.unknown"}, mark: "-last-seen"},
		{order: "last-seen", layout: mixedNamespaces, want: []string{"z.recent", "a.old", "m.unknown"}, mark: "last-seen"},
		{order: "last-seen", layout: groupedNamespaces, want: []string{"a.old", "m.unknown", "z.recent"}, mark: "last-seen"},
		{order: "first-seen", layout: oneNamespace, want: []string{"a.old", "z.recent", "m.unknown"}, mark: "first-seen"},
		{order: "name", layout: oneNamespace, want: []string{"a.old", "m.unknown", "z.recent"}, mark: "name"},
	}
	for _, tt := range tests {
		t.Run(tt.order, func(t *testing.T) {
			t.Parallel()
			idxs, sortBy := orderRowIndicesWithTimes(items, eventTableCols, tt.order, tt.layout, times)
			if got := sortedNames(items, idxs); !slices.Equal(got, tt.want) {
				t.Errorf("order %q = %v, want %v", tt.order, got, tt.want)
			}
			if got := sortBy.order(); got != tt.mark {
				t.Errorf("applied order = %q, want %q", got, tt.mark)
			}
		})
	}
}

// TestEventTableColumnsAndCells builds the rows of an Event list: the
// first column sorts as Last Seen, not as the name, and the time cells
// count from the Event's occurrences as the clock moves.
func TestEventTableColumnsAndCells(t *testing.T) {
	t.Parallel()
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "events"}
	folder := NewObjectsFolder(Deps{}, gvr, "a", []string{"namespaces", "a", "events"}, nil)
	rl := &tablecache.RowList{
		Columns: eventTableCols,
		Items:   []tablecache.Row{eventRow("a", "a.old", "10s", "10s"), eventRow("a", "z.recent", "10s", "3m")},
	}
	objs := rowObjects{
		"a/a.old":    eventObject("a", "a.old", 20*time.Minute, 20*time.Minute),
		"a/z.recent": eventObject("a", "z.recent", 5*time.Second, 3*time.Minute),
	}
	for _, mode := range []string{appconfig.ColumnsModeNormal, appconfig.ColumnsModeWide} {
		t.Run(mode, func(t *testing.T) {
			rows := folder.rowsFromRowListWithObjects(rl, mode, "last-seen", nil, objs)
			keys := folder.SortKeys()
			if keys[0] != "last-seen" {
				t.Fatalf("first column key = %q, want last-seen (keys %v)", keys[0], keys)
			}
			if wantName := mode == appconfig.ColumnsModeWide; slices.Contains(keys, appconfig.ObjectsOrderName) != wantName {
				t.Fatalf("keys %v: name key present = %v, want %v", keys, !wantName, wantName)
			}
			if mode == appconfig.ColumnsModeWide && keys[len(keys)-1] != appconfig.ObjectsOrderName {
				t.Fatalf("keys %v: the Name column must sort by name", keys)
			}
			folder.ageMu.Lock()
			for _, h := range folder.ageHooks {
				h.now = func() time.Time { return eventTestNow.Add(5 * time.Minute) }
			}
			if folder.ageTimer != nil {
				folder.ageTimer.Stop()
				folder.ageTimer = nil
			}
			folder.ageMu.Unlock()
			var got [][]string
			for _, r := range rows {
				_, cells, _, _ := r.Columns()
				got = append(got, slices.Clone(cells))
			}
			// Five minutes later: z.recent was last seen 5m5s ago.
			if got[0][0] != "5m5s" || got[1][0] != "25m" {
				t.Fatalf("Last Seen cells = %q, %q, want 5m5s, 25m (rows %v)", got[0][0], got[1][0], got)
			}
			if mode == appconfig.ColumnsModeWide {
				if first := slices.Index(keys, "first-seen"); got[0][first] != "8m" {
					t.Fatalf("First Seen cell = %q, want 8m", got[0][first])
				}
			}
			folder.installAgeHooks(nil)
		})
	}
}

func TestNameColumn(t *testing.T) {
	t.Parallel()
	if got := nameColumn(eventTableCols); got != 6 {
		t.Fatalf("Event table name column = %d, want 6", got)
	}
	if got := nameColumn([]metav1.TableColumnDefinition{{Name: "Name"}, {Name: "Age"}}); got != 0 {
		t.Fatalf("unmarked table name column = %d, want 0", got)
	}
}
