package ui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/models"
	navui "github.com/stokaro/kc/internal/navigation"
	"github.com/stokaro/kc/pkg/appconfig"
)

// typeCatalogModalKey is the dialog that lists the resource types of a
// panel's type list: the other types, or all of them.
const typeCatalogModalKey = "type_catalog"

// typeList is the nearest list of resource types in a navigator: the
// current folder, or the folder above it the user came through.
type typeList struct {
	source models.TypeCatalogSource
	// steps reach the type list from the root.
	steps []string
}

// nearestTypeList returns the type list of nav. ok is false when no folder
// on the way lists resource types, for example in a Helm release.
func nearestTypeList(nav *navui.Navigator) (typeList, bool) {
	if nav == nil {
		return typeList{}, false
	}
	var folders []models.Folder
	nav.ForEach(func(f models.Folder) { folders = append(folders, f) })
	steps := nav.Steps()
	for i, f := range slices.Backward(folders) {
		if src, ok := f.(models.TypeCatalogSource); ok && i <= len(steps) {
			return typeList{source: src, steps: slices.Clone(steps[:i])}, true
		}
	}
	return typeList{}, false
}

// outsideProfileMark is the frame label of a panel inside a resource type
// that its type list leaves out ("outside Work"), or "". A type list
// itself has no mark: its "other types" row says what it leaves out.
func outsideProfileMark(nav *navui.Navigator) string {
	if nav == nil {
		return ""
	}
	if _, ok := nav.Current().(models.TypeCatalogSource); ok {
		return ""
	}
	var folders []models.Folder
	nav.ForEach(func(f models.Folder) { folders = append(folders, f) })
	steps := nav.Steps()
	for i := min(len(folders), len(steps)) - 1; i >= 0; i-- {
		src, ok := folders[i].(models.TypeCatalogSource)
		if !ok {
			continue
		}
		d, profile, ok := src.TypeDecision(steps[i])
		if !ok || d.Included {
			return ""
		}
		return "outside " + models.ProfileTitle(profile)
	}
	return ""
}

// syncTypeMark updates the "outside" mark in the frame of panel idx.
func (a *App) syncTypeMark(idx int) {
	if panel := a.panelByIndex(idx); panel != nil {
		panel.SetTypeMark(outsideProfileMark(a.navigatorFor(idx)))
	}
}

// showTypeCatalog lists the resource types of the type list panel idx is
// in or below: the types the profile leaves out (otherOnly), or all. Enter
// opens the type as an explicit jump, the way cd does; the list itself is
// a dialog, so it never becomes part of a path, a hotlist entry or the
// history.
func (a *App) showTypeCatalog(idx int, otherOnly bool) tea.Cmd {
	if a.modalManager == nil {
		return nil
	}
	list, ok := nearestTypeList(a.navigatorFor(idx))
	if !ok {
		return a.ShowToast("No list of resource types here", 2*time.Second)
	}
	catalog := list.source.TypeCatalog()
	entries := catalog.Entries
	title := fmt.Sprintf("Resource types (%d)", len(entries))
	if otherOnly {
		entries = catalog.Other()
		title = fmt.Sprintf("Other types (%d)", len(entries))
	}
	if len(entries) == 0 {
		if otherOnly {
			return a.ShowInfo("The "+models.ProfileTitle(catalog.Profile)+" profile lists every type here", 2*time.Second)
		}
		return a.ShowToast("No resource types here yet", 2*time.Second)
	}
	byID := make(map[string]models.TypeEntry, len(entries))
	for _, e := range entries {
		byID[e.ID] = e
	}
	picker := NewScopePickerModel(typeCatalogChoices(entries, catalog.Profile, otherOnly), "", func(c scopeChoice) tea.Cmd {
		e, ok := byID[c.Value]
		if !ok {
			return nil
		}
		a.hideModal()
		return a.openCatalogType(idx, list, e)
	}, func() tea.Cmd {
		a.hideModal()
		return nil
	})
	modal := NewModal(title, picker)
	modal.SetCloseOnSingleEsc(true)
	a.modalManager.Register(typeCatalogModalKey, modal)
	a.configureModalWindow(modal, picker, idx, "", 84, typeCatalogHeight(len(entries)))
	a.showModal(typeCatalogModalKey)
	return nil
}

// openCatalogType jumps panel idx to the object list of e, below the type
// list it was picked from.
func (a *App) openCatalogType(idx int, list typeList, e models.TypeEntry) tea.Cmd {
	target, ok := a.panelLocation(idx)
	if !ok {
		return nil
	}
	target.Steps = append(slices.Clone(list.steps), e.ID)
	target.Path = "/" + strings.Join(e.Path, "/")
	return a.jumpTo(idx, target, true)
}

// typeCatalogHeight is the dialog height for n types.
func typeCatalogHeight(n int) int { return min(max(n, 4)+3, 30) }

// typeCatalogChoices shows each type as its plural name with the API
// group/version as the detail; the note says "cluster" for a
// cluster-scoped type and, in the list of all types under Work, whether
// Work lists it. The filter also matches the kind and the short names.
func typeCatalogChoices(entries []models.TypeEntry, profile string, otherOnly bool) []scopeChoice {
	out := make([]scopeChoice, 0, len(entries))
	for _, e := range entries {
		var notes []string
		if !e.Namespaced {
			notes = append(notes, "cluster")
		}
		if !otherOnly && profile != appconfig.VisibilityProfileAll {
			if e.Decision.Included {
				notes = append(notes, models.ProfileTitle(profile))
			} else {
				notes = append(notes, "other")
			}
		}
		out = append(out, scopeChoice{
			Label:  e.GVR.Resource,
			Detail: e.GVR.GroupVersion().String(),
			Value:  e.ID,
			Note:   strings.Join(notes, "  "),
			Search: strings.Join(append([]string{e.Kind}, e.ShortNames...), " "),
		})
	}
	return out
}

// setPanelTypeProfile makes panel list resource types under the built-in
// profile ("work" or "all"). Its folders read the panel's configuration,
// so the folders it opens next use the profile from their first rows;
// applyResourceOptions rebuilds the folder it shows.
func (a *App) setPanelTypeProfile(panel *Panel, profile string) {
	cfg := a.ensurePanelConfig(panel)
	profile = appconfig.NormalizeVisibilityProfile(profile)
	if cfg.Resources.Visibility.DefaultProfile != profile {
		cfg.Resources.Visibility.DefaultProfile = profile
	}
}
