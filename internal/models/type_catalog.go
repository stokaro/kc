package models

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/visibility"
	"github.com/stokaro/kc/pkg/appconfig"
)

// OtherTypesRowID is the row ID of the "other types" entry of a type list.
const OtherTypesRowID = "other-types"

// Reasons a type the profile lists has no row (TypeEntry.Hidden).
const (
	// HiddenEmpty: "Include empty" is No and the type is empty or its
	// emptiness is not known yet.
	HiddenEmpty = "empty"
	// HiddenNoAccess: the user may not list the type here.
	HiddenNoAccess = "no access"
)

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
	// Hidden says why a type the profile lists has no row (HiddenEmpty,
	// HiddenNoAccess); "" when it has one or the profile leaves it out.
	Hidden string
}

// TypeCatalog is what a type list knows about its types: every discovered
// type of its scope, in the folder's order, and the profile that decided
// which of them are listed.
type TypeCatalog struct {
	Profile appconfig.EffectiveVisibility
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

// ContextTypes are the resource types of a kube context that kc can
// browse, in both scopes, by resource name and group. FailedGroups are the
// API groups whose discovery failed: their types are missing from Entries.
// Entries have no ID or Path: they are not rows of a folder.
type ContextTypes struct {
	Entries      []TypeEntry
	FailedGroups []string
}

// TypeCatalogSource is a folder that lists resource types under a
// visibility profile.
type TypeCatalogSource interface {
	TypeCatalog() TypeCatalog
	// TypeDecision returns the decision on the type with row ID id, with
	// the profile. ok is false for a row that is not a type.
	TypeDecision(id string) (d visibility.Decision, profile appconfig.EffectiveVisibility, ok bool)
	// ContextTypes reads the types of the folder's context from cached
	// discovery, for the visibility editor.
	ContextTypes(ctx context.Context) (ContextTypes, error)
}

// TypeProfileFollower folders list resource types under the profile their
// configuration gives their context (the panel's choice, the context's
// binding or the global default).
type TypeProfileFollower interface {
	// SyncTypeProfile rebuilds the rows when they were built under another
	// profile than the configuration gives now.
	SyncTypeProfile()
}

// OpenTypeCatalogRequest asks the UI to show the resource types of the
// active panel's type list: those the profile leaves out (Other), or all.
type OpenTypeCatalogRequest struct {
	OtherOnly bool
}

// typeProfile is the profile the folder lists its types under. A panel's
// folders share the panel's configuration, so it is the panel's choice, or
// what the saved settings give the folder's context. A profile that
// cannot be used lists Work; the UI reports broken settings when it
// starts, so the folder only logs it, once.
func (f *ResourcesFolder) typeProfile() appconfig.EffectiveVisibility {
	cfg := f.Deps.AppConfig
	if cfg == nil {
		return appconfig.EffectiveVisibility{Name: appconfig.VisibilityProfileWork, Profile: visibility.Work()}
	}
	e := cfg.Resources.Visibility.Resolve(f.Deps.CtxName)
	if e.Err != nil {
		logVisibilityErrorOnce(f.Deps.Ctx, e.Err)
	}
	return e
}

// loggedVisibilityErrors keeps a broken profile to one log line per
// process instead of one per folder and refresh.
var loggedVisibilityErrors sync.Map

func logVisibilityErrorOnce(ctx context.Context, err error) {
	if _, seen := loggedVisibilityErrors.LoadOrStore(err.Error(), struct{}{}); seen {
		return
	}
	ctrllog.FromContext(ctx).WithName("resourcesFolder").Info("visibility profile unusable, listing work", "error", err.Error())
}

// SyncTypeProfile rebuilds the rows when the last population used another
// profile than the configuration gives now. A folder never populated has
// nothing to rebuild.
func (f *ResourcesFolder) SyncTypeProfile() {
	if f == nil {
		return
	}
	f.itemsMu.RLock()
	built, populated := f.catalog.Profile, f.catalogBuilt
	f.itemsMu.RUnlock()
	if populated && !built.Same(f.typeProfile()) {
		f.Refresh()
	}
}

// evaluateTypes applies the profile to the types of specs.
func (f *ResourcesFolder) evaluateTypes(ctx context.Context, profile appconfig.EffectiveVisibility, specs []resourceGroupSpec) *visibility.Set {
	grs := make([]schema.GroupResource, 0, len(specs))
	for _, s := range specs {
		grs = append(grs, s.gvr.GroupResource())
	}
	return evaluateOrWork(ctx, profile.Profile, grs)
}

// evaluateOrWork applies p to grs. Resolve hands out valid profiles only;
// should one still fail, Work is the narrow fallback, never all.
func evaluateOrWork(ctx context.Context, p visibility.Profile, grs []schema.GroupResource) *visibility.Set {
	set, err := visibility.Evaluate(p, grs)
	if err != nil {
		ctrllog.FromContext(ctx).WithName("resourcesFolder").Info("visibility profile invalid, using work", "error", err.Error())
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
	if c.Profile.Same(profile) || len(c.Entries) == 0 {
		c.Profile = profile
		return c
	}
	grs := make([]schema.GroupResource, 0, len(c.Entries))
	for _, e := range c.Entries {
		grs = append(grs, e.GVR.GroupResource())
	}
	set := evaluateOrWork(f.Deps.Ctx, profile.Profile, grs)
	for i := range c.Entries {
		c.Entries[i].Decision, _ = set.Decide(c.Entries[i].GVR.GroupResource())
		// Whether a newly listed type would have a row is not known
		// until the folder counts it.
		c.Entries[i].Hidden = ""
	}
	c.Profile = profile
	return c
}

// TypeDecision returns the decision on the type with row ID id under the
// profile the folder lists under now.
func (f *ResourcesFolder) TypeDecision(id string) (visibility.Decision, appconfig.EffectiveVisibility, bool) {
	c := f.TypeCatalog()
	for _, e := range c.Entries {
		if e.ID == id {
			return e.Decision, c.Profile, true
		}
	}
	return visibility.Decision{}, c.Profile, false
}

// ContextTypes reads every type of the folder's context that kc can
// browse (list and watch) from the cluster's last discovery read, both
// scopes. It never waits for the server.
func (f *ResourcesFolder) ContextTypes(ctx context.Context) (ContextTypes, error) {
	if f.Deps.Cl == nil {
		return ContextTypes{}, errors.New("no cluster")
	}
	infos, failed, err := f.Deps.Cl.DiscoveredResources(ctx)
	if err != nil {
		return ContextTypes{}, err
	}
	out := ContextTypes{FailedGroups: failed}
	for _, info := range infos {
		if !verbsInclude(info.Verbs, "list") || !verbsInclude(info.Verbs, "watch") {
			continue
		}
		out.Entries = append(out.Entries, TypeEntry{
			GVR:        schema.GroupVersionResource{Group: info.GVK.Group, Version: info.GVK.Version, Resource: info.Resource},
			Kind:       info.GVK.Kind,
			ShortNames: slices.Clone(info.ShortNames),
			Namespaced: info.Namespaced,
		})
	}
	slices.SortFunc(out.Entries, func(a, b TypeEntry) int {
		return cmp.Or(cmp.Compare(a.GVR.Resource, b.GVR.Resource), cmp.Compare(a.GVR.Group, b.GVR.Group))
	})
	return out, nil
}

func (f *ResourcesFolder) setCatalog(c TypeCatalog) {
	f.itemsMu.Lock()
	f.catalog = c
	f.catalogBuilt = true
	f.itemsMu.Unlock()
}

// otherTypesItem is the "other types" entry: Enter lists the types the
// profile leaves out. It is a dialog, not a folder, so locations never go
// through it.
type otherTypesItem struct {
	*RowItem
}

func newOtherTypesItem(n int, profile appconfig.EffectiveVisibility, parent []string) *otherTypesItem {
	cells := []string{fmt.Sprintf("/other types (%d)", n), "outside " + profile.Title(), ""}
	row := NewRowItem(OtherTypesRowID, cells, slices.Clone(parent), DimStyle())
	row.details = fmt.Sprintf("%d discovered types are outside the %s profile; Enter lists them", n, profile.Title())
	return &otherTypesItem{RowItem: row}
}

// EnterMessage asks the UI for the list of the other types.
func (o *otherTypesItem) EnterMessage() any { return OpenTypeCatalogRequest{OtherOnly: true} }
