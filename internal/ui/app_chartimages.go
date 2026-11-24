package ui

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/ui/dashboard"
	"github.com/stokaro/kc/internal/ui/kittygfx"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
	infowidget "github.com/stokaro/kc/internal/ui/panelcontent/info"
	"github.com/stokaro/kc/pkg/appconfig"
)

// Kitty image IDs are the 256-color foreground of the placeholder cells,
// so kc has the IDs 16 to 255 to give out (kittygfx.Color). Each owner
// of images gets its own block, so a picture of one never replaces a
// picture of another, and a release deletes its own block only.
const (
	// chartImageFirstID is the first ID of the dashboard charts: high in
	// the 256-color range, where other programs in the same window are
	// unlikely to put theirs.
	chartImageFirstID = 0xF0
	// infoImageFirstID is the first ID of the Info panels, infoImageBlock
	// IDs per panel below the dashboard's.
	infoImageFirstID = 0xC0
	infoImageBlock   = 0x10
)

// tmuxTimeout bounds one question to the tmux server.
const tmuxTimeout = 500 * time.Millisecond

// chartGraphics is the charts.graphics decision, made once per run, and
// the image sets of the dashboard and of each Info panel.
type chartGraphics struct {
	checked bool
	support kittygfx.Support
	images  *kittygfx.Images
	// info holds the image set of each panel, by the panel; the order
	// they were given out in picks their ID block.
	info  map[*Panel]*kittygfx.Images
	order []*Panel
}

// graphics is the charts.graphics decision: images or text, and for auto
// what the terminal shows.
func (a *App) graphics() kittygfx.Support {
	if a.chartGfx.checked {
		return a.chartGfx.support
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
	a.chartGfx.support = sup
	return sup
}

// chartImages returns the images of the dashboard charts, or nil when the
// charts draw text (charts.graphics, and for auto the terminal).
func (a *App) chartImages() *kittygfx.Images {
	if a.chartGfx.images != nil {
		return a.chartGfx.images
	}
	if sup := a.graphics(); sup.On {
		a.chartGfx.images = kittygfx.NewImages(kittygfx.Encoder{Tmux: sup.Tmux}, chartImageFirstID, dashboard.ImageKeys)
	}
	return a.chartGfx.images
}

// infoImages returns the images of the Info mode of panel p, or nil when
// Info draws text. charts.graphics decides for Info as for the dashboard.
// A panel keeps its set, and with it its IDs, when the panels swap: the
// two sets never share an ID.
func (a *App) infoImages(p *Panel) *kittygfx.Images {
	if im, ok := a.chartGfx.info[p]; ok {
		return im
	}
	sup := a.graphics()
	if !sup.On {
		return nil
	}
	first, ok := infoImageID(len(a.chartGfx.order))
	if !ok {
		return nil
	}
	im := kittygfx.NewImages(kittygfx.Encoder{Tmux: sup.Tmux}, first, infowidget.ImageKeys)
	if a.chartGfx.info == nil {
		a.chartGfx.info = make(map[*Panel]*kittygfx.Images)
	}
	a.chartGfx.info[p] = im
	a.chartGfx.order = append(a.chartGfx.order, p)
	return im
}

// infoImageID is the first ID of the n-th Info image set, false when no
// block is left below the dashboard's IDs.
func infoImageID(n int) (uint8, bool) {
	first := infoImageFirstID + n*infoImageBlock
	if n < 0 || first+2*infowidget.ImageKeys > chartImageFirstID {
		return 0, false
	}
	return uint8(first), true // #nosec G115 -- first is below chartImageFirstID, checked above
}

// routeImageMsg hands a message of the image protocol (a cell size
// report, or a transmit that went out) to every owner of images: the open
// dashboard and the Info widgets of both panels. Each takes only its own
// transmits. It goes past dialogs and the dashboard, so no owner misses
// the return of its transmit and waits for it forever.
func (a *App) routeImageMsg(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	if a.dashboardOpen() {
		cmd, _ := a.dash.model.Update(msg)
		cmds = append(cmds, cmd)
		a.invalidateView("dashboard")
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	for idx := range 2 {
		if panel := a.panelByIndex(idx); panel != nil {
			cmds = append(cmds, panel.DeliverBackground(ctx, panelcontent.TerminalMsg{Msg: msg}))
		}
	}
	a.invalidateView("terminal images")
	return tea.Batch(cmds...)
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

// releaseAllImages deletes every image kc sent, the dashboard's and the
// Info panels', as raw output for the exit; "" when kc sent none.
func (a *App) releaseAllImages() string {
	var b strings.Builder
	b.WriteString(a.releaseChartImages())
	for _, p := range a.chartGfx.order {
		b.WriteString(a.chartGfx.info[p].Release())
	}
	return b.String()
}
