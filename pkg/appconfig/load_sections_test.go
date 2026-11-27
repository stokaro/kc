package appconfig_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stokaro/kc/pkg/appconfig"
)

// TestLoadKeepsSectionsThatLoad covers files that parse but have a section
// that does not load: that section keeps its defaults as a whole, the
// others load, the error names the field, and a readOnly section that does
// not load makes every context read-only.
func TestLoadKeepsSectionsThatLoad(t *testing.T) {
	t.Parallel()
	defaults := appconfig.Default()
	tests := []struct {
		name string
		file string
		// sections that did not load; nil when nothing of the file loaded.
		wantSections []string
		wantReason   string
		check        func(t *testing.T, cfg *appconfig.Config)
	}{
		{
			// The reproduction of issue 372.
			name:         "an invalid unrelated field keeps readOnly.contexts",
			file:         "readOnly:\n  contexts: [good]\nlayout:\n  rightPanelMode: describ\nviewer:\n  theme: monokai\n",
			wantSections: []string{"layout"},
			wantReason:   `layout.rightPanelMode: unknown panel mode "describ"; use list, describe, manifest, logs or info`,
			check: func(t *testing.T, cfg *appconfig.Config) {
				if !cfg.ReadOnly.For("good") || cfg.ReadOnly.For("other") {
					t.Fatalf("readOnly = %+v, want only good read-only", cfg.ReadOnly)
				}
				if cfg.Layout != defaults.Layout {
					t.Fatalf("layout = %+v, want the defaults", cfg.Layout)
				}
				if cfg.Viewer.Theme != "monokai" {
					t.Fatalf("theme = %q, want the file's monokai", cfg.Viewer.Theme)
				}
			},
		},
		{
			name:         "a type error elsewhere keeps readOnly.enabled",
			file:         "readOnly:\n  enabled: true\nkubernetes:\n  clusters:\n    ttl: 120\n  discovery:\n    refresh: 1m\n",
			wantSections: []string{"kubernetes"},
			wantReason:   "kubernetes.clusters.ttl: want a duration such as 30s or 2m, got 120",
			check: func(t *testing.T, cfg *appconfig.Config) {
				if !cfg.ReadOnly.Enabled {
					t.Fatalf("readOnly = %+v, want the file's enabled: true", cfg.ReadOnly)
				}
				// The section is not mixed: refresh, which decodes, is
				// not taken either.
				if cfg.Kubernetes != defaults.Kubernetes {
					t.Fatalf("kubernetes = %+v, want the defaults", cfg.Kubernetes)
				}
			},
		},
		{
			name:         "a readOnly section that does not load makes every context read-only",
			file:         "readOnly:\n  contexts: prod\nviewer:\n  theme: monokai\n",
			wantSections: []string{"readOnly"},
			wantReason:   `readOnly.contexts: want a list, got "prod"`,
			check: func(t *testing.T, cfg *appconfig.Config) {
				if !cfg.ReadOnly.For("staging") {
					t.Fatalf("readOnly = %+v, want every context read-only", cfg.ReadOnly)
				}
				if cfg.Viewer.Theme != "monokai" {
					t.Fatalf("theme = %q, want the file's monokai", cfg.Viewer.Theme)
				}
			},
		},
		{
			name:         "readOnly that is not a mapping",
			file:         "readOnly: true\n",
			wantSections: []string{"readOnly"},
			wantReason:   "readOnly: want a mapping, got true",
			check:        everyContextReadOnly,
		},
		{
			name:         "readOnly spelled in another case",
			file:         "readonly:\n  enabled: maybe\n",
			wantSections: []string{"readOnly"},
			wantReason:   `readOnly.enabled: want true or false, got "maybe"`,
			check:        everyContextReadOnly,
		},
		{
			// Keeping the command without its bad field would make a
			// dangerous command run in read-only mode.
			name:         "a bad command field drops the user's commands, not the field",
			file:         "commands:\n  - name: Wipe\n    command: kubectl delete ns shop\n    dangerous: yes please\n",
			wantSections: []string{"commands"},
			wantReason:   `commands[0].dangerous: want true or false, got "yes please"`,
			check: func(t *testing.T, cfg *appconfig.Config) {
				if len(cfg.Commands) != len(defaults.Commands) || slices.ContainsFunc(cfg.Commands, func(c appconfig.CommandConfig) bool { return c.Name == "Wipe" }) {
					t.Fatalf("commands = %+v, want the default commands", cfg.Commands)
				}
			},
		},
		{
			name:         "a command with its own decoder names the field once",
			file:         "commands:\n  - name: x\n    command: echo\n    confirm: [1]\n",
			wantSections: []string{"commands"},
			wantReason:   "commands[0].confirm: want true, false or a message, got [1]",
		},
		{
			name:         "a map entry of the wrong type",
			file:         "columns:\n  v1/pods: name\n",
			wantSections: []string{"columns"},
			wantReason:   `columns.v1/pods: want a list, got "name"`,
		},
		{
			name:         "several sections, in the order of the settings",
			file:         "charts:\n  graphics: sixel\nlayout:\n  rightPanelMode: tree\nreadOnly:\n  contexts: [prod]\n",
			wantSections: []string{"layout", "charts"},
			wantReason:   `layout.rightPanelMode: unknown panel mode "tree"; use list, describe, manifest, logs or info; charts.graphics: unknown value "sixel"; use auto, text or kitty`,
			check: func(t *testing.T, cfg *appconfig.Config) {
				if !cfg.ReadOnly.For("prod") || cfg.ReadOnly.For("dev") {
					t.Fatalf("readOnly = %+v, want only prod read-only", cfg.ReadOnly)
				}
				if cfg.Charts != defaults.Charts {
					t.Fatalf("charts = %+v, want the defaults", cfg.Charts)
				}
			},
		},
		{
			name:         "a decode problem and a validation problem",
			file:         "kubernetes:\n  clusters:\n    ttl: 120\nlayout:\n  leftPanelMode: info\n",
			wantSections: []string{"layout", "kubernetes"},
			wantReason:   `layout.leftPanelMode "info" and layout.rightPanelMode "info": one panel must start in list mode`,
		},
		{
			// The second reproduction of issue 372: an indentation slip.
			name:       "not valid YAML",
			file:       "viewer:\n  theme: monokai\nreadOnly:\n  enabled: false\n  contexts: [prod]\n layout:\n  rightPanelMode: info\n",
			wantReason: "yaml: line 5: did not find expected key",
			check: func(t *testing.T, cfg *appconfig.Config) {
				everyContextReadOnly(t, cfg)
				if cfg.Viewer.Theme != defaults.Viewer.Theme {
					t.Fatalf("theme = %q, want the default", cfg.Viewer.Theme)
				}
			},
		},
		{
			name:       "a list at the top",
			file:       "- readOnly\n",
			wantReason: "want a mapping of settings at the top level, got a list",
			check:      everyContextReadOnly,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := writeConfig(t, tc.file, 0o600)
			cfg, err := appconfig.NewStore(p).Load()
			le, ok := errors.AsType[*appconfig.LoadError](err)
			if !ok {
				t.Fatalf("Load error = %v, want a *LoadError", err)
			}
			if le.Path != p || !strings.HasPrefix(err.Error(), p+": ") {
				t.Fatalf("error %q does not start with %s", err, p)
			}
			if !slices.Equal(le.Sections, tc.wantSections) {
				t.Fatalf("sections = %q, want %q", le.Sections, tc.wantSections)
			}
			if !strings.Contains(le.Reason(), tc.wantReason) {
				t.Fatalf("reason = %q, want it to contain %q", le.Reason(), tc.wantReason)
			}
			if strings.Contains(le.Reason(), "\n") {
				t.Fatalf("reason %q is more than one line", le.Reason())
			}
			wantLost := tc.wantSections == nil || slices.Contains(tc.wantSections, "readOnly")
			if le.ReadOnlyLost() != wantLost {
				t.Fatalf("ReadOnlyLost = %v, want %v", le.ReadOnlyLost(), wantLost)
			}
			if wantLost && !cfg.ReadOnly.For("unlisted-context") {
				t.Fatalf("readOnly = %+v, want every context read-only", cfg.ReadOnly)
			}
			if tc.check != nil {
				tc.check(t, cfg)
			}
		})
	}
}

func everyContextReadOnly(t *testing.T, cfg *appconfig.Config) {
	t.Helper()
	if !cfg.ReadOnly.Enabled {
		t.Fatalf("readOnly = %+v, want every context read-only", cfg.ReadOnly)
	}
}
