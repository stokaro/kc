package ui

import (
	"testing"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/pkg/appconfig"
)

// Not parallel: it installs the package-wide health config. App.Init in
// other tests installs the default (empty) config, which this test does
// not read back.
func TestLoadHealthConfigReportsProblems(t *testing.T) {
	t.Cleanup(func() { models.InstallHealthAdapters(nil) })
	app := newHotkeyApp(t)
	app.cfg.Health = map[string][]appconfig.HealthRule{
		"example.com/v1/widgets": {{Condition: "Ready", Health: appconfig.HealthOK}},
	}
	if cmd := app.loadHealthConfig(); cmd != nil {
		t.Fatalf("a valid health config must not be reported")
	}
	app.cfg.Health["example.com/v1/gadgets"] = []appconfig.HealthRule{{Condition: "Ready", Health: "green"}}
	if cmd := app.loadHealthConfig(); cmd == nil {
		t.Fatalf("an unknown health must be reported")
	}
	if len(app.cfg.Health["example.com/v1/gadgets"]) != 1 {
		t.Fatalf("the configuration must stay as written")
	}
}
