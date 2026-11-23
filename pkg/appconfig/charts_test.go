package appconfig_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stokaro/kc/pkg/appconfig"
)

func TestLoadChartsGraphics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		file    string
		want    string
		wantErr string
	}{
		{name: "left out is auto", file: "layout:\n  menuBarVisible: true\n", want: "auto"},
		{name: "empty is auto", file: "charts:\n  graphics: ''\n", want: "auto"},
		{name: "case and spaces", file: "charts:\n  graphics: ' Kitty '\n", want: "kitty"},
		{name: "text", file: "charts:\n  graphics: text\n", want: "text"},
		{name: "unknown", file: "charts:\n  graphics: sixel\n", wantErr: `charts.graphics: unknown value "sixel"`},
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
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Charts.Graphics != tc.want {
				t.Errorf("charts.graphics = %q, want %q", cfg.Charts.Graphics, tc.want)
			}
		})
	}
	if got := appconfig.Default().Charts.Graphics; got != appconfig.ChartsGraphicsAuto {
		t.Errorf("default charts.graphics = %q, want auto", got)
	}
}
