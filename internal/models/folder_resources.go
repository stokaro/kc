package models

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	kccluster "github.com/stokaro/kc/internal/cluster"
	table "github.com/stokaro/kc/internal/table"
	"github.com/stokaro/kc/internal/visibility"
	"github.com/stokaro/kc/pkg/appconfig"
)

// ResourcesFolder provides shared scaffolding for resource-group folders (namespace and cluster scoped).
type ResourcesFolder struct {
	*BaseFolder
	// items holds every known type, visible or not. finalize replaces the
	// map as a whole and never changes it afterwards; itemsMu guards the
	// swap so TypeItem can read it from another goroutine.
	itemsMu   sync.RWMutex
	items     map[string]*ResourceGroupItem
	lastSpecs map[string]resourceGroupSignature
	overrides *resourceViewOptions
	// catalog is every type of the last population with the profile's
	// decision; catalogBuilt is set once a population stored it. itemsMu
	// guards both.
	catalog      TypeCatalog
	catalogBuilt bool
	// active marks the items whose count or emptiness peek was started.
	// Only types the profile lists are started; finalize alone uses it.
	active map[string]bool
}

type resourceViewOptions struct {
	showNonEmpty *bool
	order        appconfig.ResourcesViewOrder
	hasOrder     bool
	favorites    []string
	hasFavorites bool
}

// NewResourcesFolder constructs a ResourcesFolder with default columns and caller-provided metadata.
func NewResourcesFolder(base *BaseFolder) *ResourcesFolder {
	base.SetColumns([]table.Column{{Title: " Name"}, {Title: "Group"}, {Title: "Count"}})
	return &ResourcesFolder{
		BaseFolder: base,
		items:      make(map[string]*ResourceGroupItem),
		lastSpecs:  make(map[string]resourceGroupSignature),
		active:     make(map[string]bool),
	}
}

func (f *ResourcesFolder) viewSettings() (bool, appconfig.ResourcesViewOrder, []string) {
	show := true
	order := appconfig.OrderAlpha
	var favorites []string
	if cfg := f.Deps.AppConfig; cfg != nil {
		show = cfg.Resources.ShowNonEmptyOnly
		order = cfg.Resources.Order
		favorites = cfg.Resources.Favorites
	}
	if f.overrides != nil {
		if f.overrides.showNonEmpty != nil {
			show = *f.overrides.showNonEmpty
		}
		if f.overrides.hasOrder {
			order = f.overrides.order
		}
		if f.overrides.hasFavorites {
			favorites = f.overrides.favorites
		}
	}
	return show, order, favorites
}

func (f *ResourcesFolder) ApplyResourceViewOptions(showNonEmpty bool, order appconfig.ResourcesViewOrder, favorites []string) {
	if f == nil {
		return
	}
	if f.overrides == nil {
		f.overrides = &resourceViewOptions{}
	}
	ov := f.overrides
	changed := false
	if ov.showNonEmpty == nil || *ov.showNonEmpty != showNonEmpty {
		val := showNonEmpty
		ov.showNonEmpty = &val
		changed = true
	}
	if !ov.hasOrder || ov.order != order {
		ov.order = order
		ov.hasOrder = true
		changed = true
	}
	if favorites != nil {
		if !ov.hasFavorites || !slices.Equal(ov.favorites, favorites) {
			ov.favorites = slices.Clone(favorites)
			ov.hasFavorites = true
			changed = true
		}
	} else if ov.hasFavorites {
		ov.hasFavorites = false
		ov.favorites = nil
		changed = true
	}
	if changed {
		f.Refresh()
	}
}

// Reread asks the cluster to read API discovery again and lists the
// resource types again, so a type installed since (a new CRD) shows when
// that read ends instead of after the next periodic discovery refresh. The
// read runs in the background: the UI goroutine calls Reread, and the
// discovery listeners repaint the type lists when the read succeeded.
func (f *ResourcesFolder) Reread(ctx context.Context) {
	ctrllog.FromContext(ctx).WithName("resourcesFolder").Info("rereading API discovery", "context", f.Deps.CtxName, "namespace", f.Deps.ClusterKey.Namespace)
	f.Deps.Cl.RequestDiscoveryRefresh()
	f.BaseFolder.Reread(ctx)
}

func (f *ResourcesFolder) finalize(ctx context.Context, specs []resourceGroupSpec) []table.Row {
	log := ctrllog.FromContext(ctx).WithName("resourcesFolder")

	if len(specs) == 0 {
		changed := len(f.items) > 0 || len(f.lastSpecs) > 0
		f.Close()
		f.setItems(make(map[string]*ResourceGroupItem))
		f.setCatalog(TypeCatalog{Profile: f.typeProfile()})
		f.lastSpecs = make(map[string]resourceGroupSignature)
		f.active = make(map[string]bool)
		if changed {
			f.BaseFolder.markDirtyFromSource()
			log.Info("resources cleared")
		}
		return nil
	}

	showNonEmpty, _, _ := f.viewSettings()
	// The profile decides first, from discovery alone: a type it leaves
	// out starts no count, no informer handler and no emptiness peek.
	profile := f.typeProfile()
	set := f.evaluateTypes(ctx, profile, specs)
	rows := make([]table.Row, 0, len(specs)+1)
	seen := make(map[string]*ResourceGroupItem, len(specs))
	sigs := make(map[string]resourceGroupSignature, len(specs))
	entries := make([]TypeEntry, 0, len(specs))
	changed := len(specs) != len(f.lastSpecs)
	for idx, spec := range specs {
		decision, _ := set.Decide(spec.gvr.GroupResource())
		item := f.typeItem(spec, decision.Included)
		visible, hidden := typeRowVisible(item, spec, decision, showNonEmpty)
		entries = append(entries, TypeEntry{
			ID:         spec.id,
			GVR:        spec.gvr,
			Kind:       spec.gvk.Kind,
			ShortNames: spec.shortNames,
			Namespaced: spec.namespaced,
			Path:       slices.Clone(spec.path),
			Decision:   decision,
			Hidden:     hidden,
		})
		seen[spec.id] = item
		sig := makeResourceGroupSignature(spec, decision.Included, visible, idx)
		sigs[spec.id] = sig
		if prev, ok := f.lastSpecs[spec.id]; !ok || prev != sig {
			changed = true
		}
		refreshCountCell(item, decision.Explicit && !spec.uncounted)
		if !visible {
			continue
		}
		rows = append(rows, item)
	}
	for id := range f.active {
		if _, ok := seen[id]; !ok {
			delete(f.active, id)
		}
	}
	f.setCatalog(TypeCatalog{Profile: profile, Entries: entries})
	if n := len(set.Other()); n > 0 {
		rows = append(rows, newOtherTypesItem(n, profile, f.Path()))
	}

	if len(rows) == 0 {
		f.closeDropped(seen)
		f.setItems(seen)
		f.lastSpecs = sigs
		if changed {
			f.BaseFolder.markDirtyFromSource()
			log.Info("resources updated", "count", 0)
		}
		return nil
	}

	if !changed {
		for id := range f.lastSpecs {
			if _, ok := sigs[id]; !ok {
				changed = true
				break
			}
		}
	}

	f.closeDropped(seen)
	f.setItems(seen)
	f.lastSpecs = sigs
	if changed {
		f.BaseFolder.markDirtyFromSource()
		log.Info("resources updated", "count", len(rows))
	}
	return rows
}

// resourceGroupVisible reports whether the row of a resource type is shown.
// It never peeks the apiserver, because it runs on the UI thread: it uses the
// cached answer and lets a background peek refresh the folder.
func resourceGroupVisible(item *ResourceGroupItem, spec resourceGroupSpec, showNonEmpty bool) bool {
	// EmptyState also starts the background peek, so it runs even for a
	// forbidden type, as it always has.
	empty, pending := item.EmptyState()
	if item.Forbidden() {
		return false
	}
	// Uncounted types (all namespaces) have no emptiness to filter on. A type
	// whose emptiness is not known yet stays hidden, so the list grows to the
	// non-empty types instead of showing all of them and then shrinking.
	return !showNonEmpty || spec.uncounted || (!empty && !pending)
}

// typeRowVisible says whether a type gets a row and, for a type the
// profile lists without one, why (HiddenEmpty, HiddenNoAccess). Only a
// listed type may peek: EmptyState starts one. A type the user selected
// by name stays listed when it is empty, still counting or forbidden; its
// count cell says which.
func typeRowVisible(item *ResourceGroupItem, spec resourceGroupSpec, decision visibility.Decision, showNonEmpty bool) (visible bool, hidden string) {
	if !decision.Included {
		return false, ""
	}
	if resourceGroupVisible(item, spec, showNonEmpty) || decision.Explicit {
		return true, ""
	}
	if item.Forbidden() {
		return false, HiddenNoAccess
	}
	return false, HiddenEmpty
}

// refreshCountCell shows the cached object count, or an empty cell while the
// count is unknown. A type selected by name (states) is listed whatever its
// count, so its cell tells a count still loading ("…"), one that failed
// ("n/a") and a type the user may not list ("denied") from a real 0.
func refreshCountCell(item *ResourceGroupItem, states bool) {
	if count, ok := item.TryCount(); ok {
		item.setCountCell(fmt.Sprintf("%d", count))
		return
	}
	if !states {
		item.setCountCell("")
		return
	}
	switch {
	case item.Forbidden():
		item.setCountCell("denied")
	case item.countFailed():
		item.setCountCell("n/a")
	default:
		item.setCountCell("…")
	}
}

func (f *ResourcesFolder) setItems(items map[string]*ResourceGroupItem) {
	f.itemsMu.Lock()
	f.items = items
	f.itemsMu.Unlock()
}

// TypeItem returns the row of resource type gr together with its row ID. It
// also finds rows the folder hides (empty types, types whose emptiness is
// not known yet, forbidden types), because an explicit jump such as cd names
// the type on purpose. ok is false while the type is unknown to the folder,
// for example before its first population. Versions are ignored; with
// several versions of one type the lowest row ID wins.
func (f *ResourcesFolder) TypeItem(gr schema.GroupResource) (item Item, id string, ok bool) {
	f.itemsMu.RLock()
	items := f.items
	f.itemsMu.RUnlock()
	for key, it := range items {
		if it == nil || it.gvr.GroupResource() != gr {
			continue
		}
		if !ok || key < id {
			item, id, ok = it, key, true
		}
	}
	return item, id, ok
}

// ExplicitItemByID returns the row of the known resource type with row ID
// id, shown or hidden. A type the folder hides (empty, emptiness not known
// yet, forbidden) is still served, so an explicit jump opens it; its object
// list then reports what the apiserver allows. ok is false for an ID that
// is not a type the cluster serves now, for example a version no longer
// served or a removed CRD.
func (f *ResourcesFolder) ExplicitItemByID(_ context.Context, id string) (Item, bool) {
	f.itemsMu.RLock()
	item, ok := f.items[id]
	f.itemsMu.RUnlock()
	if !ok || item == nil {
		return nil, false
	}
	return item, true
}

// closeDropped closes items that are no longer part of the folder.
func (f *ResourcesFolder) closeDropped(keep map[string]*ResourceGroupItem) {
	for id, item := range f.items {
		if keep[id] != item {
			item.Close()
		}
	}
}

// Close stops background work of all items; call it when the folder leaves
// navigation for good.
func (f *ResourcesFolder) Close() {
	for _, item := range f.items {
		item.Close()
	}
}

func groupVersionString(group, version string) string {
	if group == "" {
		return version
	}
	return group + "/" + version
}

func verbsInclude(verbs []string, want string) bool {
	for _, v := range verbs {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

func sortResourceEntries(entries []resourceEntry, order appconfig.ResourcesViewOrder, fav map[string]bool) {
	switch order {
	case appconfig.OrderGroup:
		sort.SliceStable(entries, func(i, j int) bool {
			groupI := entries[i].info.GVK.Group
			groupJ := entries[j].info.GVK.Group
			if groupI == groupJ {
				return entries[i].info.Resource < entries[j].info.Resource
			}
			if groupI == "" {
				return true
			}
			if groupJ == "" {
				return false
			}
			gi := groupVersionString(groupI, entries[i].info.GVK.Version)
			gj := groupVersionString(groupJ, entries[j].info.GVK.Version)
			return gi < gj
		})
	case appconfig.OrderFavorites:
		isFav := func(res string) bool {
			if fav == nil {
				return false
			}
			return fav[strings.ToLower(res)]
		}
		sort.SliceStable(entries, func(i, j int) bool {
			fi, fj := isFav(entries[i].info.Resource), isFav(entries[j].info.Resource)
			if fi != fj {
				return fi
			}
			return entries[i].info.Resource < entries[j].info.Resource
		})
	default:
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].info.Resource < entries[j].info.Resource })
	}
}

func (f *ResourcesFolder) onResourceGroupCountChanged(item *ResourceGroupItem) {
	if f == nil || item == nil {
		return
	}
	// The rebuild writes the count cell on the UI thread; this runs on a
	// background goroutine and must not touch the row.
	// Rebuild the rows, not just repaint them: emptiness and permissions
	// arrive from background peeks and decide which rows are visible.
	// populate only reads cached state, so this is cheap.
	f.BaseFolder.markDirty()
}

func favoritesMap(list []string) map[string]bool {
	if len(list) == 0 {
		return nil
	}
	set := make(map[string]bool, len(list))
	for _, item := range list {
		if item == "" {
			continue
		}
		set[strings.ToLower(item)] = true
	}
	return set
}

func favoritesFromCategories(entries []resourceEntry) []string {
	if len(entries) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	favorites := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !hasAllCategory(entry.info.Categories) {
			continue
		}
		key := strings.ToLower(entry.info.Resource)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		favorites = append(favorites, entry.info.Resource)
	}
	if len(favorites) == 0 {
		return nil
	}
	return favorites
}

func hasAllCategory(categories []string) bool {
	for _, cat := range categories {
		if strings.EqualFold(cat, "all") {
			return true
		}
	}
	return false
}

type resourceEntry struct {
	info ResourceInfo
	gvr  schema.GroupVersionResource
}

type ResourceInfo = kccluster.ResourceInfo

// resourceDetail is the status line text of the row of entry. A resource
// name that another group in all also serves is listed twice with the
// same Name, so the text says which row is which. It stays short enough
// for the status line of a half-width panel.
func resourceDetail(entry resourceEntry, all []resourceEntry) string {
	gv := groupVersionString(entry.info.GVK.Group, entry.info.GVK.Version)
	detail := fmt.Sprintf("%s (%s)", entry.info.Resource, gv)
	var others []string
	for _, o := range all {
		if o.info.Resource == entry.info.Resource && o.gvr.Group != entry.gvr.Group {
			others = append(others, groupVersionString(o.info.GVK.Group, o.info.GVK.Version))
		}
	}
	if len(others) == 0 {
		return detail
	}
	if isEventGVR(entry.gvr) {
		if entry.gvr.Group == "" {
			return detail + ": core Events, what kubectl get events lists"
		}
		return detail + ": the same Events, newer API"
	}
	return fmt.Sprintf("%s; another type of this name is in %s", detail, strings.Join(others, ", "))
}

type resourceGroupSpec struct {
	id        string
	cells     []string
	path      []string
	style     *lipgloss.Style
	detail    string
	gvr       schema.GroupVersionResource
	gvk       schema.GroupVersionKind
	namespace string
	watchable bool
	// uncounted types are not watched for a count and are never hidden as
	// empty: across all namespaces a count would watch the whole cluster.
	uncounted bool
	enter     func() (Folder, error)
	verbs     []string
	// namespaced and shortNames describe the type in the type catalog.
	namespaced bool
	shortNames []string
}

type resourceGroupSignature struct {
	gvr       schema.GroupVersionResource
	namespace string
	watchable bool
	cellsHash string
	pathHash  string
	included  bool
	visible   bool
	detail    string
	index     int
}

func makeResourceGroupSignature(spec resourceGroupSpec, included, visible bool, index int) resourceGroupSignature {
	return resourceGroupSignature{
		gvr:       spec.gvr,
		namespace: spec.namespace,
		watchable: spec.watchable,
		cellsHash: joinStrings(spec.cells),
		pathHash:  joinStrings(spec.path),
		included:  included,
		visible:   visible,
		detail:    spec.detail,
		index:     index,
	}
}

func joinStrings(parts []string) string {
	return strings.Join(parts, "\x00")
}

// typeItem returns the item of spec's type with spec applied. A listed type
// gets its change handler and count; a type the profile leaves out gets a
// passive item that starts nothing until a jump enters it.
func (f *ResourcesFolder) typeItem(spec resourceGroupSpec, listed bool) *ResourceGroupItem {
	item, created := f.ensureResourceGroupItem(spec)
	if !listed && f.active[spec.id] {
		// The type left the list: stop its count and peeks. A closed item
		// stays closed, so an explicit jump gets a fresh one, and so does
		// the row if the type comes back.
		item.Close()
		delete(f.active, spec.id)
		item, created = newResourceGroupItemFromSpec(f.Deps, spec), true
	}
	item.applySpec(spec, f.Deps, created)
	if listed {
		handler := func() { f.onResourceGroupCountChanged(item) }
		item.SetOnChange(handler)
		item.ComputeCountAsync(handler)
		f.active[spec.id] = true
	}
	return item
}

func (f *ResourcesFolder) ensureResourceGroupItem(spec resourceGroupSpec) (*ResourceGroupItem, bool) {
	if existing, ok := f.items[spec.id]; ok && !existing.isClosed() {
		return existing, false
	}
	return newResourceGroupItemFromSpec(f.Deps, spec), true
}

func newResourceGroupItemFromSpec(deps Deps, spec resourceGroupSpec) *ResourceGroupItem {
	return NewResourceGroupItem(deps, spec.gvr, spec.gvk, spec.namespace, spec.id, spec.cells, spec.path, spec.detail, spec.style, spec.watchable, spec.enter)
}
