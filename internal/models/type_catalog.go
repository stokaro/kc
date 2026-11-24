package models

import (
	"context"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/visibility"
	"github.com/stokaro/kc/pkg/appconfig"
)

// OtherTypesRowID is the row ID of the "other types" entry of a type list.
const OtherTypesRowID = "other-types"

// TypeEntry is one discovered resource type of a type list, listed or not.
type TypeEntry struct {
	// ID is the type's row ID in its folder; an explicit jump with it
	// opens the type even when the folder does not list it.
	ID         string
	GVR        schema.GroupVersionResource
	Kind       string
	ShortNames []string
	Namespaced bool
	// Path is the path of the type's object list, such as
	// [namespaces default pods].
	Path     []string
	Decision visibility.Decision
}

// TypeCatalog is what a type list knows about its types: every discovered
// type of its scope, in the folder's order, and the profile that decided
// which of them are listed.
type TypeCatalog struct {
	// Profile is the built-in profile name: appconfig.VisibilityProfileWork
	// or appconfig.VisibilityProfileAll.
	Profile string
	Entries []TypeEntry
}

// Other returns the entries the profile leaves out.
func (c TypeCatalog) Other() []TypeEntry {
	var out []TypeEntry
	for _, e := range c.Entries {
		if !e.Decision.Included {
			out = append(out, e)
		}
	}
	return out
}

// TypeCatalogSource is a folder that lists resource types under a
// visibility profile.
type TypeCatalogSource interface {
	TypeCatalog() TypeCatalog
	// TypeDecision returns the decision on the type with row ID id, with
	// the profile name. ok is false for a row that is not a type.
	TypeDecision(id string) (d visibility.Decision, profile string, ok bool)
}

// TypeProfileFollower folders list resource types under the profile their
// configuration names (resources.visibility.defaultProfile of the panel's
// configuration).
type TypeProfileFollower interface {
	// SyncTypeProfile rebuilds the rows when they were built under another
	// profile than the configuration names now.
	SyncTypeProfile()
}

// OpenTypeCatalogRequest asks the UI to show the resource types of the
// active panel's type list: those the profile leaves out (Other), or all.
type OpenTypeCatalogRequest struct {
	OtherOnly bool
}

// ProfileTitle is how a built-in profile name reads on screen.
func ProfileTitle(profile string) string {
	if profile == appconfig.VisibilityProfileAll {
		return "All"
	}
	return "Work"
}

// visibilityProfile is the policy of a built-in profile name.
func visibilityProfile(name string) visibility.Profile {
	if name == appconfig.VisibilityProfileAll {
		return visibility.Profile{Mode: visibility.ModeAll}
	}
	return visibility.Work()
}

// typeProfile is the profile the folder lists its types under. A panel's
// folders share the panel's configuration, so it is the panel's choice.
func (f *ResourcesFolder) typeProfile() string {
	if cfg := f.Deps.AppConfig; cfg != nil {
		return appconfig.NormalizeVisibilityProfile(cfg.Resources.Visibility.DefaultProfile)
	}
	return appconfig.VisibilityProfileWork
}

// SyncTypeProfile rebuilds the rows when the last population used another
// profile than the configuration names now. A folder never populated has
// nothing to rebuild.
func (f *ResourcesFolder) SyncTypeProfile() {
	if f == nil {
		return
	}
	f.itemsMu.RLock()
	built := f.catalog.Profile
	f.itemsMu.RUnlock()
	if built != "" && built != f.typeProfile() {
		f.Refresh()
	}
}

// evaluateTypes applies the profile to the types of specs.
func (f *ResourcesFolder) evaluateTypes(ctx context.Context, profile string, specs []resourceGroupSpec) *visibility.Set {
	grs := make([]schema.GroupResource, 0, len(specs))
	for _, s := range specs {
		grs = append(grs, s.gvr.GroupResource())
	}
	set, err := visibility.Evaluate(visibilityProfile(profile), grs)
	if err != nil {
		// Built-in profiles are valid; Work is the narrow fallback.
		ctrllog.FromContext(ctx).WithName("resourcesFolder").Info("visibility profile invalid, using work", "profile", profile, "error", err.Error())
		set, _ = visibility.Evaluate(visibility.Work(), grs)
	}
	return set
}

// TypeCatalog returns the types of the folder's last population with the
// decisions of the profile the folder lists under now. A profile changed
// since that population is applied to the same types, so a panel that is
// below the folder still gets current decisions.
func (f *ResourcesFolder) TypeCatalog() TypeCatalog {
	f.itemsMu.RLock()
	c := TypeCatalog{Profile: f.catalog.Profile, Entries: slices.Clone(f.catalog.Entries)}
	f.itemsMu.RUnlock()
	profile := f.typeProfile()
	if c.Profile == profile || len(c.Entries) == 0 {
		c.Profile = profile
		return c
	}
	grs := make([]schema.GroupResource, 0, len(c.Entries))
	for _, e := range c.Entries {
		grs = append(grs, e.GVR.GroupResource())
	}
	set, err := visibility.Evaluate(visibilityProfile(profile), grs)
	if err != nil {
		return c
	}
	for i := range c.Entries {
		c.Entries[i].Decision, _ = set.Decide(c.Entries[i].GVR.GroupResource())
	}
	c.Profile = profile
	return c
}

// TypeDecision returns the decision on the type with row ID id under the
// profile the folder lists under now.
func (f *ResourcesFolder) TypeDecision(id string) (visibility.Decision, string, bool) {
	c := f.TypeCatalog()
	for _, e := range c.Entries {
		if e.ID == id {
			return e.Decision, c.Profile, true
		}
	}
	return visibility.Decision{}, c.Profile, false
}

func (f *ResourcesFolder) setCatalog(c TypeCatalog) {
	f.itemsMu.Lock()
	f.catalog = c
	f.itemsMu.Unlock()
}

// otherTypesItem is the "other types" entry: Enter lists the types the
// profile leaves out. It is a dialog, not a folder, so locations never go
// through it.
type otherTypesItem struct {
	*RowItem
}

func newOtherTypesItem(n int, profile string, parent []string) *otherTypesItem {
	cells := []string{fmt.Sprintf("/other types (%d)", n), "outside " + ProfileTitle(profile), ""}
	row := NewRowItem(OtherTypesRowID, cells, slices.Clone(parent), DimStyle())
	row.details = fmt.Sprintf("%d discovered types are outside the %s profile; Enter lists them", n, ProfileTitle(profile))
	return &otherTypesItem{RowItem: row}
}

// EnterMessage asks the UI for the list of the other types.
func (o *otherTypesItem) EnterMessage() any { return OpenTypeCatalogRequest{OtherOnly: true} }
