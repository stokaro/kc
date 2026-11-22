package models

import (
	"strings"

	"github.com/stokaro/kc/internal/nodeshell"
	table "github.com/stokaro/kc/internal/table"
)

// nodeShellEnabled reports whether the node shell is turned on for the
// context of deps.
func nodeShellEnabled(deps Deps) bool {
	return deps.AppConfig != nil && deps.AppConfig.NodeShell.EnabledFor(deps.CtxName)
}

// nodeShellRows offers a node shell inside a node, when it is enabled.
func (f *NodeChildFolder) nodeShellRows() []table.Row {
	if !nodeShellEnabled(f.Deps) {
		return nil
	}
	item := &NodeShellItem{
		SimpleItem: NewSimpleItem(f.nodeName+"/node-shell", []string{"Start a node shell..."}, f.Path(), WhiteStyle()),
		deps:       f.Deps,
		node:       f.nodeName,
	}
	item.RowItem.details = "Enter starts a privileged pod on the node and opens a root shell in the host namespaces"
	return []table.Row{item}
}

// NodeShellItem asks for consent to start a node shell. The pod is
// privileged and shares the host namespaces, so the dialog says so.
type NodeShellItem struct {
	*SimpleItem
	deps Deps
	node string
	pod  string // chosen when the dialog opens, so it names what is created
}

var _ ConfirmedAction = (*NodeShellItem)(nil)
var _ ConfirmFollowUp = (*NodeShellItem)(nil)

func (i *NodeShellItem) options() nodeshell.Options {
	return nodeshell.OptionsFrom(i.deps.AppConfig.NodeShell)
}

// Confirmation lists what will be created and where.
func (i *NodeShellItem) Confirmation() Confirmation {
	opts := i.options()
	i.pod = nodeshell.PodName(i.node)
	notes := []string{
		"Creates a PRIVILEGED pod pinned to the node with",
		"the host PID, network and IPC namespaces, and",
		"tolerating every taint. The shell runs as root",
		"in the node's namespaces (nsenter -t 1).",
		"kc deletes the pod when the shell ends or kc",
		"quits. If kc dies, the pod stops a minute",
		"later, and after " + opts.MaxDuration.String() + " in any case.",
		"",
		"Similar to:",
	}
	cmd := kubectlCommand(i.deps, nodeshell.KubectlDebugArgs(i.node, opts)...)
	notes = append(notes, wrapWords(cmd, 50)...)
	return Confirmation{
		Title: "Start a privileged node shell?",
		Details: [][2]string{
			{"Context", i.deps.CtxName},
			{"Node", i.node},
			{"Namespace", opts.Namespace},
			{"Pod", i.pod},
			{"Image", opts.Image},
		},
		Notes:  notes,
		Action: "Start",
	}
}

// Confirm does nothing here; the UI starts the shell from FollowUp.
func (i *NodeShellItem) Confirm() error { return nil }

// FollowUp asks the UI to start the shell the dialog described.
func (i *NodeShellItem) FollowUp() any {
	if i.pod == "" {
		i.pod = nodeshell.PodName(i.node)
	}
	return NodeShellRequest{Context: i.deps.CtxName, Node: i.node, Namespace: i.options().Namespace, Pod: i.pod}
}

// wrapWords breaks s into lines of at most width characters at spaces,
// indented by two spaces after the first line. A word longer than width
// gets a line of its own.
func wrapWords(s string, width int) []string {
	var lines []string
	line := ""
	for _, w := range strings.Fields(s) {
		switch {
		case line == "":
			line = w
		case len(line)+1+len(w) > width:
			lines = append(lines, line)
			line = "  " + w
		default:
			line += " " + w
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}
