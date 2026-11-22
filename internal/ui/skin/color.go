// Package skin loads panel skins and per-context frame colors.
//
// A skin is a small YAML file that sets the colors of the panel area (see
// uistyles.Palette). Context colors tint the frame of a panel whose
// kubeconfig context matches a rule, so a production cluster looks different.
package skin

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

// namedColors are the 16 ANSI colors under their mc-like names. The terminal
// theme decides how they actually look.
var namedColors = map[string]color.Color{
	"black":          lipgloss.Black,
	"red":            lipgloss.Red,
	"green":          lipgloss.Green,
	"yellow":         lipgloss.Yellow,
	"blue":           lipgloss.Blue,
	"magenta":        lipgloss.Magenta,
	"cyan":           lipgloss.Cyan,
	"white":          lipgloss.White,
	"gray":           lipgloss.BrightBlack,
	"grey":           lipgloss.BrightBlack,
	"bright-black":   lipgloss.BrightBlack,
	"bright-red":     lipgloss.BrightRed,
	"bright-green":   lipgloss.BrightGreen,
	"bright-yellow":  lipgloss.BrightYellow,
	"bright-blue":    lipgloss.BrightBlue,
	"bright-magenta": lipgloss.BrightMagenta,
	"bright-cyan":    lipgloss.BrightCyan,
	"bright-white":   lipgloss.BrightWhite,
}

// ParseColor reads a color written as an ANSI name ("red", "bright-cyan"),
// an ANSI 256 index ("196") or a hex RGB value ("#ff0000", "#f00").
// Unlike lipgloss.Color it reports bad input instead of returning no color.
func ParseColor(s string) (color.Color, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "" {
		return nil, fmt.Errorf("empty color")
	}
	if c, ok := namedColors[v]; ok {
		return c, nil
	}
	if strings.HasPrefix(v, "#") {
		hex := v[1:]
		if len(hex) != 3 && len(hex) != 6 {
			return nil, fmt.Errorf("color %q: hex needs 3 or 6 digits", s)
		}
		if _, err := strconv.ParseUint(hex, 16, 32); err != nil {
			return nil, fmt.Errorf("color %q: not a hex value", s)
		}
		return lipgloss.Color(v), nil
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 || n > 255 {
			return nil, fmt.Errorf("color %q: ANSI index must be 0-255", s)
		}
		return lipgloss.Color(v), nil
	}
	return nil, fmt.Errorf("color %q: use an ANSI name, 0-255 or #rrggbb", s)
}
