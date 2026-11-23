package info

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	models "github.com/stokaro/kc/internal/models"
)

// findingLines render the preview of an overview finding: what it is, its
// value and why, the first objects behind it, and the keys that act on it
// in the other panel (ADR 0173). Every line wraps at width with its indent,
// as the rest of Info does, and keeps its color on each wrapped line.
func findingLines(p models.FindingPreview, width int) []string {
	var out []string
	blank := func() { out = append(out, "") }
	// para wraps the plain text s: the first line under indent, the
	// wrapped ones under cont.
	para := func(indent, cont, s string, h models.RowHealth) {
		for i, part := range wrapText(s, max(width-len(cont), 1)) {
			lead := cont
			if i == 0 {
				lead = indent
			}
			out = append(out, lead+styled(part, h))
		}
	}
	text := func(indent, s string) { para(indent, indent, s, models.RowHealthUnknown) }
	para(" ", " ", p.Title, p.Health)
	text(" ", "Scope: "+p.Scope)
	out = append(out, valueStateLines(p, width)...)
	blank()
	switch {
	case p.Unavailable != "":
		text(" ", "Not available")
		text("   ", p.Unavailable)
		if len(p.Verify) > 0 {
			blank()
			text(" ", "Check with")
			for _, c := range p.Verify {
				text("   ", c)
			}
		}
	case p.NotApplicable != "":
		text(" ", "Not applicable")
		text("   ", p.NotApplicable)
	case p.TargetCount == 0:
		text(" ", "Nothing matched. Checked:")
		text("   ", p.Why)
	default:
		text(" ", p.Why)
		blank()
		text(" ", fmt.Sprintf("%d %s, as Enter lists them", p.TargetCount, plural(p.TargetCount, "object", "objects")))
		for _, t := range p.Targets {
			line := t.String()
			if t.Value != "" {
				line += "  " + t.Value
			}
			para("   ", "     ", line, t.Health)
		}
		if more := p.TargetCount - len(p.Targets); more > 0 {
			text("   ", fmt.Sprintf("and %d more", more))
		}
	}
	if p.Partial != "" && p.Unavailable == "" {
		blank()
		text(" ", "Partial, not counted")
		text("   ", p.Partial)
	}
	blank()
	text(" ", "In the list: Enter lists the objects, F3 shows the calculation")
	if p.Inputs > 0 {
		text(" ", fmt.Sprintf("M-c copies the %d input %s", p.Inputs, plural(p.Inputs, "command", "commands")))
	}
	return out
}

// valueStateLines are the "Value: v  State: s" line, both values in the
// finding's color. When the line does not fit, State goes on a line of its
// own, and a value too long for one line wraps.
func valueStateLines(p models.FindingPreview, width int) []string {
	if 1+ansi.StringWidth("Value: "+p.Value+"  State: "+p.State) <= width {
		return []string{" Value: " + styled(p.Value, p.Health) + "  State: " + styled(p.State, p.Health)}
	}
	var out []string
	for _, kv := range [][2]string{{"Value: ", p.Value}, {"State: ", p.State}} {
		for i, part := range wrapText(kv[0]+kv[1], max(width-1, 1)) {
			if i == 0 && strings.HasPrefix(part, kv[0]) {
				out = append(out, " "+kv[0]+styled(strings.TrimPrefix(part, kv[0]), p.Health))
				continue
			}
			out = append(out, " "+styled(part, p.Health))
		}
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
