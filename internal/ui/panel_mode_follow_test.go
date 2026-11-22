package ui

import (
	"strings"
	"testing"

	models "github.com/stokaro/kc/internal/models"
	modeltesting "github.com/stokaro/kc/internal/models/testing"
	navui "github.com/stokaro/kc/internal/navigation"
	"github.com/stokaro/kc/internal/table"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// TestPanelModeShowsOtherPanelSelection checks that a panel switched to
// Describe or Manifest shows the other panel's selection at once, not its
// own cursor row.
func TestPanelModeShowsOtherPanelSelection(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		mode PanelViewMode
		bad  []string
	}{
		{"describe", PanelModeDescribe, []string{"Select a resource to describe."}},
		{"manifest", PanelModeManifest, []string{"Select a resource to view its manifest.", "does not expose manifest content"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			a := NewApp()
			t.Cleanup(a.cancel)
			a.width, a.height = 100, 30

			pods := modeltesting.NewStaticFolder("pods", []table.Column{{Title: " Name"}}, []table.Row{testPodItem()})
			root := modeltesting.NewStaticFolder("/", []table.Column{{Title: " Name"}}, []table.Row{
				modeltesting.NewEnterableItem("overview", []string{"overview"}, []string{"overview"}, func() (models.Folder, error) {
					return modeltesting.NewStaticFolder("overview", nil, nil), nil
				}, models.WhiteStyle()),
			})
			a.leftNav = navui.NewNavigator(pods)
			a.rightNav = navui.NewNavigator(root)
			a.leftPanel.UseFolder(ctx, true)
			a.rightPanel.UseFolder(ctx, true)
			a.syncPanelWithNavigator(0)
			a.syncPanelWithNavigator(1)
			a.leftPanel.SelectByRowID(ctx, "web")
			a.rightPanel.SelectByRowID(ctx, "overview")

			a.setPanelMode(ctx, a.rightPanel, tc.mode, "test")

			got := a.rightPanel.ensureActiveWidget(ctx).View(ctx, panelcontent.Frame{Size: panelcontent.Size{Width: 80, Height: 10}}).Content
			for _, bad := range tc.bad {
				if strings.Contains(got, bad) {
					t.Fatalf("%s shows the panel's own row:\n%s", tc.name, got)
				}
			}
			if sel := a.rightPanel.CurrentSelection(ctx); sel.Item == nil || selectionRowID(sel.Item) != "web" {
				t.Fatalf("right panel selection = %+v, want the pod web", sel)
			}
		})
	}
}

func selectionRowID(item models.Item) string {
	id, _, _, _ := item.Columns()
	return id
}
