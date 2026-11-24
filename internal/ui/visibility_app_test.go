package ui

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/models"
	table "github.com/stokaro/kc/internal/table"
	"github.com/stokaro/kc/internal/visibility"
	"github.com/stokaro/kc/pkg/appconfig"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

// newVisibilityApp returns an app in context kind-dev with a saved "app"
// profile (only pods) and a configuration file in a temporary directory.
func newVisibilityApp(t *testing.T) *App {
	t.Helper()
	app := NewApp()
	t.Cleanup(app.cancel)
	app.cfgStore = appconfig.NewStore(filepath.Join(t.TempDir(), "config.yaml"))
	app.cfg = appconfig.Default()
	app.cfg.Resources.Visibility.Profiles = map[string]appconfig.VisibilityProfileConfig{
		"app": {Mode: "only", Show: []appconfig.TypeSelector{{Resource: "pods"}}},
	}
	app.leftConfig, app.rightConfig = nil, nil
	app.currentCtx = &kubeconfig.Context{Name: "kind-dev"}
	return app
}

func TestSetPanelVisibilityFollowsTheSettings(t *testing.T) {
	t.Parallel()
	app := newVisibilityApp(t)
	if err := app.setPanelProfile(app.leftPanel, "app"); err != nil {
		t.Fatal(err)
	}
	got := app.panelVisibility(app.leftPanel)
	if got.Name != "app" || got.Source != appconfig.VisibilityFromPanel || got.Edited {
		t.Fatalf("left = %+v, want app chosen for the panel", got)
	}
	if right := app.panelVisibility(app.rightPanel); right.Name != appconfig.VisibilityProfileWork {
		t.Fatalf("right = %+v, want work", right)
	}
	// Picking what the settings give the context drops the panel's own
	// choice, so the panel follows a later saved change again.
	if err := app.setPanelProfile(app.leftPanel, appconfig.VisibilityProfileWork); err != nil {
		t.Fatal(err)
	}
	if o := app.ensurePanelConfig(app.leftPanel).Resources.Visibility.Override; o != nil {
		t.Fatalf("override = %+v, want none", o)
	}
	app.setPanelVisibility(app.leftPanel, "app", visibility.Profile{Mode: visibility.ModeOnly, Show: []schema.GroupResource{{Resource: "pods"}, {Group: "apps", Resource: "deployments"}}})
	if got := app.panelVisibility(app.leftPanel); !got.Edited || got.Title() != "app*" {
		t.Fatalf("edited draft = %+v (%s)", got, got.Title())
	}
	if err := app.setPanelProfile(app.leftPanel, "missing"); err == nil {
		t.Fatalf("a missing profile was applied")
	}

	// A context switch drops the panels' own choices.
	app.resetPanelVisibility()
	if got := app.panelVisibility(app.leftPanel); got.Source == appconfig.VisibilityFromPanel {
		t.Fatalf("after a context switch the panel keeps %+v", got)
	}
}

// Saving writes the profile and the binding to the file and lists the
// panel under it through the binding; the other panel keeps its copy.
func TestSaveVisibilityProfile(t *testing.T) {
	t.Parallel()
	app := newVisibilityApp(t)
	right := app.ensurePanelConfig(app.rightPanel)
	p := visibility.Profile{Mode: visibility.ModeHide, Hide: []schema.GroupResource{{Group: "coordination.k8s.io", Resource: "leases"}}}
	cmd := app.saveVisibilityProfile(0, visibilitySaveRequest{name: "platform", profile: p, context: "kind-dev", bindContext: true})
	saved, ok := findMsg[configSavedMsg](runCmd(cmd))
	if !ok || saved.err != nil {
		t.Fatalf("save = %+v, %v", saved, ok)
	}
	got := app.panelVisibility(app.leftPanel)
	if got.Name != "platform" || got.Source != appconfig.VisibilityFromContext || got.Edited {
		t.Fatalf("left after save = %+v, want platform through the binding", got)
	}
	if _, ok := right.Resources.Visibility.Profiles["platform"]; ok || app.panelVisibility(app.rightPanel).Name != appconfig.VisibilityProfileWork {
		t.Fatalf("the right panel took the saved profile")
	}
	file, err := app.configStore().Load()
	if err != nil {
		t.Fatal(err)
	}
	v := file.Resources.Visibility
	if pc := v.Profiles["platform"]; pc.Mode != "hide" || len(pc.Hide) != 1 || pc.Hide[0].Resource != "leases" || v.Contexts["kind-dev"] != "platform" {
		t.Fatalf("file = %+v", v)
	}
	if errs := v.Validate(); len(errs) > 0 {
		t.Fatalf("the saved file does not validate: %v", errs)
	}
}

func TestTypeProfileSaver(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		contexts map[string]string
		choice   string
		want     appconfig.ResourcesVisibilityConfig
	}{
		{name: "global default", choice: "app", want: appconfig.ResourcesVisibilityConfig{DefaultProfile: "app"}},
		{name: "context binding", contexts: map[string]string{"kind-dev": "all"}, choice: "app", want: appconfig.ResourcesVisibilityConfig{Contexts: map[string]string{"kind-dev": "app"}}},
		{name: "already so", choice: appconfig.VisibilityProfileWork},
		{name: "unsaved changes", choice: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app := newVisibilityApp(t)
			app.cfg.Resources.Visibility.Contexts = tc.contexts
			var got appconfig.ResourcesVisibilityConfig
			app.typeProfileSaver(ResourcesOptionsChangedMsg{HasTypes: true, TypeProfile: tc.choice})(&got)
			if got.DefaultProfile != tc.want.DefaultProfile || !mapsEqual(got.Contexts, tc.want.Contexts) {
				t.Fatalf("saver wrote %+v, want %+v", got, tc.want)
			}
		})
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestTypeProfileChoices(t *testing.T) {
	t.Parallel()
	app := newVisibilityApp(t)
	choices, current := app.typeProfileChoices(app.leftPanel)
	var labels []string
	for _, c := range choices {
		labels = append(labels, c.Label)
	}
	if !slices.Equal(labels, []string{"Work", "All", "app"}) || current != appconfig.VisibilityProfileWork {
		t.Fatalf("choices %v current %q", labels, current)
	}
	app.setPanelVisibility(app.leftPanel, "app", visibility.Profile{Mode: visibility.ModeOnly})
	choices, current = app.typeProfileChoices(app.leftPanel)
	if choices[0].Label != "app*" || choices[0].Value != "" || current != "" {
		t.Fatalf("with unsaved changes: %v current %q", choices, current)
	}
}

func TestViewOptionsNamedProfilesAndEditorRow(t *testing.T) {
	t.Parallel()
	choices := []ViewOptionsChoice{{Value: "", Label: "app*"}, {Value: "work", Label: "Work"}, {Value: "all", Label: "All"}, {Value: "app", Label: "app"}}
	m := NewViewOptionsModel(ViewOptionsConfig{
		SkipPanelSection: true,
		Resources:        &ViewOptionsResourcesConfig{ShowTypes: true, TypeProfile: "", TypeChoices: choices, ShowInclude: true},
	})
	focus := func(opt viewOptionKind) {
		m.focus = slices.IndexFunc(m.entries, func(e viewOptionEntry) bool { return e.option == opt })
	}
	focus(viewOptionResourceTypes)
	if _, v := m.optionLabelAndValue(viewOptionResourceTypes); v != "app*" {
		t.Fatalf("Types = %q, want app*", v)
	}
	if msg := m.commit(true, false); msg.Resources.TypeChanged {
		t.Fatalf("an unchanged Types row reports a change")
	}
	m.adjustCurrent(-1)
	if msg := m.commit(true, false); msg.Resources.TypeProfile != "app" || !msg.Resources.TypeChanged {
		t.Fatalf("Left from app* = %+v, want app", msg.Resources)
	}
	focus(viewOptionResourceVisibility)
	if !strings.Contains(m.View().Content, "Resource visibility...") {
		t.Fatalf("no editor row:\n%s", m.View().Content)
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	msg, ok := cmd().(ViewOptionsCommittedMsg)
	if !ok || !msg.Accept || !msg.Resources.OpenVisibility || msg.Resources.TypeProfile != "app" {
		t.Fatalf("Enter on the editor row = %+v", msg)
	}
}

func TestVisibilityConfigError(t *testing.T) {
	t.Parallel()
	if err := visibilityConfigError(appconfig.Default().Resources.Visibility); err != nil {
		t.Fatalf("defaults report %v", err)
	}
	err := visibilityConfigError(appconfig.ResourcesVisibilityConfig{DefaultProfile: "gone"})
	if err == nil || !strings.Contains(err.Error(), `profile "gone" is not defined`) || !strings.Contains(err.Error(), "panels list Work instead") {
		t.Fatalf("error = %v", err)
	}
}

// catalogFolder is a type list for quick search: rows plus a catalog.
type catalogFolder struct {
	namesFolder
	catalog models.TypeCatalog
}

func (f *catalogFolder) TypeCatalog() models.TypeCatalog { return f.catalog }
func (f *catalogFolder) TypeDecision(string) (visibility.Decision, appconfig.EffectiveVisibility, bool) {
	return visibility.Decision{}, f.catalog.Profile, false
}
func (f *catalogFolder) ContextTypes() (models.ContextTypes, error) {
	return models.ContextTypes{}, nil
}

// Quick search in a type list finds a type the list does not show when no
// row matches, says why in the frame and the details line, and leaves it
// as soon as a row matches again.
func TestQuickSearchFindsHiddenTypes(t *testing.T) {
	ctx := t.Context()
	entries := catalogEntries()
	entries = append(entries, models.TypeEntry{
		ID: "batch/v1/jobs", GVR: schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"},
		Decision: visibility.Decision{Included: true, Reason: visibility.ReasonWork}, Hidden: models.HiddenEmpty,
	})
	rows := []table.Row{models.NewSimpleItem("/v1/pods", []string{"/pods"}, nil, models.WhiteStyle())}
	folder := &catalogFolder{rows: rows, catalog: models.TypeCatalog{Profile: workVisibility, Entries: entries}}
	p := NewPanel("t")
	p.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	p.UseFolder(ctx, true)
	p.SetFolder(ctx, folder, false)
	if !p.StartQuickSearch() {
		t.Fatalf("quick search did not start")
	}
	press := func(s string) { p.QuickSearchKey(tea.KeyPressMsg{Code: []rune(s)[0], Text: s}) }
	hidden := func() string {
		e, ok := p.HiddenTypeMatch()
		if !ok {
			return ""
		}
		return e.GVR.Resource + " " + p.search.hidden.why
	}

	press("p")
	if got := hidden(); got != "" {
		t.Fatalf("p matches the pods row, but quick search found %q", got)
	}
	press("l")
	if got := hidden(); got != "" || p.search.text != "p" {
		t.Fatalf("pl matches nothing: text %q hidden %q", p.search.text, got)
	}
	p.QuickSearchKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	press("l")
	if got := hidden(); got != "leases outside Work" || p.search.text != "l" {
		t.Fatalf("l: text %q hidden %q, want leases outside Work", p.search.text, got)
	}
	if note := p.search.hidden.note(); note != "Enter opens leases (coordination.k8s.io/v1), outside Work" {
		t.Fatalf("note = %q", note)
	}
	p.QuickSearchKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	press("j")
	if got := hidden(); got != "jobs empty" {
		t.Fatalf("j: hidden %q, want jobs empty", got)
	}
	p.QuickSearchKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	press("c")
	press("u")
	if got := hidden(); got != "customresourcedefinitions outside Work" {
		t.Fatalf("cu: hidden %q", got)
	}
	label := renderQuickSearch(p.search.text, p.search.hidden, "-", 60)
	if !strings.Contains(label, "Search: cu_ outside Work") {
		t.Fatalf("label = %q", label)
	}
	p.QuickSearchKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	p.QuickSearchKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	press("p")
	if _, ok := p.HiddenTypeMatch(); ok {
		t.Fatalf("a matching row did not end the hidden match")
	}
}
