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

func (f *ResourcesFolder) finalize(ctx context.Context, specs []resourceGroupSpec) []table.Row {
	log := ctrllog.FromContext(ctx).WithName("resourcesFolder")

	if len(specs) == 0 {
		changed := len(f.items) > 0 || len(f.lastSpecs) > 0
		f.Close()
		f.setItems(make(map[string]*ResourceGroupItem))
		f.lastSpecs = make(map[string]resourceGroupSignature)
		if changed {
			f.BaseFolder.markDirtyFromSource()
			log.Info("resources cleared")
		}
		return nil
	}

	showNonEmpty, _, _ := f.viewSettings()
	rows := make([]table.Row, 0, len(specs))
	seen := make(map[string]*ResourceGroupItem, len(specs))
	sigs := make(map[string]resourceGroupSignature, len(specs))
	changed := len(specs) != len(f.lastSpecs)
	for idx, spec := range specs {
		item, created := f.ensureResourceGroupItem(spec)
		if item == nil {
			continue
		}
		item.applySpec(spec, f.Deps, created)
		current := item
		handler := func() { f.onResourceGroupCountChanged(current) }
		item.SetOnChange(handler)
		item.ComputeCountAsync(handler)
		visible := resourceGroupVisible(item, spec, showNonEmpty)
		sig := makeResourceGroupSignature(spec, visible, idx)
		sigs[spec.id] = sig
		if prev, ok := f.lastSpecs[spec.id]; !ok || prev != sig {
			changed = true
		}
		seen[spec.id] = item
		refreshCountCell(item)
		if !visible {
			continue
		}
		rows = append(rows, item)
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

// refreshCountCell shows the cached object count, or an empty cell while the
// count is unknown.
func refreshCountCell(item *ResourceGroupItem) {
	if count, ok := item.TryCount(); ok {
		item.setCountCell(fmt.Sprintf("%d", count))
		return
	}
	item.setCountCell("")
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
}

type resourceGroupSignature struct {
	gvr       schema.GroupVersionResource
	namespace string
	watchable bool
	cellsHash string
	pathHash  string
	visible   bool
	detail    string
	index     int
}

func makeResourceGroupSignature(spec resourceGroupSpec, visible bool, index int) resourceGroupSignature {
	return resourceGroupSignature{
		gvr:       spec.gvr,
		namespace: spec.namespace,
		watchable: spec.watchable,
		cellsHash: joinStrings(spec.cells),
		pathHash:  joinStrings(spec.path),
		visible:   visible,
		detail:    spec.detail,
		index:     index,
	}
}

func joinStrings(parts []string) string {
	return strings.Join(parts, "\x00")
}

func (f *ResourcesFolder) ensureResourceGroupItem(spec resourceGroupSpec) (*ResourceGroupItem, bool) {
	if existing, ok := f.items[spec.id]; ok {
		return existing, false
	}
	item := NewResourceGroupItem(f.Deps, spec.gvr, spec.gvk, spec.namespace, spec.id, spec.cells, spec.path, spec.detail, spec.style, spec.watchable, spec.enter)
	return item, true
}
