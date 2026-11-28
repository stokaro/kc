package models

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"

	table "github.com/stokaro/kc/internal/table"
	"github.com/stokaro/kc/pkg/appconfig"
)

// ClusterResourcesFolder models resource groups that are cluster scoped (e.g., nodes).
type ClusterResourcesFolder struct {
	*ResourcesFolder
}

// NewClusterResourcesFolder creates a cluster-scoped resources folder.
func NewClusterResourcesFolder(deps Deps, path []string) *ClusterResourcesFolder {
	base := NewBaseFolder(deps, nil, path)
	folder := &ClusterResourcesFolder{ResourcesFolder: NewResourcesFolder(base)}
	base.SetPopulate(folder.populate)
	return folder
}

func (f *ClusterResourcesFolder) populate(ctx context.Context) ([]table.Row, error) {
	specs, err := f.resourceGroupSpecs(ctx)
	if err != nil {
		return nil, err
	}
	rows := f.ResourcesFolder.finalize(ctx, specs)
	return rows, nil
}

// resourceGroupSpecs lists the cluster-scoped types of the cluster's last
// discovery read; populate runs on the UI goroutine and must not wait for
// the server.
func (f *ClusterResourcesFolder) resourceGroupSpecs(ctx context.Context) ([]resourceGroupSpec, error) {
	_, order, favorites := f.viewSettings()
	favSet := favoritesMap(favorites)
	infos, _, err := f.Deps.Cl.DiscoveredResources(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]resourceEntry, 0, len(infos))
	for _, info := range infos {
		if info.Namespaced || info.Resource == "namespaces" {
			continue
		}
		if !verbsInclude(info.Verbs, "list") || !verbsInclude(info.Verbs, "watch") {
			continue
		}
		gvr := schema.GroupVersionResource{Group: info.GVK.Group, Version: info.GVK.Version, Resource: info.Resource}
		entries = append(entries, resourceEntry{info: info, gvr: gvr})
	}
	if order == appconfig.OrderFavorites && len(favSet) == 0 {
		favorites = favoritesFromCategories(entries)
		favSet = favoritesMap(favorites)
	}
	sortResourceEntries(entries, order, favSet)
	specs := make([]resourceGroupSpec, 0, len(entries))
	nameStyle := WhiteStyle()
	favoriteStyle := FavoriteResourceStyle()
	highlightFavorites := order == appconfig.OrderFavorites && len(favSet) > 0
	for _, entry := range entries {
		verbsCopy := append([]string(nil), entry.info.Verbs...)
		id := fmt.Sprintf("%s/%s/%s", entry.gvr.Group, entry.gvr.Version, entry.gvr.Resource)
		gvLabel := groupVersionString(entry.info.GVK.Group, entry.info.GVK.Version)
		cells := []string{"/" + entry.info.Resource, gvLabel, ""}
		basePath := append(slices.Clone(f.Path()), entry.info.Resource)
		cellsCopy := append([]string(nil), cells...)
		pathCopy := append([]string(nil), basePath...)
		gvr := entry.gvr
		kind := entry.info.GVK.Kind
		detail := resourceDetail(entry, entries)
		style := nameStyle
		if highlightFavorites && favSet[strings.ToLower(entry.info.Resource)] {
			style = favoriteStyle
		}
		specs = append(specs, resourceGroupSpec{
			id:         id,
			cells:      cellsCopy,
			path:       pathCopy,
			detail:     detail,
			style:      style,
			gvr:        gvr,
			gvk:        entry.info.GVK,
			namespace:  "",
			watchable:  true,
			verbs:      verbsCopy,
			shortNames: slices.Clone(entry.info.ShortNames),
			enter: func() (Folder, error) {
				folder := NewClusterObjectsFolder(f.Deps, gvr, pathCopy, verbsCopy)
				folder.setKind(kind)
				return folder, nil
			},
		})
	}
	return specs, nil
}
