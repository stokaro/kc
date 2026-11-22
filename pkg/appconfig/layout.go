package appconfig

import (
	"fmt"
	"slices"
	"strings"
)

// PanelStartMode is the mode a panel starts in (layout.leftPanelMode and
// layout.rightPanelMode).
type PanelStartMode string

const (
	PanelStartList     PanelStartMode = "list"
	PanelStartDescribe PanelStartMode = "describe"
	PanelStartManifest PanelStartMode = "manifest"
	PanelStartLogs     PanelStartMode = "logs"
	PanelStartInfo     PanelStartMode = "info"
)

// PanelStartModes lists the valid start modes in the order of the panel
// mode menu.
func PanelStartModes() []PanelStartMode {
	return []PanelStartMode{PanelStartList, PanelStartDescribe, PanelStartManifest, PanelStartLogs, PanelStartInfo}
}

func (m PanelStartMode) valid() bool {
	return slices.Contains(PanelStartModes(), m)
}

// normalize lower-cases the start modes and fills an empty one with its
// default, so "Info" and "info" mean the same and `leftPanelMode: ""` is
// the same as leaving the key out.
func (c *LayoutConfig) normalize() {
	defaults := Default().Layout
	norm := func(m *PanelStartMode, def PanelStartMode) {
		*m = PanelStartMode(strings.ToLower(strings.TrimSpace(string(*m))))
		if *m == "" {
			*m = def
		}
	}
	norm(&c.LeftPanelMode, defaults.LeftPanelMode)
	norm(&c.RightPanelMode, defaults.RightPanelMode)
}

// Validate reports a start mode kc does not know, and a layout where
// neither panel starts in list mode: every other mode shows the other
// panel's selection, so there would be nothing to move the cursor in.
func (c LayoutConfig) Validate() error {
	for _, f := range []struct {
		key  string
		mode PanelStartMode
	}{
		{"layout.leftPanelMode", c.LeftPanelMode},
		{"layout.rightPanelMode", c.RightPanelMode},
	} {
		if !f.mode.valid() {
			return fmt.Errorf("%s: unknown panel mode %q; use list, describe, manifest, logs or info", f.key, string(f.mode))
		}
	}
	if c.LeftPanelMode != PanelStartList && c.RightPanelMode != PanelStartList {
		return fmt.Errorf("layout.leftPanelMode %q and layout.rightPanelMode %q: one panel must start in list mode",
			string(c.LeftPanelMode), string(c.RightPanelMode))
	}
	return nil
}
