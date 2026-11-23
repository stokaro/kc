package dashboard

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stokaro/kc/internal/investigation"
	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/promhistory"
	"github.com/stokaro/kc/internal/ui/chart"
)

// Sources are what a dashboard reads. The App builds them for one
// context and namespace scope; tests give fakes.
type Sources struct {
	// Context names the kube context for the title bar.
	Context string
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
	widgetTop
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
	// sel is the selected consumer, evSel the selected Event.
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
	seq   int
	// rects is where the last frame put each widget, for the mouse.
	rects map[widget]rect
	// cells is the plot width of the charts in the last frame.
	cells int
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
	case TickMsg, usageMsg:
		return true
	}
	return false
}

// New opens a dashboard of t.
func New(src Sources, t Target) *Model {
	m := &Model{src: src, sess: newSession(), mode: chart.Braille}
	m.stack = []*view{m.newView(t, nil)}
	return m
}

func (m *Model) newView(t Target, from *view) *view {
	v := &view{target: t, cursor: -1, promIdx: defaultPromSpan, sessionIdx: defaultSessionSpan}
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
		return m
	}
	m.src = src
	return m
}

// Start returns the commands of an opened dashboard.
func (m *Model) Start() tea.Cmd {
	m.followWindow()
	m.refresh()
	return tea.Batch(m.tick(), m.maybeRead())
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
	m.sess.add(v.target.Key(), a)
	if !v.paused || !v.seen || v.now.Loading {
		v.now = a
	}
	v.seen = true
	v.sel = min(v.sel, max(0, len(v.now.Top)-1))
}

// Update handles a message for the open dashboard.
func (m *Model) Update(msg tea.Msg) (tea.Cmd, Action) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m.maybeRead(), Action{}
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
		return m.handleKey(msg)
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
		cells = chart.PlotWidth(max(m.w, 40), labelWidth)
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
	if v.target.Kind == KindPod {
		return []widget{widgetCPU, widgetMemory, widgetEvents}
	}
	return []widget{widgetCPU, widgetMemory, widgetEvents, widgetTop}
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

func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Cmd, Action) {
	v := m.top()
	switch msg.String() {
	case "esc":
		return m.back()
	case "tab":
		v.moveFocus(1)
	case "shift+tab":
		v.moveFocus(-1)
	case "enter":
		if v.focus == widgetEvents {
			return nil, m.open(v)
		}
		return m.enter(v), Action{}
	case "space", " ":
		if v.paused {
			m.detach(v)
			v.paused, v.cursor = false, -1
			m.refresh()
		} else {
			m.pause(v)
		}
	case "left", "right", "home", "end":
		m.moveCursor(v, msg.String())
	case "up":
		m.moveSelection(v, -1)
	case "down":
		m.moveSelection(v, 1)
	case "+", "=":
		m.zoom(v, -1)
	case "-":
		m.zoom(v, 1)
	case "b":
		m.mode = map[chart.Mode]chart.Mode{chart.Braille: chart.Blocks, chart.Blocks: chart.Braille}[m.mode]
	case "r":
		v.prom.key, v.prom.at = "", time.Time{}
	case "o":
		return nil, m.open(v)
	case "w":
		m.useAsWindow(v)
	case "l":
		return nil, m.eventLogs(v)
	default:
		return nil, Action{}
	}
	return m.maybeRead(), Action{}
}

// moveSelection moves the selection of the focused list by step.
func (m *Model) moveSelection(v *view, step int) {
	switch v.focus {
	case widgetTop:
		v.sel = min(max(0, len(v.now.Top)-1), max(0, v.sel+step))
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

// enter expands or collapses a chart, and opens the dashboard of the
// selected pod from the consumers.
func (m *Model) enter(v *view) tea.Cmd {
	if v.focus == widgetEvents {
		return nil
	}
	if v.focus != widgetTop {
		v.expanded = !v.expanded
		return nil
	}
	if v.sel >= len(v.now.Top) {
		return nil
	}
	c := v.now.Top[v.sel]
	next := m.newView(v.target.PodTarget(c.Namespace, c.Name), v)
	m.stack = append(m.stack, next)
	m.refresh()
	return m.maybeRead()
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
	if v.focus == widgetTop && v.sel < len(v.now.Top) {
		c := v.now.Top[v.sel]
		ref, ok = v.target.PodTarget(c.Namespace, c.Name).Object(types.UID(""))
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
	if v.focus == widgetTop {
		v.focus = widgetCPU
	}
	v.evSel = 0
	if v.cursor < 0 {
		m.pause(v)
		v.cursor = cells - 1
		if key == "home" {
			v.cursor = 0
		}
		return
	}
	pan := v.span(m) / 4
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
		case widgetTop:
			row := mouse.Y - r.y - 1
			if row < 0 || row >= len(v.now.Top) {
				return nil
			}
			if row == v.sel {
				return m.enter(v)
			}
			v.sel = row
		case widgetEvents:
			if row := mouse.Y - r.y - 2; row >= 0 && row < len(m.shownEvents(v)) {
				v.evSel = row
			} else if cell := mouse.X - (r.x + r.w - m.cells); row == -1 && cell >= 0 && cell < m.cells {
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
		if w == widgetTop || w == widgetEvents || !r.contains(mouse.X, mouse.Y) {
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

// Context is the kube context the dashboard reads.
func (m *Model) Context() string { return m.src.Context }

// Retarget starts the dashboard again at t, keeping the session history
// and the draw mode.
func (m *Model) Retarget(t Target) *Model {
	for _, v := range m.stack {
		v.cancelRead()
	}
	m.stack = []*view{m.newView(t, nil)}
	return m
}
