package info

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	models "github.com/stokaro/kc/internal/models"
)

// findingLines render the preview of an overview finding: what it is, its
// value and why, the first objects behind it, and the keys that act on it
// in the other panel (ADR 0173).
func findingLines(p models.FindingPreview, width int) []string {
	var out []string
	add := func(s string) { out = append(out, ansi.Truncate(s, width, "")) }
	wrap := func(indent, s string) {
		for _, line := range wrapWords(s, max(width-len(indent), 10)) {
			add(indent + line)
		}
	}
	add(" " + styled(p.Title, p.Health))
	add(" Scope: " + p.Scope)
	add(" Value: " + styled(p.Value, p.Health) + "  State: " + styled(p.State, p.Health))
	add("")
	switch {
	case p.Unavailable != "":
		add(" Not available")
		wrap("   ", p.Unavailable)
		if len(p.Verify) > 0 {
			add("")
			add(" Check with")
			for _, c := range p.Verify {
				wrap("   ", c)
			}
		}
	case p.NotApplicable != "":
		add(" Not applicable")
		wrap("   ", p.NotApplicable)
	case p.TargetCount == 0:
		add(" Nothing matched. Checked:")
		wrap("   ", p.Why)
	default:
		wrap(" ", p.Why)
		add("")
		add(fmt.Sprintf(" %d %s, as Enter lists them", p.TargetCount, plural(p.TargetCount, "object", "objects")))
		for _, t := range p.Targets {
			line := "   " + t.String()
			if t.Value != "" {
				line += "  " + t.Value
			}
			add(styled(line, t.Health))
		}
		if more := p.TargetCount - len(p.Targets); more > 0 {
			add(fmt.Sprintf("   and %d more", more))
		}
	}
	if p.Partial != "" && p.Unavailable == "" {
		add("")
		add(" Partial, not counted")
		wrap("   ", p.Partial)
	}
	add("")
	add(" In the list: Enter lists the objects, F3 shows the calculation")
	if p.Inputs > 0 {
		add(fmt.Sprintf(" M-c copies the %d input %s", p.Inputs, plural(p.Inputs, "command", "commands")))
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// wrapWords splits s into lines of at most width cells, at spaces.
func wrapWords(s string, width int) []string {
	var lines []string
	var cur strings.Builder
	for word := range strings.FieldsSeq(s) {
		if cur.Len() > 0 && ansi.StringWidth(cur.String())+1+ansi.StringWidth(word) > width {
			lines = append(lines, cur.String())
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteByte(' ')
		}
		cur.WriteString(word)
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}
