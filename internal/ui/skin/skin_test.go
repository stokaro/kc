package skin_test

import (
	"image/color"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/sttts/kc/internal/ui/skin"
	uistyles "github.com/sttts/kc/internal/ui/styles"
)

func sameColor(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == b
	}
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}

func TestParseColor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    color.Color
		wantErr string
	}{
		{in: "red", want: lipgloss.Red},
		{in: " Bright-Cyan ", want: lipgloss.BrightCyan},
		{in: "gray", want: lipgloss.BrightBlack},
		{in: "1", want: lipgloss.Red},
		{in: "196", want: lipgloss.ANSIColor(196)},
		{in: "#ff0000", want: color.RGBA{R: 0xff, A: 0xff}},
		{in: "#F00", want: color.RGBA{R: 0xff, A: 0xff}},
		{in: "", wantErr: "empty color"},
		{in: "redd", wantErr: `color "redd"`},
		{in: "256", wantErr: "0-255"},
		{in: "-1", wantErr: "0-255"},
		{in: "#ff00", wantErr: "3 or 6 digits"},
		{in: "#gggggg", wantErr: "not a hex value"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, err := skin.ParseColor(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ParseColor(%q) error = %v, want it to mention %q", tc.in, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseColor(%q): %v", tc.in, err)
			}
			if !sameColor(got, tc.want) {
				t.Fatalf("ParseColor(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParse(t *testing.T) {
	t.Parallel()
	t.Run("partial skin keeps the other defaults", func(t *testing.T) {
		t.Parallel()
		p, err := skin.Parse([]byte("panel:\n  background: black\n  cursor:\n    background: '#00ff00'\n"))
		if err != nil {
			t.Fatal(err)
		}
		want := uistyles.DefaultPalette()
		want.Background = lipgloss.Black
		want.CursorBackground = lipgloss.Color("#00ff00")
		if !reflect.DeepEqual(p, want) {
			t.Fatalf("palette = %+v, want %+v", p, want)
		}
	})
	t.Run("every bad color is reported", func(t *testing.T) {
		t.Parallel()
		p, err := skin.Parse([]byte("panel:\n  frame: purple\n  title:\n    foreground: '#12'\n  header: yellow\n"))
		if err == nil {
			t.Fatal("expected an error")
		}
		for _, want := range []string{"panel.frame", `"purple"`, "panel.title.foreground"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
		if !reflect.DeepEqual(p, uistyles.DefaultPalette()) {
			t.Errorf("a bad skin should fall back to the default palette, got %+v", p)
		}
	})
	t.Run("unknown key", func(t *testing.T) {
		t.Parallel()
		_, err := skin.Parse([]byte("panel:\n  backgroud: black\n"))
		if err == nil || !strings.Contains(err.Error(), "backgroud") {
			t.Fatalf("error = %v, want it to name the unknown key", err)
		}
	})
}

func TestBuiltins(t *testing.T) {
	t.Parallel()
	names := skin.Builtins()
	if !slices.Equal(names, []string{"dark", "mc"}) {
		t.Fatalf("Builtins() = %v", names)
	}
	for _, name := range names {
		if _, err := skin.Load(name, ""); err != nil {
			t.Errorf("built-in skin %s: %v", name, err)
		}
	}
	// The mc skin must be exactly the look kc has without any config.
	mc, err := skin.Load(skin.DefaultName, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(mc, uistyles.DefaultPalette()) {
		t.Fatalf("mc skin = %+v, want the default palette %+v", mc, uistyles.DefaultPalette())
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("prodlike", "panel:\n  background: red\n")
	write("dark", "panel:\n  background: magenta\n")
	write("broken", "panel:\n  background: nope\n")

	tests := []struct {
		name    string
		skin    string
		wantBg  color.Color
		wantErr string
	}{
		{name: "empty means mc", skin: "", wantBg: lipgloss.Blue},
		{name: "user file", skin: "prodlike", wantBg: lipgloss.Red},
		{name: "user file overrides built-in", skin: "dark", wantBg: lipgloss.Magenta},
		{name: "bad color names the file", skin: "broken", wantBg: lipgloss.Blue, wantErr: "broken.yaml"},
		{name: "missing", skin: "nosuch", wantBg: lipgloss.Blue, wantErr: "built-in skins: dark, mc"},
		{name: "path traversal", skin: "../config", wantBg: lipgloss.Blue, wantErr: "name may only use"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := skin.Load(tc.skin, dir)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Load(%q) error = %v, want it to mention %q", tc.skin, err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("Load(%q): %v", tc.skin, err)
			}
			if !sameColor(p.Background, tc.wantBg) {
				t.Fatalf("background = %v, want %v", p.Background, tc.wantBg)
			}
		})
	}

	t.Run("built-in without a user dir", func(t *testing.T) {
		t.Parallel()
		p, err := skin.Load("dark", "")
		if err != nil {
			t.Fatal(err)
		}
		if !sameColor(p.Background, lipgloss.Black) {
			t.Fatalf("dark background = %v", p.Background)
		}
	})
}
