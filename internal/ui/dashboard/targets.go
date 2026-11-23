package dashboard

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/models"
)

// targetItem is one line of the target list: the way up (..) or a row of
// the breakdown.
type targetItem struct {
	up     bool
	target Target
	row    models.UsageRow
}

// scopeTarget is the namespace scope the dashboard's engine reads.
func (m *Model) scopeTarget() Target {
	ns := m.src.Scope
	if ns == "" && m.stack[0].target.Kind == KindScope {
		ns = m.stack[0].target.Namespace
	}
	return Target{Kind: KindScope, Namespace: ns}
}

// parent is the target one level above v's: a pod's workload when the
// engine knows it, else the scope. The scope has none.
func (m *Model) parent(v *view) (Target, bool) {
	switch v.target.Kind {
	case KindScope:
		return Target{}, false
	case KindPod:
		for _, r := range v.now.Rows {
			if r.Owner != nil && r.Name == v.target.Name {
				return Target{Kind: KindWorkload, Namespace: r.Owner.Namespace, Name: r.Owner.Name, Workload: r.Owner.GVR}, true
			}
		}
	}
	return m.scopeTarget(), true
}

// rowTarget is the dashboard target of a breakdown row.
func rowTarget(r models.UsageRow) Target {
	if w := r.Workload; w != nil {
		return Target{Kind: KindWorkload, Namespace: w.Namespace, Name: w.Name, Workload: w.GVR}
	}
	return Target{Kind: KindPod, Namespace: r.Namespace, Name: r.Name}
}

// targetItems are the lines of v's target list.
func (m *Model) targetItems(v *view) []targetItem {
	var out []targetItem
	if p, ok := m.parent(v); ok {
		out = append(out, targetItem{up: true, target: p})
	}
	for _, r := range v.now.Rows {
		if v.target.Kind == KindPod {
			// A pod lists itself; Enter has nowhere to go.
			out = append(out, targetItem{target: v.target, row: r})
			continue
		}
		out = append(out, targetItem{target: rowTarget(r), row: r})
	}
	return out
}

// up goes one level up: back from a drilldown, else to the parent of the
// target the dashboard was opened for.
func (m *Model) up() bool {
	v := m.top()
	if len(m.stack) > 1 {
		v.cancelRead()
		m.stack = m.stack[:len(m.stack)-1]
		m.refresh()
		return true
	}
	p, ok := m.parent(v)
	if !ok {
		return false
	}
	v.cancelRead()
	next := m.newView(p, v)
	m.firstRow(next)
	m.stack = []*view{next}
	m.refresh()
	return true
}

// drill opens the dashboard of the selected item.
func (m *Model) drill(v *view) {
	items := m.targetItems(v)
	if v.sel >= len(items) {
		return
	}
	it := items[v.sel]
	switch {
	case it.up:
		m.up()
	case it.target == v.target:
	default:
		next := m.newView(it.target, v)
		m.firstRow(next)
		m.stack = append(m.stack, next)
		m.refresh()
	}
}

// listTitle names the target list after what it lists.
func listTitle(v *view) string {
	t := v.target
	switch t.Kind {
	case KindNode:
		return "Workloads on node " + t.Name
	case KindWorkload:
		return "Pods of " + workloadKind(t.Workload) + " " + t.Name
	case KindPod:
		return "Pod " + t.Name
	}
	if t.Namespace == models.AllNamespaces {
		return "Workloads in all namespaces"
	}
	return "Workloads in " + t.Namespace
}

// listFooter counts what the list shows: workloads, pods, ready ones.
func listFooter(v *view) string {
	pods, ready, wl := 0, 0, 0
	for _, r := range v.now.Rows {
		pods += r.Pods
		ready += r.Ready
		if r.Workload != nil {
			wl++
		}
	}
	var parts []string
	if wl > 0 {
		parts = append(parts, plural(wl, "workload"))
	}
	parts = append(parts, plural(pods, "pod"), fmt.Sprintf("%d ready", ready))
	return strings.Join(parts, " · ")
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return strconv.Itoa(n) + " " + what + "s"
}

// Column widths of the target list.
const (
	colReady = 6
	colCPU   = 7
	colMem   = 8
	colPct   = 8
	colRst   = 9
	// minNameCol is the least room for a name.
	minNameCol = 10
)

// targetLines draws the target list in w x h cells: a header, the items
// with a second line for a row that is not well, and returns the item of
// each line (-1 for the header and a blank).
func (m *Model) targetLines(v *view, w, h int) ([]string, []int) {
	items := m.targetItems(v)
	v.sel = min(v.sel, max(0, len(items)-1))
	wide := w >= minNameCol+colReady+colCPU+colMem+2*colPct+colRst
	nameW := w - colReady - colCPU - colMem - 2*colPct - colRst
	if !wide {
		nameW = w - colReady - colMem - colPct
	}
	nameW = max(minNameCol, nameW)
	lines, at := []string{headStyle().Render(fit(listHeader(wide, nameW), w))}, []int{-1}
	if len(items) == 0 {
		note := v.now.Note
		switch {
		case v.now.Loading:
			note = "Loading the cluster's objects…"
		case note == "":
			note = "no pods that have not terminated"
		}
		return append(lines, noteStyle().Render(note)), append(at, -1)
	}
	// One or two lines per item; scroll so the selection shows.
	type block struct {
		lines []string
		item  int
	}
	var blocks []block
	focused := v.focus == widgetTargets
	for i, it := range items {
		sel := focused && i == v.sel
		blocks = append(blocks, block{lines: itemLines(it, sel, wide, m.scopeTarget().Namespace == models.AllNamespaces, mixedKinds(v.now.Rows), nameW, w), item: i})
	}
	first, rows := 0, h-1
	for {
		used := 0
		last := first
		for last < len(blocks) && used+len(blocks[last].lines) <= rows {
			used += len(blocks[last].lines)
			last++
		}
		if v.sel < last || first >= v.sel {
			break
		}
		first++
	}
	for _, b := range blocks[first:] {
		for _, l := range b.lines {
			if len(lines) >= h {
				return lines, at
			}
			lines, at = append(lines, l), append(at, b.item)
		}
	}
	// A short list of workloads leaves room: show the pods of the
	// selected one under it.
	if v.sel < len(items) && items[v.sel].row.Workload != nil && h-len(lines) >= 3 {
		pods := podsOf(v, items[v.sel].row.Workload, wide, nameW, w)
		pods = pods[:min(len(pods), h-len(lines))]
		lines = append(lines, pods...)
		for range pods {
			at = append(at, -1)
		}
	}
	return lines, at
}

// listHeader names the columns of the list.
func listHeader(wide bool, nameW int) string {
	head := pad("Name", nameW) + lpad("Ready", colReady)
	if wide {
		head += lpad("CPU", colCPU) + lpad("of req", colPct)
	}
	head += lpad("Memory", colMem) + lpad("of req", colPct)
	if wide {
		head += lpad("Restarts", colRst)
	}
	return head
}

// podsOf are the lines of the pods of workload wl under the list: a
// blank line, a heading and the pods, which are shown, not selected.
func podsOf(v *view, wl *models.InfoWorkload, wide bool, nameW, w int) []string {
	lines := []string{"", headStyle().Render(fit("Pods of "+workloadKind(wl.GVR)+" "+wl.Name+" (Enter to open)", w))}
	for _, p := range v.now.PodRows {
		if p.Owner != nil && *p.Owner == *wl {
			lines = append(lines, itemLines(targetItem{row: p}, false, wide, false, false, nameW, w)...)
		}
	}
	return lines
}

// itemLines draws one item: its numbers, and under them what is not well.
func itemLines(it targetItem, sel, wide, showNS, showKind bool, nameW, w int) []string {
	if it.up {
		text := ".. up to " + it.target.Title()
		if sel {
			return []string{curStyle().Render(fit(text, w))}
		}
		return []string{noteStyle().Render(fit(text, w))}
	}
	r := it.row
	name := r.Name
	if r.Workload != nil && showKind {
		name = workloadKind(r.Workload.GVR) + " " + name
	}
	if showNS && r.Namespace != "" {
		name = r.Namespace + "/" + name
	}
	ready := fmt.Sprintf("%d/%d", r.Ready, r.Pods)
	cpu, cpuPct, cpuSt := usageCells(r.CPU, formatCores)
	mem, memPct, memSt := usageCells(r.Memory, formatBytes)
	base := baseStyle()
	readySt := base
	if r.Ready < r.Pods {
		readySt = badStyle()
	}
	if sel {
		base, readySt, cpuSt, memSt = curStyle(), curStyle(), curStyle(), curStyle()
	}
	line := base.Render(pad(truncateMiddle(name, nameW-1), nameW)) + readySt.Render(lpad(ready, colReady))
	if wide {
		line += base.Render(lpad(cpu, colCPU)) + cpuSt.Render(lpad(cpuPct, colPct))
	}
	line += base.Render(lpad(mem, colMem)) + memSt.Render(lpad(memPct, colPct))
	if wide {
		line += base.Render(lpad(strconv.Itoa(int(r.Restarts)), colRst))
	}
	if sel {
		line += curStyle().Render(strings.Repeat(" ", max(0, w-lipgloss.Width(line))))
	}
	out := []string{line}
	if p := problemText(r); p != "" {
		st := badStyle()
		if sel {
			st = curStyle()
		}
		out = append(out, st.Render(fit("  ⚠ "+p, w)))
	}
	return out
}

// problemText says in words what is not well with a row, or "".
func problemText(r models.UsageRow) string {
	var parts []string
	if r.Problem != "" {
		parts = append(parts, r.Problem)
	}
	if r.OOMKilled > 0 {
		parts = append(parts, plural(r.OOMKilled, "container")+" OOMKilled")
	}
	if r.Restarts > 0 && r.Problem != "" {
		parts = append(parts, plural(int(r.Restarts), "restart"))
	}
	return strings.Join(parts, " · ")
}

// limitNear is the share of the limit from which usage is drawn as a
// danger.
const limitNear = 0.9

// usageCells are the usage of one resource, its share of the request,
// and the style that share is drawn in: over the request in yellow, near
// the limit in red.
func usageCells(a models.ResourceAmount, format func(float64) string) (use, pct string, st lipgloss.Style) {
	st = baseStyle()
	if !a.UsedKnown {
		return "-", "-", st
	}
	use, pct = format(a.Used), "no req"
	if a.Requests > 0 {
		pct = strconv.Itoa(int(a.Used/a.Requests*100+0.5)) + "%"
	}
	switch {
	case a.Limits > 0 && !a.LimitsPartial && a.Used >= limitNear*a.Limits:
		st = badStyle()
	case a.Requests > 0 && a.Used > a.Requests:
		st = overStyle()
	}
	return use, pct, st
}

func pad(s string, w int) string {
	if n := ansi.StringWidth(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return ansi.Truncate(s, w, "")
}

func lpad(s string, w int) string {
	if n := ansi.StringWidth(s); n < w {
		return strings.Repeat(" ", w-n) + s
	}
	return ansi.Truncate(s, w, "")
}

// mixedKinds reports whether the rows are of more than one kind, so the
// names need their kind.
func mixedKinds(rows []models.UsageRow) bool {
	kind := ""
	for i, r := range rows {
		k := "pod"
		if r.Workload != nil {
			k = r.Workload.GVR.String()
		}
		if i > 0 && k != kind {
			return true
		}
		kind = k
	}
	return false
}

// firstRow focuses the target list of v on its first row below "..".
func (m *Model) firstRow(v *view) {
	v.focus, v.sel = widgetTargets, 0
	if _, ok := m.parent(v); ok {
		v.sel = 1
	}
}

// truncateMiddle shortens s to w runes by cutting its middle, so the end
// that tells pods of one workload apart stays: web-69…-dgqgj.
func truncateMiddle(s string, w int) string {
	r := []rune(s)
	if len(r) <= w || w < 3 {
		return truncate(s, w)
	}
	tail := (w - 1) / 2
	return string(r[:w-1-tail]) + "…" + string(r[len(r)-tail:])
}
