package ui

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/ui/dashboard"
	"github.com/stokaro/kc/internal/ui/kittygfx"
	"github.com/stokaro/kc/pkg/appconfig"
)

// chartImageFirstID is the first Kitty image ID of the dashboard charts:
// high in the 256-color range, where other programs in the same window
// are unlikely to put theirs.
const chartImageFirstID = 0xF0

// tmuxTimeout bounds one question to the tmux server.
const tmuxTimeout = 500 * time.Millisecond

// chartGraphics is the charts.graphics decision, made once per run.
type chartGraphics struct {
	checked bool
	images  *kittygfx.Images
}

// chartImages returns the images of the dashboard charts, or nil when the
// charts draw text (charts.graphics, and for auto the terminal).
func (a *App) chartImages() *kittygfx.Images {
	if a.chartGfx.checked {
		return a.chartGfx.images
	}
	a.chartGfx.checked = true
	mode := kittygfx.ModeAuto
	if a.cfg != nil {
		mode = kittygfx.Mode(a.cfg.Charts.Graphics)
	}
	if mode == "" {
		mode = kittygfx.Mode(appconfig.ChartsGraphicsAuto)
	}
	root := a.ctx
	sup := kittygfx.Detect(mode, kittygfx.Env{
		Getenv: os.Getenv,
		Tmux:   func() (string, string, bool) { return askTmux(root) },
	})
	ctrllog.FromContext(root).WithName("charts").Info("chart graphics", "mode", string(mode), "images", sup.On, "tmux", sup.Tmux, "reason", sup.Reason)
	if sup.On {
		a.chartGfx.images = kittygfx.NewImages(kittygfx.Encoder{Tmux: sup.Tmux}, chartImageFirstID, dashboard.ImageKeys)
	}
	return a.chartGfx.images
}

// askTmux asks the tmux server for the terminal of this client and for
// allow-passthrough. It runs tmux, not a terminal query, so no answer can
// end up in the input.
func askTmux(ctx context.Context) (clientTerm, passthrough string, ok bool) {
	run := func(args ...string) (string, bool) {
		ctx, cancel := context.WithTimeout(ctx, tmuxTimeout)
		defer cancel()
		ctrllog.FromContext(ctx).WithName("charts").Info("asking tmux", "args", args)
		out, err := exec.CommandContext(ctx, "tmux", args...).Output()
		return strings.TrimSpace(string(out)), err == nil
	}
	clientTerm, ok1 := run("display-message", "-p", "#{client_termname}")
	passthrough, ok2 := run("show-options", "-gv", "allow-passthrough")
	return clientTerm, passthrough, ok1 && ok2
}

// releaseChartImages deletes the chart images from the terminal, as raw
// output; "" when kc sent none.
func (a *App) releaseChartImages() string {
	if a.chartGfx.images == nil {
		return ""
	}
	return a.chartGfx.images.Release()
}
