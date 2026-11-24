package visibility

import (
	"cmp"
	"slices"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Reason says why a type is in the catalog or not.
type Reason int

const (
	// ReasonWork: the type is in the built-in Work set.
	ReasonWork Reason = iota + 1
	// ReasonAdded: a Show selector of the Work profile adds the type.
	ReasonAdded
	// ReasonRemoved: a Hide selector of the Work profile removes it.
	ReasonRemoved
	// ReasonNotInWork: the type is not in the Work set. It is not a
	// claim that the type is internal.
	ReasonNotInWork
	// ReasonAlias: the type is another name of a Work type that is
	// discovered and listed instead (events.k8s.io events next to core
	// events).
	ReasonAlias
	// ReasonAll: mode all lists every type.
	ReasonAll
	// ReasonSelected: a Show selector of an only profile.
	ReasonSelected
	// ReasonNotSelected: an only profile does not select the type.
	ReasonNotSelected
	// ReasonHidden: a Hide selector of a hide profile.
	ReasonHidden
	// ReasonNotHidden: a hide profile does not hide the type.
	ReasonNotHidden
)

// String is the reason as the catalog shows it.
func (r Reason) String() string {
	switch r {
	case ReasonWork:
		return "in Work"
	case ReasonAdded:
		return "added to Work"
	case ReasonRemoved:
		return "removed from Work"
	case ReasonNotInWork:
		return "not in Work"
	case ReasonAlias:
		return "listed as core events"
	case ReasonAll:
		return "all types"
	case ReasonSelected:
		return "selected"
	case ReasonNotSelected:
		return "not selected"
	case ReasonHidden:
		return "hidden"
	case ReasonNotHidden:
		return "not hidden"
	}
	return "unknown"
}

// Decision is the verdict on one discovered type.
type Decision struct {
	// Included types are rows of the ordinary catalog.
	Included bool
	// Explicit types were selected by name (a Show selector of a Work or
	// only profile). The catalog lists them even when they are empty or
	// their count is not known yet; the non-empty filter applies to the
	// others. Explicit grants no access.
	Explicit bool
	Reason   Reason
}

// Set is a profile applied to the discovered types of one catalog.
type Set struct {
	decisions map[schema.GroupResource]Decision
	other     []schema.GroupResource
	missing   []schema.GroupResource
}

// Evaluate applies p to the discovered types: the top-level types of the
// catalog's scope that kc can browse. It reads nothing from the cluster.
// An invalid profile is an error; the caller must not treat it as all
// types.
func Evaluate(p Profile, discovered []schema.GroupResource) (*Set, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	served := make(map[schema.GroupResource]bool, len(discovered))
	for _, gr := range discovered {
		served[gr] = true
	}
	show := selectorSet(p.Show)
	hide := selectorSet(p.Hide)
	s := &Set{decisions: make(map[schema.GroupResource]Decision, len(served))}
	for gr := range served {
		d := decide(p.Mode, gr, served, show, hide)
		s.decisions[gr] = d
		if !d.Included {
			s.other = append(s.other, gr)
		}
	}
	for _, gr := range slices.Concat(p.Show, p.Hide) {
		if !served[gr] && !slices.Contains(s.missing, gr) {
			s.missing = append(s.missing, gr)
		}
	}
	slices.SortFunc(s.other, compareGR)
	slices.SortFunc(s.missing, compareGR)
	return s, nil
}

func decide(mode Mode, gr schema.GroupResource, served, show, hide map[schema.GroupResource]bool) Decision {
	switch mode {
	case ModeAll:
		return Decision{Included: true, Reason: ReasonAll}
	case ModeHide:
		if hide[gr] {
			return Decision{Reason: ReasonHidden}
		}
		return Decision{Included: true, Reason: ReasonNotHidden}
	case ModeOnly:
		if show[gr] {
			return Decision{Included: true, Explicit: true, Reason: ReasonSelected}
		}
		return Decision{Reason: ReasonNotSelected}
	}
	// ModeWork; Validate rejected every other mode.
	if hide[gr] {
		return Decision{Reason: ReasonRemoved}
	}
	if show[gr] {
		return Decision{Included: true, Explicit: true, Reason: ReasonAdded}
	}
	entry, ok := workIndex[gr]
	if !ok {
		return Decision{Reason: ReasonNotInWork}
	}
	if entry.OnlyWithout != (schema.GroupResource{}) && served[entry.OnlyWithout] {
		return Decision{Reason: ReasonAlias}
	}
	return Decision{Included: true, Reason: ReasonWork}
}

// Decide returns the verdict on gr; ok is false for a type that was not
// discovered.
func (s *Set) Decide(gr schema.GroupResource) (d Decision, ok bool) {
	d, ok = s.decisions[gr]
	return d, ok
}

// Other returns the discovered types the profile leaves out of the
// catalog, sorted by group and resource. Its length is the N of "Other
// types (N)": a number of types, not of objects. The caller owns the
// slice.
func (s *Set) Other() []schema.GroupResource { return slices.Clone(s.other) }

// Missing returns the selectors of the profile that match no discovered
// type, sorted. They are kept, not dropped: the type may come back, or
// discovery may be partial. The caller owns the slice.
func (s *Set) Missing() []schema.GroupResource { return slices.Clone(s.missing) }

func selectorSet(list []schema.GroupResource) map[schema.GroupResource]bool {
	set := make(map[schema.GroupResource]bool, len(list))
	for _, gr := range list {
		set[gr] = true
	}
	return set
}

func compareGR(a, b schema.GroupResource) int {
	return cmp.Or(cmp.Compare(a.Group, b.Group), cmp.Compare(a.Resource, b.Resource))
}
