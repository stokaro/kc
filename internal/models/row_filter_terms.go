package models

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/stokaro/kc/pkg/appconfig"
)

// filterTerm is one term of a RowFilter other than -l.
type filterTerm interface {
	matches(r RowFacts) bool
	String() string
}

// Keys of column terms that read the row's metadata, not a table cell.
const (
	filterKeyName      = sortKeyName
	filterKeyNamespace = sortKeyNamespace
	filterKeyAge       = sortKeyAge
)

// columnTermPattern reads "<column><op><value>". Object names hold no
// operator characters, so a name pattern never reads as a column term.
var columnTermPattern = regexp.MustCompile(`^([A-Za-z%][A-Za-z0-9%/._-]*)(>=|<=|!=|==|=|>|<)(.*)$`)

// ParseRowFilter parses a filter text. Empty text is no filter. Terms are
// separated by spaces (spaces inside parentheses do not separate), and a
// row must match all of them:
//
//	web, ^web-\d$     the name or the row ID matches the regular expression, any case
//	!web, ! web       the name and the row ID do not match
//	-l <selector>     the labels match; the selector runs as far as it parses
//	-n a,b            the namespace is a or b (the same as namespace=a,b)
//	<col>=<v>[,<v>]   the column equals one of the values
//	<col>!=<v>[,<v>]  the column equals none of the values
//	<col>>v, >=, <, <=  the column compares with v
//
// A column is named by its sort key: the title in lower case with spaces
// as dashes (cpu, mem, restarts, status, node, nominated-node). Values
// compare the way the column sorts: quantities (500m, 1Gi), counts,
// durations (5m, 2d), numbers, IP addresses, otherwise text in any case.
// A row whose cell is missing (empty, <none>, n/a) or whose list lacks the
// column matches no column term, whatever the operator.
func ParseRowFilter(text string) (RowFilter, error) {
	toks := splitFilterTerms(text)
	if len(toks) == 0 {
		return RowFilter{}, nil
	}
	var (
		f     RowFilter
		canon []string
		reqs  labels.Requirements
	)
	for i := 0; i < len(toks); {
		tok := toks[i]
		switch tok {
		case "-l":
			sel, n, err := parseSelectorTerms(toks[i+1:])
			if err != nil {
				return RowFilter{}, err
			}
			r, _ := sel.Requirements()
			reqs = append(reqs, r...)
			canon = append(canon, "-l "+strings.Join(toks[i+1:i+1+n], " "))
			i += 1 + n
		case "-n":
			if i+1 >= len(toks) {
				return RowFilter{}, fmt.Errorf("namespace list after -n is empty")
			}
			t, err := newColumnTerm("-n "+toks[i+1], filterKeyNamespace, "=", toks[i+1])
			if err != nil {
				return RowFilter{}, err
			}
			f.terms = append(f.terms, t)
			canon = append(canon, t.text)
			i += 2
		case "!":
			if i+1 >= len(toks) {
				return RowFilter{}, fmt.Errorf("pattern after ! is empty")
			}
			t, err := newNameTerm("! "+toks[i+1], toks[i+1], true)
			if err != nil {
				return RowFilter{}, err
			}
			f.terms = append(f.terms, t)
			canon = append(canon, t.text)
			i += 2
		default:
			t, err := parseFilterTerm(tok)
			if err != nil {
				return RowFilter{}, err
			}
			f.terms = append(f.terms, t)
			canon = append(canon, t.String())
			i++
		}
	}
	if len(reqs) > 0 {
		f.selector = labels.NewSelector().Add(reqs...)
	}
	f.text = strings.Join(canon, " ")
	return f, nil
}

func parseFilterTerm(tok string) (filterTerm, error) {
	if rest, ok := strings.CutPrefix(tok, "!"); ok {
		return newNameTerm(tok, rest, true)
	}
	if m := columnTermPattern.FindStringSubmatch(tok); m != nil {
		return newColumnTerm(tok, appconfig.ObjectsOrderKey(m[1]), m[2], m[3])
	}
	return newNameTerm(tok, tok, false)
}

// splitFilterTerms splits text at spaces outside parentheses.
func splitFilterTerms(text string) []string {
	var (
		out   []string
		cur   strings.Builder
		depth int
	)
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range strings.TrimSpace(text) {
		switch {
		case r == '(':
			depth++
		case r == ')' && depth > 0:
			depth--
		case (r == ' ' || r == '\t') && depth == 0:
			flush()
			continue
		}
		cur.WriteRune(r)
	}
	flush()
	return out
}

// parseSelectorTerms reads the label selector at the start of toks: the
// most terms that parse as one selector. It returns how many it used.
func parseSelectorTerms(toks []string) (labels.Selector, int, error) {
	if len(toks) == 0 {
		return nil, 0, fmt.Errorf("label selector is empty")
	}
	var firstErr error
	for n := len(toks); n > 0; n-- {
		sel, err := labels.Parse(strings.Join(toks[:n], " "))
		if err == nil {
			return sel, n, nil
		}
		firstErr = err
	}
	return nil, 0, fmt.Errorf("invalid label selector: %w", firstErr)
}

// nameTerm matches the name or the row ID with a regular expression.
type nameTerm struct {
	text   string
	re     *regexp.Regexp
	invert bool
}

func newNameTerm(text, expr string, invert bool) (nameTerm, error) {
	if expr == "" {
		return nameTerm{}, fmt.Errorf("pattern after ! is empty")
	}
	if _, err := regexp.Compile(expr); err != nil {
		return nameTerm{}, fmt.Errorf("invalid regular expression: %w", err)
	}
	re, err := regexp.Compile("(?i)" + expr)
	if err != nil {
		return nameTerm{}, fmt.Errorf("invalid regular expression: %w", err)
	}
	return nameTerm{text: text, re: re, invert: invert}, nil
}

func (t nameTerm) String() string { return t.text }

func (t nameTerm) matches(r RowFacts) bool {
	hit := t.re.MatchString(r.Name) || (r.ID != r.Name && t.re.MatchString(r.ID))
	return hit != t.invert
}

// columnTerm compares one column with one or more values.
type columnTerm struct {
	text   string
	key    string
	op     string
	values []string
}

func newColumnTerm(text, key, op, value string) (columnTerm, error) {
	if op == "==" {
		op = "="
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return columnTerm{}, fmt.Errorf("%s: the value is empty", text)
	}
	values := strings.Split(value, ",")
	for i, v := range values {
		values[i] = strings.TrimSpace(v)
		if values[i] == "" {
			return columnTerm{}, fmt.Errorf("%s: a value in the list is empty", text)
		}
	}
	if len(values) > 1 && op != "=" && op != "!=" {
		return columnTerm{}, fmt.Errorf("%s: %s compares with one value", text, op)
	}
	return columnTerm{text: text, key: key, op: op, values: values}, nil
}

func (t columnTerm) String() string { return t.text }

func (t columnTerm) matches(r RowFacts) bool {
	kind, v, ok := r.cell(t.key)
	if !ok || v.missing {
		return false
	}
	switch t.op {
	case "=", "!=":
		equal := false
		for _, s := range t.values {
			want := parseSortValue(kind, s)
			if want.missing {
				// A value the column cannot hold narrows the view.
				return false
			}
			if compareSortValues(v, want, false) == 0 {
				equal = true
			}
		}
		return equal == (t.op == "=")
	}
	want := parseSortValue(kind, t.values[0])
	if want.missing {
		return false
	}
	c := compareSortValues(v, want, false)
	switch t.op {
	case ">":
		return c > 0
	case ">=":
		return c >= 0
	case "<":
		return c < 0
	case "<=":
		return c <= 0
	}
	return false
}

// cell returns the value of column key for the row and the kind it
// compares as. ok is false when the list has no such column.
func (r RowFacts) cell(key string) (sortKind, sortValue, bool) {
	switch key {
	case filterKeyName:
		return sortString, parseSortValue(sortString, r.Name), true
	case filterKeyNamespace:
		return sortString, parseSortValue(sortString, r.Namespace), true
	case filterKeyAge:
		if r.Created.IsZero() {
			return sortDuration, sortValue{missing: true}, true
		}
		return sortDuration, sortValue{num: float64(time.Since(r.Created))}, true
	}
	if r.cols == nil {
		return 0, sortValue{}, false
	}
	return r.cols.value(key, r.row)
}

// filterColumns holds the cells of one listing for column terms. The cells
// of a column are read on first use, so a filter without column terms
// costs nothing.
type filterColumns struct {
	keys  []string
	index map[string]int
	defs  []metav1.TableColumnDefinition
	cells [][]any // per row

	mu    sync.Mutex
	kinds map[string]sortKind
	vals  map[string][]sortValue
}

func newFilterColumns(defs []metav1.TableColumnDefinition, cells [][]any) *filterColumns {
	c := &filterColumns{index: make(map[string]int, len(defs)), defs: defs, cells: cells}
	for i, d := range defs {
		key := appconfig.ObjectsOrderKey(d.Name)
		if key == "" {
			continue
		}
		if _, dup := c.index[key]; dup {
			continue
		}
		c.index[key] = i
		c.keys = append(c.keys, key)
	}
	return c
}

func (c *filterColumns) column(key string) (sortKind, []sortValue, bool) {
	col, ok := c.index[key]
	if !ok {
		return 0, nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if vals, ok := c.vals[key]; ok {
		return c.kinds[key], vals, true
	}
	cells := make([]any, len(c.cells))
	for i, row := range c.cells {
		if col < len(row) {
			cells[i] = row[col]
		}
	}
	kind, vals := cellSortValues(cells, c.defs[col])
	if c.vals == nil {
		c.vals = make(map[string][]sortValue)
		c.kinds = make(map[string]sortKind)
	}
	c.vals[key], c.kinds[key] = vals, kind
	return kind, vals, true
}

func (c *filterColumns) value(key string, row int) (sortKind, sortValue, bool) {
	kind, vals, ok := c.column(key)
	if !ok || row < 0 || row >= len(vals) {
		return 0, sortValue{}, false
	}
	return kind, vals[row], true
}

// RowFilterValidator folders check a filter against the columns they list,
// so the Filter dialog can refuse a term that would hide every row.
type RowFilterValidator interface {
	ValidateRowFilter(f RowFilter) error
}

// validateColumns reports column terms that name no column of cols or
// whose values the column cannot hold. cols nil means the list has not
// loaded yet; then only the metadata keys are checked.
func (f RowFilter) validateColumns(cols *filterColumns) error {
	for _, t := range f.terms {
		ct, ok := t.(columnTerm)
		if !ok {
			continue
		}
		var kind sortKind
		switch ct.key {
		case filterKeyName, filterKeyNamespace:
			continue
		case filterKeyAge:
			kind = sortDuration
		default:
			if cols == nil {
				continue
			}
			k, _, ok := cols.column(ct.key)
			if !ok {
				keys := slices.Concat([]string{filterKeyNamespace, filterKeyAge}, cols.keys)
				return fmt.Errorf("%s: this list has no %q column; columns: %s", ct.text, ct.key, strings.Join(keys, ", "))
			}
			kind = k
		}
		for _, v := range ct.values {
			if parseSortValue(kind, v).missing {
				return fmt.Errorf("%s: %q is not %s", ct.text, v, sortKindName(kind))
			}
		}
	}
	return nil
}

func sortKindName(k sortKind) string {
	switch k {
	case sortNumber:
		return "a number"
	case sortRatio:
		return "a ratio like 1/2"
	case sortCount:
		return "a count"
	case sortDuration:
		return "a duration like 5m or 2d"
	case sortQuantity:
		return "a quantity like 500m or 1Gi"
	case sortIP:
		return "an IP address"
	}
	return "text"
}

// kubectlParts splits the filter for an equivalent kubectl command: the one
// namespace a single "-n ns" term names, and the terms kubectl cannot
// express. The label selector goes to -l separately.
func (f RowFilter) kubectlParts() (namespace string, other []string) {
	for _, t := range f.terms {
		if ct, ok := t.(columnTerm); ok && namespace == "" && ct.key == filterKeyNamespace && ct.op == "=" && len(ct.values) == 1 {
			namespace = ct.values[0]
			continue
		}
		other = append(other, t.String())
	}
	if f.FaultsOnly {
		other = append(other, "faults")
	}
	return namespace, other
}
