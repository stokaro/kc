package models

// White-box: finalize, the item lifecycle (closed, onChange) and the
// started-types set are internal; the folder's public API shows rows only.

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	table "github.com/stokaro/kc/internal/table"
	"github.com/stokaro/kc/internal/visibility"
	"github.com/stokaro/kc/pkg/appconfig"
)

var (
	visPods    = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	visLeases  = schema.GroupVersionResource{Group: "coordination.k8s.io", Version: "v1", Resource: "leases"}
	visWidgets = schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
)

func visSpec(gvr schema.GroupVersionResource, namespaced bool) resourceGroupSpec {
	id := gvr.Group + "/" + gvr.Version + "/" + gvr.Resource
	return resourceGroupSpec{
		id:         id,
		cells:      []string{"/" + gvr.Resource, gvr.GroupVersion().String(), ""},
		path:       []string{gvr.Resource},
		detail:     gvr.Resource,
		gvr:        gvr,
		gvk:        schema.GroupVersionKind{Group: gvr.Group, Version: gvr.Version, Kind: "K" + gvr.Resource},
		namespaced: namespaced,
		shortNames: []string{gvr.Resource[:2]},
	}
}

func visSpecs() []resourceGroupSpec {
	return []resourceGroupSpec{visSpec(visPods, true), visSpec(visLeases, true), visSpec(visWidgets, false)}
}

// newVisFolder returns a folder that lists visSpecs under the profile of
// cfg, with empty types shown.
func newVisFolder(t *testing.T, profile string) (*ResourcesFolder, *appconfig.Config) {
	t.Helper()
	cfg := appconfig.Default()
	cfg.Resources.ShowNonEmptyOnly = false
	cfg.Resources.Visibility.DefaultProfile = profile
	f := NewResourcesFolder(NewBaseFolder(Deps{AppConfig: cfg, Ctx: t.Context()}, nil, nil))
	f.SetPopulate(func(ctx context.Context) ([]table.Row, error) { return f.finalize(ctx, visSpecs()), nil })
	t.Cleanup(f.Close)
	return f, cfg
}

func groupItem(t *testing.T, f *ResourcesFolder, gvr schema.GroupVersionResource) *ResourceGroupItem {
	t.Helper()
	it, ok := f.ExplicitItemByID(t.Context(), gvr.Group+"/"+gvr.Version+"/"+gvr.Resource)
	if !ok {
		t.Fatalf("no item for %s", gvr)
	}
	return it.(*ResourceGroupItem)
}

func TestFinalizeWorkProfileListsWorkTypesThenOther(t *testing.T) {
	t.Parallel()
	f, _ := newVisFolder(t, appconfig.VisibilityProfileWork)
	rows := f.finalize(t.Context(), visSpecs())

	got := rowIDs(rows)
	want := []string{"/v1/pods", OtherTypesRowID}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	other := rows[1].(MessageRow)
	if _, cells, _, _ := other.Columns(); cells[0] != "/other types (2)" || cells[1] != "outside Work" {
		t.Fatalf("other row cells = %q", cells)
	}
	if msg, ok := other.EnterMessage().(OpenTypeCatalogRequest); !ok || !msg.OtherOnly {
		t.Fatalf("other row Enter = %#v", other.EnterMessage())
	}

	// Types left out start nothing: no change handler, not started, and
	// still served to an explicit jump.
	if groupItem(t, f, visPods).onChange == nil || !f.active["/v1/pods"] {
		t.Fatalf("the listed type was not started")
	}
	for _, gvr := range []schema.GroupVersionResource{visLeases, visWidgets} {
		item := groupItem(t, f, gvr)
		if item.onChange != nil || f.active[item.ID()] {
			t.Fatalf("%s was started although Work leaves it out", gvr)
		}
	}

	c := f.TypeCatalog()
	if c.Profile.Name != appconfig.VisibilityProfileWork || len(c.Entries) != 3 {
		t.Fatalf("catalog = %+v", c)
	}
	if other := c.Other(); len(other) != 2 || other[0].GVR != visLeases || other[1].GVR != visWidgets {
		t.Fatalf("catalog other = %+v", other)
	}
	if e := c.Other()[1]; e.Namespaced || e.Kind != "Kwidgets" || e.ShortNames[0] != "wi" || e.Path[0] != "widgets" {
		t.Fatalf("widgets entry = %+v", e)
	}
	d, profile, ok := f.TypeDecision("coordination.k8s.io/v1/leases")
	if !ok || d.Included || profile.Name != appconfig.VisibilityProfileWork {
		t.Fatalf("TypeDecision(leases) = %+v %+v %v", d, profile, ok)
	}
	if d, _, _ := f.TypeDecision("/v1/pods"); !d.Included || d.Reason != visibility.ReasonWork {
		t.Fatalf("TypeDecision(pods) = %+v", d)
	}
	if _, _, ok := f.TypeDecision(OtherTypesRowID); ok {
		t.Fatalf("the other row is not a type")
	}
}

func TestFinalizeAllProfileListsEveryType(t *testing.T) {
	t.Parallel()
	f, _ := newVisFolder(t, appconfig.VisibilityProfileAll)
	got := rowIDs(f.finalize(t.Context(), visSpecs()))
	if len(got) != 3 {
		t.Fatalf("rows = %v, want the three types and no other row", got)
	}
	for _, id := range got {
		if id == OtherTypesRowID {
			t.Fatalf("rows = %v: no type is left out", got)
		}
	}
}

func TestFinalizeReshownTypeGetsAFreshItem(t *testing.T) {
	t.Parallel()
	f, cfg := newVisFolder(t, appconfig.VisibilityProfileAll)
	f.finalize(t.Context(), visSpecs())
	first := groupItem(t, f, visLeases)

	cfg.Resources.Visibility.DefaultProfile = appconfig.VisibilityProfileWork
	f.finalize(t.Context(), visSpecs())
	if !first.isClosed() {
		t.Fatalf("the item of a type that left the list keeps running")
	}
	passive := groupItem(t, f, visLeases)
	if passive == first || passive.isClosed() {
		t.Fatalf("an explicit jump would get the closed item")
	}

	cfg.Resources.Visibility.DefaultProfile = appconfig.VisibilityProfileAll
	rows := f.finalize(t.Context(), visSpecs())
	var shown *ResourceGroupItem
	for _, r := range rows {
		if it, ok := r.(*ResourceGroupItem); ok && it.gvr == visLeases {
			shown = it
		}
	}
	if shown == nil || shown == first || shown.isClosed() || shown.onChange == nil {
		t.Fatalf("the re-shown row is %p (closed first is %p)", shown, first)
	}
}

func TestSyncTypeProfileRebuildsRowsBuiltUnderAnotherProfile(t *testing.T) {
	t.Parallel()
	f, cfg := newVisFolder(t, appconfig.VisibilityProfileWork)
	f.clearDirty()
	f.SyncTypeProfile() // never populated: nothing to rebuild
	if f.IsDirty() {
		t.Fatalf("a folder never populated was marked for a rebuild")
	}
	ctx := t.Context()
	if n := f.Len(ctx); n != 2 {
		t.Fatalf("Len = %d, want pods and the other row", n)
	}
	f.clearDirty()
	f.SyncTypeProfile()
	if f.IsDirty() {
		t.Fatalf("rebuilt although the profile did not change")
	}

	cfg.Resources.Visibility.DefaultProfile = appconfig.VisibilityProfileAll
	// Decisions follow the profile at once, before the rebuild, so a
	// panel below the folder marks the right types.
	if d, profile, _ := f.TypeDecision("coordination.k8s.io/v1/leases"); !d.Included || profile.Name != appconfig.VisibilityProfileAll {
		t.Fatalf("TypeDecision(leases) under All = %+v %+v", d, profile)
	}
	f.SyncTypeProfile()
	if !f.IsDirty() {
		t.Fatalf("the folder was not rebuilt for the new profile")
	}
	if n := f.Len(ctx); n != 3 {
		t.Fatalf("Len after the switch = %d, want 3", n)
	}
}

// onlyLeases is a saved profile that lists Leases and nothing else.
func onlyLeases(cfg *appconfig.Config) {
	cfg.Resources.Visibility.Profiles = map[string]appconfig.VisibilityProfileConfig{
		"leases": {Mode: "only", Show: []appconfig.TypeSelector{{Group: "coordination.k8s.io", Resource: "leases"}}},
	}
}

// A type selected by name stays listed while "Include empty" is No and its
// count is not known yet; its cell says so instead of claiming 0.
func TestFinalizeListsExplicitTypesWhileCounting(t *testing.T) {
	t.Parallel()
	f, cfg := newVisFolder(t, appconfig.VisibilityProfileWork)
	cfg.Resources.ShowNonEmptyOnly = true
	onlyLeases(cfg)
	cfg.Resources.Visibility.DefaultProfile = "leases"

	rows := f.finalize(t.Context(), visSpecs())
	got := rowIDs(rows)
	if len(got) != 2 || got[0] != "coordination.k8s.io/v1/leases" || got[1] != OtherTypesRowID {
		t.Fatalf("rows = %v, want leases and the other row", got)
	}
	if _, cells, _, _ := rows[0].Columns(); cells[2] != "…" {
		t.Fatalf("count cell of a type still counting = %q, want …", cells[2])
	}
	if _, cells, _, _ := rows[1].Columns(); cells[1] != "outside leases" {
		t.Fatalf("other row = %q", cells)
	}
	d, profile, _ := f.TypeDecision("coordination.k8s.io/v1/leases")
	if !d.Included || !d.Explicit || profile.Name != "leases" {
		t.Fatalf("TypeDecision(leases) = %+v under %+v", d, profile)
	}
}

// Under Work with "Include empty" No, a listed type whose emptiness is not
// known has no row; the catalog says why, for quick search.
func TestFinalizeRecordsWhyAListedTypeHasNoRow(t *testing.T) {
	t.Parallel()
	f, cfg := newVisFolder(t, appconfig.VisibilityProfileWork)
	cfg.Resources.ShowNonEmptyOnly = true
	rows := f.finalize(t.Context(), visSpecs())
	if got := rowIDs(rows); len(got) != 1 || got[0] != OtherTypesRowID {
		t.Fatalf("rows = %v, want only the other row", got)
	}
	for _, e := range f.TypeCatalog().Entries {
		want := ""
		if e.GVR == visPods {
			want = HiddenEmpty
		}
		if e.Hidden != want {
			t.Errorf("%s hidden = %q, want %q", e.GVR.Resource, e.Hidden, want)
		}
	}
}

// The profile bound to the folder's context wins over the global default.
func TestFinalizeUsesTheContextBinding(t *testing.T) {
	t.Parallel()
	cfg := appconfig.Default()
	onlyLeases(cfg)
	cfg.Resources.Visibility.DefaultProfile = appconfig.VisibilityProfileAll
	cfg.Resources.Visibility.Contexts = map[string]string{"prod": "leases"}
	f := NewResourcesFolder(NewBaseFolder(Deps{AppConfig: cfg, Ctx: t.Context(), CtxName: "prod"}, nil, nil))
	t.Cleanup(f.Close)
	got := rowIDs(f.finalize(t.Context(), visSpecs()))
	if len(got) != 2 || got[0] != "coordination.k8s.io/v1/leases" {
		t.Fatalf("rows in context prod = %v, want leases and the other row", got)
	}
}

// A default that names no profile lists Work, never every type.
func TestFinalizeMissingProfileListsWork(t *testing.T) {
	t.Parallel()
	cfg := appconfig.Default()
	cfg.Resources.ShowNonEmptyOnly = false
	cfg.Resources.Visibility.DefaultProfile = "missing"
	f := NewResourcesFolder(NewBaseFolder(Deps{AppConfig: cfg, Ctx: t.Context()}, nil, nil))
	t.Cleanup(f.Close)
	got := rowIDs(f.finalize(t.Context(), visSpecs()))
	if len(got) != 2 || got[0] != "/v1/pods" || got[1] != OtherTypesRowID {
		t.Fatalf("rows = %v, want Work: pods and the other row", got)
	}
	if c := f.TypeCatalog(); c.Profile.Err == nil || c.Profile.Title() != "Work" {
		t.Fatalf("catalog profile = %+v, want Work with the error", c.Profile)
	}
}
