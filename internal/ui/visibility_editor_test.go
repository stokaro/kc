package ui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/visibility"
	"github.com/stokaro/kc/pkg/appconfig"
)

var (
	grPods    = schema.GroupResource{Resource: "pods"}
	grLeases  = schema.GroupResource{Group: "coordination.k8s.io", Resource: "leases"}
	grCRDs    = schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"}
	grWidgets = schema.GroupResource{Group: "example.com", Resource: "widgets"}
)

// editorTypes is a small context: pods (in Work), leases and CRDs (not).
func editorTypes() models.ContextTypes {
	return models.ContextTypes{Entries: []models.TypeEntry{
		{GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Kind: "Pod", ShortNames: []string{"po"}, Namespaced: true},
		{GVR: schema.GroupVersionResource{Group: "coordination.k8s.io", Version: "v1", Resource: "leases"}, Kind: "Lease", Namespaced: true},
		{GVR: schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}, Kind: "CustomResourceDefinition", ShortNames: []string{"crd"}},
	}}
}

func newTestEditor(t *testing.T, name string, p visibility.Profile, saved appconfig.ResourcesVisibilityConfig) *VisibilityEditorModel {
	t.Helper()
	m := NewVisibilityEditorModel("kind-dev", appconfig.EffectiveVisibility{Name: name, Profile: p}, saved, editorTypes())
	m.SetDimensions(90, 16)
	return m
}

func edKey(s string) tea.KeyPressMsg {
	switch s {
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "insert":
		return tea.KeyPressMsg{Code: tea.KeyInsert}
	case "f2":
		return tea.KeyPressMsg{Code: tea.KeyF2}
	case "f4":
		return tea.KeyPressMsg{Code: tea.KeyF4}
	case "f5":
		return tea.KeyPressMsg{Code: tea.KeyF5}
	case "f6":
		return tea.KeyPressMsg{Code: tea.KeyF6}
	case "f8":
		return tea.KeyPressMsg{Code: tea.KeyF8}
	}
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}

func edKeys(m tea.Model, keys ...string) {
	for _, k := range keys {
		m.Update(edKey(k))
	}
}

func edText(m tea.Model, text string) {
	for _, r := range text {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// selectRow moves the cursor to gr.
func selectRow(t *testing.T, m *VisibilityEditorModel, gr schema.GroupResource) {
	t.Helper()
	for i, idx := range m.visible {
		if m.rows[idx].gr == gr {
			m.selected = i
			return
		}
	}
	t.Fatalf("no row for %s", gr)
}

func TestVisibilityEditorSpaceCyclesWorkMarks(t *testing.T) {
	t.Parallel()
	m := newTestEditor(t, appconfig.VisibilityProfileWork, visibility.Work(), appconfig.ResourcesVisibilityConfig{})
	selectRow(t, m, grLeases)
	steps := []struct {
		mark       string
		show, hide []schema.GroupResource
		listed     bool
	}{
		{mark: " + ", show: []schema.GroupResource{grLeases}, listed: true},
		{mark: " - ", hide: []schema.GroupResource{grLeases}},
		{mark: "   "},
	}
	for i, step := range steps {
		edKeys(m, "space")
		_, p := m.Profile()
		if got := m.draft.mark(grLeases); got != step.mark {
			t.Fatalf("step %d: mark %q, want %q", i, got, step.mark)
		}
		if !slices.Equal(p.Show, step.show) || !slices.Equal(p.Hide, step.hide) {
			t.Fatalf("step %d: profile %+v", i, p)
		}
		if d, _ := m.set.Decide(grLeases); d.Included != step.listed {
			t.Fatalf("step %d: leases listed = %v", i, d.Included)
		}
	}
	if m.Edited() {
		t.Fatalf("back to Work's choices, the draft still counts as edited")
	}
}

// With "Include empty: No" a type the profile lists by its own rules has
// a row only while it has objects, and Listed says so; a type selected by
// name is listed in any case.
func TestVisibilityEditorShowsTheEmptyFilter(t *testing.T) {
	t.Parallel()
	m := newTestEditor(t, appconfig.VisibilityProfileWork, visibility.Work(), appconfig.ResourcesVisibilityConfig{})
	m.hideEmpty = true
	selectRow(t, m, grLeases)
	edKeys(m, "space")
	tests := []struct {
		gr     schema.GroupResource
		listed string
	}{
		{gr: grPods, listed: "if any"},
		{gr: grLeases, listed: "yes"},
		{gr: grCRDs, listed: "no"},
	}
	for _, tc := range tests {
		r := m.rows[slices.IndexFunc(m.rows, func(r visibilityRow) bool { return r.gr == tc.gr })]
		if listed, _, _ := m.effect(r); listed != tc.listed {
			t.Errorf("%s: Listed %q, want %q", tc.gr, listed, tc.listed)
		}
	}
	m.hideEmpty = false
	if listed, _, _ := m.effect(m.rows[slices.IndexFunc(m.rows, func(r visibilityRow) bool { return r.gr == grPods })]); listed != "yes" {
		t.Errorf("with empty types included pods say %q, want yes", listed)
	}
}

// A manual profile starts empty: switching from Work to Only selected
// does not carry Work's marks over, and switching back keeps them.
func TestVisibilityEditorModesKeepTheirOwnMarks(t *testing.T) {
	t.Parallel()
	m := newTestEditor(t, appconfig.VisibilityProfileWork, visibility.Profile{Mode: visibility.ModeWork, Show: []schema.GroupResource{grLeases}}, appconfig.ResourcesVisibilityConfig{})
	edKeys(m, "tab", "tab")
	name, p := m.Profile()
	if p.Mode != visibility.ModeOnly || len(p.Show) != 0 || name != visibilityCustomName {
		t.Fatalf("Only selected after Work = %s %+v, want an empty custom profile", name, p)
	}
	if d, _ := m.set.Decide(grPods); d.Included {
		t.Fatalf("an empty Only profile lists pods")
	}
	selectRow(t, m, grCRDs)
	edKeys(m, "space")
	if _, p := m.Profile(); !slices.Equal(p.Show, []schema.GroupResource{grCRDs}) {
		t.Fatalf("Only after a mark = %+v", p)
	}
	edKeys(m, "shift+tab", "shift+tab")
	if name, p := m.Profile(); p.Mode != visibility.ModeWork || !slices.Equal(p.Show, []schema.GroupResource{grLeases}) || name != appconfig.VisibilityProfileWork {
		t.Fatalf("back in Work = %s %+v, want Work +leases", name, p)
	}
	edKeys(m, "f4", "f4", "f4")
	if name, p := m.Profile(); p.Mode != visibility.ModeAll || name != appconfig.VisibilityProfileAll || m.Edited() {
		t.Fatalf("All = %s %+v edited=%v, want the built-in All", name, p, m.Edited())
	}
}

func TestVisibilityEditorBulkChangesTheFilteredTypes(t *testing.T) {
	t.Parallel()
	m := newTestEditor(t, "app", visibility.Profile{Mode: visibility.ModeOnly}, appconfig.ResourcesVisibilityConfig{})
	edText(m, "es") // leases and customresourcedefinitions
	if len(m.visible) != 2 {
		t.Fatalf("filter es shows %d rows, want 2", len(m.visible))
	}
	if hints := m.FooterHints(); hints[4].Enabled {
		t.Fatalf("F6 Hide is enabled in Only selected")
	}
	edKeys(m, "f6") // no effect outside Work
	edKeys(m, "f5")
	if _, p := m.Profile(); !slices.Equal(p.Show, []schema.GroupResource{grCRDs, grLeases}) {
		t.Fatalf("F5 checked %v, want CRDs and leases", p.Show)
	}
	edKeys(m, "backspace", "backspace")
	edText(m, "lease")
	edKeys(m, "f8")
	if _, p := m.Profile(); !slices.Equal(p.Show, []schema.GroupResource{grCRDs}) {
		t.Fatalf("F8 on leases left %v, want CRDs", p.Show)
	}

	if s := ansi.Strip(m.View().Content); !strings.Contains(s, "1 match the filter") {
		t.Fatalf("the summary does not count the filtered types:\n%s", s)
	}

	w := newTestEditor(t, appconfig.VisibilityProfileWork, visibility.Work(), appconfig.ResourcesVisibilityConfig{})
	edText(w, "po")
	edKeys(w, "f6")
	if _, p := w.Profile(); !slices.Equal(p.Hide, []schema.GroupResource{grPods}) {
		t.Fatalf("F6 in Work hid %v, want pods", p.Hide)
	}
	edKeys(w, "f8")
	if _, p := w.Profile(); len(p.Hide)+len(p.Show) != 0 {
		t.Fatalf("F8 in Work left %+v", p)
	}
}

// Selectors whose types discovery does not report stay in the profile,
// labeled, and F5/F8 never touch them.
func TestVisibilityEditorKeepsUndiscoveredSelectors(t *testing.T) {
	t.Parallel()
	p := visibility.Profile{Mode: visibility.ModeOnly, Show: []schema.GroupResource{grPods, grWidgets, {Group: "failed.example.com", Resource: "gadgets"}}}
	m := NewVisibilityEditorModel("kind-dev", appconfig.EffectiveVisibility{Name: "app", Profile: p}, appconfig.ResourcesVisibilityConfig{},
		models.ContextTypes{Entries: editorTypes().Entries, FailedGroups: []string{"failed.example.com"}})
	m.SetDimensions(90, 16)
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"widgets.example.com", "not discovered", "gadgets.failed.example.com", "discovery failed", "2 selected not discovered", "discovery failed for failed.example.com"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	edKeys(m, "f8")
	if _, got := m.Profile(); !slices.Equal(got.Show, []schema.GroupResource{{Group: "example.com", Resource: "widgets"}, {Group: "failed.example.com", Resource: "gadgets"}}) {
		t.Fatalf("after F8 the profile shows %v, want the undiscovered selectors kept", got.Show)
	}
	// A retained selector can still be unchecked by hand.
	selectRow(t, m, grWidgets)
	edKeys(m, "space")
	if _, got := m.Profile(); slices.Contains(got.Show, grWidgets) {
		t.Fatalf("Space did not uncheck widgets: %v", got.Show)
	}
}

func TestVisibilityEditorNamesAndTitle(t *testing.T) {
	t.Parallel()
	saved := appconfig.ResourcesVisibilityConfig{Profiles: map[string]appconfig.VisibilityProfileConfig{
		"app": {Mode: "only", Show: []appconfig.TypeSelector{{Resource: "pods"}}},
	}}
	m := newTestEditor(t, "app", visibility.Profile{Mode: visibility.ModeOnly, Show: []schema.GroupResource{grPods}}, saved)
	if m.Edited() || !strings.Contains(ansi.Strip(m.View().Content), "Profile: app   Mode: Only selected   Context: kind-dev") {
		t.Fatalf("header:\n%s", ansi.Strip(m.View().Content))
	}
	selectRow(t, m, grLeases)
	edKeys(m, "insert")
	if !m.Edited() || !strings.Contains(ansi.Strip(m.View().Content), "Profile: app*") {
		t.Fatalf("an unsaved change is not marked:\n%s", ansi.Strip(m.View().Content))
	}
	if cur, _ := m.current(); cur.gr == grLeases {
		t.Fatalf("Insert did not move to the next row")
	}
	if err := m.LoadProfile("app"); err != nil || m.Edited() {
		t.Fatalf("LoadProfile(app) = %v, edited %v", err, m.Edited())
	}
	if err := m.LoadProfile("missing"); err == nil {
		t.Fatalf("loading a missing profile worked")
	}
}

// The editor fits the dialog of an 80x24 screen (74x17 content): no line
// is wider, Version gives way to the name, and the name keeps 30 cells.
func TestVisibilityEditorLayout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		width, height int
		want, not     []string
	}{
		{name: "80x24", width: 74, height: 17, want: []string{"Type", "Scope", "Listed", "Why", "customresourcedefinitions.apiextens…", "cluster", "not in Work"}, not: []string{"Version"}},
		{name: "wide", width: 104, height: 30, want: []string{"Version", "customresourcedefinitions.apiextensions.k8s.io", "in Work"}},
		{name: "120x30", width: 104, height: 23, want: []string{"Version", "Scope", "Listed", "customresourcedefinitions.apiextensions.k8s.io"}},
		{name: "narrow", width: 40, height: 10, want: []string{"Type", "Why"}, not: []string{"Listed"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newTestEditor(t, appconfig.VisibilityProfileWork, visibility.Work(), appconfig.ResourcesVisibilityConfig{})
			m.SetDimensions(tc.width, tc.height)
			view := ansi.Strip(m.View().Content)
			lines := strings.Split(view, "\n")
			if len(lines) != tc.height {
				t.Fatalf("%d lines, want %d:\n%s", len(lines), tc.height, view)
			}
			for _, line := range lines {
				if w := lipgloss.Width(line); w != tc.width {
					t.Errorf("line %q is %d wide, want %d", line, w, tc.width)
				}
			}
			for _, w := range tc.want {
				if !strings.Contains(view, w) {
					t.Errorf("view lacks %q:\n%s", w, view)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(view, n) {
					t.Errorf("view has %q:\n%s", n, view)
				}
			}
		})
	}
}

func TestVisibilityEditorMouse(t *testing.T) {
	t.Parallel()
	m := newTestEditor(t, "app", visibility.Profile{Mode: visibility.ModeOnly}, appconfig.ResourcesVisibilityConfig{})
	m.View()
	click := func(x, y int) {
		m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	}
	// Rows sort by name: customresourcedefinitions, leases, pods.
	click(20, visibilityEditorHeader+1)
	if cur, _ := m.current(); cur.gr != grLeases {
		t.Fatalf("a click selected %v, want leases", cur.gr)
	}
	if _, p := m.Profile(); len(p.Show) != 0 {
		t.Fatalf("the first click on a row toggled it")
	}
	click(20, visibilityEditorHeader+1)
	click(1, visibilityEditorHeader+2)
	if _, p := m.Profile(); !slices.Equal(p.Show, []schema.GroupResource{grPods, grLeases}) {
		t.Fatalf("clicks checked %v, want pods and leases", p.Show)
	}
	click(m.modeHit[0], 0)
	if m.draft.mode != visibility.ModeAll {
		t.Fatalf("a click on the mode gave %s, want all", m.draft.mode)
	}
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.selected != 2 {
		t.Fatalf("the wheel moved to %d, want 2", m.selected)
	}
}

func TestVisibilityEditorKeysCallBack(t *testing.T) {
	t.Parallel()
	m := newTestEditor(t, appconfig.VisibilityProfileWork, visibility.Work(), appconfig.ResourcesVisibilityConfig{})
	var applied, saved string
	m.onApply = func(name string, p visibility.Profile) tea.Cmd { applied = name + ":" + string(p.Mode); return nil }
	m.onSave = func(name string, p visibility.Profile) tea.Cmd { saved = name + ":" + string(p.Mode); return nil }
	edKeys(m, "tab", "enter", "f2")
	if applied != "custom:hide" || saved != "custom:hide" {
		t.Fatalf("Enter gave %q and F2 %q, want custom:hide", applied, saved)
	}
	edText(m, "zz")
	if handled, _ := m.HandleModalEscape(tea.KeyPressMsg{Code: tea.KeyEsc}); !handled || m.filter != "" {
		t.Fatalf("Esc did not clear the filter")
	}
	if handled, _ := m.HandleModalEscape(tea.KeyPressMsg{Code: tea.KeyEsc}); handled {
		t.Fatalf("Esc without a filter did not close")
	}
}

func TestVisibilityEditorShowsTheOpenedError(t *testing.T) {
	t.Parallel()
	opened := appconfig.EffectiveVisibility{Name: "gone", Profile: visibility.Work(), Err: errors.New(`profile "gone" is not defined`)}
	m := NewVisibilityEditorModel("", opened, appconfig.ResourcesVisibilityConfig{}, editorTypes())
	m.SetDimensions(90, 12)
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, `profile "gone" is not defined; listing Work`) {
		t.Fatalf("view lacks the error:\n%s", view)
	}
}

func TestSaveNameProblem(t *testing.T) {
	t.Parallel()
	work, only := visibility.Work(), visibility.Profile{Mode: visibility.ModeOnly}
	tests := []struct {
		name    string
		profile visibility.Profile
		ok      bool
	}{
		{name: "app", profile: only, ok: true},
		{name: "work", profile: work, ok: true},
		{name: "work", profile: only},
		{name: "all", profile: only},
		{name: "", profile: only},
		{name: "My App", profile: only},
	}
	for _, tc := range tests {
		if got := saveNameProblem(tc.name, tc.profile); (got == "") != tc.ok {
			t.Errorf("saveNameProblem(%q, %s) = %q", tc.name, tc.profile.Mode, got)
		}
	}
}

func TestVisibilitySaveRequestChange(t *testing.T) {
	t.Parallel()
	only := visibility.Profile{Mode: visibility.ModeOnly, Show: []schema.GroupResource{grPods}}
	tests := []struct {
		name        string
		start       appconfig.ResourcesVisibilityConfig
		req         visibilitySaveRequest
		wantCtx     string
		wantBound   bool
		wantDefault string
	}{
		{
			name:        "bind and default",
			req:         visibilitySaveRequest{name: "app", profile: only, context: "prod", bindContext: true, globalDefault: true},
			wantCtx:     "app",
			wantBound:   true,
			wantDefault: "app",
		},
		{
			name:        "unbind and drop the default",
			start:       appconfig.ResourcesVisibilityConfig{DefaultProfile: "app", Contexts: map[string]string{"prod": "app"}},
			req:         visibilitySaveRequest{name: "app", profile: only, context: "prod"},
			wantCtx:     "",
			wantBound:   true,
			wantDefault: appconfig.VisibilityProfileWork,
		},
		{
			name: "other bindings stay",
			start: appconfig.ResourcesVisibilityConfig{
				DefaultProfile: "platform",
				Contexts:       map[string]string{"prod": "platform"},
				Profiles:       map[string]appconfig.VisibilityProfileConfig{"platform": {Mode: "all"}},
			},
			req:         visibilitySaveRequest{name: "app", profile: only, context: "prod"},
			wantCtx:     "platform",
			wantBound:   true,
			wantDefault: "platform",
		},
		{
			name:        "no context",
			req:         visibilitySaveRequest{name: "app", profile: only},
			wantDefault: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := tc.start.Clone()
			tc.req.change(&v)
			if got := v.Profiles["app"]; got.Mode != "only" || len(got.Show) != 1 || got.Show[0].Resource != "pods" {
				t.Fatalf("saved profile = %+v", got)
			}
			if bound, ok := v.Contexts["prod"]; bound != tc.wantCtx || ok != tc.wantBound {
				t.Fatalf("contexts = %v, want prod=%q", v.Contexts, tc.wantCtx)
			}
			if v.DefaultProfile != tc.wantDefault {
				t.Fatalf("default = %q, want %q", v.DefaultProfile, tc.wantDefault)
			}
			if errs := v.Validate(); len(errs) > 0 {
				t.Fatalf("the saved settings do not validate: %v", errs)
			}
		})
	}
}

func TestVisibilitySaveDialog(t *testing.T) {
	t.Parallel()
	only := visibility.Profile{Mode: visibility.ModeOnly, Show: []schema.GroupResource{grPods}}
	saved := appconfig.ResourcesVisibilityConfig{
		Profiles: map[string]appconfig.VisibilityProfileConfig{"app": appconfig.ProfileConfig(only)},
		Contexts: map[string]string{"prod": "app"},
	}
	m := NewVisibilitySaveModel("app", only, saved, "prod")
	if m.input.Value() != "app" || !m.bindContext || m.globalDefault {
		t.Fatalf("dialog starts with %q bind=%v default=%v", m.input.Value(), m.bindContext, m.globalDefault)
	}
	if fresh := NewVisibilitySaveModel(visibilityCustomName, only, saved, "prod"); fresh.input.Value() != "" {
		t.Fatalf("an unsaved custom profile suggests %q", fresh.input.Value())
	}
	work := visibility.Profile{Mode: visibility.ModeWork, Show: []schema.GroupResource{grPods}}
	if other := NewVisibilitySaveModel("app", work, saved, "prod"); other.input.Value() != "" || other.bindContext {
		t.Fatalf("a Work draft of the Only profile app suggests %q bind=%v", other.input.Value(), other.bindContext)
	}
	if plain := NewVisibilitySaveModel(appconfig.VisibilityProfileWork, work, saved, "prod"); plain.input.Value() != appconfig.VisibilityProfileWork {
		t.Fatalf("changes to Work suggest %q", plain.input.Value())
	}
	var got visibilitySaveRequest
	m.onSave = func(r visibilitySaveRequest) tea.Cmd { got = r; return nil }
	m.SetDimensions(60, 9)
	m.FocusName()
	m.input.SetValue("All")
	m.Update(edKey("enter"))
	if m.Error() == "" || got.name != "" {
		t.Fatalf("saved under All: error %q, request %+v", m.Error(), got)
	}
	m.input.SetValue("team-a")
	m.Update(edKey("enter"))
	if got.name != "team-a" || !got.bindContext || got.context != "prod" {
		t.Fatalf("request = %+v", got)
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Use for context prod") || !strings.Contains(view, "Use as the global default") {
		t.Fatalf("view:\n%s", view)
	}
}
