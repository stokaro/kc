package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/types"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/investigation"
	models "github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/ui/menubar"
	logswidget "github.com/stokaro/kc/internal/ui/panelcontent/logs"
)

// Keys after C-x (docs/adr/0187-pin-freeze-window.md).
const (
	pinKey    = "p"
	freezeKey = "f"
	windowKey = "w"
)

const windowModalKey = "investigation_window"

// pinSignalMsg wakes the UI after the shared cache reported about a
// pinned object.
type pinSignalMsg struct{}

// windowResultMsg is the investigation window dialog's answer.
type windowResultMsg struct {
	Value          string
	Confirm, Close bool
}

// handlePinChord runs kc's own C-x chords of this file. ok is false for
// other keys.
func (a *App) handlePinChord(key string) (tea.Cmd, bool) {
	switch key {
	case pinKey:
		return a.togglePin(), true
	case freezeKey:
		return a.toggleFreeze(), true
	case windowKey:
		return a.showWindowDialog(), true
	}
	return nil, false
}

// updatePin handles the messages of pinning and the window dialog.
func (a *App) updatePin(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case pinSignalMsg:
		return tea.Batch(a.checkPins(), a.waitPinSignal()), true
	case windowResultMsg:
		return a.handleWindowResult(m), true
	}
	return nil, false
}

// followerPanel is the panel C-x p and C-x f act on: the active panel
// when it shows Describe, Manifest, Logs or Info, else the other one.
func (a *App) followerPanel() (int, *Panel, bool) {
	for _, idx := range []int{a.activePanel, 1 - a.activePanel} {
		if p := a.panelByIndex(idx); p != nil && followerMode(p.Mode()) {
			return idx, p, true
		}
	}
	return 0, nil, false
}

const noFollowerPanel = "Pin and freeze work on a Describe, Manifest, Logs or Info panel"

// togglePin pins the follower panel to what it shows, or unpins it.
func (a *App) togglePin() tea.Cmd {
	idx, panel, ok := a.followerPanel()
	if !ok {
		return a.ShowToast(noFollowerPanel, 3*time.Second)
	}
	return a.togglePinOf(idx, panel)
}

// togglePinOf pins or unpins panel idx.
func (a *App) togglePinOf(idx int, panel *Panel) tea.Cmd {
	if panel.pinned() != nil {
		return a.unpin(panel)
	}
	return a.pin(idx, panel)
}

// pin keeps what panel shows: the row its widget was given (the other
// panel's selection), or for Info the other panel's scope and row.
func (a *App) pin(idx int, panel *Panel) tea.Cmd {
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	pin := &panelPin{}
	if panel.Mode() == PanelModeInfo {
		if other := a.panelByIndex(1 - idx); other != nil {
			if item, ok := other.SelectedNavItem(ctx); ok {
				pin.item = item
			}
		}
		pin.scope = a.overviewNamespace(1 - idx)
	} else {
		sel := panel.CurrentSelection(ctx)
		pin.item, pin.id = sel.Item, sel.ID
		if pin.item == nil {
			return a.ShowToast("Nothing to pin: the panel shows no object", 3*time.Second)
		}
	}
	ident, watchable := a.identityOf(pin.item)
	pin.ident = ident
	if watchable {
		pin.watch = &pinWatch{}
	}
	panel.SetPin(pin)
	label := ident.label()
	if !watchable {
		label = namespaceScopeLabel(pin.scope)
	}
	a.invalidateView("pin")
	return tea.Batch(a.ShowInfo("Pinned "+label+"; C-x p unpins", 2*time.Second), a.startPinWatch(pin))
}

// identityOf names the object item stands for. ok is false when the row
// names none (an Info panel on a namespace).
func (a *App) identityOf(item models.Item) (pinIdentity, bool) {
	id := pinIdentity{cluster: a.currentClusterKey()}
	if obj, ok := item.(models.ObjectItem); ok && obj.GVR().Resource != "" {
		id.gvr, id.namespace, id.name = obj.GVR(), obj.Namespace(), obj.Name()
		if inst, ok := item.(models.InstanceItem); ok {
			id.uid = inst.UID()
		}
		return id, id.name != ""
	}
	// A container row or a log row: the pod it belongs to.
	if req, ok := logswidget.Target(item); ok {
		id.gvr, id.namespace, id.name = logsPodsGVR, req.Spec.Namespace, req.Spec.Pod
		return id, true
	}
	return id, false
}

// startPinWatch adds a handler for the pinned name to the shared
// informer of its type. GetInformer may wait for discovery, so it runs in
// a command.
func (a *App) startPinWatch(pin *panelPin) tea.Cmd {
	if pin.watch == nil || a.cl == nil {
		return nil
	}
	if a.pinSignal == nil {
		a.pinSignal = make(chan struct{}, 1)
	}
	var wait tea.Cmd
	if !a.pinWaiting {
		a.pinWaiting = true
		wait = a.waitPinSignal()
	}
	deps := a.makeDeps(a.cl, nil, pin.ident.cluster)
	w, signal, id := pin.watch, a.pinSignal, pin.ident
	start := func() tea.Msg {
		stop, err := models.WatchObject(deps, id.gvr, id.namespace, id.name, func(s models.ObjectSighting) {
			w.set(s)
			select {
			case signal <- struct{}{}:
			default:
			}
		})
		if err != nil {
			ctrllog.FromContext(deps.Ctx).WithName("pin").Info("cannot watch the pinned object", "gvr", id.gvr.String(), "namespace", id.namespace, "name", id.name, "error", err.Error())
			return nil
		}
		w.attach(stop)
		return nil
	}
	return tea.Batch(start, wait)
}

// waitPinSignal waits for the next report about a pinned object.
func (a *App) waitPinSignal() tea.Cmd {
	signal, done := a.pinSignal, a.ctx.Done()
	return func() tea.Msg {
		select {
		case <-signal:
			return pinSignalMsg{}
		case <-done:
			return nil
		}
	}
}

// checkPins compares what the shared cache reports with each pin. A
// deleted or replaced instance freezes its panel: kc does not show
// another object under the pinned name.
func (a *App) checkPins() tea.Cmd {
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	var cmds []tea.Cmd
	for idx := range 2 {
		panel := a.panelByIndex(idx)
		if panel == nil || panel.pinned() == nil || panel.pinned().watch == nil {
			continue
		}
		pin := panel.pinned()
		s, seen := pin.watch.get()
		if !seen {
			continue
		}
		lost := pinLoss(&pin.ident, s)
		if lost == "" {
			// The pinned object changed: reread it, whatever the other
			// panel shows.
			cmds = append(cmds, panel.RefreshContent(ctx))
			continue
		}
		if lost == pin.lost {
			continue
		}
		pin.lost = lost
		panel.Freeze(ctx, time.Now(), "pinned object "+lost)
		cmds = append(cmds, a.ShowToast(fmt.Sprintf("Pinned %s was %s; C-x p unpins", pin.ident.label(), lost), 5*time.Second))
	}
	a.invalidateView("pinned object changed")
	return tea.Batch(cmds...)
}

// pinLoss says why the sighting is not the pinned instance, "" when it
// is. A pin made without a UID takes the first one the cache reports.
func pinLoss(id *pinIdentity, s models.ObjectSighting) string {
	if id.uid == "" && !s.Gone {
		id.uid = s.UID
	}
	switch {
	case s.Gone && (s.UID == id.uid || id.uid == ""):
		return "deleted"
	case !s.Gone && s.UID != id.uid:
		return "replaced by uid " + shortUID(s.UID)
	}
	return ""
}

func shortUID(uid types.UID) string {
	s := string(uid)
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// unpin returns panel to following the other panel and shows that
// panel's selection.
func (a *App) unpin(panel *Panel) tea.Cmd {
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	panel.ClearPin()
	panel.Thaw()
	cmds := []tea.Cmd{a.ShowInfo("Unpinned: the panel follows the other panel again", 2*time.Second)}
	cmds = append(cmds, a.followOtherPanel(ctx, panel), panel.RefreshContent(ctx))
	a.invalidateView("unpin")
	return tea.Batch(cmds...)
}

// dropPins unpins and thaws both panels, as a context switch must: the
// pinned objects belong to the old cluster.
func (a *App) dropPins() {
	for idx := range 2 {
		if p := a.panelByIndex(idx); p != nil {
			p.ClearPin()
			p.Thaw()
		}
	}
}

// toggleFreeze freezes the follower panel's display, or returns it to
// live data.
func (a *App) toggleFreeze() tea.Cmd {
	_, panel, ok := a.followerPanel()
	if !ok {
		return a.ShowToast(noFollowerPanel, 3*time.Second)
	}
	return a.toggleFreezeOf(panel)
}

// toggleFreezeOf freezes or thaws panel.
func (a *App) toggleFreezeOf(panel *Panel) tea.Cmd {
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	defer a.invalidateView("freeze")
	if !panel.Frozen() {
		panel.Freeze(ctx, time.Now(), "")
		return a.ShowInfo("Frozen; C-x f returns to live data", 2*time.Second)
	}
	if pin := panel.pinned(); pin != nil && pin.lost != "" {
		// Live data under the pinned name is another object.
		return a.ShowToast("The pinned object was "+pin.lost+"; C-x p unpins", 4*time.Second)
	}
	missed := panel.Thaw()
	cmds := []tea.Cmd{a.ShowInfo("Live", time.Second), panel.RefreshContent(ctx)}
	if missed && panel.pinned() == nil {
		cmds = append(cmds, a.followOtherPanel(ctx, panel))
	}
	return tea.Batch(cmds...)
}

// showWindowDialog asks for the investigation window.
func (a *App) showWindowDialog() tea.Cmd {
	now, loc := time.Now(), time.Local
	zone := now.In(loc).Format("-07:00")
	modal := NewTextInputModal(TextInputModalConfig{
		Title:        "Investigation window",
		Label:        "Window",
		Description:  "15m or 2h (live), or START .. END such as 10:00 .. 11:30 (time zone " + zone + "); empty turns it off. Logs and Info panels use it.",
		ConfirmLabel: "OK",
		CancelLabel:  "Cancel",
		InitialValue: a.window.Text(loc),
		MinWidth:     40,
		MaxWidth:     72,
		MinHeight:    6,
		HideTitle:    true,
		Validate: func(s string) error {
			_, err := investigation.Parse(s, time.Now(), loc)
			return err
		},
		ResultMsg: func(res TextInputModalResult) tea.Msg {
			return windowResultMsg(res)
		},
	})
	m := a.modalManager.modals[windowModalKey]
	if m == nil {
		m = NewModal("Investigation window", modal)
		m.SetCloseOnSingleEsc(true)
		a.modalManager.Register(windowModalKey, m)
	} else {
		m.SetContent(modal)
	}
	a.configureModalWindow(m, modal, a.activePanel, "", 60, 8)
	a.showModal(windowModalKey)
	return nil
}

func (a *App) handleWindowResult(msg windowResultMsg) tea.Cmd {
	if msg.Close {
		a.hideModal()
	}
	if !msg.Confirm {
		return nil
	}
	w, err := investigation.Parse(msg.Value, time.Now(), time.Local)
	if err != nil {
		return a.ShowToast("Investigation window: "+err.Error(), 3*time.Second)
	}
	a.window = w
	cmds := a.applyWindow()
	text := "Investigation window off"
	if !w.Off() {
		text = "Investigation window " + w.Label(time.Local) + " (Logs and Info panels)"
	}
	return tea.Batch(append(cmds, a.ShowInfo(text, 3*time.Second))...)
}

// applyWindow hands the window to the Logs panels; Info panels read it on
// each redraw.
func (a *App) applyWindow() []tea.Cmd {
	var cmds []tea.Cmd
	for idx := range 2 {
		if w := a.panelLogsWidget(idx); w != nil {
			cmds = append(cmds, w.SetSpan(a.logsSpan()))
		}
	}
	a.invalidateView("investigation window")
	return cmds
}

func (a *App) logsSpan() logswidget.Span {
	return logswidget.Span{Window: a.window, Loc: time.Local}
}

// panelLogsWidget returns the Logs widget of panel idx once it exists.
func (a *App) panelLogsWidget(idx int) *logswidget.Widget {
	if p := a.panelByIndex(idx); p != nil {
		return p.logsWidget()
	}
	return nil
}

// menuFollower returns panel idx when the Left or Right menu may pin or
// freeze it.
func (a *App) menuFollower(idx int) (*Panel, bool) {
	panel := a.panelByIndex(idx)
	return panel, panel != nil && a.panelVisible(idx) && followerMode(panel.Mode())
}

// pinMenuItem is the Left and Right menu entry that pins panel idx.
func (a *App) pinMenuItem(idx int) menubar.Item {
	panel, ok := a.menuFollower(idx)
	return menubar.Item{Label: "&Pin target", Shortcut: "C-x p", Checked: ok && panel.pinned() != nil, Disabled: !ok, Run: func() tea.Cmd {
		return a.togglePinOf(idx, a.panelByIndex(idx))
	}}
}

// freezeMenuItem is the Left and Right menu entry that freezes panel idx.
func (a *App) freezeMenuItem(idx int) menubar.Item {
	panel, ok := a.menuFollower(idx)
	return menubar.Item{Label: "Free&ze display", Shortcut: "C-x f", Checked: ok && panel.Frozen(), Disabled: !ok, Run: func() tea.Cmd {
		return a.toggleFreezeOf(a.panelByIndex(idx))
	}}
}

// windowMenuItem is the Command menu entry of the investigation window.
func (a *App) windowMenuItem() menubar.Item {
	label := "Investigation &window..."
	if !a.window.Off() {
		label = "Investigation &window (" + strings.TrimSpace(a.window.Label(time.Local)) + ")..."
	}
	return menubar.Item{Label: label, Shortcut: "C-x w", Checked: !a.window.Off(), Run: a.showWindowDialog}
}
