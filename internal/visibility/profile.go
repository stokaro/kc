// Package visibility decides which resource types a type catalog (the panel
// root, the resources of a namespace, the cluster resources) lists. It is a
// pure policy over the discovered types: it reads no objects, counts
// nothing and grants no access. A type it leaves out of the catalog stays
// reachable by explicit navigation (cd, the hotlist, history, links).
//
// A selector is an exact API group and plural resource name
// (schema.GroupResource, group "" for core). It matches whatever version
// discovery prefers, so it is a policy matcher only; rows, folders and
// saved locations keep their full group, version and resource.
package visibility

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Mode is how a profile picks the types of the catalog.
type Mode string

const (
	// ModeWork lists the built-in Work set, plus the profile's Show
	// selectors, minus its Hide selectors. It is the default.
	ModeWork Mode = "work"
	// ModeAll lists every discovered type.
	ModeAll Mode = "all"
	// ModeHide lists every discovered type except the Hide selectors.
	ModeHide Mode = "hide"
	// ModeOnly lists the Show selectors and nothing else. An empty list
	// lists no types; it never means all.
	ModeOnly Mode = "only"
)

// Modes lists the modes in the order a picker offers them.
var Modes = []Mode{ModeWork, ModeAll, ModeHide, ModeOnly}

// ParseMode returns the mode named s.
func ParseMode(s string) (Mode, error) {
	m := Mode(s)
	if slices.Contains(Modes, m) {
		return m, nil
	}
	return "", fmt.Errorf("unknown mode %q (want work, all, hide or only)", s)
}

// Profile is one way to pick the types of the catalog.
//
// Show and Hide mean, per mode:
//   - work: Show adds types to the Work set, Hide removes them. A selector
//     may not be in both.
//   - hide: Hide lists the types to leave out; Show must be empty.
//   - only: Show lists the types to list; Hide must be empty.
//   - all: both must be empty.
//
// A profile never inherits the selections of another profile or of Work.
type Profile struct {
	Mode Mode
	Show []schema.GroupResource
	Hide []schema.GroupResource
}

// Work is the default profile: the built-in Work set without changes.
func Work() Profile { return Profile{Mode: ModeWork} }

// Clone returns a copy that shares no slices with p.
func (p Profile) Clone() Profile {
	return Profile{Mode: p.Mode, Show: slices.Clone(p.Show), Hide: slices.Clone(p.Hide)}
}

// Equal reports whether p and q pick types the same way: the same mode and
// the same Show and Hide selectors, in any order.
func (p Profile) Equal(q Profile) bool {
	return p.Mode == q.Mode && sameSelectors(p.Show, q.Show) && sameSelectors(p.Hide, q.Hide)
}

func sameSelectors(a, b []schema.GroupResource) bool {
	x, y := selectorSet(a), selectorSet(b)
	if len(x) != len(y) {
		return false
	}
	for gr := range x {
		if !y[gr] {
			return false
		}
	}
	return true
}

// Validate reports every problem of p: an unknown mode, an invalid
// selector, a list the mode does not use, and a selector both shown and
// hidden.
func (p Profile) Validate() error {
	var errs []error
	if _, err := ParseMode(string(p.Mode)); err != nil {
		errs = append(errs, err)
	}
	for _, list := range []struct {
		name string
		sel  []schema.GroupResource
	}{{"show", p.Show}, {"hide", p.Hide}} {
		for _, gr := range list.sel {
			if err := ValidateSelector(gr); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", list.name, err))
			}
		}
	}
	switch p.Mode {
	case ModeAll:
		if len(p.Show) > 0 || len(p.Hide) > 0 {
			errs = append(errs, errors.New("mode all takes no show or hide list"))
		}
	case ModeHide:
		if len(p.Show) > 0 {
			errs = append(errs, errors.New("mode hide takes a hide list only"))
		}
	case ModeOnly:
		if len(p.Hide) > 0 {
			errs = append(errs, errors.New("mode only takes a show list only"))
		}
	case ModeWork:
		for _, gr := range p.Show {
			if slices.Contains(p.Hide, gr) {
				errs = append(errs, fmt.Errorf("%s is both shown and hidden", Format(gr)))
			}
		}
	}
	return errors.Join(errs...)
}

// ValidateSelector checks that gr names a type exactly: a plural resource
// name and an API group, without wildcards or a version.
func ValidateSelector(gr schema.GroupResource) error {
	if gr.Resource == "" {
		return fmt.Errorf("selector %s has no resource", Format(gr))
	}
	if msgs := validation.IsDNS1123Label(gr.Resource); len(msgs) > 0 {
		return fmt.Errorf("selector %s: resource %q is not a plural resource name: %s", Format(gr), gr.Resource, strings.Join(msgs, "; "))
	}
	if gr.Group != "" {
		if msgs := validation.IsDNS1123Subdomain(gr.Group); len(msgs) > 0 {
			return fmt.Errorf("selector %s: group %q is not an API group: %s", Format(gr), gr.Group, strings.Join(msgs, "; "))
		}
	}
	return nil
}

// Format writes a selector the way kubectl names a type: pods,
// deployments.apps.
func Format(gr schema.GroupResource) string {
	if gr.Group == "" {
		return gr.Resource
	}
	return gr.Resource + "." + gr.Group
}
