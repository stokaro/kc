package ui

import (
	"slices"
	"strings"
	"testing"
)

func TestInfoModeIsAvailable(t *testing.T) {
	app := NewApp()
	if !slices.Contains(app.rightPanel.AvailableModes(), PanelModeInfo) {
		t.Fatalf("modes = %v, want Info", app.rightPanel.AvailableModes())
	}
	if got := modeLabel(PanelModeInfo); got != "Info" {
		t.Fatalf("modeLabel(Info) = %q", got)
	}
	if got := NextPanelMode(PanelModeInfo); got != PanelModeCommand {
		t.Fatalf("NextPanelMode(Info) = %v, want Command", got)
	}
	found := false
	for _, item := range app.panelMenuItems(1) {
		found = found || item.Label == "&Info"
	}
	if !found {
		t.Fatal("the panel menu has no Info item")
	}
	if tgt := app.infoTarget(t.Context(), app.rightPanel); tgt.Open != nil || !strings.Contains(tgt.Hint, "Not connected") {
		t.Fatalf("target without a cluster = %+v", tgt)
	}
}

// TestInfoTargetFollowsTheOtherPanelEnvtest checks that the Info mode of
// the right panel shows the left panel's namespace scope.
func TestInfoTargetFollowsTheOtherPanelEnvtest(t *testing.T) {
	app, cl := setupAppWithCluster(t)
	ns := "info-target"
	t.Cleanup(createNamespace(t, cl, ns))
	waitForNamespaceReady(t, app, ns)
	app.goToNamespace(ns)

	tgt := app.infoTarget(t.Context(), app.rightPanel)
	if tgt.Open == nil || !strings.HasSuffix(tgt.Key, "|"+ns) || !strings.Contains(tgt.Label, ns) {
		t.Fatalf("target = %+v, want the left panel's namespace %q", tgt, ns)
	}
	src := tgt.Open()
	defer src.Close()
	if s := src.Summary(""); s.Err != "" {
		t.Fatalf("summary error: %s", s.Err)
	}
}
