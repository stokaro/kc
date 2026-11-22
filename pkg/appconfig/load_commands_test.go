package appconfig_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stokaro/kc/pkg/appconfig"
)

// Not parallel: Load reads ~/.kc/config.yaml, so the test sets HOME.
func TestLoadCommandsDoNotInheritDefaults(t *testing.T) {
	tests := []struct {
		name  string
		file  string
		check func(t *testing.T, cmds []appconfig.CommandConfig)
	}{
		{
			name: "user commands replace the defaults",
			file: "commands:\n  - name: Scale\n    command: echo scale\n    type: sticky\n    confirm: true\n",
			check: func(t *testing.T, cmds []appconfig.CommandConfig) {
				if len(cmds) != 1 {
					t.Fatalf("got %d commands, want 1", len(cmds))
				}
				c := cmds[0]
				if c.WatchInterval.Duration != 0 || c.Interactive {
					t.Fatalf("the command inherited fields of a default command: %+v", c)
				}
				if err := appconfig.ValidateCommand(c); err != nil {
					t.Fatalf("a valid confirmed command is refused: %v", err)
				}
			},
		},
		{
			name: "no commands key keeps the defaults",
			file: "viewer:\n  theme: monokai\n",
			check: func(t *testing.T, cmds []appconfig.CommandConfig) {
				if len(cmds) != len(appconfig.Default().Commands) {
					t.Fatalf("got %d commands, want the defaults", len(cmds))
				}
			},
		},
		{
			name: "an empty list means no commands",
			file: "commands: []\n",
			check: func(t *testing.T, cmds []appconfig.CommandConfig) {
				if len(cmds) != 0 {
					t.Fatalf("got %d commands, want none", len(cmds))
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			if err := os.MkdirAll(filepath.Join(home, ".kc"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, ".kc", "config.yaml"), []byte(tc.file), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := appconfig.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			tc.check(t, cfg.Commands)
		})
	}
}
