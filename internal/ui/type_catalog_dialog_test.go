package ui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/ui/panelcontent"
	"github.com/stokaro/kc/internal/visibility"
	"github.com/stokaro/kc/pkg/appconfig"
)

func catalogEntries() []models.TypeEntry {
	return []models.TypeEntry{
		{
			ID: "/v1/pods", GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"},
			Kind: "Pod", ShortNames: []string{"po"}, Namespaced: true,
			Decision: visibility.Decision{Included: true, Reason: visibility.ReasonWork},
		},
		{
			ID: "coordination.k8s.io/v1/leases", GVR: schema.GroupVersionResource{Group: "coordination.k8s.io", Version: "v1", Resource: "leases"},
			Kind: "Lease", Namespaced: true,
			Decision: visibility.Decision{Reason: visibility.ReasonNotInWork},
		},
		{
			ID: "apiextensions.k8s.io/v1/customresourcedefinitions", GVR: schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"},
			Kind: "CustomResourceDefinition", ShortNames: []string{"crd", "crds"},
			Decision: visibility.Decision{Reason: visibility.ReasonNotInWork},
		},
	}
}

var (
	workVisibility = appconfig.EffectiveVisibility{Name: appconfig.VisibilityProfileWork, Profile: visibility.Work()}
	allVisibility  = appconfig.EffectiveVisibility{Name: appconfig.VisibilityProfileAll, Profile: visibility.Profile{Mode: visibility.ModeAll}}
)

func TestTypeCatalogChoices(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		profile   appconfig.EffectiveVisibility
		otherOnly bool
		notes     []string
	}{
		{name: "all types under Work", profile: workVisibility, notes: []string{"Work", "other", "cluster  other"}},
		{name: "other types", profile: workVisibility, otherOnly: true, notes: []string{"", "", "cluster"}},
		{name: "all types under All", profile: allVisibility, notes: []string{"", "", "cluster"}},
		{name: "all types under a named profile", profile: appconfig.EffectiveVisibility{Name: "app", Edited: true, Profile: visibility.Profile{Mode: visibility.ModeOnly}}, notes: []string{"app*", "other", "cluster  other"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := typeCatalogChoices(catalogEntries(), tc.profile, tc.otherOnly)
			if len(got) != len(tc.notes) {
				t.Fatalf("got %d choices, want %d", len(got), len(tc.notes))
			}
			for i, c := range got {
				if c.Note != tc.notes[i] {
					t.Errorf("choice %d note = %q, want %q", i, c.Note, tc.notes[i])
				}
				if c.Value != catalogEntries()[i].ID {
					t.Errorf("choice %d value = %q", i, c.Value)
				}
			}
			if got[0].Label != "pods" || got[0].Detail != "v1" || got[1].Detail != "coordination.k8s.io/v1" {
				t.Errorf("rows = %+v, want plural names with group/version details", got[:2])
			}
		})
	}
}

func TestTypeCatalogFilterMatchesKindShortNameAndGroup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		filter string
		want   []string
	}{
		{filter: "crd", want: []string{"apiextensions.k8s.io/v1/customresourcedefinitions"}},
		{filter: "lease", want: []string{"coordination.k8s.io/v1/leases"}},
		{filter: "po", want: []string{"/v1/pods"}},
		{filter: "k8s.io", want: []string{"coordination.k8s.io/v1/leases", "apiextensions.k8s.io/v1/customresourcedefinitions"}},
		{filter: "nothing", want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.filter, func(t *testing.T) {
			t.Parallel()
			picker := NewScopePickerModel(typeCatalogChoices(catalogEntries(), workVisibility, false), "", nil, nil)
			for _, r := range tc.filter {
				picker.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
			}
			var got []string
			for i := range picker.visible {
				got = append(got, picker.choiceAt(i).Value)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("filter %q shows %v, want %v", tc.filter, got, tc.want)
			}
		})
	}
}

func TestScopePickerDetailsNeedRoom(t *testing.T) {
	t.Parallel()
	choices := typeCatalogChoices(catalogEntries(), workVisibility, true)
	tests := []struct {
		name  string
		width int
		want  []string
		not   []string
	}{
		// The label column is as wide as the longest name.
		{name: "wide", width: 70, want: []string{" leases" + strings.Repeat(" ", 21) + "coordination.k8s.io/v1", "customresourcedefinitions  apiextensions.k8s.io/v1  cluster"}},
		{name: "cut detail", width: 50, want: []string{"customresourcedefinitions  apiextension…  cluster"}},
		{name: "narrow", width: 36, want: []string{" leases ", "customresourcedefinitions  cluster"}, not: []string{"coordination", " a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := NewScopePickerModel(choices, "", nil, nil)
			p.SetDimensions(tc.width, 6)
			view := ansi.Strip(p.View().Content)
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
			for line := range strings.SplitSeq(view, "\n") {
				if w := lipgloss.Width(line); w != tc.width {
					t.Errorf("line %q is %d wide, want %d", line, w, tc.width)
				}
			}
		})
	}
}

func TestScopePickerNotesLineUp(t *testing.T) {
	t.Parallel()
	p := NewScopePickerModel(typeCatalogChoices(catalogEntries(), workVisibility, false), "", nil, nil)
	p.SetDimensions(70, 6)
	lines := strings.Split(ansi.Strip(p.View().Content), "\n")
	// " " + the label column (25) + "  " + the detail column (23) + "  ".
	const noteAt = 1 + 25 + 2 + 23 + 2
	for i, note := range []string{"Work", "other", "cluster  other"} {
		if got := strings.Index(lines[i], note); got != noteAt {
			t.Errorf("note %q of line %q starts at %d, want %d", note, lines[i], got, noteAt)
		}
	}
}

func TestRenderTypeMark(t *testing.T) {
	t.Parallel()
	dash := "─"
	if got := renderTypeMark("outside Work", dash, 37); !strings.Contains(got, " outside Work ") || lipgloss.Width(got) != len(" outside Work ")+1 {
		t.Fatalf("mark in a 40-column panel = %q", got)
	}
	// It takes at most half of the room, and is left out rather than cut.
	if got := renderTypeMark("outside Work", dash, 29); got != "" {
		t.Fatalf("mark in 29 columns = %q, want none", got)
	}
	if got := renderTypeMark("", dash, 100); got != "" {
		t.Fatalf("no mark drew %q", got)
	}
}

func TestPanelFrameShowsTypeMarkInListMode(t *testing.T) {
	t.Parallel()
	p := NewPanel("left")
	p.SetTypeMark("outside Work")
	frame := p.renderFrame(strings.Repeat(" ", 10), panelcontent.FrameInfo{}, "/leases", 40, 6, true, false)
	top, _, _ := strings.Cut(frame, "\n")
	if !strings.Contains(ansi.Strip(top), "/leases") || !strings.Contains(ansi.Strip(top), " outside Work ──┐") {
		t.Fatalf("top border = %q", ansi.Strip(top))
	}
	if w := lipgloss.Width(top); w != 40 {
		t.Fatalf("top border is %d columns, want 40", w)
	}
	p.mode = PanelModeDescribe
	frame = p.renderFrame(strings.Repeat(" ", 10), panelcontent.FrameInfo{}, "/leases/web-leader", 40, 6, true, false)
	if top, _, _ := strings.Cut(frame, "\n"); strings.Contains(ansi.Strip(top), "outside") {
		t.Fatalf("the mark shows outside List mode")
	}
}

func TestShowTypeCatalogOutsideTypeList(t *testing.T) {
	t.Parallel()
	tr := newJumpTree(true)
	a := newJumpApp(t, tr)
	cmd := a.showTypeCatalog(0, true)
	if cmd == nil {
		t.Fatalf("no message for a panel without a type list")
	}
	if m := a.modalManager.modals[typeCatalogModalKey]; m != nil && m.IsVisible() {
		t.Fatalf("the dialog opened without a type list")
	}
}

func TestViewOptionsTypesRow(t *testing.T) {
	t.Parallel()
	m := NewViewOptionsModel(ViewOptionsConfig{
		SkipPanelSection: true,
		Resources:        &ViewOptionsResourcesConfig{ShowTypes: true, TypeProfile: "work", ShowInclude: true},
	})
	m.focus = slices.IndexFunc(m.entries, func(e viewOptionEntry) bool {
		return e.kind == viewOptionEntryOption && e.option == viewOptionResourceTypes
	})
	if m.focus < 0 {
		t.Fatalf("no Types row on a type list")
	}
	if label, value := m.optionLabelAndValue(viewOptionResourceTypes); label != "Types" || value != "Work" {
		t.Fatalf("row = %s: %s, want Types: Work", label, value)
	}
	m.adjustCurrent(1)
	msg := m.commit(true, false)
	if msg.Resources == nil || !msg.Resources.HasTypes || msg.Resources.TypeProfile != appconfig.VisibilityProfileAll {
		t.Fatalf("payload = %+v, want Types All", msg.Resources)
	}

	without := NewViewOptionsModel(ViewOptionsConfig{SkipPanelSection: true, Resources: &ViewOptionsResourcesConfig{ShowInclude: true}})
	if slices.ContainsFunc(without.entries, func(e viewOptionEntry) bool {
		return e.kind == viewOptionEntryOption && e.option == viewOptionResourceTypes
	}) {
		t.Fatalf("Types offered where the dialog was not asked for it")
	}
}

func TestResourcesOptionsTypesAppliesToThePanelOnly(t *testing.T) {
	t.Parallel()
	app := NewApp()
	t.Cleanup(app.cancel)
	app.cfg = appconfig.Default()
	app.leftConfig, app.rightConfig = nil, nil
	app.activePanel = 0
	app.modalManager.Show("view_options")
	app.Update(ResourcesOptionsChangedMsg{HasTypes: true, TypeProfile: "all", TypeChanged: true, TableMode: "scroll", Accept: true, Close: true})
	if got := app.panelVisibility(app.leftPanel); got.Name != appconfig.VisibilityProfileAll || got.Source != appconfig.VisibilityFromPanel {
		t.Fatalf("left panel profile = %+v, want all, chosen for the panel", got)
	}
	if got := app.panelVisibility(app.rightPanel).Name; got != appconfig.VisibilityProfileWork {
		t.Fatalf("right panel profile = %q, want work", got)
	}
	if got := app.cfg.Resources.Visibility.DefaultProfile; got != appconfig.VisibilityProfileWork {
		t.Fatalf("Enter without C-s changed the default to %q", got)
	}
}
