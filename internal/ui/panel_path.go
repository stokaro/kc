package ui

import (
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/models"
)

// PathAnchor says where a PanelPath starts.
type PathAnchor int

const (
	// AnchorCurrent starts at the panel's current folder. When that folder
	// does not list resource types and the first element names a type
	// ("pods", "deploy"), the walk starts at the root instead, so `cd svc`
	// works from anywhere.
	AnchorCurrent PathAnchor = iota
	// AnchorCurrentStrict starts at the current folder with no root
	// shortcut ("./name").
	AnchorCurrentStrict
	// AnchorRoot starts at the root of the panel's namespace scope ("/").
	AnchorRoot
	// AnchorHome starts at the root of the namespace kc started in ("~"),
	// unless the path rescopes the panel itself.
	AnchorHome
)

// PathElem is one step of a PanelPath. Exactly one field is set. ID and
// Resource match exactly; Name is what a user typed and is resolved
// against the folder at walk time.
type PathElem struct {
	// ID is a row ID, as stored by PanelLocation.
	ID string
	// Resource is a resource type; it matches the type row in a folder
	// that lists types, hidden rows included.
	Resource schema.GroupResource
	// Name is typed text: "..", a resource type name in any form the
	// RESTMapper knows (plural, singular, short name, "deployments.apps"),
	// a row ID, or the text of a row's first column.
	Name string
}

func (e PathElem) String() string {
	switch {
	case e.ID != "":
		return e.ID
	case e.Resource.Resource != "":
		return e.Resource.String()
	}
	return e.Name
}

func (e PathElem) isParent() bool { return e.ID == "" && e.Resource.Resource == "" && e.Name == ".." }

// PanelPath is a location in a panel's folder tree. NavigatePanel walks it;
// cd, the startup intents and saved locations all use it.
type PanelPath struct {
	Anchor PathAnchor
	// Rescope switches the panel to Namespace before the walk; then the
	// walk starts at the root. Namespace is models.AllNamespaces for all.
	Rescope   bool
	Namespace string
	Elems     []PathElem
	// SelectLast puts the cursor on the last element instead of entering
	// it. Without it the last element is entered when it can be, and
	// selected otherwise.
	SelectLast bool
}

// String renders the path the way cd accepts it.
func (p PanelPath) String() string {
	var b strings.Builder
	if p.Rescope {
		if p.Namespace == models.AllNamespaces {
			b.WriteString("-A ")
		} else {
			b.WriteString("-n " + p.Namespace + " ")
		}
	}
	parts := make([]string, 0, len(p.Elems))
	for _, e := range p.Elems {
		parts = append(parts, e.String())
	}
	joined := strings.Join(parts, "/")
	switch p.Anchor {
	case AnchorRoot:
		b.WriteString("/" + joined)
	case AnchorHome:
		b.WriteString("~")
		if joined != "" {
			b.WriteString("/" + joined)
		}
	case AnchorCurrentStrict:
		b.WriteString("./" + joined)
	default:
		if joined == "" {
			joined = "."
		}
		b.WriteString(joined)
	}
	return b.String()
}

// ParsePanelPath parses a cd path: "/a/b" (from the root), "~" or "~/a"
// (from the home root), "./a" (strictly relative), "a/b" (relative, with
// the type shortcut), "." and empty segments are skipped, ".." goes up.
// "-" is not a path; cd handles it.
func ParsePanelPath(s string) (PanelPath, error) {
	s = strings.TrimSpace(s)
	var p PanelPath
	switch {
	case s == "~" || strings.HasPrefix(s, "~/"):
		p.Anchor = AnchorHome
		s = strings.TrimPrefix(s, "~")
	case strings.HasPrefix(s, "~"):
		return PanelPath{}, fmt.Errorf("%q: only ~ and ~/ are supported", s)
	case strings.HasPrefix(s, "/"):
		p.Anchor = AnchorRoot
	case s == "." || strings.HasPrefix(s, "./"):
		p.Anchor = AnchorCurrentStrict
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" || seg == "." {
			continue
		}
		p.Elems = append(p.Elems, PathElem{Name: seg})
	}
	return p, nil
}

// cdRequest is a parsed cd command line.
type cdRequest struct {
	previous bool // cd -
	path     PanelPath
}

// errNotCd marks a line that cd does not handle; it goes to the shell.
var errNotCd = errors.New("not a cd command")

// shellSyntax holds characters that make a line more than a plain cd; such
// lines go to the shell unchanged.
const shellSyntax = ";&|<>$`(){}\\'\"*?[]!#="

// parseCdLine parses a shell line. It returns errNotCd when the line is not
// a plain cd that kc takes over; any other error is a cd usage error to
// show to the user.
func parseCdLine(line string) (cdRequest, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 || fields[0] != "cd" {
		return cdRequest{}, errNotCd
	}
	args := fields[1:]
	for _, a := range args {
		if strings.ContainsAny(a, shellSyntax) && !strings.HasPrefix(a, "--namespace=") {
			return cdRequest{}, errNotCd
		}
	}
	var req cdRequest
	var target []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-n" || a == "--namespace":
			if i+1 >= len(args) {
				return cdRequest{}, fmt.Errorf("%s needs a namespace", a)
			}
			i++
			req.path.Rescope, req.path.Namespace = true, args[i]
		case strings.HasPrefix(a, "--namespace="):
			ns := strings.TrimPrefix(a, "--namespace=")
			if ns == "" || strings.ContainsAny(ns, shellSyntax) {
				return cdRequest{}, errNotCd
			}
			req.path.Rescope, req.path.Namespace = true, ns
		case a == "-A" || a == "--all-namespaces":
			req.path.Rescope, req.path.Namespace = true, models.AllNamespaces
		case a == "-":
			req.previous = true
		case strings.HasPrefix(a, "-") && len(a) > 1:
			return cdRequest{}, fmt.Errorf("unknown option %s", a)
		default:
			target = append(target, a)
		}
	}
	if len(target) > 1 {
		return cdRequest{}, fmt.Errorf("too many arguments: %s", strings.Join(target, " "))
	}
	if req.previous {
		if len(target) > 0 || req.path.Rescope {
			return cdRequest{}, errors.New("- takes no other arguments")
		}
		return req, nil
	}
	switch {
	case len(target) == 1:
		p, err := ParsePanelPath(target[0])
		if err != nil {
			return cdRequest{}, err
		}
		p.Rescope, p.Namespace = req.path.Rescope, req.path.Namespace
		req.path = p
	case req.path.Rescope:
		// cd -n ns: the root of that namespace.
		req.path.Anchor = AnchorRoot
	default:
		// A bare cd goes home, as in a shell.
		req.path.Anchor = AnchorHome
	}
	return req, nil
}
