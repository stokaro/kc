package ui

import (
	"errors"
	"fmt"
	"strings"

	textinput "charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/ui/dialog"
	"github.com/stokaro/kc/internal/visibility"
	"github.com/stokaro/kc/pkg/appconfig"
)

const (
	visibilitySaveOKID     = "save"
	visibilitySaveCancelID = "cancel"
)

// visibilitySaveRequest is what the save dialog asks for: store profile
// under name, and bind it to the context and make it the global default
// as the check boxes say. Unchecking a box that was on removes the
// binding or the default.
type visibilitySaveRequest struct {
	name          string
	profile       visibility.Profile
	context       string
	bindContext   bool
	globalDefault bool
}

// change applies r to the settings: the profile, the context's binding and
// the global default. Store.Update merges maps key by key, so an unbound
// context keeps its key with an empty value, which binds nothing.
func (r visibilitySaveRequest) change(v *appconfig.ResourcesVisibilityConfig) {
	if v.Profiles == nil {
		v.Profiles = make(map[string]appconfig.VisibilityProfileConfig)
	}
	v.Profiles[r.name] = appconfig.ProfileConfig(r.profile)
	if r.context != "" {
		switch {
		case r.bindContext:
			if v.Contexts == nil {
				v.Contexts = make(map[string]string)
			}
			v.Contexts[r.context] = r.name
		case appconfig.NormalizeVisibilityProfile(v.Contexts[r.context]) == r.name && strings.TrimSpace(v.Contexts[r.context]) != "":
			v.Contexts[r.context] = ""
		}
	}
	switch {
	case r.globalDefault:
		v.DefaultProfile = r.name
	case appconfig.NormalizeVisibilityProfile(v.DefaultProfile) == r.name:
		v.DefaultProfile = appconfig.VisibilityProfileWork
	}
}

// VisibilitySaveModel is the "Save profile" dialog of the visibility
// editor: the name, whether the context uses the profile, and whether it
// is the global default.
type VisibilitySaveModel struct {
	profile visibility.Profile
	saved   appconfig.ResourcesVisibilityConfig
	context string

	input         textinput.Model
	bindContext   bool
	globalDefault bool
	form          dialog.Form
	errMsg        string
	width, height int
	rules         []int

	onSave   func(visibilitySaveRequest) tea.Cmd
	onCancel func() tea.Cmd
}

// NewVisibilitySaveModel opens the dialog for profile p, suggesting name
// when it can be saved under it. The check boxes start as the settings
// are for that name.
func NewVisibilitySaveModel(name string, p visibility.Profile, saved appconfig.ResourcesVisibilityConfig, context string) *VisibilitySaveModel {
	// A saved name is suggested for a profile of the same mode only, so
	// a bare Enter does not save one mode over a profile of another.
	if name == appconfig.VisibilityProfileWork {
		if p.Mode != visibility.ModeWork {
			name = ""
		}
	} else if _, ok := saved.Profiles[name]; !ok {
		name = ""
	} else if stored, err := saved.Profile(name); err != nil || stored.Mode != p.Mode {
		name = ""
	}
	skin := dialog.Normal()
	m := &VisibilitySaveModel{profile: p, saved: saved, context: context, input: dialog.NewInput(skin, name), form: dialog.Form{Skin: skin}}
	if name != "" {
		m.bindContext = context != "" && strings.TrimSpace(saved.Contexts[context]) != "" && appconfig.NormalizeVisibilityProfile(saved.Contexts[context]) == name
		m.globalDefault = appconfig.NormalizeVisibilityProfile(saved.DefaultProfile) == name
	}
	m.syncItems()
	return m
}

func (m *VisibilitySaveModel) syncItems() {
	items := []dialog.Item{
		dialog.Text{Text: fmt.Sprintf("Save the %s profile as:", strings.ToLower(visibilityModeTitle(m.profile.Mode)))},
		dialog.Input{Model: &m.input},
		dialog.Text{Text: m.errMsg, Error: true},
		dialog.Rule{},
	}
	if m.context != "" {
		items = append(items, dialog.Check{ID: "context", Label: dialog.Label("Use for &context " + m.context), On: &m.bindContext})
	}
	items = append(items,
		dialog.Check{ID: "default", Label: "Use as the &global default", On: &m.globalDefault},
		dialog.Rule{},
		dialog.Buttons{
			{ID: visibilitySaveOKID, Label: "&Save", Default: true},
			{ID: visibilitySaveCancelID, Label: "Ca&ncel"},
		},
	)
	m.form.Items = items
}

func (m *VisibilitySaveModel) Init() tea.Cmd          { return nil }
func (m *VisibilitySaveModel) SetDimensions(w, h int) { m.width, m.height = w, h }

// PreferredSize implements ModalSizer.
func (m *VisibilitySaveModel) PreferredSize(maxW, _ int) (width, height int) {
	m.syncItems()
	width = min(max(m.form.Width(30), 48), 72)
	if maxW > 0 {
		width = min(width, maxW)
	}
	return width, m.form.Height()
}

// FooterHints implements ModalFooterHints.
func (m *VisibilitySaveModel) FooterHints() []FooterHint {
	return []FooterHint{
		{Key: "Enter", Label: "Save", Enabled: true},
		{Key: "Esc", Label: "Cancel", Enabled: true},
	}
}

// RuleRows implements ModalRules.
func (m *VisibilitySaveModel) RuleRows() []int { return m.rules }

// Error is the validation message on display, or "".
func (m *VisibilitySaveModel) Error() string { return m.errMsg }

// FocusName focuses the name input.
func (m *VisibilitySaveModel) FocusName() tea.Cmd {
	m.syncItems()
	return m.form.FocusFirst()
}

func (m *VisibilitySaveModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok && (key.String() == "ctrl+c" || key.String() == "ctrl+g") {
		return m, call(m.onCancel)
	}
	m.syncItems()
	ev, cmd := m.form.Update(msg)
	if ev.Edited || ev.Toggled != "" {
		m.errMsg = ""
	}
	switch ev.Pressed {
	case visibilitySaveOKID:
		req, err := m.request()
		if err != nil {
			m.errMsg = err.Error()
			return m, nil
		}
		if m.onSave == nil {
			return m, nil
		}
		return m, m.onSave(req)
	case visibilitySaveCancelID:
		return m, call(m.onCancel)
	}
	return m, cmd
}

// request checks the name and returns what to save.
func (m *VisibilitySaveModel) request() (visibilitySaveRequest, error) {
	name := strings.TrimSpace(m.input.Value())
	if problem := saveNameProblem(name, m.profile); problem != "" {
		return visibilitySaveRequest{}, errors.New(problem)
	}
	return visibilitySaveRequest{
		name:          name,
		profile:       m.profile.Clone(),
		context:       m.context,
		bindContext:   m.bindContext,
		globalDefault: m.globalDefault,
	}, nil
}

// saveNameProblem says why profile p cannot be saved as name, or "": All
// is built in, Work stays a Work profile, and names are DNS labels.
func saveNameProblem(name string, p visibility.Profile) string {
	switch {
	case name == "":
		return "Type a name, such as app or platform"
	case name == appconfig.VisibilityProfileAll:
		return "all is built in; pick another name"
	case name == appconfig.VisibilityProfileWork && p.Mode != visibility.ModeWork:
		return "work must stay a Work profile; pick another name"
	case appconfig.ValidateProfileName(name) != nil:
		return "Use lower case letters, digits and '-'"
	}
	return ""
}

func (m *VisibilitySaveModel) View() tea.View {
	m.syncItems()
	content, cursor, rules := m.form.View(max(m.width, 20))
	m.rules = rules
	return viewWithCursor(content, cursor)
}
