package models

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"

	table "github.com/stokaro/kc/internal/table"
)

// Row IDs of the rows /overview adds around the findings. Finding rows keep
// the finding's ID, so the cursor stays on a finding when its value,
// severity or position changes.
const (
	overviewAttentionID   = "overview/attention"
	overviewQuietIDPrefix = "quiet/"
)

// findingClass is how the summary counts a finding (ADR 0172).
type findingClass int

const (
	classFailed findingClass = iota
	classWarning
	classUnavailable // the source could not be read: never a pass
	classOK
	classNotApplicable // nothing to check: not a pass either
)

func classify(f overviewFinding) findingClass {
	switch {
	case f.notApplicable != "":
		return classNotApplicable
	case f.severity == severityUnknown:
		return classUnavailable
	case f.severity == severityFailed:
		return classFailed
	case f.severity == severityWarning:
		return classWarning
	}
	return classOK
}

// needsAttention reports whether f is listed under /attention.
func needsAttention(f overviewFinding) bool { return classify(f) < classOK }

// quietFinding reports whether f is folded into its section's quiet row: a
// check that applied and found nothing, or one with nothing to check. A
// healthy row with objects behind it (the capacity rows) stays visible.
func quietFinding(f overviewFinding) bool {
	switch classify(f) {
	case classNotApplicable:
		return true
	case classOK:
		return len(f.targets) == 0
	}
	return false
}

// classCounts counts findings per class.
type classCounts [classNotApplicable + 1]int

func countClasses(findings []overviewFinding) classCounts {
	var c classCounts
	for _, f := range findings {
		c[classify(f)]++
	}
	return c
}

// problems is "1 failed, 2 warning, 1 unavailable", or "" when none.
func (c classCounts) problems() string {
	var parts []string
	for _, p := range []struct {
		class findingClass
		word  string
	}{{classFailed, "failed"}, {classWarning, "warning"}, {classUnavailable, "unavailable"}} {
		if n := c[p.class]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, p.word))
		}
	}
	return strings.Join(parts, ", ")
}

// passed is "20 ok, 8 n/a", or "" when both are zero.
func (c classCounts) passed() string {
	var parts []string
	if n := c[classOK]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d ok", n))
	}
	if n := c[classNotApplicable]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d n/a", n))
	}
	return strings.Join(parts, ", ")
}

// style is that of the worst class counted.
func (c classCounts) style() *lipgloss.Style {
	switch {
	case c[classFailed] > 0:
		return overviewSeverityStyle(severityFailed)
	case c[classWarning] > 0:
		return overviewSeverityStyle(severityWarning)
	case c[classUnavailable] > 0:
		return overviewSeverityStyle(severityUnknown)
	}
	return overviewSeverityStyle(severityOK)
}

// emptyScopeText says that a namespace has nothing the namespace checks
// apply to, or "" when it has, or when the overview covers every
// namespace.
func emptyScopeText(findings []overviewFinding, namespace string) string {
	if namespace == AllNamespaces {
		return ""
	}
	local := 0
	for _, f := range findings {
		if _, cluster := findingScope(f.id, namespace); cluster || slices.Contains(overviewWindowRules, f.id) {
			continue
		}
		local++
		if classify(f) != classNotApplicable {
			return ""
		}
	}
	if local == 0 {
		return ""
	}
	return "no workloads in " + namespaceScopeText(namespace)
}

// attentionRow is the first row: the counts of the whole report. Enter
// lists what needs attention.
func (f *OverviewFolder) attentionRow(report *overviewReport) table.Row {
	c := countClasses(report.findings)
	value := c.problems()
	if value == "" {
		value = "nothing"
	}
	state := c.passed()
	if empty := emptyScopeText(report.findings, f.namespace); empty != "" {
		state = empty
	}
	path := append(slices.Clone(f.Path()), "attention")
	findings := func(r *overviewReport) []overviewFinding { return attentionFindings(r.findings) }
	item := styledChildItem(overviewAttentionID, []string{"/attention", value, state}, path, c.style(), func() (Folder, error) {
		return newOverviewFindingsFolder(f, path, "attention", findings), nil
	})
	item.SetDetails(fmt.Sprintf("Needs attention: %s. Passed or nothing to check: %s. Unavailable checks are never counted as passed.", value, orNone(c.passed())))
	return item
}

func styledChildItem(id string, cells, path []string, style *lipgloss.Style, enter func() (Folder, error)) *ChildResourceItem {
	return &ChildResourceItem{RowItem: NewRowItem(id, cells, path, style), enter: enter}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// attentionFindings are the findings that need attention, failed first,
// then warnings, then unavailable sources, each in rule order.
func attentionFindings(findings []overviewFinding) []overviewFinding {
	var out []overviewFinding
	for _, f := range findings {
		if needsAttention(f) {
			out = append(out, f)
		}
	}
	slices.SortStableFunc(out, func(a, b overviewFinding) int { return int(classify(a)) - int(classify(b)) })
	return out
}

// quietRow folds the quiet findings of a section; Enter lists them.
func (f *OverviewFolder) quietRow(section string, quiet []overviewFinding) table.Row {
	c := countClasses(quiet)
	key := strings.ToLower(section)
	path := append(slices.Clone(f.Path()), key+" quiet checks")
	findings := func(r *overviewReport) []overviewFinding {
		var out []overviewFinding
		for _, x := range r.findings {
			if x.section == section && quietFinding(x) {
				out = append(out, x)
			}
		}
		return out
	}
	item := styledChildItem(overviewQuietIDPrefix+key, []string{fmt.Sprintf("/%d quiet checks", len(quiet)), c.passed(), "hidden"}, path, DimStyle(), func() (Folder, error) {
		return newOverviewFindingsFolder(f, path, section+" quiet checks", findings), nil
	})
	item.SetDetails(fmt.Sprintf("%s checks that found nothing (%s); n/a means there was nothing to check. Enter lists them.", section, c.passed()))
	return item
}

// overviewFindingsFolder lists some findings of the overview's latest
// report: those needing attention, or a section's quiet checks. Its rows
// are the overview's own finding rows, so Enter and F3 work as there.
type overviewFindingsFolder struct {
	*BaseFolder
	overview *OverviewFolder
	what     string
	findings func(*overviewReport) []overviewFinding
	unsub    func()
}

func newOverviewFindingsFolder(overview *OverviewFolder, path []string, what string, findings func(*overviewReport) []overviewFinding) *overviewFindingsFolder {
	base := NewBaseFolder(overview.Deps, overviewFindingColumns(), path)
	f := &overviewFindingsFolder{BaseFolder: base, overview: overview, what: what, findings: findings}
	engine := overview.engine
	base.SetRowSource(newReadingRowSource(newSliceRowSource(f.populate), func() { _, _ = engine.snapshot() }))
	f.unsub = engine.subscribe(f.Refresh)
	return f
}

// Reread recomputes the overview at once; the rows follow when it is done.
func (f *overviewFindingsFolder) Reread(ctx context.Context) {
	f.overview.engine.reread()
	f.BaseFolder.Reread(ctx)
}

// Close stops following the report; the overview below keeps the engine.
func (f *overviewFindingsFolder) Close() {
	if f.unsub != nil {
		f.unsub()
	}
}

// NamespaceScope is the overview's.
func (f *overviewFindingsFolder) NamespaceScope() string { return f.overview.namespace }

func (f *overviewFindingsFolder) populate(context.Context) ([]table.Row, error) {
	report, _ := f.overview.engine.snapshot()
	if report == nil {
		return nil, nil
	}
	list := f.findings(report)
	if len(list) == 0 {
		return []table.Row{NewSimpleItem("overview/none", []string{" nothing in " + f.what, "", ""}, f.Path(), DimStyle())}, nil
	}
	rows := make([]table.Row, 0, len(list))
	for _, finding := range list {
		// Out of its section a cluster finding always says so.
		rows = append(rows, f.overview.findingRow(finding, true, f.Path()))
	}
	return rows, nil
}

// KubectlCommand is the overview's.
func (f *overviewFindingsFolder) KubectlCommand() string { return f.overview.KubectlCommand() }

// CommandLineFor shows the selected finding's inputs, as in the overview.
func (f *overviewFindingsFolder) CommandLineFor(item Item) (CommandLine, bool) {
	return f.overview.CommandLineFor(item)
}
