package models

import (
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stokaro/kc/internal/tablecache"
	"github.com/stokaro/kc/pkg/appconfig"
)

// Sort keys with a meaning of their own. Every other key names a server-side
// Table column (see appconfig.ObjectsOrderKey).
const (
	sortKeyName      = appconfig.ObjectsOrderName
	sortKeyAge       = "age"
	sortKeyCreation  = appconfig.ObjectsOrderCreation
	sortKeyNamespace = "namespace"
)

// objectSort is the sort a list was ordered by after resolving the order
// against the table's columns.
type objectSort struct {
	// key is "name", "age", "namespace" or the key of a table column.
	key string
	// column is the index into the table's column definitions for a
	// column key, or -1.
	column int
	desc   bool
}

// resolveObjectSort maps order to a sort over cols. "creation" is the age
// column read the other way round: oldest first is the largest age first.
// An order that names a column the table lacks falls back to name, so one
// saved order works for every resource. Namespace only sorts lists that span
// namespaces.
func resolveObjectSort(order string, cols []metav1.TableColumnDefinition, byNamespace bool) objectSort {
	order = appconfig.NormalizeObjectsOrder(order)
	desc := strings.HasPrefix(order, "-")
	key := strings.TrimPrefix(order, "-")
	switch key {
	case sortKeyCreation:
		return objectSort{key: sortKeyAge, column: -1, desc: !desc}
	case sortKeyAge:
		return objectSort{key: sortKeyAge, column: -1, desc: desc}
	case sortKeyName:
		return objectSort{key: sortKeyName, column: -1, desc: desc}
	case sortKeyNamespace:
		if byNamespace {
			return objectSort{key: sortKeyNamespace, column: -1, desc: desc}
		}
	}
	for i, c := range cols {
		if i == 0 {
			continue // the first column is the name
		}
		if appconfig.ObjectsOrderKey(c.Name) == key {
			return objectSort{key: key, column: i, desc: desc}
		}
	}
	return objectSort{key: sortKeyName, column: -1}
}

// order returns the order string for the resolved sort.
func (s objectSort) order() string {
	if s.desc {
		return "-" + s.key
	}
	return s.key
}

// namespaceLayout is how a list places the namespaces of its rows.
type namespaceLayout int

const (
	// oneNamespace lists one namespace or a cluster-scoped resource.
	oneNamespace namespaceLayout = iota
	// groupedNamespaces lists several namespaces, each one as a block.
	groupedNamespaces
	// mixedNamespaces lists several namespaces as one list: the sort
	// column decides the order across all of them.
	mixedNamespaces
)

// layoutFor returns the layout of a list that spans namespaces or not.
func layoutFor(spansNamespaces, group bool) namespaceLayout {
	switch {
	case !spansNamespaces:
		return oneNamespace
	case group:
		return groupedNamespaces
	default:
		return mixedNamespaces
	}
}

// orderRowIndices sorts the rows by order and returns the indices in display
// order with the sort it applied. Name and Age compare the object's name and
// creation timestamp; other columns compare their cells by value (see
// columnSortValues). Empty cells and placeholders such as <none> come last in
// both directions, and ties keep name order, then namespace order.
//
// With groupedNamespaces the rows are grouped by namespace first, as kubectl
// get -A prints them, and the order applies inside each namespace. With
// mixedNamespaces the order applies to all rows at once. Sorting by
// namespace orders the namespaces and keeps names ascending inside in both
// layouts.
func orderRowIndices(items []tablecache.Row, cols []metav1.TableColumnDefinition, order string, layout namespaceLayout) ([]int, objectSort) {
	sortBy := resolveObjectSort(order, cols, layout != oneNamespace)
	idxs := make([]int, len(items))
	names := make([]string, len(items))
	for i := range items {
		idxs[i] = i
		names[i] = strings.ToLower(rowName(&items[i]))
	}
	byName := func(a, b int) int { return strings.Compare(names[a], names[b]) }
	byNamespace := func(a, b int) int { return strings.Compare(items[a].Namespace, items[b].Namespace) }

	var primary func(a, b int) int
	switch sortBy.key {
	case sortKeyName:
		primary = func(a, b int) int { return direction(byName(a, b), sortBy.desc) }
	case sortKeyNamespace:
		primary = func(a, b int) int { return direction(byNamespace(a, b), sortBy.desc) }
	case sortKeyAge:
		// A larger age is an earlier creation time.
		primary = func(a, b int) int {
			ta := items[a].CreationTimestamp.Time
			tb := items[b].CreationTimestamp.Time
			return direction(tb.Compare(ta), sortBy.desc)
		}
	default:
		vals := columnSortValues(items, sortBy.column, cols[sortBy.column])
		primary = func(a, b int) int { return compareSortValues(vals[a], vals[b], sortBy.desc) }
	}
	slices.SortStableFunc(idxs, func(a, b int) int {
		if layout == groupedNamespaces && sortBy.key != sortKeyNamespace {
			if c := byNamespace(a, b); c != 0 {
				return c
			}
		}
		if c := primary(a, b); c != 0 {
			return c
		}
		if c := byName(a, b); c != 0 {
			return c
		}
		return byNamespace(a, b)
	})
	return idxs, sortBy
}

func direction(c int, desc bool) int {
	if desc {
		return -c
	}
	return c
}

// sortKind is how the cells of one column compare.
type sortKind int

const (
	sortString   sortKind = iota
	sortNumber            // 3, 1.5, true
	sortRatio             // READY: 1/2
	sortCount             // RESTARTS: 3 (5m ago)
	sortDuration          // date columns and durations: 5m, 3d4h
	sortQuantity          // CPU and sizes: 250m, 1Gi
	sortIP                // 10.0.0.12, fd00::1
)

// sortValue is one cell prepared for comparison.
type sortValue struct {
	missing bool
	num     float64
	num2    float64 // denominator of a ratio
	ip      netip.Addr
	str     string
}

// columnSortValues prepares the cells of column col. The column type decides
// for integer, number, boolean and date columns. Most built-in columns are
// strings, so for those the kind is the first one that reads every present
// cell, which keeps one column on one kind of comparison.
func columnSortValues(items []tablecache.Row, col int, def metav1.TableColumnDefinition) []sortValue {
	cells := make([]any, len(items))
	for i := range items {
		if col < len(items[i].Cells) {
			cells[i] = items[i].Cells[col]
		}
	}
	_, vals := cellSortValues(cells, def)
	return vals
}

// cellSortValues prepares the cells of one column and returns the kind they
// compare as.
func cellSortValues(cells []any, def metav1.TableColumnDefinition) (sortKind, []sortValue) {
	var kind sortKind
	switch def.Type {
	case "integer", "number", "boolean":
		kind = sortNumber
	case "date":
		kind = sortDuration
	default:
		kind = detectSortKind(cells)
	}
	vals := make([]sortValue, len(cells))
	for i, c := range cells {
		vals[i] = parseSortValue(kind, c)
	}
	return kind, vals
}

// sortKindOrder lists the kinds detectSortKind tries. Number goes first, as
// plain numbers also read as quantities. Duration goes before quantity: "5m"
// reads as both, and either way the order is the same for one unit.
var sortKindOrder = []sortKind{sortNumber, sortRatio, sortCount, sortDuration, sortQuantity, sortIP}

func detectSortKind(cells []any) sortKind {
	for _, kind := range sortKindOrder {
		seen := false
		ok := true
		for _, c := range cells {
			v := parseSortValue(kind, c)
			if v.missing {
				if !isMissingCell(c) {
					ok = false
					break
				}
				continue
			}
			seen = true
		}
		if ok && seen {
			return kind
		}
	}
	return sortString
}

var (
	countPattern    = regexp.MustCompile(`^(\d+)(?:\s.*)?$`)
	durationPattern = regexp.MustCompile(`^(?:(\d+)y)?(?:(\d+)d)?(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$`)
)

// isMissingCell reports cells that hold no value: nil, empty, and the
// placeholders kubectl prints.
func isMissingCell(c any) bool {
	if c == nil {
		return true
	}
	s, ok := c.(string)
	if !ok {
		return false
	}
	switch strings.TrimSpace(s) {
	case "", "-", "<none>", "<unknown>", "<nil>", "<pending>", "<invalid>", "n/a", "N/A":
		return true
	}
	return false
}

// parseSortValue reads c as kind. A cell that does not read as kind is
// missing.
//
//nolint:gocyclo // one case per sort kind; splitting the switch would scatter the parsers
func parseSortValue(kind sortKind, c any) sortValue {
	missing := sortValue{missing: true}
	if isMissingCell(c) {
		return missing
	}
	if kind == sortNumber {
		if f, ok := numericCell(c); ok {
			return sortValue{num: f}
		}
	}
	s := strings.TrimSpace(fmt.Sprint(c))
	switch kind {
	case sortString:
		return sortValue{str: strings.ToLower(s)}
	case sortNumber:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return missing
		}
		return sortValue{num: f}
	case sortRatio:
		a, b, ok := strings.Cut(s, "/")
		if !ok {
			return missing
		}
		n, err1 := strconv.ParseFloat(a, 64)
		d, err2 := strconv.ParseFloat(b, 64)
		if err1 != nil || err2 != nil {
			return missing
		}
		return sortValue{num: n, num2: d}
	case sortCount:
		m := countPattern.FindStringSubmatch(s)
		if m == nil {
			return missing
		}
		n, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return missing
		}
		return sortValue{num: n}
	case sortDuration:
		d, ok := parseHumanDuration(s)
		if !ok {
			return missing
		}
		return sortValue{num: float64(d)}
	case sortQuantity:
		q, err := resource.ParseQuantity(s)
		if err != nil {
			return missing
		}
		return sortValue{num: q.AsApproximateFloat64()}
	case sortIP:
		ip, err := netip.ParseAddr(s)
		if err != nil {
			return missing
		}
		return sortValue{ip: ip}
	}
	return missing
}

func numericCell(c any) (float64, bool) {
	switch v := c.(type) {
	case int:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case float32:
		return float64(v), true
	case float64:
		return v, true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// parseHumanDuration reads the durations kubectl prints for ages and date
// columns (duration.HumanDuration): 45s, 5m, 3h20m, 2d4h, 1y30d.
func parseHumanDuration(s string) (time.Duration, bool) {
	if s == "" {
		return 0, false
	}
	m := durationPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	units := []time.Duration{365 * 24 * time.Hour, 24 * time.Hour, time.Hour, time.Minute, time.Second}
	var d time.Duration
	for i, u := range units {
		if m[i+1] == "" {
			continue
		}
		n, err := strconv.ParseInt(m[i+1], 10, 64)
		if err != nil {
			return 0, false
		}
		d += time.Duration(n) * u
	}
	return d, true
}

// compareSortValues orders a before b. Missing values come last in both
// directions, so the rows that say something stay on top.
func compareSortValues(a, b sortValue, desc bool) int {
	switch {
	case a.missing && b.missing:
		return 0
	case a.missing:
		return 1
	case b.missing:
		return -1
	}
	c := cmp.Compare(a.num, b.num)
	if c == 0 {
		c = cmp.Compare(a.num2, b.num2)
	}
	if c == 0 {
		c = a.ip.Compare(b.ip)
	}
	if c == 0 {
		c = strings.Compare(a.str, b.str)
	}
	return direction(c, desc)
}

// SortMark reports the column an object list is sorted by.
type SortMark struct {
	// Column is the index into Folder.Columns.
	Column int
	Desc   bool
}

// ObjectSortable folders sort their rows by a column and report it, so the
// panel can mark the column header and cycle the sort.
type ObjectSortable interface {
	ObjectOrderConfigurable
	// SortMark returns the sorted column, or false when no shown column is
	// the sort key (for example a wide column in normal mode).
	SortMark() (SortMark, bool)
	// SortKeys returns the order key of each column in Folder.Columns.
	SortKeys() []string
	// ObjectOrder returns the order the rows are sorted by now, after
	// falling back to name for a column this list lacks.
	ObjectOrder() string
}
