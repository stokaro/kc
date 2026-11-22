package skin

import (
	"embed"
	"errors"
	"fmt"
	"image/color"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	yaml "sigs.k8s.io/yaml"

	uistyles "github.com/stokaro/kc/internal/ui/styles"
)

// DefaultName is the built-in skin kc uses when none is configured.
const DefaultName = "mc"

//go:embed builtin/*.yaml
var builtinFS embed.FS

// File is the on-disk form of a skin. Every color is optional; a missing one
// keeps the value of the default mc skin.
type File struct {
	Panel PanelColors `json:"panel"`
}

// PanelColors are the colors of the panel area.
type PanelColors struct {
	Background string    `json:"background,omitempty"`
	Foreground string    `json:"foreground,omitempty"`
	Frame      string    `json:"frame,omitempty"`
	Header     string    `json:"header,omitempty"`
	Marked     string    `json:"marked,omitempty"`
	Cursor     ColorPair `json:"cursor,omitempty"`
	Title      ColorPair `json:"title,omitempty"`
	Context    string    `json:"context,omitempty"`
	Namespace  string    `json:"namespace,omitempty"`
}

// ColorPair is a foreground and background color.
type ColorPair struct {
	Foreground string `json:"foreground,omitempty"`
	Background string `json:"background,omitempty"`
}

// Parse reads a skin file. Unknown keys and bad colors are errors; all of
// them are reported, not only the first.
func Parse(data []byte) (uistyles.Palette, error) {
	var f File
	if err := yaml.UnmarshalStrict(data, &f); err != nil {
		return uistyles.DefaultPalette(), fmt.Errorf("parse skin: %w", err)
	}
	return f.Palette()
}

// Palette resolves the file on top of the default palette.
func (f File) Palette() (uistyles.Palette, error) {
	p := uistyles.DefaultPalette()
	var errs []error
	set := func(dst *color.Color, field, value string) {
		if value == "" {
			return
		}
		c, err := ParseColor(value)
		if err != nil {
			errs = append(errs, fmt.Errorf("panel.%s: %w", field, err))
			return
		}
		*dst = c
	}
	pc := f.Panel
	set(&p.Background, "background", pc.Background)
	set(&p.Foreground, "foreground", pc.Foreground)
	set(&p.Frame, "frame", pc.Frame)
	set(&p.Header, "header", pc.Header)
	set(&p.Marked, "marked", pc.Marked)
	set(&p.CursorForeground, "cursor.foreground", pc.Cursor.Foreground)
	set(&p.CursorBackground, "cursor.background", pc.Cursor.Background)
	set(&p.TitleForeground, "title.foreground", pc.Title.Foreground)
	set(&p.TitleBackground, "title.background", pc.Title.Background)
	set(&p.Context, "context", pc.Context)
	set(&p.Namespace, "namespace", pc.Namespace)
	if len(errs) > 0 {
		return uistyles.DefaultPalette(), errors.Join(errs...)
	}
	return p, nil
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Load returns the skin called name. A file <dir>/<name>.yaml wins over a
// built-in skin of the same name, so users can override the shipped ones.
// An empty name means DefaultName. On error the default palette is returned
// along with the error.
func Load(name, dir string) (uistyles.Palette, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultName
	}
	if !validName.MatchString(name) {
		return uistyles.DefaultPalette(), fmt.Errorf("skin %q: name may only use letters, digits, '.', '_' and '-'", name)
	}
	if dir != "" {
		p := filepath.Join(dir, name+".yaml")
		data, err := os.ReadFile(p)
		switch {
		case err == nil:
			pal, err := Parse(data)
			if err != nil {
				return pal, fmt.Errorf("skin %s: %w", p, err)
			}
			return pal, nil
		case !errors.Is(err, fs.ErrNotExist):
			return uistyles.DefaultPalette(), fmt.Errorf("skin %q: %w", name, err)
		}
	}
	data, err := builtinFS.ReadFile("builtin/" + name + ".yaml")
	if err != nil {
		return uistyles.DefaultPalette(), fmt.Errorf("skin %q not found in %s; built-in skins: %s", name, dir, strings.Join(Builtins(), ", "))
	}
	pal, err := Parse(data)
	if err != nil {
		return pal, fmt.Errorf("built-in skin %q: %w", name, err)
	}
	return pal, nil
}

// DefaultDir is where user skins live: ~/.kc/skins.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("skins dir: %w", err)
	}
	return filepath.Join(home, ".kc", "skins"), nil
}

// Builtins lists the names of the skins shipped with kc.
func Builtins() []string {
	entries, _ := fs.ReadDir(builtinFS, "builtin")
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	slices.Sort(names)
	return names
}
