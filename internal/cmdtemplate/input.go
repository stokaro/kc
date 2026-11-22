// Package cmdtemplate handles the prompt answers of custom commands.
//
// A command template refers to an answer as {{.Input.<name>}}. The answers
// are known once, when the user closes the prompt dialog, while the rest of
// the template (.Name, .Namespace, ...) is rendered again on every restart
// of the command. Bind therefore substitutes the answers into the template
// text and leaves every other action in place, so the result is still a
// template for the usual renderer.
package cmdtemplate

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"
)

// InputField is the template field that holds the prompt answers.
const InputField = "Input"

// InputRefs returns the prompt names tmpl refers to as .Input.<name>, in
// order of first use. It fails when the template does not parse or uses
// .Input in another shape (.Input alone, .Input.a.b).
func InputRefs(tmpl string) ([]string, error) {
	t, err := parseTemplate(tmpl)
	if err != nil {
		return nil, err
	}
	var names []string
	err = walkTemplates(t, func(n parse.Node) error {
		name, ok, err := inputName(n)
		if err != nil || !ok {
			return err
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
		return nil
	}, nil)
	return names, err
}

// Bind replaces the references to .Input.<name> in tmpl with values. An
// action that uses nothing but answers and literals ({{.Input.replicas}},
// {{.Input.image | printf "%q"}}) is evaluated now and becomes plain text.
// In an action that also uses other fields the reference becomes a quoted
// string literal. A template without .Input comes back unchanged.
func Bind(tmpl string, values map[string]string) (string, error) {
	refs, err := InputRefs(tmpl)
	if err != nil {
		return "", err
	}
	if len(refs) == 0 {
		return tmpl, nil
	}
	for _, name := range refs {
		if _, ok := values[name]; !ok {
			return "", fmt.Errorf("no answer for .%s.%s", InputField, name)
		}
	}
	t, err := parseTemplate(tmpl)
	if err != nil {
		return "", err
	}
	var evalErr error
	replaceAction := func(a *parse.ActionNode) (parse.Node, bool) {
		if !onlyInput(a.Pipe) {
			return nil, false
		}
		out, err := evalPipe(a.Pipe, values)
		if err != nil {
			evalErr = errors.Join(evalErr, err)
			return nil, false
		}
		if strings.Contains(out, "{{") {
			// As plain text it would be parsed again as template code.
			return stringAction(a, out), true
		}
		return &parse.TextNode{NodeType: parse.NodeText, Pos: a.Pos, Text: []byte(out)}, true
	}
	noop := func(parse.Node) error { return nil }
	if err := walkTemplates(t, noop, replaceAction); err != nil {
		return "", err
	}
	if evalErr != nil {
		return "", evalErr
	}
	// References in actions that also read other data become literals.
	replaceFields(t, values)
	return render(t), nil
}

func parseTemplate(tmpl string) (*template.Template, error) {
	t, err := template.New("command").Parse(tmpl)
	if err != nil {
		return nil, fmt.Errorf("parsing command template: %w", err)
	}
	return t, nil
}

// inputName reports whether n is a reference to .Input.<name> (or
// $.Input.<name>) and returns the name.
func inputName(n parse.Node) (string, bool, error) {
	var ident []string
	switch n := n.(type) {
	case *parse.FieldNode:
		ident = n.Ident
	case *parse.VariableNode:
		if len(n.Ident) == 0 || n.Ident[0] != "$" {
			return "", false, nil
		}
		ident = n.Ident[1:]
	default:
		return "", false, nil
	}
	if len(ident) == 0 || ident[0] != InputField {
		return "", false, nil
	}
	if len(ident) != 2 {
		return "", false, fmt.Errorf("use .%s.<name> to refer to a prompt, not .%s", InputField, strings.Join(ident, "."))
	}
	return ident[1], true, nil
}

// walkTemplates calls visit for every argument node of every pipeline in
// t and its associated templates. When action is not nil it may replace an
// action node of a list.
func walkTemplates(t *template.Template, visit func(parse.Node) error, action func(*parse.ActionNode) (parse.Node, bool)) error {
	for _, tt := range t.Templates() {
		if tt.Tree == nil || tt.Tree.Root == nil {
			continue
		}
		if err := walkList(tt.Tree.Root, visit, action); err != nil {
			return err
		}
	}
	return nil
}

func walkList(l *parse.ListNode, visit func(parse.Node) error, action func(*parse.ActionNode) (parse.Node, bool)) error {
	if l == nil {
		return nil
	}
	for i, n := range l.Nodes {
		if a, ok := n.(*parse.ActionNode); ok && action != nil {
			if repl, ok := action(a); ok {
				l.Nodes[i] = repl
				continue
			}
		}
		if err := walkNode(n, visit, action); err != nil {
			return err
		}
	}
	return nil
}

func walkNode(n parse.Node, visit func(parse.Node) error, action func(*parse.ActionNode) (parse.Node, bool)) error {
	switch n := n.(type) {
	case *parse.ActionNode:
		return walkPipe(n.Pipe, visit)
	case *parse.IfNode:
		return walkBranch(&n.BranchNode, visit, action)
	case *parse.RangeNode:
		return walkBranch(&n.BranchNode, visit, action)
	case *parse.WithNode:
		return walkBranch(&n.BranchNode, visit, action)
	case *parse.TemplateNode:
		return walkPipe(n.Pipe, visit)
	case *parse.ListNode:
		return walkList(n, visit, action)
	}
	return nil
}

func walkBranch(b *parse.BranchNode, visit func(parse.Node) error, action func(*parse.ActionNode) (parse.Node, bool)) error {
	if err := walkPipe(b.Pipe, visit); err != nil {
		return err
	}
	if err := walkList(b.List, visit, action); err != nil {
		return err
	}
	return walkList(b.ElseList, visit, action)
}

func walkPipe(p *parse.PipeNode, visit func(parse.Node) error) error {
	if p == nil {
		return nil
	}
	for _, c := range p.Cmds {
		for _, arg := range c.Args {
			if err := visit(arg); err != nil {
				return err
			}
			switch arg := arg.(type) {
			case *parse.PipeNode:
				if err := walkPipe(arg, visit); err != nil {
					return err
				}
			case *parse.ChainNode:
				if err := visit(arg.Node); err != nil {
					return err
				}
				if p, ok := arg.Node.(*parse.PipeNode); ok {
					if err := walkPipe(p, visit); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// onlyInput reports whether p reads no data but prompt answers: no other
// field, no variable, no dot, and no declaration.
func onlyInput(p *parse.PipeNode) bool {
	if p == nil || len(p.Decl) > 0 || p.IsAssign {
		return false
	}
	usesInput := false
	ok := true
	var check func(*parse.PipeNode)
	check = func(p *parse.PipeNode) {
		for _, c := range p.Cmds {
			for _, arg := range c.Args {
				switch arg := arg.(type) {
				case *parse.FieldNode, *parse.VariableNode:
					if _, isInput, _ := inputName(arg); isInput {
						usesInput = true
					} else {
						ok = false
					}
				case *parse.DotNode, *parse.ChainNode:
					ok = false
				case *parse.PipeNode:
					if len(arg.Decl) > 0 {
						ok = false
					}
					check(arg)
				}
			}
		}
	}
	check(p)
	return ok && usesInput
}

func evalPipe(p *parse.PipeNode, values map[string]string) (string, error) {
	t, err := template.New("input").Option("missingkey=error").Parse("{{" + p.String() + "}}")
	if err != nil {
		return "", fmt.Errorf("parsing %q: %w", p.String(), err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, map[string]any{InputField: values}); err != nil {
		return "", fmt.Errorf("evaluating %q: %w", p.String(), err)
	}
	return buf.String(), nil
}

// stringAction turns a into {{"out"}}: an action that prints out
// verbatim. It reuses a, since an action node prints through its tree.
func stringAction(a *parse.ActionNode, out string) *parse.ActionNode {
	str := &parse.StringNode{NodeType: parse.NodeString, Pos: a.Pos, Quoted: strconv.Quote(out), Text: out}
	a.Pipe.Cmds = []*parse.CommandNode{{NodeType: parse.NodeCommand, Pos: a.Pos, Args: []parse.Node{str}}}
	return a
}

// replaceFields turns the remaining .Input.<name> arguments into string
// literals.
func replaceFields(t *template.Template, values map[string]string) {
	var fixPipe func(*parse.PipeNode)
	fixArgs := func(args []parse.Node) {
		for i, arg := range args {
			if name, ok, _ := inputName(arg); ok {
				v := values[name]
				args[i] = &parse.StringNode{NodeType: parse.NodeString, Pos: arg.Position(), Quoted: strconv.Quote(v), Text: v}
				continue
			}
			switch arg := arg.(type) {
			case *parse.PipeNode:
				fixPipe(arg)
			case *parse.ChainNode:
				if p, ok := arg.Node.(*parse.PipeNode); ok {
					fixPipe(p)
				}
			}
		}
	}
	fixPipe = func(p *parse.PipeNode) {
		if p == nil {
			return
		}
		for _, c := range p.Cmds {
			fixArgs(c.Args)
		}
	}
	var fixList func(*parse.ListNode)
	fixList = func(l *parse.ListNode) {
		if l == nil {
			return
		}
		for _, n := range l.Nodes {
			switch n := n.(type) {
			case *parse.ActionNode:
				fixPipe(n.Pipe)
			case *parse.IfNode:
				fixPipe(n.Pipe)
				fixList(n.List)
				fixList(n.ElseList)
			case *parse.RangeNode:
				fixPipe(n.Pipe)
				fixList(n.List)
				fixList(n.ElseList)
			case *parse.WithNode:
				fixPipe(n.Pipe)
				fixList(n.List)
				fixList(n.ElseList)
			case *parse.TemplateNode:
				fixPipe(n.Pipe)
			}
		}
	}
	for _, tt := range t.Templates() {
		if tt.Tree != nil {
			fixList(tt.Tree.Root)
		}
	}
}

// render writes t back as template text: the main body first, then the
// associated templates as {{define}} blocks.
func render(t *template.Template) string {
	var b strings.Builder
	if t.Tree != nil && t.Tree.Root != nil {
		b.WriteString(t.Tree.Root.String())
	}
	others := t.Templates()
	slices.SortFunc(others, func(x, y *template.Template) int { return strings.Compare(x.Name(), y.Name()) })
	for _, tt := range others {
		if tt.Name() == t.Name() || tt.Tree == nil || tt.Tree.Root == nil {
			continue
		}
		fmt.Fprintf(&b, "{{define %q}}%s{{end}}", tt.Name(), tt.Tree.Root.String())
	}
	return b.String()
}
