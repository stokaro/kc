package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/visibility"
	"github.com/stokaro/kc/pkg/appconfig"
)

// The dialogs of the resource visibility editor.
const (
	visibilityEditorModalKey   = "resource_visibility"
	visibilitySaveModalKey     = "resource_visibility_save"
	visibilityProfilesModalKey = "resource_visibility_profiles"
)

// panelVisibility returns the profile panel lists resource types under:
// its own choice, the binding of the panel's context, the global default
// or Work.
func (a *App) panelVisibility(panel *Panel) appconfig.EffectiveVisibility {
	return a.ensurePanelConfig(panel).Resources.Visibility.Resolve(a.connOfPanel(panel).name())
}

// setPanelVisibility makes panel list resource types under p named name.
// A choice the saved settings already make for the context drops the
// panel's own choice, so the panel follows the settings again. The
// folders read the panel's configuration; reloadPanelTypes rebuilds the
// one on screen.
func (a *App) setPanelVisibility(panel *Panel, name string, p visibility.Profile) {
	v := &a.ensurePanelConfig(panel).Resources.Visibility
	stored, err := v.Profile(name)
	edited := err != nil || !stored.Equal(p)
	if saved := v.ResolveSaved(a.connOfPanel(panel).name()); !edited && saved.Err == nil && saved.Name == name {
		v.Override = nil
		return
	}
	v.Override = &appconfig.VisibilityOverride{Name: name, Edited: edited, Profile: p.Clone()}
}

// setPanelProfile makes panel list resource types under the saved profile
// name.
func (a *App) setPanelProfile(panel *Panel, name string) error {
	p, err := a.ensurePanelConfig(panel).Resources.Visibility.Profile(name)
	if err != nil {
		return err
	}
	a.setPanelVisibility(panel, name, p)
	return nil
}

// resetPanelVisibilityOf drops panel idx's own profile choice and gives
// it the saved settings again: the choice was made for the context the
// panel had then.
func (a *App) resetPanelVisibilityOf(idx int) {
	if panel := a.panelByIndex(idx); panel != nil {
		a.ensurePanelConfig(panel).Resources.Visibility = a.cfg.Resources.Visibility.Clone()
	}
}

// reloadPanelTypes rebuilds the folder panel idx shows after its profile
// changed, and the "outside" mark of a type below it.
func (a *App) reloadPanelTypes(idx int) {
	panel, nav := a.panelByIndex(idx), a.navigatorFor(idx)
	if panel == nil || nav == nil || nav.Current() == nil {
		return
	}
	a.applyResourceOptions(panel)
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	if rf, ok := nav.Current().(interface{ Refresh() }); ok {
		rf.Refresh()
	}
	a.setPanelFolder(ctx, idx, nav.Current(), nav.HasBack())
	panel.SetCurrentPath(a.navigatorPath(nav))
	panel.RefreshFolder(ctx)
}

// showVisibilityEditor opens the resource visibility editor for panel idx
// on the profile the panel lists under now. It lists the types of the
// panel's context from cached discovery, in both scopes.
func (a *App) showVisibilityEditor(idx int) tea.Cmd {
	if a.modalManager == nil {
		return nil
	}
	panel := a.panelByIndex(idx)
	list, ok := nearestTypeList(a.navigatorFor(idx))
	if panel == nil || !ok {
		return a.ShowToast("No list of resource types here", 2*time.Second)
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	types, err := list.source.ContextTypes(ctx)
	cancel()
	if err != nil {
		return a.ShowToast("Cannot read the resource types: "+err.Error(), 3*time.Second)
	}
	cfg := a.ensurePanelConfig(panel)
	editor := NewVisibilityEditorModel(a.connOfPanel(panel).name(), a.panelVisibility(panel), cfg.Resources.Visibility, types)
	editor.hideEmpty, _ = panel.ResourceViewOptions()
	editor.onApply = func(name string, p visibility.Profile) tea.Cmd {
		a.hideModalName(visibilityEditorModalKey)
		a.setPanelVisibility(panel, name, p)
		a.reloadPanelTypes(idx)
		return nil
	}
	editor.onSave = func(name string, p visibility.Profile) tea.Cmd {
		return a.showVisibilitySave(idx, editor, name, p)
	}
	editor.onPick = func() tea.Cmd { return a.showVisibilityProfiles(editor) }
	editor.onCancel = func() tea.Cmd {
		a.hideModalName(visibilityEditorModalKey)
		return nil
	}
	modal := NewModal("Resource visibility", editor)
	// A lone Esc waits for a digit, so Esc 2 saves on a terminal without
	// function keys; Esc with a filter clears it first.
	modal.SetCloseOnSingleEsc(false)
	modal.SetCloseOnEscTimeout(true)
	a.modalManager.Register(visibilityEditorModalKey, modal)
	a.configureModalWindow(modal, editor, -1, "", 0, 0)
	a.showModal(visibilityEditorModalKey)
	return nil
}

// showVisibilitySave asks for the name to save the editor's profile under,
// and whether the context and the global default use it.
func (a *App) showVisibilitySave(idx int, editor *VisibilityEditorModel, name string, p visibility.Profile) tea.Cmd {
	save := NewVisibilitySaveModel(name, p, editor.Saved(), editor.Context())
	save.onSave = func(req visibilitySaveRequest) tea.Cmd { return a.saveVisibilityProfile(idx, req) }
	save.onCancel = func() tea.Cmd {
		a.hideModalName(visibilitySaveModalKey)
		return nil
	}
	modal := NewModal("Save profile", save)
	a.modalManager.Register(visibilitySaveModalKey, modal)
	a.configureModalWindow(modal, save, -1, "", 0, 0)
	a.showModal(visibilitySaveModalKey)
	return save.FocusName()
}

// saveVisibilityProfile stores req in the settings and the configuration
// file and lists panel idx under the saved profile. The other panel keeps
// its copy of the settings, as with every other saved option, until the
// context changes.
func (a *App) saveVisibilityProfile(idx int, req visibilitySaveRequest) tea.Cmd {
	change := func(c *appconfig.Config) { req.change(&c.Resources.Visibility) }
	change(a.cfg)
	panel := a.panelByIndex(idx)
	if panel != nil {
		a.ensurePanelConfig(panel).Resources.Visibility = a.cfg.Resources.Visibility.Clone()
		a.setPanelVisibility(panel, req.name, req.profile)
	}
	a.hideModalName(visibilitySaveModalKey)
	a.hideModalName(visibilityEditorModalKey)
	a.reloadPanelTypes(idx)
	return a.saveSettings(req.summary(), change)
}

// showVisibilityProfiles lists the saved profiles; the chosen one replaces
// the editor's draft.
func (a *App) showVisibilityProfiles(editor *VisibilityEditorModel) tea.Cmd {
	saved := editor.Saved()
	names := saved.ProfileNames()
	choices := make([]scopeChoice, 0, len(names))
	for _, name := range names {
		p, err := saved.Profile(name)
		var notes []string
		if bound := saved.Contexts[editor.Context()]; editor.Context() != "" && strings.TrimSpace(bound) != "" && appconfig.NormalizeVisibilityProfile(bound) == name {
			notes = append(notes, "context")
		}
		if appconfig.NormalizeVisibilityProfile(saved.DefaultProfile) == name {
			notes = append(notes, "default")
		}
		choices = append(choices, scopeChoice{
			Label:  appconfig.EffectiveVisibility{Name: name}.Title(),
			Value:  name,
			Detail: describeProfile(p, err),
			Note:   strings.Join(notes, " "),
		})
	}
	current, _ := editor.Profile()
	picker := NewScopePickerModel(choices, current, func(c scopeChoice) tea.Cmd {
		a.hideModalName(visibilityProfilesModalKey)
		if err := editor.LoadProfile(c.Value); err != nil {
			return a.ShowToast("Cannot load the profile: "+err.Error(), 3*time.Second)
		}
		return nil
	}, func() tea.Cmd {
		a.hideModalName(visibilityProfilesModalKey)
		return nil
	})
	modal := NewModal("Profiles", picker)
	modal.SetCloseOnSingleEsc(true)
	a.modalManager.Register(visibilityProfilesModalKey, modal)
	a.configureModalWindow(modal, picker, -1, "", 60, min(max(len(choices), 4)+3, 20))
	a.showModal(visibilityProfilesModalKey)
	return nil
}

// describeProfile says in a few words what a profile lists.
func describeProfile(p visibility.Profile, err error) string {
	if err != nil {
		return "invalid"
	}
	switch p.Mode {
	case visibility.ModeAll:
		return "every type"
	case visibility.ModeOnly:
		return fmt.Sprintf("only %d types", len(p.Show))
	case visibility.ModeHide:
		return fmt.Sprintf("hides %d types", len(p.Hide))
	}
	if len(p.Show) == 0 && len(p.Hide) == 0 {
		return "types of daily work"
	}
	s := "Work"
	if n := len(p.Show); n > 0 {
		s += fmt.Sprintf(" +%d", n)
	}
	if n := len(p.Hide); n > 0 {
		s += fmt.Sprintf(" -%d", n)
	}
	return s
}

// visibilityConfigError is the startup message for broken visibility
// settings, or nil. Every broken profile lists Work, never all types.
func visibilityConfigError(v appconfig.ResourcesVisibilityConfig) error {
	errs := v.Validate()
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%w; panels list Work instead", errors.Join(errs...))
}

// typeProfileChoices are the profiles the Types row of panel's options
// offers, and the value of the one it lists under: a saved profile, or ""
// for unsaved changes made in the editor.
func (a *App) typeProfileChoices(panel *Panel) ([]ViewOptionsChoice, string) {
	v := a.ensurePanelConfig(panel).Resources.Visibility
	eff := a.panelVisibility(panel)
	var choices []ViewOptionsChoice
	current := eff.Name
	if eff.Edited && eff.Err == nil {
		choices = append(choices, ViewOptionsChoice{Value: "", Label: eff.Title()})
		current = ""
	}
	if eff.Err != nil {
		current = appconfig.VisibilityProfileWork
	}
	for _, name := range v.ProfileNames() {
		choices = append(choices, ViewOptionsChoice{Value: name, Label: appconfig.EffectiveVisibility{Name: name}.Title()})
	}
	return choices, current
}

// applyTypeChoice makes panel list types under the profile the Types row
// of its options was changed to.
func (a *App) applyTypeChoice(panel *Panel, m ResourcesOptionsChangedMsg) tea.Cmd {
	if !m.HasTypes || !m.TypeChanged || m.TypeProfile == "" {
		return nil
	}
	if err := a.setPanelProfile(panel, m.TypeProfile); err != nil {
		return a.ShowToast("Cannot use the profile: "+err.Error(), 3*time.Second)
	}
	return nil
}

// typeProfileSaver returns what "Save" in the options of panel idx writes
// for the Types row: the chosen profile where the saved settings decide
// the profile of the panel's context, the context's binding when it has
// one and the global default otherwise. A choice the settings already
// make writes nothing, so saving another option never rebinds the context.
func (a *App) typeProfileSaver(idx int, m ResourcesOptionsChangedMsg) func(*appconfig.ResourcesVisibilityConfig) {
	ctxName := a.ownConn(idx).name()
	saved := a.cfg.Resources.Visibility.ResolveSaved(ctxName)
	name := m.TypeProfile
	if !m.HasTypes || name == "" || (saved.Err == nil && saved.Name == name) {
		return func(*appconfig.ResourcesVisibilityConfig) {}
	}
	if saved.Source == appconfig.VisibilityFromContext {
		return func(v *appconfig.ResourcesVisibilityConfig) {
			if v.Contexts == nil {
				v.Contexts = make(map[string]string)
			}
			v.Contexts[ctxName] = name
		}
	}
	return func(v *appconfig.ResourcesVisibilityConfig) { v.DefaultProfile = name }
}

// openHiddenType opens the type quick search found among the types panel
// idx does not list.
func (a *App) openHiddenType(idx int, e models.TypeEntry) tea.Cmd {
	list, ok := nearestTypeList(a.navigatorFor(idx))
	if !ok {
		return nil
	}
	return a.openCatalogType(idx, list, e)
}
