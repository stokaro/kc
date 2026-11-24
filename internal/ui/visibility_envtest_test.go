package ui

import (
	"path/filepath"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/models"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/pkg/appconfig"
)

// The editor lists the context's types in both scopes. An Only profile
// applied with Enter lists its member at once, empty and before its count
// is known, while "Include empty" is No, and leaves every other type out.
// Saving it stores the profile and lists the panel under the saved name.
func TestVisibilityEditorOnlyProfileListsEmptyMembers(t *testing.T) {
	app, nsA, _ := newHiddenTypeApp(t)
	app.cfgStore = appconfig.NewStore(filepath.Join(t.TempDir(), "config.yaml"))
	ctx := t.Context()

	if cmd := app.showVisibilityEditor(0); cmd != nil {
		t.Fatalf("opening the editor reported %v", cmd())
	}
	modal := app.modalManager.modals[visibilityEditorModalKey]
	if modal == nil || !modal.IsVisible() {
		t.Fatalf("the editor is not open")
	}
	editor := modal.content.(*VisibilityEditorModel)
	var namespaced, cluster bool
	for _, r := range editor.rows {
		namespaced = namespaced || r.gr == leasesGVR.GroupResource()
		cluster = cluster || r.gr == schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"}
	}
	if !namespaced || !cluster {
		t.Fatalf("the editor lacks leases (%v) or CRDs (%v)", namespaced, cluster)
	}

	edKeys(editor, "tab", "tab")
	edText(editor, "leases.coordination")
	edKeys(editor, "f5", "enter")
	if modal.IsVisible() {
		t.Fatalf("Enter did not close the editor")
	}
	if got := app.panelVisibility(app.leftPanel); got.Title() != "custom*" || got.Profile.Mode != "only" {
		t.Fatalf("panel profile = %+v", got)
	}

	root := app.leftNav.Current()
	root.Len(ctx)
	leasesID := resourceRowID(nsA, leasesGVR, true)
	item, ok := root.ItemByID(ctx, leasesID)
	if !ok {
		t.Fatalf("the Only member leases is not listed while it counts")
	}
	for _, id := range []string{resourceRowID(nsA, podsGVRForTest, true), resourceRowID(nsA, jobsGVR, true)} {
		if _, listed := root.ItemByID(ctx, id); listed {
			t.Fatalf("%s is listed outside the Only profile", id)
		}
	}
	if other, ok := root.ItemByID(ctx, models.OtherTypesRowID); !ok {
		t.Fatalf("no other types row")
	} else if _, cells, _, _ := other.Columns(); cells[1] != "outside custom*" {
		t.Fatalf("other types row = %q", cells)
	}
	kctesting.Eventually(t, 10*time.Second, 50*time.Millisecond, func() bool {
		root.Len(ctx)
		if it, ok := root.ItemByID(ctx, leasesID); ok {
			item = it
		}
		_, cells, _, _ := item.Columns()
		return cells[2] == "0"
	})

	edKeys(editor, "f2")
	saveModal := app.modalManager.modals[visibilitySaveModalKey]
	if saveModal == nil || !saveModal.IsVisible() {
		t.Fatalf("F2 did not open the save dialog")
	}
	save := saveModal.content.(*VisibilitySaveModel)
	save.input.SetValue("leases-only")
	_, cmd := save.Update(edKey("enter"))
	saved, ok := findMsg[configSavedMsg](runCmd(cmd))
	if !ok || saved.err != nil {
		t.Fatalf("save = %+v %v", saved, ok)
	}
	if got := app.panelVisibility(app.leftPanel); got.Title() != "leases-only" {
		t.Fatalf("after save the panel lists under %q", got.Title())
	}
	if pc, ok := app.cfg.Resources.Visibility.Profiles["leases-only"]; !ok || pc.Mode != "only" || len(pc.Show) != 1 {
		t.Fatalf("saved profile = %+v", pc)
	}
}
