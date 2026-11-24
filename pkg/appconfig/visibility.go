package appconfig

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/stokaro/kc/internal/visibility"
)

// Built-in resource type profiles. VisibilityProfileWork lists the types of
// kc's Work list; VisibilityProfileAll lists every discovered type.
const (
	VisibilityProfileWork = "work"
	VisibilityProfileAll  = "all"
)

// ResourcesVisibilityConfig picks which resource types the root, the
// resources of a namespace and the cluster resources list. Types left out
// stay reachable: cd, the hotlist, history, links and "Other types".
//
// A panel lists its types under, in this order: its own choice for the
// session (Override), the profile bound to its kube context (Contexts),
// DefaultProfile, and Work.
type ResourcesVisibilityConfig struct {
	// DefaultProfile names the profile of every context without a
	// binding: "work" (the default), "all", or a key of Profiles.
	DefaultProfile string `json:"defaultProfile"`
	// Profiles are the named profiles. The key "work" holds the Show and
	// Hide changes of the Work profile; "all" is built in and cannot be
	// defined.
	Profiles map[string]VisibilityProfileConfig `json:"profiles,omitempty"`
	// Contexts binds exact kube context names to profile names.
	Contexts map[string]string `json:"contexts,omitempty"`
	// Override is a panel's own choice for the session (Apply in the
	// visibility editor, Types in the panel options). Only a panel's copy
	// of the configuration has one. It is never saved and ends when the
	// context changes.
	Override *VisibilityOverride `json:"-"`
}

// VisibilityProfileConfig is one named profile.
//
// Show and Hide mean, per mode: work adds Show to the Work set and removes
// Hide from it; hide leaves out Hide; only lists Show and nothing else;
// all lists everything and takes neither list.
type VisibilityProfileConfig struct {
	// Mode is "work", "all", "hide" or "only".
	Mode string `json:"mode"`
	// Show and Hide are written even when empty: the store merges a saved
	// file key by key, and a list left out would keep its old value.
	Show []TypeSelector `json:"show"`
	Hide []TypeSelector `json:"hide"`
}

// TypeSelector names one resource type exactly: its API group ("" for
// core) and plural resource name. It matches whatever version the cluster
// serves.
type TypeSelector struct {
	Group    string `json:"group"`
	Resource string `json:"resource"`
}

// GroupResource is the selector as the visibility policy reads it.
func (s TypeSelector) GroupResource() schema.GroupResource {
	return schema.GroupResource{Group: s.Group, Resource: s.Resource}
}

// VisibilityOverride is a panel's own profile for the session.
type VisibilityOverride struct {
	// Name is the profile the choice started from.
	Name string
	// Edited is set when Profile differs from the profile saved as Name.
	Edited  bool
	Profile visibility.Profile
}

// VisibilitySource says which setting picked a panel's profile.
type VisibilitySource int

const (
	// VisibilityFromBuiltIn: nothing is configured; the panel lists Work.
	VisibilityFromBuiltIn VisibilitySource = iota
	// VisibilityFromDefault: resources.visibility.defaultProfile.
	VisibilityFromDefault
	// VisibilityFromContext: the profile bound to the panel's context.
	VisibilityFromContext
	// VisibilityFromPanel: the panel's own choice for the session.
	VisibilityFromPanel
)

// String is how the editor names the source.
func (s VisibilitySource) String() string {
	switch s {
	case VisibilityFromDefault:
		return "global default"
	case VisibilityFromContext:
		return "context"
	case VisibilityFromPanel:
		return "this panel"
	}
	return "built in"
}

// EffectiveVisibility is the profile a panel lists its types under.
type EffectiveVisibility struct {
	// Name is the profile name: "work", "all" or a key of Profiles.
	Name string
	// Edited is set for a panel's unsaved changes to Name.
	Edited bool
	Source VisibilitySource
	// Profile is the policy. When Err is set it is Work.
	Profile visibility.Profile
	// Err says why the configured profile could not be used: it is not
	// defined or it is invalid. A broken profile lists Work, never all.
	Err error
}

// Title is how the profile reads on screen: "Work", "All" or the name,
// with "*" for unsaved changes.
func (e EffectiveVisibility) Title() string {
	title := e.Name
	switch {
	case e.Err != nil, e.Name == VisibilityProfileWork, e.Name == "":
		title = "Work"
	case e.Name == VisibilityProfileAll:
		title = "All"
	}
	if e.Edited && e.Err == nil {
		title += "*"
	}
	return title
}

// Same reports whether e and o list types the same way under the same
// name, so rows built under one are still right under the other.
func (e EffectiveVisibility) Same(o EffectiveVisibility) bool {
	return e.Name == o.Name && e.Edited == o.Edited && (e.Err == nil) == (o.Err == nil) && e.Profile.Equal(o.Profile)
}

// Clone returns a copy that shares no map, slice or override with v.
func (v ResourcesVisibilityConfig) Clone() ResourcesVisibilityConfig {
	out := ResourcesVisibilityConfig{DefaultProfile: v.DefaultProfile, Contexts: maps.Clone(v.Contexts)}
	if v.Profiles != nil {
		out.Profiles = make(map[string]VisibilityProfileConfig, len(v.Profiles))
		for name, p := range v.Profiles {
			out.Profiles[name] = VisibilityProfileConfig{Mode: p.Mode, Show: slices.Clone(p.Show), Hide: slices.Clone(p.Hide)}
		}
	}
	if v.Override != nil {
		o := *v.Override
		o.Profile = o.Profile.Clone()
		out.Override = &o
	}
	return out
}

// ProfileNames returns the profiles a panel can pick: work, all, then the
// configured ones by name.
func (v ResourcesVisibilityConfig) ProfileNames() []string {
	names := []string{VisibilityProfileWork, VisibilityProfileAll}
	for _, name := range slices.Sorted(maps.Keys(v.Profiles)) {
		if name != VisibilityProfileWork && name != VisibilityProfileAll {
			names = append(names, name)
		}
	}
	return names
}

// Profile returns the policy of the profile named name.
func (v ResourcesVisibilityConfig) Profile(name string) (visibility.Profile, error) {
	if name == VisibilityProfileAll {
		return visibility.Profile{Mode: visibility.ModeAll}, nil
	}
	cfg, ok := v.Profiles[name]
	if !ok {
		if name == VisibilityProfileWork {
			return visibility.Work(), nil
		}
		return visibility.Profile{}, fmt.Errorf("profile %q is not defined", name)
	}
	p, err := cfg.Policy()
	if err != nil {
		return visibility.Profile{}, fmt.Errorf("profile %s: %w", name, err)
	}
	if name == VisibilityProfileWork && p.Mode != visibility.ModeWork {
		return visibility.Profile{}, fmt.Errorf("profile work: mode must be work, not %s", p.Mode)
	}
	return p, nil
}

// Resolve returns the profile a panel in context lists its types under:
// the panel's own choice, the context's binding, the global default, or
// Work. A missing or invalid profile resolves to Work with Err set.
func (v ResourcesVisibilityConfig) Resolve(context string) EffectiveVisibility {
	if o := v.Override; o != nil {
		e := EffectiveVisibility{Name: o.Name, Edited: o.Edited, Source: VisibilityFromPanel, Profile: o.Profile.Clone()}
		if err := e.Profile.Validate(); err != nil {
			return EffectiveVisibility{Name: o.Name, Source: VisibilityFromPanel, Profile: visibility.Work(), Err: err}
		}
		return e
	}
	return v.ResolveSaved(context)
}

// ResolveSaved is Resolve without the panel's own choice: what the saved
// settings give context.
func (v ResourcesVisibilityConfig) ResolveSaved(context string) EffectiveVisibility {
	name, source := VisibilityProfileWork, VisibilityFromBuiltIn
	if bound, ok := v.Contexts[context]; ok && strings.TrimSpace(bound) != "" {
		name, source = NormalizeVisibilityProfile(bound), VisibilityFromContext
	} else if strings.TrimSpace(v.DefaultProfile) != "" {
		name, source = NormalizeVisibilityProfile(v.DefaultProfile), VisibilityFromDefault
	}
	p, err := v.Profile(name)
	if err != nil {
		return EffectiveVisibility{Name: name, Source: source, Profile: visibility.Work(), Err: err}
	}
	return EffectiveVisibility{Name: name, Source: source, Profile: p}
}

// Validate reports every problem of the visibility settings: bad profile
// names, modes and selectors, a profile that shows and hides one type, a
// redefined "all", and a default or a binding that names no profile.
func (v ResourcesVisibilityConfig) Validate() []error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(v.Profiles)) {
		if name == VisibilityProfileAll {
			errs = append(errs, errors.New("resources.visibility.profiles.all: all is built in and cannot be defined"))
			continue
		}
		if err := ValidateProfileName(name); err != nil {
			errs = append(errs, fmt.Errorf("resources.visibility.profiles: %w", err))
			continue
		}
		p, err := v.Profiles[name].Policy()
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("resources.visibility.profiles.%s: %w", name, err))
		case name == VisibilityProfileWork && p.Mode != visibility.ModeWork:
			errs = append(errs, fmt.Errorf("resources.visibility.profiles.work: mode must be work, not %s", p.Mode))
		}
	}
	if name := strings.TrimSpace(v.DefaultProfile); name != "" && !v.defines(NormalizeVisibilityProfile(name)) {
		errs = append(errs, fmt.Errorf("resources.visibility.defaultProfile: profile %q is not defined", name))
	}
	for _, ctx := range slices.Sorted(maps.Keys(v.Contexts)) {
		name := v.Contexts[ctx]
		switch {
		case strings.TrimSpace(ctx) == "":
			errs = append(errs, errors.New("resources.visibility.contexts: a context name is empty"))
		case !v.defines(NormalizeVisibilityProfile(name)):
			errs = append(errs, fmt.Errorf("resources.visibility.contexts.%s: profile %q is not defined", ctx, name))
		}
	}
	return errs
}

// defines reports whether name is a built-in or a configured profile.
func (v ResourcesVisibilityConfig) defines(name string) bool {
	if name == VisibilityProfileWork || name == VisibilityProfileAll {
		return true
	}
	_, ok := v.Profiles[name]
	return ok
}

// ValidateProfileName checks that name can name a saved profile: a lower
// case DNS label, such as app or platform-team.
func ValidateProfileName(name string) error {
	if name == "" {
		return errors.New("a profile name is empty")
	}
	if msgs := validation.IsDNS1123Label(name); len(msgs) > 0 {
		return fmt.Errorf("profile name %q: use lower case letters, digits and '-' (%s)", name, strings.Join(msgs, "; "))
	}
	return nil
}

// Policy returns the profile as the visibility policy reads it, or why it
// is invalid.
func (p VisibilityProfileConfig) Policy() (visibility.Profile, error) {
	mode := strings.ToLower(strings.TrimSpace(p.Mode))
	if mode == "" {
		return visibility.Profile{}, errors.New("mode is missing (want work, all, hide or only)")
	}
	m, err := visibility.ParseMode(mode)
	if err != nil {
		return visibility.Profile{}, err
	}
	out := visibility.Profile{Mode: m, Show: selectorsOf(p.Show), Hide: selectorsOf(p.Hide)}
	if err := out.Validate(); err != nil {
		return visibility.Profile{}, err
	}
	return out, nil
}

// ProfileConfig is the saved form of a policy, with the selectors sorted.
func ProfileConfig(p visibility.Profile) VisibilityProfileConfig {
	return VisibilityProfileConfig{Mode: string(p.Mode), Show: typeSelectors(p.Show), Hide: typeSelectors(p.Hide)}
}

func selectorsOf(list []TypeSelector) []schema.GroupResource {
	if len(list) == 0 {
		return nil
	}
	out := make([]schema.GroupResource, 0, len(list))
	for _, s := range list {
		out = append(out, s.GroupResource())
	}
	return out
}

func typeSelectors(list []schema.GroupResource) []TypeSelector {
	out := make([]TypeSelector, 0, len(list))
	for _, gr := range list {
		out = append(out, TypeSelector{Group: gr.Group, Resource: gr.Resource})
	}
	slices.SortFunc(out, func(a, b TypeSelector) int {
		return strings.Compare(a.Group+"/"+a.Resource, b.Group+"/"+b.Resource)
	})
	return slices.Compact(out)
}

// NormalizeVisibilityProfile returns the profile name p means: trimmed and
// in lower case, "work" when empty. An unknown name is kept: resolving it
// lists Work and reports the error, so a typo never widens the catalog.
func NormalizeVisibilityProfile(p string) string {
	if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
		return p
	}
	return VisibilityProfileWork
}

// normalizeVisibilityForSave writes empty Show and Hide lists as [] so a
// saved profile replaces the lists of the file instead of keeping them.
func normalizeVisibilityForSave(v *ResourcesVisibilityConfig) {
	v.DefaultProfile = NormalizeVisibilityProfile(v.DefaultProfile)
	for name, p := range v.Profiles {
		if p.Show == nil {
			p.Show = make([]TypeSelector, 0)
		}
		if p.Hide == nil {
			p.Hide = make([]TypeSelector, 0)
		}
		v.Profiles[name] = p
	}
}
