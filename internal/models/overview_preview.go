package models

import (
	"fmt"
	"strings"
)

// findingPreviewTargets is how many objects the Info preview of a finding
// lists; Enter lists all of them.
const findingPreviewTargets = 8

// FindingPreview is what the Info panel mode shows for the overview finding
// under the other panel's cursor (ADR 0173). It is built from one report
// snapshot, so its value, state, scope and objects agree.
type FindingPreview struct {
	ID     string // the finding's row ID
	Title  string // "/pods pending"
	Scope  string // "namespace shop", "cluster: every node"
	Value  string
	State  string
	Health RowHealth
	// Why is the first paragraph of the calculation: what the rule checks
	// and the thresholds it uses.
	Why string
	// Targets are the first objects behind the finding, in the order Enter
	// lists them; TargetCount is how many there are in all.
	Targets     []FindingPreviewTarget
	TargetCount int
	// Unavailable says which source could not be read; Verify are
	// read-only commands that show it.
	Unavailable string
	Verify      []string
	// NotApplicable says why the finding had nothing to check (ADR 0172);
	// its value is then "none", not a result.
	NotApplicable string
	// Inputs is how many input commands M-c copies.
	Inputs int
}

// FindingPreviewTarget is one object behind a finding.
type FindingPreviewTarget struct {
	Resource  string // "pods", "deployments.apps"
	Namespace string
	Name      string
	Value     string // why it is listed
	Health    RowHealth
}

// FindingPreviewer is a row that the Info panel mode describes as a
// finding instead of showing the capacity summary.
type FindingPreviewer interface {
	FindingPreview() (FindingPreview, bool)
}

var _ FindingPreviewer = (*OverviewFindingItem)(nil)

// FindingPreview reads the latest report once and describes the finding.
// ok is false while the report is not computed or no longer has the
// finding.
func (i *OverviewFindingItem) FindingPreview() (FindingPreview, bool) {
	finding, ok := i.folder.currentFinding(i.id)
	if !ok {
		return FindingPreview{}, false
	}
	return i.folder.findingPreview(finding), true
}

func (f *OverviewFolder) findingPreview(finding overviewFinding) FindingPreview {
	scope, _ := findingScope(finding.id, f.namespace)
	p := FindingPreview{
		ID: finding.id, Title: finding.title, Scope: scope,
		Value: finding.value, State: finding.state, Health: finding.severity.RowHealth(),
		Why:           firstParagraph(finding.explain),
		TargetCount:   len(finding.targets),
		Unavailable:   finding.unavailable,
		NotApplicable: finding.notApplicable,
		Inputs:        len(runnableInputs(findingInputs(f.Deps, finding, nil))),
	}
	for _, t := range finding.targets[:min(len(finding.targets), findingPreviewTargets)] {
		p.Targets = append(p.Targets, FindingPreviewTarget{
			Resource: kubectlResource(t.gvr), Namespace: t.namespace, Name: t.name,
			Value: t.value, Health: t.severity.RowHealth(),
		})
	}
	if finding.unavailable != "" {
		p.Verify = f.verifyCommands(finding)
	}
	return p
}

// runnableInputs are the commands of inputs that run as they are.
func runnableInputs(inputs []findingInput) []string {
	var out []string
	for _, in := range inputs {
		out = append(out, in.commands...)
	}
	return out
}

// verifyCommands are read-only commands that show why finding could not
// be computed: a permission check when the reason is forbidden, then the
// finding's first input command.
func (f *OverviewFolder) verifyCommands(finding overviewFinding) []string {
	var out []string
	resource, reason, ok := strings.Cut(finding.unavailable, ": ")
	if ok && strings.Contains(strings.ToLower(reason), "forbidden") && !strings.ContainsAny(resource, " /") {
		args := []string{"auth", "can-i", "list", resource}
		if _, cluster := findingScope(finding.id, f.namespace); !cluster {
			args = append(args, namespaceArgs(f.namespace, true)...)
		}
		out = append(out, kubectlCommand(f.Deps, args...))
	}
	if cmds := runnableInputs(findingInputs(f.Deps, finding, nil)); len(cmds) > 0 {
		out = append(out, cmds[0])
	}
	return out
}

// firstParagraph is text up to the first blank line, joined into one line.
func firstParagraph(text string) string {
	para, _, _ := strings.Cut(strings.TrimSpace(text), "\n\n")
	return strings.Join(strings.Fields(para), " ")
}

// String names the target for a list: "pods shop/web".
func (t FindingPreviewTarget) String() string {
	if t.Namespace == "" {
		return fmt.Sprintf("%s %s", t.Resource, t.Name)
	}
	return fmt.Sprintf("%s %s/%s", t.Resource, t.Namespace, t.Name)
}
