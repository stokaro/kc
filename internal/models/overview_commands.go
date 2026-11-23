package models

import (
	"fmt"
	"slices"
	"strings"
)

// Placeholders in a finding's kubectl arguments. A target of the right kind
// resolves them. On the finding's row, a command with a placeholder is
// expanded once per target that resolves it, and otherwise copied only as
// a comment that says how to fill it in, never as a runnable command
// (ADR 0178).
const (
	placeholderNode     = "<node>"     // a node name, in any argument
	placeholderWorkload = "<workload>" // a whole argument: resource/name, then -n <namespace>
)

// overviewExpandMax bounds the per-target commands of one input on the
// finding's row; the copied text says how many it left out.
const overviewExpandMax = 20

// findingInput is one input of a finding, resolved for the row.
type findingInput struct {
	supplies string   // what it gives the calculation, may be empty
	commands []string // runnable commands, quoted; empty when none resolves
	template string   // the command with its placeholder, when commands is empty or expanded
	omitted  int      // targets past overviewExpandMax
}

// findingInputs resolves the inputs of finding for target, or for the
// finding's row when target is nil.
func findingInputs(deps Deps, finding overviewFinding, target *overviewTarget) []findingInput {
	out := make([]findingInput, 0, len(finding.kubectl))
	for i, args := range finding.kubectl {
		in := findingInput{}
		if i < len(finding.supplies) {
			in.supplies = finding.supplies[i]
		}
		if resolved, ok := resolvePlaceholders(args, target); ok {
			in.commands = []string{kubectlCommand(deps, resolved...)}
			out = append(out, in)
			continue
		}
		in.template = kubectlCommand(deps, args...)
		if target == nil {
			for j := range finding.targets {
				resolved, ok := resolvePlaceholders(args, &finding.targets[j])
				if !ok {
					continue
				}
				if len(in.commands) == overviewExpandMax {
					in.omitted++
					continue
				}
				in.commands = append(in.commands, kubectlCommand(deps, resolved...))
			}
		}
		out = append(out, in)
	}
	return out
}

// resolvePlaceholders fills the placeholders of args from target. ok is
// false when one is left.
func resolvePlaceholders(args []string, target *overviewTarget) (out []string, ok bool) {
	out = make([]string, 0, len(args)+2)
	for _, a := range args {
		switch {
		case strings.Contains(a, placeholderNode):
			if target == nil || target.gvr != ovNodesGVR || target.name == "" {
				return nil, false
			}
			out = append(out, strings.ReplaceAll(a, placeholderNode, target.name))
		case a == placeholderWorkload:
			if target == nil || target.namespace == "" || target.name == "" {
				return nil, false
			}
			out = append(out, kubectlResource(target.gvr)+"/"+target.name, "-n", target.namespace)
		default:
			out = append(out, a)
		}
	}
	return out, true
}

// templateHint says how to fill in the placeholder of command.
func templateHint(command string) string {
	switch {
	case strings.Contains(command, placeholderWorkload):
		return "for one workload, replace " + placeholderWorkload + " with kind/name -n namespace: " + command
	case strings.Contains(command, placeholderNode):
		return "for one node, replace " + placeholderNode + " with a name from kubectl get nodes: " + command
	}
	return command
}

// findingCommandLine is the command line of finding, or of one of its
// targets. kc computes a finding, so the line labels its commands as
// inputs. With one runnable command M-c copies that command; with more it
// copies all of them under a comment saying that none reproduces the
// value, each after a comment saying what it supplies.
func findingCommandLine(deps Deps, finding overviewFinding, target *overviewTarget) (CommandLine, bool) {
	inputs := findingInputs(deps, finding, target)
	if len(inputs) == 0 {
		return CommandLine{}, false
	}
	var all []string
	for _, in := range inputs {
		all = append(all, in.commands...)
	}
	line := CommandLine{Label: "Input"}
	if len(inputs) > 1 {
		line.Label = fmt.Sprintf("Inputs (%d)", len(inputs))
	}
	subject := finding.title
	if target != nil {
		subject += " for " + target.name
	}
	if len(inputs) == 1 && len(all) == 1 {
		line.Show = all[0]
		return line, true
	}
	switch {
	case len(all) == 0:
		line.Show = "# " + templateHint(inputs[0].template)
	case len(all) == 1:
		line.Show = all[0] + "  # M-c copies all inputs"
	default:
		line.Show = fmt.Sprintf("%s  # +%d more, M-c copies all", all[0], len(all)-1)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: kc computes this value from the inputs below; no single command reproduces it.\n", subject)
	for _, in := range inputs {
		if in.supplies != "" {
			fmt.Fprintf(&b, "# %s\n", in.supplies)
		}
		if len(in.commands) == 0 {
			fmt.Fprintf(&b, "# %s\n", templateHint(in.template))
			continue
		}
		if in.template != "" {
			b.WriteString("# one command per listed object\n")
		}
		for _, c := range in.commands {
			b.WriteString(c + "\n")
		}
		if in.omitted > 0 {
			fmt.Fprintf(&b, "# %d more; %s\n", in.omitted, templateHint(in.template))
		}
	}
	line.Copy = b.String()
	line.What = fmt.Sprintf("%d %s for %s", len(all), plural(len(all), "input command", "input commands"), subject)
	return line, true
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// targetFor finds the target of finding that item is.
func targetFor(finding overviewFinding, item Item) (*overviewTarget, bool) {
	obj, ok := item.(ObjectItem)
	if !ok {
		return nil, false
	}
	i := slices.IndexFunc(finding.targets, func(t overviewTarget) bool {
		return t.gvr == obj.GVR() && t.namespace == obj.Namespace() && t.name == obj.Name()
	})
	if i < 0 {
		return nil, false
	}
	return &finding.targets[i], true
}

// CommandLineFor follows the cursor: a finding row shows its inputs;
// a section row shows the folder's command.
func (f *OverviewFolder) CommandLineFor(item Item) (CommandLine, bool) {
	row, ok := item.(*OverviewFindingItem)
	if !ok {
		return CommandLine{}, false
	}
	finding, ok := f.currentFinding(row.id)
	if !ok {
		return CommandLine{}, false
	}
	return findingCommandLine(f.Deps, finding, nil)
}

// CommandLineFor shows the finding's inputs resolved for the selected
// object: the stats of that node, the rollout of that workload.
func (f *OverviewTargetsFolder) CommandLineFor(item Item) (CommandLine, bool) {
	finding, ok := f.finding()
	if !ok {
		return CommandLine{}, false
	}
	target, _ := targetFor(finding, item)
	return findingCommandLine(f.Deps, finding, target)
}

func (f *OverviewTargetsFolder) finding() (overviewFinding, bool) {
	report, _ := f.engine.snapshot()
	return report.finding(f.findingID)
}
