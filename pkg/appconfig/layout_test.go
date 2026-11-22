package appconfig_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stokaro/kc/pkg/appconfig"
)

func TestDefaultPanelStartModes(t *testing.T) {
	got := appconfig.Default().Layout
	if got.LeftPanelMode != appconfig.PanelStartList || got.RightPanelMode != appconfig.PanelStartInfo {
		t.Fatalf("default start modes = %q / %q, want list / info", got.LeftPanelMode, got.RightPanelMode)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("default layout does not validate: %v", err)
	}
}

func TestLoadPanelStartModes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		file      string
		wantLeft  appconfig.PanelStartMode
		wantRight appconfig.PanelStartMode
		wantErr   string
	}{
		{
			name:      "keys left out keep the defaults",
			file:      "layout:\n  menuBarVisible: false\n",
			wantLeft:  appconfig.PanelStartList,
			wantRight: appconfig.PanelStartInfo,
		},
		{
			name:      "two lists",
			file:      "layout:\n  rightPanelMode: list\n",
			wantLeft:  appconfig.PanelStartList,
			wantRight: appconfig.PanelStartList,
		},
		{
			name:      "info on the left, case and spaces ignored",
			file:      "layout:\n  leftPanelMode: ' Info '\n  rightPanelMode: LIST\n",
			wantLeft:  appconfig.PanelStartInfo,
			wantRight: appconfig.PanelStartList,
		},
		{
			name:      "empty value means the default",
			file:      "layout:\n  leftPanelMode: ''\n  rightPanelMode: describe\n",
			wantLeft:  appconfig.PanelStartList,
			wantRight: appconfig.PanelStartDescribe,
		},
		{
			name:      "logs on the left",
			file:      "layout:\n  leftPanelMode: logs\n  rightPanelMode: list\n",
			wantLeft:  appconfig.PanelStartLogs,
			wantRight: appconfig.PanelStartList,
		},
		{
			name:      "manifest on the right",
			file:      "layout:\n  rightPanelMode: manifest\n",
			wantLeft:  appconfig.PanelStartList,
			wantRight: appconfig.PanelStartManifest,
		},
		{
			name:    "unknown mode names the key",
			file:    "layout:\n  rightPanelMode: tree\n",
			wantErr: `layout.rightPanelMode: unknown panel mode "tree"; use list, describe, manifest, logs or info`,
		},
		{
			name:    "command mode is not a start mode",
			file:    "layout:\n  leftPanelMode: command\n",
			wantErr: `layout.leftPanelMode: unknown panel mode "command"`,
		},
		{
			name:    "no list panel",
			file:    "layout:\n  leftPanelMode: info\n",
			wantErr: `layout.leftPanelMode "info" and layout.rightPanelMode "info": one panel must start in list mode`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.file), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := appconfig.NewStore(path).Load()
			if tc.wantErr != "" {
				var loadErr *appconfig.LoadError
				if !errors.As(err, &loadErr) || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Load() error = %v, want a LoadError with %q", err, tc.wantErr)
				}
				if def := appconfig.Default().Layout; cfg.Layout != def {
					t.Fatalf("layout after a failed load = %+v, want the defaults %+v", cfg.Layout, def)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.Layout.LeftPanelMode != tc.wantLeft || cfg.Layout.RightPanelMode != tc.wantRight {
				t.Fatalf("start modes = %q / %q, want %q / %q",
					cfg.Layout.LeftPanelMode, cfg.Layout.RightPanelMode, tc.wantLeft, tc.wantRight)
			}
		})
	}
}

// TestUpdateRefusesAnInvalidStartMode checks that saving another setting
// does not write over a file with a bad start mode.
func TestUpdateRefusesAnInvalidStartMode(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("layout:\n  rightPanelMode: tree\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := appconfig.NewStore(path).Update(func(c *appconfig.Config) { c.Layout.MenuBarVisible = false })
	if _, ok := errors.AsType[*appconfig.LoadError](err); !ok {
		t.Fatalf("Update() error = %v, want a LoadError", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatalf("file changed to %q", got)
	}
}
