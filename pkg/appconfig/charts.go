package appconfig

import (
	"fmt"
	"slices"
	"strings"
)

// ChartsConfig is how the dashboard draws its charts.
type ChartsConfig struct {
	// Graphics picks text or Kitty graphics images for the plots: auto
	// uses images in terminals known to show Kitty Unicode placeholders
	// (kitty, Ghostty; under tmux only with allow-passthrough on), text
	// never does, kitty always does.
	Graphics string `json:"graphics"`
}

// Values of charts.graphics.
const (
	ChartsGraphicsAuto  = "auto"
	ChartsGraphicsText  = "text"
	ChartsGraphicsKitty = "kitty"
)

// DefaultCharts is the default charts section.
func DefaultCharts() ChartsConfig { return ChartsConfig{Graphics: ChartsGraphicsAuto} }

// normalize lower-cases the mode; empty is the default.
func (c *ChartsConfig) normalize() {
	c.Graphics = strings.ToLower(strings.TrimSpace(c.Graphics))
	if c.Graphics == "" {
		c.Graphics = ChartsGraphicsAuto
	}
}

// Validate reports a charts.graphics value kc does not know.
func (c ChartsConfig) Validate() error {
	if !slices.Contains([]string{ChartsGraphicsAuto, ChartsGraphicsText, ChartsGraphicsKitty}, c.Graphics) {
		return fmt.Errorf("charts.graphics: unknown value %q; use auto, text or kitty", c.Graphics)
	}
	return nil
}
