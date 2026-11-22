package models

import (
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	table "github.com/stokaro/kc/internal/table"
)

// RowFilter hides rows of an object list. The text is a list of terms
// separated by spaces, and a row must match every term:
//
//	nginx           the name matches the regular expression (any case)
//	!nginx          the name does not match
//	-l app=web      the labels match the label selector
//	-n web,api      the namespace is one of the listed ones
//	cpu>500m        a column compares as given (see ParseRowFilter)
//
// FaultsOnly keeps only the rows whose object is in a fault state (see
// RowHealth). The zero value filters nothing.
type RowFilter struct {
	text       string
	terms      []filterTerm
	selector   labels.Selector
	FaultsOnly bool
}

// Text returns the filter text in its canonical form.
func (f RowFilter) Text() string { return f.text }

// Selector returns the label selector of all -l terms, or "" when the
// filter has none.
func (f RowFilter) Selector() string {
	if f.selector == nil {
		return ""
	}
	return f.selector.String()
}

// Active reports whether the filter hides anything.
func (f RowFilter) Active() bool { return f.text != "" || f.FaultsOnly }

// Label is the short form shown in the panel frame, for example
// "!nginx, faults". It is empty when the filter is not active.
func (f RowFilter) Label() string {
	parts := make([]string, 0, 2)
	if f.text != "" {
		parts = append(parts, f.text)
	}
	if f.FaultsOnly {
		parts = append(parts, "faults")
	}
	return strings.Join(parts, ", ")
}

// RowFacts is what a filter looks at for one row.
type RowFacts struct {
	// Name is the object name; ID is the row ID, which is "namespace/name"
	// in lists across namespaces. The pattern matches either.
	Name      string
	ID        string
	Namespace string
	Labels    map[string]string
	Health    RowHealth
	// Created is the creation time, which the age column compares by.
	Created time.Time

	// cols holds the list's cells for column terms; row is this row's
	// index in it.
	cols *filterColumns
	row  int
}

// Matches reports whether the filter keeps a row.
func (f RowFilter) Matches(r RowFacts) bool {
	if f.FaultsOnly && !r.Health.Faulty() {
		return false
	}
	if f.selector != nil && !f.selector.Matches(labels.Set(r.Labels)) {
		return false
	}
	for _, t := range f.terms {
		if !t.matches(r) {
			return false
		}
	}
	return true
}

// RowFilterable is a folder whose rows can be filtered. The filter belongs
// to the folder, so it stays while the folder is on the panel's navigation
// stack and goes away with it.
type RowFilterable interface {
	RowFilter() RowFilter
	SetRowFilter(RowFilter)
}

// rowFactsCarrier is a row that knows its filter facts.
type rowFactsCarrier interface {
	rowFacts() (RowFacts, bool)
}

func (o *ObjectRow) rowFacts() (RowFacts, bool) { return o.facts, o.hasFacts }

// setRowFacts records what a row filter needs to know about the row.
func (o *ObjectRow) setRowFacts(f RowFacts) {
	o.facts = f
	o.hasFacts = true
}

// keepRow reports whether the filter keeps row. Rows without facts (the
// back row, synthetic rows) are always kept.
func (f RowFilter) keepRow(row table.Row) bool {
	if !f.Active() {
		return true
	}
	c, ok := row.(rowFactsCarrier)
	if !ok {
		return true
	}
	facts, ok := c.rowFacts()
	if !ok {
		return true
	}
	return f.Matches(facts)
}

// RowFilter returns the folder's current filter.
func (o *ObjectsFolder) RowFilter() RowFilter {
	if o.rows == nil {
		return RowFilter{}
	}
	return o.rows.rowFilter()
}

// SetRowFilter replaces the folder's filter. The rows already loaded are
// filtered again at once; later list loads (live updates) apply it too.
func (o *ObjectsFolder) SetRowFilter(f RowFilter) {
	if o.rows == nil {
		return
	}
	o.rows.setRowFilter(f)
	o.BaseFolder.markDirtyFromSource()
}

func (s *liveObjectRowSource) rowFilter() RowFilter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.filter
}

func (s *liveObjectRowSource) setRowFilter(f RowFilter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filter = f
	s.applyFilterLocked()
}

// applyFilterLocked derives the served rows from all loaded rows.
func (s *liveObjectRowSource) applyFilterLocked() {
	if !s.filter.Active() {
		s.rows = s.all
	} else {
		rows := make([]table.Row, 0, len(s.all))
		for _, row := range s.all {
			if s.filter.keepRow(row) {
				rows = append(rows, row)
			}
		}
		s.rows = rows
	}
	s.rebuildIndexLocked()
}

// rowFactsFor collects the filter facts of an object row.
// ns is the row's namespace, cols the listing's cells and row the row's
// index in them.
func rowFactsFor(info RowStyleInfo, id, ns string, cols *filterColumns, row int) RowFacts {
	return RowFacts{
		Name:      info.ObjectMeta.Name,
		ID:        id,
		Namespace: ns,
		Labels:    info.ObjectMeta.Labels,
		Health:    ClassifyRowHealth(info.GVR, info.Unstructured),
		Created:   info.ObjectMeta.CreationTimestamp.Time,
		cols:      cols,
		row:       row,
	}
}

// withCustomCells returns cells padded to n with the added cells after
// them. Without added cells it returns cells as they are.
func withCustomCells(cells []any, n int, custom []string) []any {
	if len(custom) == 0 {
		return cells
	}
	width := max(n, len(cells))
	out := make([]any, width, width+len(custom))
	copy(out, cells)
	for _, c := range custom {
		out = append(out, c)
	}
	return out
}

// customColumnDefs describes the added columns for column filter terms.
func customColumnDefs(cc *customColumns) []metav1.TableColumnDefinition {
	cols := cc.tableColumns()
	defs := make([]metav1.TableColumnDefinition, len(cols))
	for i, c := range cols {
		defs[i] = metav1.TableColumnDefinition{Name: strings.TrimSpace(c.Title), Type: "string"}
	}
	return defs
}

// setFilterColumns records the cells of the last listing for column terms.
func (o *ObjectsFolder) setFilterColumns(fc *filterColumns) {
	o.sortMu.Lock()
	o.filterCols = fc
	o.sortMu.Unlock()
}

// ValidateRowFilter reports column terms of f that name no column of this
// list, or values the column cannot hold.
func (o *ObjectsFolder) ValidateRowFilter(f RowFilter) error {
	o.sortMu.Lock()
	fc := o.filterCols
	o.sortMu.Unlock()
	return f.validateColumns(fc)
}
