package dashboard

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stokaro/kc/internal/investigation"
	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/promhistory"
	"github.com/stokaro/kc/internal/ui/chart"
	"github.com/stokaro/kc/internal/ui/kittygfx"
)

// Sources are what a dashboard reads. The App builds them for one
// context and namespace scope; tests give fakes.
type Sources struct {
	// Context names the kube context for the title bar.
	Context string
	// Scope is the namespace scope of the engine (models.AllNamespaces for
	// the cluster): where Backspace ends.
	Scope string
	// Amounts returns the Metrics API measurement of a focus from the
	// overview engine. It runs on the UI goroutine and does no I/O.
	Amounts func(focus models.UsageFocus, topN int) models.UsageAmounts
	// Usage starts a Prometheus read; nil when the context has no
	// backend. read runs in a tea.Cmd; cancel ends it early.
	Usage func(scope promhistory.Scope, r promhistory.Range) (read func() (promhistory.Usage, error), cancel func())
	// Backend names the backend for the source labels.
	Backend string
	// Now is the clock; Loc the time zone of the labels.
	Now func() time.Time
	Loc *time.Location
	// Tick schedules the next refresh; nil is tea.Tick.
	Tick func(time.Duration, func(time.Time) tea.Msg) tea.Cmd
	// Window is the investigation window (C-x w), which the views follow
	// while it is set; SetWindow sets it (w). Either may be nil.
	Window    func() investigation.Window
	SetWindow func(investigation.Window)
	// Images draws the plots as Kitty graphics images (charts.graphics);
	// nil draws text only. The App owns it and deletes the images when
	// the dashboard closes and when kc exits.
	Images *kittygfx.Images
}

const (
	// tickEvery is how often the dashboard reads the overview engine.
	tickEvery = time.Second
	// minRefresh is the least time between two Prometheus reads of a live
	// view; one scrape interval.
	minRefresh = 15 * time.Second
	// retryAfter is the wait after a failed Prometheus read.
	retryAfter = 30 * time.Second
	// topN is how many consumers the engine returns.
	topN = 20
)

var (
	// promSpans are the time ranges a Prometheus view zooms through.
	promSpans = []time.Duration{15 * time.Minute, time.Hour, 3 * time.Hour, 12 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour}
	// sessionSpans are those of the Metrics API session history.
	sessionSpans = []time.Duration{5 * time.Minute, 15 * time.Minute, SessionRetention}
)

const (
	defaultPromSpan    = 1
	defaultSessionSpan = 1
)

// widget is a part of a view that takes the keyboard focus.
type widget int

const (
	widgetCPU widget = iota
	widgetMemory
	widgetTargets
	widgetEvents
)

// view is one dashboard of the drilldown stack.
type view struct {
	target Target
	// promIdx and sessionIdx pick the shown range among promSpans and
	// sessionSpans; a view shows the session spans while it has no
	// Prometheus result to show.
	promIdx, sessionIdx int
	// paused freezes the shown range at end; the cursor needs it.
	paused bool
	end    time.Time
	// follow shows the investigation window win (window.go); custom is
	// the span a detached window left, used until the next zoom.
	follow bool
	win    investigation.Window
	custom time.Duration
	focus  widget
	// expanded shows the focused widget alone.
	expanded bool
	// cursor is the plot cell under the cursor; -1 when there is none.
	cursor int
	// sel is the selected item of the target list, evSel the selected
	// Event.
	sel, evSel int
	// now is the last measurement; it stays as it was while paused. seen
	// is false until the engine was read once.
	now  models.UsageAmounts
	seen bool
	prom promState
}

// promState is the Prometheus read of a view.
type promState struct {
	key     string // what the shown result was read for
	usage   *promhistory.Usage
	err     error
	at      time.Time // when the last read ended
	pending string    // the key of the read in flight
	cancel  func()
	seq     int
	// span is the shown span of the last read: only the clock moving the
	// same span waits for the refresh interval.
	span time.Duration
}

// Model is an open dashboard: a stack of views, the Metrics API session
// history, and the screen size.
type Model struct {
	id    int
	src   Sources
	sess  *session
	stack []*view
	w, h  int
	mode  chart.Mode
	// pixels draws the plots as images while Sources.Images is set.
	pixels bool
	// resizes counts the size changes; settling is set from a change
	// until resizeSettle passes without another one (see resized).
	resizes  int
	settling bool
	// rasterKeys are the RasterKey of the picture last sent per image key,
	// so a cursor move rasterizes nothing.
	rasterKeys [ImageKeys]uint64
	seq        int
	// rects is where the last frame put each widget, for the mouse.
	rects map[widget]rect
	// cells is the plot width of the charts in the last frame.
	cells int
	// itemAt is the target list item of each line inside its frame in the
	// last frame, -1 for none.
	itemAt []int
	// lastWin is the investigation window the views were last pointed at.
	lastWin investigation.Window
}

// Action is what the App does after an update.
type Action struct {
	// Close leaves the dashboard; the App keeps the model to restore.
	Close bool
	// Open shows the object in Commander; the dashboard closes first.
	// Event is the Event it was opened from, if any.
	Open  *models.ObjectRef
	Event *models.UsageEvent
	// Logs shows the logs of the pod or workload in Commander with Window
	// as the investigation window; the dashboard closes first.
	Logs   *models.ObjectRef
	Window investigation.Window
	// Note is a message for the user; the dashboard stays open.
	Note string
}

// TickMsg drives an open dashboard.
type TickMsg struct{ id int }

// usageMsg is the result of a Prometheus read.
type usageMsg struct {
	id, seq int
	key     string
	usage   promhistory.Usage
	err     error
}

// IsMsg reports whether msg belongs to a dashboard; the App drops one
// that arrives when no dashboard is open.
func IsMsg(msg tea.Msg) bool {
	switch msg.(type) {
	case TickMsg, usageMsg, settledMsg:
		return true
	}
	return kittygfx.IsMsg(msg)
}

// New opens a dashboard of t.
func New(src Sources, t Target) *Model {
	m := &Model{src: src, sess: newSession(), mode: chart.Braille, pixels: true}
	m.stack = []*view{m.newView(t, nil)}
	m.firstRow(m.stack[0])
	return m
}

func (m *Model) newView(t Target, from *view) *view {
	v := &view{target: t, cursor: -1, promIdx: defaultPromSpan, sessionIdx: defaultSessionSpan, focus: widgetTargets}
	switch {
	case from != nil:
		// A drilldown keeps the time: the range, and the paused end.
		v.promIdx, v.sessionIdx, v.paused, v.end = from.promIdx, from.sessionIdx, from.paused, from.end
		v.follow, v.win, v.custom = from.follow, from.win, from.custom
	case !m.lastWin.Off():
		m.follow(v, m.lastWin)
	}
	return v
}

// Resume returns m reopened with the sources of the context it now
// runs in; a dashboard of another context starts again at t. The session
// history is kept either way.
func (m *Model) Resume(src Sources, t Target) *Model {
	if m == nil {
		return New(src, t)
	}
	m.id++
	if src.Context != m.src.Context || (src.Usage == nil) != (m.src.Usage == nil) {
		m.src = src
		m.stack = []*view{m.newView(t, nil)}
		m.firstRow(m.stack[0])
		return m
	}
	m.src = src
	return m
}

// Start returns the commands of an opened dashboard.
func (m *Model) Start() tea.Cmd {
	m.followWindow()
	m.refresh()
	cmds := []tea.Cmd{m.tick(), m.maybeRead()}
	if m.images() {
		cmds = append(cmds, kittygfx.QueryCellSize(), m.syncImages())
	}
	return tea.Batch(cmds...)
}

// Stop cancels the reads in flight; the App calls it on close.
func (m *Model) Stop() {
	for _, v := range m.stack {
		v.cancelRead()
	}
	m.id++
}

func (v *view) cancelRead() {
	if v.prom.cancel != nil {
		v.prom.cancel()
	}
	v.prom.cancel, v.prom.pending = nil, ""
}

func (m *Model) tick() tea.Cmd {
	id := m.id
	tick := m.src.Tick
	if tick == nil {
		tick = tea.Tick
	}
	return tick(tickEvery, func(time.Time) tea.Msg { return TickMsg{id: id} })
}

func (m *Model) top() *view { return m.stack[len(m.stack)-1] }

func (m *Model) now() time.Time {
	if m.src.Now != nil {
		return m.src.Now()
	}
	return time.Now()
}

// sessionShown reports whether v draws the Metrics API session history:
// without a backend, and after a failed read with no result to show.
func (m *Model) sessionShown(v *view) bool {
	return m.src.Usage == nil || v.prom.err != nil
}

// span is the range v shows.
func (v *view) span(m *Model) time.Duration {
	switch {
	case v.follow:
		start, end := m.windowBounds(v)
		return end.Sub(start)
	case v.custom > 0:
		return v.custom
	}
	if m.sessionShown(v) {
		return sessionSpans[v.sessionIdx]
	}
	return promSpans[v.promIdx]
}

// promSpan is the range v reads from Prometheus: the shown span, except
// that the session fallback's shorter steps do not change what is read.
func (v *view) promSpan(m *Model) time.Duration {
	if v.follow || v.custom > 0 {
		return v.span(m)
	}
	return promSpans[v.promIdx]
}

// zoom narrows (step -1) or widens (step 1) the shown range.
func (m *Model) zoom(v *view, step int) {
	m.detach(v)
	if c := v.custom; c > 0 {
		v.custom = 0
		v.sessionIdx = zoomFrom(sessionSpans, c, step)
		v.promIdx = zoomFrom(promSpans, c, step)
		return
	}
	if m.sessionShown(v) {
		v.sessionIdx = min(len(sessionSpans)-1, max(0, v.sessionIdx+step))
		return
	}
	v.promIdx = min(len(promSpans)-1, max(0, v.promIdx+step))
}

// shownEnd is the end of the range a view shows.
func (m *Model) shownEnd(v *view) time.Time {
	switch {
	case v.paused:
		return v.end
	case v.follow:
		_, end := m.windowBounds(v)
		return end
	}
	return m.now()
}

// refresh reads the engine for the top view and records the sample.
func (m *Model) refresh() {
	v := m.top()
	a := m.src.Amounts(v.target.Focus(), topN)
	m.sess.add(v.target.Key(), a, func(r models.UsageRow) string { return rowTarget(r).Key() })
	if !v.paused || !v.seen || v.now.Loading {
		v.now = a
	}
	v.seen = true
}

// Update handles a message for the open dashboard. With images on, it
// also sends the pictures of the next frame the terminal does not have.
func (m *Model) Update(msg tea.Msg) (tea.Cmd, Action) {
	if kittygfx.IsMsg(msg) {
		// A transmit coming back sends nothing: the live range moves
		// with the clock, so a new raster would differ and go out again,
		// and its return would start the next one, without end.
		if m.src.Images != nil && m.src.Images.Handle(msg) {
			return m.syncImages(), Action{}
		}
		return nil, Action{}
	}
	cmd, act := m.update(msg)
	if act.Close || act.Open != nil || act.Logs != nil {
		// The dashboard leaves; the App deletes the images.
		return cmd, act
	}
	return tea.Batch(cmd, m.syncImages()), act
}

func (m *Model) update(msg tea.Msg) (tea.Cmd, Action) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		changed := m.w != 0 && (m.w != msg.Width || m.h != msg.Height)
		m.w, m.h = msg.Width, msg.Height
		if changed {
			return tea.Batch(m.maybeRead(), m.resized()), Action{}
		}
		return m.maybeRead(), Action{}
	case settledMsg:
		if msg.id != m.id || msg.seq != m.resizes || !m.settling {
			return nil, Action{}
		}
		m.settling = false
		if m.images() {
			// A new font size comes with a resize.
			return kittygfx.QueryCellSize(), Action{}
		}
		return nil, Action{}
	case TickMsg:
		if msg.id != m.id {
			return nil, Action{}
		}
		m.followWindow()
		m.refresh()
		return tea.Batch(m.tick(), m.maybeRead()), Action{}
	case usageMsg:
		return m.handleUsage(msg), Action{}
	case tea.KeyPressMsg:
		return m.Key(msg.String())
	case tea.MouseClickMsg:
		return m.handleClick(msg.Mouse()), Action{}
	case tea.MouseWheelMsg:
		return m.handleWheel(msg.Mouse()), Action{}
	}
	return nil, Action{}
}

// readRange is the Prometheus range of v at the current chart width.
func (m *Model) readRange(v *view) promhistory.Range {
	end := m.shownEnd(v)
	cells := m.cells
	if cells <= 0 {
		// Before the first frame: the plot width the layout will give.
		w := max(m.w, 40)
		if r, ok := layout(v, w, max(m.h, 24))[widgetCPU]; ok {
			w = r.w - 2
		}
		cells = chart.PlotWidth(w, labelWidth)
	}
	return promhistory.RangeFor(end.Add(-v.promSpan(m)), end, chart.Samples(cells, m.mode))
}

// maybeRead starts the Prometheus read the top view needs, if any: a live
// view rereads once its range moved and the last read is old enough, a
// paused view reads only a range it has not read.
func (m *Model) maybeRead() tea.Cmd {
	v := m.top()
	if m.src.Usage == nil || v.prom.pending != "" || !v.seen {
		return nil
	}
	if (v.target.Kind == KindWorkload || v.target.Kind == KindPod) && v.now.Loading {
		// Wait for the object's identity: the read starts at its creation.
		return nil
	}
	scope, ok := v.target.Scope(v.now.UID, v.now.Created)
	if !ok {
		return nil
	}
	if !v.now.Found {
		scope.UID, scope.Since = "", time.Time{}
	}
	r := m.readRange(v)
	key := fmt.Sprintf("%+v|%d|%d|%s", scope, r.Start.Unix(), r.End.Unix(), r.Step)
	if key == v.prom.key {
		return nil
	}
	if !v.paused && v.prom.key != "" && v.prom.span == v.promSpan(m) {
		wait := minRefresh
		if v.prom.err != nil {
			wait = retryAfter
		}
		if m.now().Sub(v.prom.at) < max(wait, r.Step) {
			return nil
		}
	}
	read, cancel := m.src.Usage(scope, r)
	m.seq++
	v.prom.seq, v.prom.pending, v.prom.cancel, v.prom.span = m.seq, key, cancel, v.promSpan(m)
	id, seq := m.id, m.seq
	return func() tea.Msg {
		u, err := read()
		return usageMsg{id: id, seq: seq, key: key, usage: u, err: err}
	}
}

func (m *Model) handleUsage(msg usageMsg) tea.Cmd {
	if msg.id != m.id {
		return nil
	}
	for _, v := range m.stack {
		if v.prom.seq != msg.seq || v.prom.pending != msg.key {
			continue
		}
		if v.prom.cancel != nil {
			v.prom.cancel()
		}
		v.prom.pending, v.prom.cancel, v.prom.at, v.prom.key = "", nil, m.now(), msg.key
		v.prom.err = msg.err
		if msg.err == nil {
			u := msg.usage
			v.prom.usage = &u
		}
	}
	return nil
}

// widgets are the widgets of v in focus order.
func (v *view) widgets() []widget {
	return []widget{widgetTargets, widgetCPU, widgetMemory, widgetEvents}
}

func (v *view) moveFocus(step int) {
	ws := v.widgets()
	i := 0
	for j, w := range ws {
		if w == v.focus {
			i = j
		}
	}
	v.focus = ws[(i+step+len(ws))%len(ws)]
}

// pause freezes v at the end it shows now.
func (m *Model) pause(v *view) {
	if v.paused {
		return
	}
	// A live window stops moving, so it is no longer the window.
	m.detach(v)
	v.paused, v.end = true, m.now()
}

// Key runs a key of the dashboard, as typed or chosen from its menu bar
// or F-key bar (see Menus and FunctionKeys).
//
//nolint:gocyclo // one short case per key
func (m *Model) Key(key string) (tea.Cmd, Action) {
	v := m.top()
	if d, ok := strings.CutPrefix(key, rangeKey); ok {
		if span, ok := parseShort(d); ok {
			m.setRange(v, span)
		}
		return m.maybeRead(), Action{}
	}
	if d, ok := strings.CutPrefix(key, drawKey); ok {
		mode, pixels, ok := m.drawNamed(d)
		if !ok {
			return nil, Action{}
		}
		return tea.Batch(m.drawAs(mode, pixels), m.maybeRead()), Action{}
	}
	switch key {
	case "esc":
		return m.back()
	case "f10":
		return nil, Action{Close: true}
	case "backspace", "ctrl+h": // terminals send DEL or ^H
		m.up()
	case "f4":
		v.expanded = !v.expanded
	case "tab":
		v.moveFocus(1)
	case "shift+tab":
		v.moveFocus(-1)
	case "enter":
		switch v.focus {
		case widgetEvents:
			return nil, m.open(v)
		case widgetTargets:
			m.drill(v)
		default:
			v.expanded = !v.expanded
		}
	case "space", " ", "f3":
		if v.paused {
			m.detach(v)
			v.paused, v.cursor = false, -1
			m.refresh()
		} else {
			m.pause(v)
		}
	case "left", "right", "home", "end":
		m.moveCursor(v, key)
	case "up":
		m.moveSelection(v, -1)
	case "down":
		m.moveSelection(v, 1)
	case "+", "=":
		m.zoom(v, -1)
	case "-":
		m.zoom(v, 1)
	case "b", "f6":
		mode, pixels := m.nextDraw()
		return tea.Batch(m.drawAs(mode, pixels), m.maybeRead()), Action{}
	case "r":
		v.prom.key, v.prom.at = "", time.Time{}
	case "o":
		return nil, m.open(v)
	case "w", "f5":
		m.useAsWindow(v)
	case "l", "f7":
		return nil, m.eventLogs(v)
	default:
		return nil, Action{}
	}
	return m.maybeRead(), Action{}
}

// moveSelection moves the selection of the focused list by step.
func (m *Model) moveSelection(v *view, step int) {
	switch v.focus {
	case widgetTargets:
		v.sel = min(max(0, len(m.targetItems(v))-1), max(0, v.sel+step))
	case widgetEvents:
		v.evSel = min(max(0, len(m.shownEvents(v))-1), max(0, v.evSel+step))
	}
}

// back is Esc: it collapses an expanded widget, then removes the cursor,
// then leaves a drilldown, then closes the dashboard.
func (m *Model) back() (tea.Cmd, Action) {
	v := m.top()
	switch {
	case v.expanded:
		v.expanded = false
	case v.cursor >= 0:
		v.cursor = -1
	case len(m.stack) > 1:
		v.cancelRead()
		m.stack = m.stack[:len(m.stack)-1]
		m.refresh()
		return m.maybeRead(), Action{}
	default:
		return nil, Action{Close: true}
	}
	return nil, Action{}
}

// open shows the selected consumer, else the target, in Commander.
func (m *Model) open(v *view) Action {
	var ref models.ObjectRef
	ok := false
	if e, isEvent := m.selectedEvent(v); isEvent {
		// The involved object, else the Event itself.
		ref = e.Object
		if ref.GVR.Empty() {
			ref = e.Event
		}
		return Action{Open: &ref, Event: &e}
	}
	if items := m.targetItems(v); v.focus == widgetTargets && v.sel < len(items) && !items[v.sel].up {
		it := items[v.sel]
		uid := types.UID("")
		if it.target == v.target {
			uid = v.now.UID
		}
		ref, ok = it.target.Object(uid)
	} else {
		ref, ok = v.target.Object(v.now.UID)
	}
	if !ok {
		return Action{}
	}
	return Action{Open: &ref}
}

// moveCursor moves the cursor over the plot cells. The first move puts it
// on the newest cell and pauses the view; moving past an edge pans the
// paused range by a quarter.
func (m *Model) moveCursor(v *view, key string) {
	cells := max(1, m.cells)
	v.evSel = 0
	if v.cursor < 0 {
		m.pause(v)
		v.cursor = cells - 1
		if key == "home" {
			v.cursor = 0
		}
		return
	}
	pan := m.shownSpan(v) / 4
	switch key {
	case "left":
		if v.cursor == 0 {
			m.detach(v)
			v.end = v.end.Add(-pan)
		}
		v.cursor = max(0, v.cursor-1)
	case "right":
		if v.cursor == cells-1 {
			m.detach(v)
			v.end = minTime(v.end.Add(pan), m.now())
		}
		v.cursor = min(cells-1, v.cursor+1)
	case "home":
		v.cursor = 0
	case "end":
		v.cursor = cells - 1
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (m *Model) handleClick(mouse tea.Mouse) tea.Cmd {
	v := m.top()
	for w, r := range m.rects {
		if !r.contains(mouse.X, mouse.Y) {
			continue
		}
		v.focus = w
		switch w {
		case widgetTargets:
			line := mouse.Y - r.y - 1
			if line < 0 || line >= len(m.itemAt) || m.itemAt[line] < 0 {
				return nil
			}
			if item := m.itemAt[line]; item == v.sel {
				m.drill(v)
			} else {
				v.sel = item
			}
			return m.maybeRead()
		case widgetEvents:
			line := mouse.Y - r.y - 1
			if row := line - eventsListAt; row >= 0 && row < len(m.shownEvents(v)) {
				v.evSel = row
			} else if cell := mouse.X - r.plotX; line < eventsListAt && cell >= 0 && cell < m.cells {
				m.pause(v)
				v.cursor, v.evSel = cell, 0
			}
		default:
			if cell := mouse.X - r.plotX; cell >= 0 && cell < m.cells {
				m.pause(v)
				v.cursor = cell
			}
		}
		return nil
	}
	return nil
}

func (m *Model) handleWheel(mouse tea.Mouse) tea.Cmd {
	v := m.top()
	for w, r := range m.rects {
		if w == widgetTargets || w == widgetEvents || !r.contains(mouse.X, mouse.Y) {
			continue
		}
		switch mouse.Button {
		case tea.MouseWheelUp:
			m.zoom(v, -1)
		case tea.MouseWheelDown:
			m.zoom(v, 1)
		}
		return m.maybeRead()
	}
	return nil
}

// Root is the target the dashboard was opened for.
func (m *Model) Root() Target { return m.stack[0].target }

// EventsFocused reports whether the Events list has the keyboard focus,
// so o, Enter and l act on the selected Event.
func (m *Model) EventsFocused() bool { return m.top().focus == widgetEvents }

// Context is the kube context the dashboard reads.
func (m *Model) Context() string { return m.src.Context }

// Retarget starts the dashboard again at t, keeping the session history
// and the draw mode.
func (m *Model) Retarget(t Target) *Model {
	for _, v := range m.stack {
		v.cancelRead()
	}
	m.stack = []*view{m.newView(t, nil)}
	m.firstRow(m.stack[0])
	return m
}
