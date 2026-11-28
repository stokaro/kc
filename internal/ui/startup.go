package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/ui/dialog"
	uistyles "github.com/stokaro/kc/internal/ui/styles"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

// startup is the connection kc makes to its start context. Run reads the
// kubeconfig and picks the context before the UI starts; connecting runs
// in a command while the UI draws the connecting screen, so a server that
// does not answer cannot keep the screen empty.
type startup struct {
	kctx *kubeconfig.Context
	key  kccluster.Key
	// host is the server of the context's cluster, as the kubeconfig
	// names it.
	host string
	// only is set when kc opened the only context because none is current;
	// the notice shows with the panels.
	only  bool
	since time.Time
	// done is the result of connecting, once it is in. A result with an
	// error stays on the screen until the user opens the panels or quits.
	done *startupConnectedMsg
	// deferred are the commands of Init that show something on the panels
	// screen; they run when it opens.
	deferred []tea.Cmd
}

// startupConnectedMsg is the result of connecting to the start context.
type startupConnectedMsg struct {
	// leases are the two panels' leases; nil when acquiring failed.
	leases [2]*kccluster.Lease
	infos  []kccluster.ResourceInfo
	// acquireErr is set when kc could not build a client for the context.
	acquireErr error
	// discoveryErr is set when the server did not answer API discovery.
	discoveryErr error
}

func (m startupConnectedMsg) err() error {
	if m.acquireErr != nil {
		return m.acquireErr
	}
	return m.discoveryErr
}

// startupTickMsg redraws the elapsed time on the connecting screen.
type startupTickMsg struct{}

func startupTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return startupTickMsg{} })
}

// contextServer is the server URL of kctx's cluster in its kubeconfig.
func contextServer(kctx *kubeconfig.Context) string {
	cfg := kctx.LoadedConfig()
	if cfg == nil {
		return ""
	}
	if cl, ok := cfg.Clusters[kctx.Cluster]; ok && cl != nil {
		return cl.Server
	}
	return ""
}

// connectStartup acquires the start context's cluster for both panels and
// reads its API discovery. It blocks until the server answers or fails,
// and touches no UI state, so it runs as a command.
func (a *App) connectStartup(st *startup) func() startupConnectedMsg {
	pool, root, key := a.clPool, a.ctx, st.key
	return func() startupConnectedMsg {
		log := ctrllog.FromContext(root).WithName("init")
		var msg startupConnectedMsg
		// Each panel holds a lease of its own: switching one panel later
		// releases only that one, and the shared pool entry stays while
		// the other panel reads it.
		for idx := range 2 {
			log.Info("acquiring cluster", "key", key, "panel", panelName(idx))
			lease, err := pool.Acquire(root, key, nil)
			if err != nil {
				log.Error(err, "cluster acquisition failed")
				msg.releaseLeases()
				// Shown to the user as the reason: no prefix that
				// names kc's internals.
				msg.acquireErr = err
				return msg
			}
			msg.leases[idx] = lease
		}
		log.Info("cluster ready, fetching resource info")
		msg.infos, msg.discoveryErr = msg.leases[0].Cluster().GetResourceInfos()
		if msg.discoveryErr != nil {
			log.Error(msg.discoveryErr, "failed to fetch resource infos")
		} else {
			log.Info("resource infos fetched", "count", len(msg.infos))
		}
		return msg
	}
}

func (m *startupConnectedMsg) releaseLeases() {
	for i, lease := range m.leases {
		if lease != nil {
			lease.Release()
		}
		m.leases[i] = nil
	}
}

// updateStartup handles msg while the connecting screen is up. It quits on
// F10 and C-c, and once connecting failed, opens the panels on Enter. It
// drops the other keys and the mouse, which have nothing to act on yet,
// and leaves every other message to Update.
func (a *App) updateStartup(msg tea.Msg) (tea.Cmd, bool) {
	st := a.startup
	switch msg := msg.(type) {
	case startupConnectedMsg:
		st.done = &msg
		if msg.err() != nil {
			ctrllog.FromContext(a.ctx).WithName("init").Info("connecting failed; showing the error", "context", st.kctx.Name, "error", msg.err().Error())
			return nil, true
		}
		return a.openPanels(), true
	case startupTickMsg:
		if st.done != nil {
			return nil, true
		}
		return startupTick(), true
	case tea.KeyPressMsg:
		switch msg.String() {
		case "f10", "ctrl+c":
			return tea.Quit, true
		case "enter":
			if st.done != nil {
				return a.openPanels(), true
			}
		}
		return nil, true
	case tea.KeyMsg, tea.MouseMsg, tea.PasteMsg:
		return nil, true
	}
	return nil, false
}

// openPanels ends the connecting screen with its result and runs the
// commands Init held back.
func (a *App) openPanels() tea.Cmd {
	st := a.startup
	a.startup = nil
	a.invalidateView("startup done")
	a.invalidateFunctionBar("startup done")
	cmds := append([]tea.Cmd(nil), st.deferred...)
	if err := a.finishStartup(st, *st.done); err != nil {
		ctrllog.FromContext(a.ctx).WithName("init").Error(err, "starting without a connection")
		a.showStartProblem(err, st.kctx.Name)
		if a.toastLogger != nil {
			cmds = append(cmds, a.toastLogger.Errorf("Cannot connect to %s: %s", st.kctx.Name, errorCause(err)))
		}
	}
	return tea.Batch(append(cmds, a.panelCmds())...)
}

// Width limits of the connecting screen's box.
const (
	startupBoxMinWidth = 40
	startupBoxMaxWidth = 76
)

// renderStartup draws the connecting screen: the context, its server and
// the time spent, or the error once connecting failed.
func (a *App) renderStartup() tea.View {
	st := a.startup
	width, height := max(a.width, startupBoxMinWidth+4), max(a.height, 5)
	where := st.kctx.Name
	if st.host != "" {
		where += " (" + st.host + ")"
	}
	skin := dialog.Normal()
	var lines []string
	if st.done == nil {
		elapsed := time.Since(st.since).Truncate(time.Second)
		lines = []string{fmt.Sprintf("Connecting to %s… %s · F10 quits", where, elapsed)}
	} else {
		skin = dialog.Danger()
		lines = []string{
			"Cannot connect to " + where + ":",
			describeError(st.done.err()),
			"",
			"Enter opens the panels · F10 quits",
		}
	}
	inner := min(width-4-2*dialogPadX, startupBoxMaxWidth)
	textWidth := 0
	for _, l := range lines {
		textWidth = max(textWidth, lipgloss.Width(l))
	}
	inner = min(inner, max(textWidth, startupBoxMinWidth))
	bg := skin.Normal.GetBackground()
	box := skin.Normal.
		Border(lipgloss.DoubleBorder()).
		BorderForeground(skin.Normal.GetForeground()).
		BorderBackground(bg).
		Padding(0, 1).
		Width(inner + 4).
		Render(strings.Join(lines, "\n"))
	box = padDialog(box, lipgloss.Width(box), dialogPadX, dialogPadY, bg)
	screen := lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(uistyles.Current().Background)))
	v := tea.NewView(screen)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
