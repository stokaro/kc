package models

import (
	"fmt"
	"regexp"
	"strings"

	table "github.com/sttts/kc/internal/table"
	"k8s.io/apimachinery/pkg/labels"
)

// RowFilter hides rows of an object list. The text works like k9s' filter:
//
//	nginx        rows whose name matches the regular expression (any case)
//	!nginx       rows whose name does not match
//	-l app=web   rows whose labels match the label selector
//
// FaultsOnly keeps only the rows whose object is in a fault state (see
// RowHealth). The zero value filters nothing.
type RowFilter struct {
	text       string
	pattern    *regexp.Regexp
	invert     bool
	selector   labels.Selector
	FaultsOnly bool
}

// ParseRowFilter parses a filter text. Empty text is no filter.
func ParseRowFilter(text string) (RowFilter, error) {
	text = strings.TrimSpace(text)
	f := RowFilter{text: text}
	if text == "" {
		return f, nil
	}
	if rest, ok := cutFlag(text, "-l"); ok {
		if rest == "" {
			return RowFilter{}, fmt.Errorf("label selector is empty")
		}
		sel, err := labels.Parse(rest)
		if err != nil {
			return RowFilter{}, fmt.Errorf("invalid label selector: %w", err)
		}
		f.text = "-l " + rest
		f.selector = sel
		return f, nil
	}
	expr := text
	if strings.HasPrefix(expr, "!") {
		f.invert = true
		expr = strings.TrimSpace(expr[1:])
		if expr == "" {
			return RowFilter{}, fmt.Errorf("pattern after ! is empty")
		}
	}
	if _, err := regexp.Compile(expr); err != nil {
		return RowFilter{}, fmt.Errorf("invalid regular expression: %w", err)
	}
	re, err := regexp.Compile("(?i)" + expr)
	if err != nil {
		return RowFilter{}, fmt.Errorf("invalid regular expression: %w", err)
	}
	f.pattern = re
	return f, nil
}

// cutFlag returns the text after flag when text starts with flag followed
// by a space or the end of the text.
func cutFlag(text, flag string) (string, bool) {
	rest, ok := strings.CutPrefix(text, flag)
	if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '\t') {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// Text returns the filter text in its canonical form.
func (f RowFilter) Text() string { return f.text }

// Selector returns the label selector, or "" when the filter has none.
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
	Name   string
	ID     string
	Labels map[string]string
	Health RowHealth
}

// Matches reports whether the filter keeps a row.
func (f RowFilter) Matches(r RowFacts) bool {
	if f.FaultsOnly && !r.Health.Faulty() {
		return false
	}
	if f.selector != nil && !f.selector.Matches(labels.Set(r.Labels)) {
		return false
	}
	if f.pattern != nil {
		hit := f.pattern.MatchString(r.Name) || (r.ID != r.Name && f.pattern.MatchString(r.ID))
		if hit == f.invert {
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
func rowFactsFor(info RowStyleInfo, id string) RowFacts {
	return RowFacts{
		Name:   info.ObjectMeta.Name,
		ID:     id,
		Labels: info.ObjectMeta.Labels,
		Health: ClassifyRowHealth(info.GVR, info.Unstructured),
	}
}
