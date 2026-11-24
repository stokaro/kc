package ui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	coordinationv1 "k8s.io/api/coordination/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/models"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/pkg/appconfig"
)

var leasesGVR = schema.GroupVersionResource{Group: "coordination.k8s.io", Version: "v1", Resource: "leases"}

func typeCatalogPicker(t *testing.T, a *App) (*Modal, *ScopePickerModel) {
	t.Helper()
	modal := a.modalManager.modals[typeCatalogModalKey]
	if modal == nil || !modal.IsVisible() {
		t.Fatalf("the type dialog is not open")
	}
	return modal, modal.content.(*ScopePickerModel)
}

func TestOtherTypesDialogOpensTypeOutsideWork(t *testing.T) {
	app, nsA, _ := newHiddenTypeApp(t)
	ctx := t.Context()
	root := app.leftNav.Current()
	root.Len(ctx)
	if _, ok := root.ItemByID(ctx, models.OtherTypesRowID); !ok {
		t.Fatalf("the root has no other types row")
	}
	leasesID := resourceRowID(nsA, leasesGVR, true)
	if _, ok := root.ItemByID(ctx, leasesID); ok {
		t.Fatalf("the root lists leases, which Work leaves out")
	}

	_, cmd := app.Update(models.OpenTypeCatalogRequest{OtherOnly: true})
	if cmd != nil {
		t.Fatalf("opening the dialog reported %v", cmd())
	}
	modal, picker := typeCatalogPicker(t, app)
	if !strings.HasPrefix(modal.title, "Other types (") {
		t.Fatalf("dialog title = %q", modal.title)
	}
	var values []string
	for _, c := range picker.choices {
		values = append(values, c.Value)
	}
	if !slices.Contains(values, leasesID) || slices.Contains(values, resourceRowID(nsA, jobsGVR, true)) {
		t.Fatalf("other types = %v, want leases and not jobs", values)
	}

	for _, r := range "lease" {
		picker.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	_, cmd = picker.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	runJump(t, app, cmd)
	atList(t, app, leasesGVR, nsA)
	if modal.IsVisible() {
		t.Fatalf("the dialog stayed open")
	}
	// The dialog is not a step: the location names the type itself.
	if got := app.leftNav.Steps(); !slices.Equal(got, []string{leasesID}) {
		t.Fatalf("steps = %v, want [%s]", got, leasesID)
	}
	if app.leftPanel.typeMark != "outside Work" {
		t.Fatalf("mark = %q, want outside Work", app.leftPanel.typeMark)
	}

	// Under All the type is listed: no mark.
	app.setPanelTypeProfile(app.leftPanel, appconfig.VisibilityProfileAll)
	app.applyResourceOptions(app.leftPanel)
	if app.leftPanel.typeMark != "" {
		t.Fatalf("mark under All = %q", app.leftPanel.typeMark)
	}
	app.setPanelTypeProfile(app.leftPanel, appconfig.VisibilityProfileWork)
	app.applyResourceOptions(app.leftPanel)
	if app.leftPanel.typeMark != "outside Work" {
		t.Fatalf("mark back under Work = %q", app.leftPanel.typeMark)
	}

	app.handleFolderNav(true, "", nil)
	if app.leftNav.HasBack() || app.leftPanel.typeMark != "" {
		t.Fatalf("after Back: at %v with mark %q, want the root and no mark", app.leftNav.Current().Path(), app.leftPanel.typeMark)
	}
}

// Work decides before counts start: a type it leaves out gets no count
// even when it has objects, while the listed types are counted.
func TestWorkProfileDoesNotCountOtherTypes(t *testing.T) {
	app, nsA, _ := newHiddenTypeApp(t)
	ctx := t.Context()
	lease := &coordinationv1.Lease{}
	lease.Name, lease.Namespace = "held", nsA
	if err := app.cl.GetClient().Create(ctx, lease); err != nil {
		t.Fatalf("create lease: %v", err)
	}
	root := app.leftNav.Current()
	root.(interface{ Refresh() }).Refresh()
	root.Len(ctx)
	finder := root.(models.ExplicitFinder)
	counted := func(id string) bool {
		item, ok := finder.ExplicitItemByID(ctx, id)
		if !ok {
			t.Fatalf("no item for %s", id)
		}
		_, known := item.(interface{ TryCount() (int, bool) }).TryCount()
		return known
	}
	leasesID := resourceRowID(nsA, leasesGVR, true)
	// Pods start counting together with every listed type; a leases count
	// started with them would be known within a second of theirs.
	kctesting.Eventually(t, 10*time.Second, 50*time.Millisecond, func() bool {
		root.Len(ctx)
		return counted(resourceRowID(nsA, podsGVR, true))
	})
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if counted(leasesID) {
			t.Fatalf("leases were counted although Work leaves them out")
		}
	}

	// Control: listed under All, the same type is counted.
	app.setPanelTypeProfile(app.leftPanel, appconfig.VisibilityProfileAll)
	app.applyResourceOptions(app.leftPanel)
	kctesting.Eventually(t, 10*time.Second, 50*time.Millisecond, func() bool {
		root.Len(ctx)
		return counted(leasesID)
	})
}
