package setdiff

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// Scope says which objects of a side take part in a comparison.
type Scope int

// The scopes. The zero value compares the whole list.
const (
	// Whole compares every object the list read returned.
	Whole Scope = iota
	// Visible compares the rows the panel showed under its row filter.
	Visible
	// Marked compares the rows marked in the panel.
	Marked
	// Counterparts compares the objects whose key the other side
	// selected (Visible or Marked). A side without its own selection,
	// such as another context or an unfiltered panel facing a filtered
	// one, uses it.
	Counterparts
)

func (s Scope) String() string {
	switch s {
	case Visible:
		return "visible"
	case Marked:
		return "marked"
	case Counterparts:
		return "counterparts"
	default:
		return "whole list"
	}
}

// selects reports whether the scope names its objects through Members.
func (s Scope) selects() bool { return s == Visible || s == Marked }

// Member is one row a selection froze when the comparison started. UID is
// the instance the row showed; it is empty when the listing carried no
// metadata, and the object is then matched by name alone.
type Member struct {
	Namespace string
	Name      string
	UID       types.UID
}

// Selection is what a panel showed when the comparison started.
type Selection struct {
	Scope Scope
	// Members are the rows of a Visible or Marked scope.
	Members []Member
	// Filter is the label of the panel's row filter, empty without one.
	Filter string
}

// SideCount is what one side evaluated: the objects the read returned, and
// how many of them took part.
type SideCount struct {
	Scope    Scope
	Filter   string
	Listed   int
	Compared int
	// Lost counts selected rows whose object was deleted or replaced
	// before the read.
	Lost int
}

// String is "marked 2 of 10", "visible 2 of 10 (filter: web)",
// "counterparts 2 of 12" or "whole list 10".
func (c SideCount) String() string {
	var b strings.Builder
	if c.Scope == Whole {
		fmt.Fprintf(&b, "whole list %d", c.Listed)
	} else {
		fmt.Fprintf(&b, "%s %d of %d", c.Scope, c.Compared, c.Listed)
	}
	if c.Filter != "" {
		fmt.Fprintf(&b, " (filter: %s)", c.Filter)
	}
	if c.Lost > 0 {
		fmt.Fprintf(&b, ", %d gone since selected", c.Lost)
	}
	return b.String()
}

// objectKey is the key a row is matched by.
func objectKey(allNamespaces bool, namespace, name string) string {
	if allNamespaces {
		return namespace + "/" + name
	}
	return name
}

// restrict keeps the objects of s that its selection names. A member whose
// object is gone, or is another instance (a different UID) than the one the
// panel showed, is lost: lost maps its key to the reason. A lost key is
// unavailable on both sides, never "only" on the other side.
func restrict(s Set) (kept []unstructured.Unstructured, keys map[string]struct{}, lost map[string]string) {
	keys = make(map[string]struct{}, len(s.Selection.Members))
	lost = make(map[string]string)
	want := make(map[string]Member, len(s.Selection.Members))
	for _, m := range s.Selection.Members {
		k := objectKey(s.AllNamespaces, m.Namespace, m.Name)
		want[k] = m
		keys[k] = struct{}{}
	}
	found := make(map[string]bool, len(want))
	for _, o := range s.Objects {
		k := objectKey(s.AllNamespaces, o.GetNamespace(), o.GetName())
		m, ok := want[k]
		if !ok {
			continue
		}
		if m.UID != "" && o.GetUID() != m.UID {
			lost[k] = fmt.Sprintf("%s was replaced after it was selected in %s (uid %s, now %s)", k, s.Label(), m.UID, o.GetUID())
			continue
		}
		found[k] = true
		kept = append(kept, o)
	}
	for k := range want {
		if !found[k] {
			if _, ok := lost[k]; !ok {
				lost[k] = k + " was deleted after it was selected in " + s.Label()
			}
		}
	}
	return kept, keys, lost
}

// counterparts keeps the objects of s whose key is in keys.
func counterparts(s Set, keys map[string]struct{}) []unstructured.Unstructured {
	var kept []unstructured.Unstructured
	for _, o := range s.Objects {
		if _, ok := keys[objectKey(s.AllNamespaces, o.GetNamespace(), o.GetName())]; ok {
			kept = append(kept, o)
		}
	}
	return kept
}

// applyScopes narrows both sides to their selections before they are
// matched. A Counterparts side follows the keys the other side selected;
// with no selection on the other side it compares its whole list.
func applyScopes(left, right *Set) (lost map[string]string, lc, rc SideCount) {
	lost = make(map[string]string)
	lc = SideCount{Scope: left.Selection.Scope, Filter: left.Selection.Filter, Listed: len(left.Objects)}
	rc = SideCount{Scope: right.Selection.Scope, Filter: right.Selection.Filter, Listed: len(right.Objects)}
	var lKeys, rKeys map[string]struct{}
	for _, side := range []struct {
		set  *Set
		keys *map[string]struct{}
		cnt  *SideCount
	}{{left, &lKeys, &lc}, {right, &rKeys, &rc}} {
		if !side.set.Selection.Scope.selects() {
			continue
		}
		kept, keys, gone := restrict(*side.set)
		side.set.Objects, *side.keys = kept, keys
		side.cnt.Lost = len(gone)
		for k, why := range gone {
			if prev, ok := lost[k]; ok {
				why = prev + "; " + why
			}
			lost[k] = why
		}
	}
	follow := func(set *Set, keys map[string]struct{}, cnt *SideCount) {
		if set.Selection.Scope != Counterparts {
			return
		}
		if keys == nil {
			cnt.Scope = Whole
			return
		}
		set.Objects = counterparts(*set, keys)
	}
	follow(left, rKeys, &lc)
	follow(right, lKeys, &rc)
	lc.Compared, rc.Compared = len(left.Objects), len(right.Objects)
	return lost, lc, rc
}
