package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/sttts/kc/internal/models"
)

// leftOverviewScope returns the namespace scope of the overview the left
// panel shows, and fails when it shows something else.
func leftOverviewScope(t *testing.T, app *App) string {
	t.Helper()
	if got := app.leftPanel.GetCurrentPath(); got != "/overview" {
		t.Fatalf("left panel path = %q, want /overview", got)
	}
	f, ok := app.leftNav.Current().(*models.OverviewFolder)
	if !ok {
		t.Fatalf("left folder = %T, want *models.OverviewFolder", app.leftNav.Current())
	}
	t.Cleanup(f.Close)
	return f.NamespaceScope()
}

// TestStartupIntentOverviewEnvtest mirrors kc -n x overview and
// kc -A overview: the left panel opens /overview in that scope.
func TestStartupIntentOverviewEnvtest(t *testing.T) {
	app, cl := setupAppWithCluster(t)
	ns := "intent-overview"
	t.Cleanup(createNamespace(t, cl, ns))
	waitForNamespaceReady(t, app, ns)
	app.goToNamespace("default")

	app.startupIntent = StartupIntent{Verb: KubectlVerbOverview, Namespace: ns}
	app.startupIntentApplied = false
	runStartupSelect(app, app.applyStartupIntent())
	if got := leftOverviewScope(t, app); got != ns {
		t.Fatalf("overview scope = %q, want %q", got, ns)
	}

	app.allNamespaces = true
	app.startupIntent = StartupIntent{Verb: KubectlVerbOverview}
	app.startupIntentApplied = false
	runStartupSelect(app, app.applyStartupIntent())
	if got := leftOverviewScope(t, app); got != models.AllNamespaces {
		t.Fatalf("overview scope with -A = %q, want all namespaces", got)
	}
}

// TestOverviewKeyOpensTheListNamespaceEnvtest presses M-i in a namespace's
// pod list: the panel opens /overview for that namespace.
func TestOverviewKeyOpensTheListNamespaceEnvtest(t *testing.T) {
	app, cl := setupAppWithCluster(t)
	ns := "overview-key"
	t.Cleanup(createNamespace(t, cl, ns))
	waitForNamespaceReady(t, app, ns)
	app.goToNamespace(ns)
	app.setActivePanel(0, "test")

	if got := app.leftPanel.GetCurrentPath(); got == "/overview" {
		t.Fatalf("left panel starts in %q", got)
	}
	// The walk may finish at once (no command) or wait for rows.
	_, cmd := app.Update(tea.KeyPressMsg{Code: 'i', Mod: tea.ModAlt})
	runStartupSelect(app, cmd)
	if got := leftOverviewScope(t, app); got != ns {
		t.Fatalf("overview scope = %q, want %q", got, ns)
	}
}
