package ui

import (
	"errors"

	"github.com/sttts/kc/internal/ui/skin"
	uistyles "github.com/sttts/kc/internal/ui/styles"
	"github.com/sttts/kc/pkg/appconfig"
)

// applyLook applies the configured panel skin and the per-context frame
// colors. Problems are returned, not hidden: a bad skin falls back to the
// default mc palette and a bad context entry is skipped, and the caller
// shows the error.
func (a *App) applyLook(cfg *appconfig.Config, skinsDir string) error {
	var errs []error
	pal, err := skin.Load(cfg.Panel.Skin, skinsDir)
	if err != nil {
		errs = append(errs, err)
	}
	uistyles.Apply(pal)
	colors, err := skin.CompileContextColors(cfg.Contexts)
	if err != nil {
		errs = append(errs, err)
	}
	for _, p := range []*Panel{a.leftPanel, a.rightPanel} {
		if p != nil {
			p.SetContextColors(colors)
		}
	}
	return errors.Join(errs...)
}
