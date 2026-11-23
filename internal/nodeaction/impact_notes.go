package nodeaction

import "fmt"

// Bounds on the impact lines of the confirmation dialog.
const (
	maxWorkloadNotes = 3
	maxBudgetNotes   = 4
	maxImpactWarns   = 2
)

// notes are the impact lines of the confirmation dialog. Budget lines say
// they were read now; the API decides each eviction later.
func (im Impact) notes() []string {
	var out []string
	if len(im.Workloads) > 0 {
		out = append(out, "Workloads:")
		for i, w := range im.Workloads {
			if i == maxWorkloadNotes {
				out = append(out, fmt.Sprintf("  ... and %d more", len(im.Workloads)-maxWorkloadNotes))
				break
			}
			out = append(out, "  "+w.String())
		}
	}
	if !im.Read {
		return out
	}
	if len(im.Budgets) > 0 {
		out = append(out, "PodDisruptionBudgets, as read now:")
		for i, b := range im.Budgets {
			if i == maxBudgetNotes {
				out = append(out, fmt.Sprintf("  ... and %d more", len(im.Budgets)-maxBudgetNotes))
				break
			}
			out = append(out, "  "+b.String())
			if w := b.Waiting(); w > 0 {
				out = append(out, fmt.Sprintf("    %s wait until replacements are healthy", plural(w, "eviction")))
			}
		}
	}
	if im.Uncovered > 0 {
		out = append(out, "No budget covers "+plural(im.Uncovered, "planned pod")+".")
	}
	if im.Bypass > 0 {
		out = append(out, plural(im.Bypass, "pending, finished or terminating pod")+" bypass budgets.")
	}
	out = append(out, warnNotes(im.MultiBudget, false)...)
	out = append(out, warnNotes(im.Unreadable, true)...)
	return out
}

// warnNotes renders up to maxImpactWarns notes as "! " lines.
func warnNotes(notes []PodNote, namespaceOnly bool) []string {
	var out []string
	for i, n := range notes {
		if i == maxImpactWarns {
			out = append(out, fmt.Sprintf("... and %d more", len(notes)-maxImpactWarns))
			break
		}
		who := n.Namespace + "/" + n.Name
		if namespaceOnly {
			who = "namespace " + n.Namespace
		}
		out = append(out, "! "+who, "  "+n.Message)
	}
	return out
}
