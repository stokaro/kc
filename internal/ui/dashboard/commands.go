package dashboard

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/ui/chart"
	"github.com/stokaro/kc/internal/ui/menubar"
)

// rangeKey is the prefix of the keys the Range menu runs: "range:15m".
const rangeKey = "range:"

// FunctionKey is one slot of the dashboard's F-key bar: F1 is the first.
// An empty Label is an unused slot.
type FunctionKey struct {
	Label   string
	Enabled bool
}

// FunctionKeys are the F-key bar of the dashboard as it is now. F1 (help),
// F2 (the Range menu) and F9 (the menu bar) are run by the App; the others
// are keys of Key ("f3" to "f10").
func (m *Model) FunctionKeys() []FunctionKey {
	v := m.top()
	pause := "Pause"
	if v.paused {
		pause = "Live"
	}
	mode := "Blocks"
	if m.mode == chart.Blocks {
		mode = "Braille"
	}
	expand := "Expand"
	if v.expanded {
		expand = "Restore"
	}
	return []FunctionKey{
		{"Help", true},
		{"Range", true},
		{pause, true},
		{expand, true},
		{"Window", m.src.SetWindow != nil},
		{mode, true},
		{"Logs", true},
		{"", false},
		{"PullDn", true},
		{"Close", true},
	}
}

// RangeMenu is the index of the Range menu in Menus, which F2 drops down.
const RangeMenu = 1

// Menus are the dashboard's menu bar; each item runs its key through run,
// which the App gives Key. run may be nil to draw the titles only.
func (m *Model) Menus(run func(key string) tea.Cmd) []menubar.Menu {
	v := m.top()
	item := func(label, short, key string) menubar.Item {
		it := menubar.Item{Label: label, Shortcut: short}
		if run != nil {
			it.Run = func() tea.Cmd { return run(key) }
		}
		return it
	}
	pause := item("&Pause", "Space", "space")
	if v.paused {
		pause = item("Go &live", "Space", "space")
	}
	reread := item("&Read Prometheus again", "r", "r")
	reread.Disabled = m.src.Usage == nil
	up := item("&Up a level", "Bksp", "backspace")
	if _, ok := m.parent(v); !ok && len(m.stack) == 1 {
		up.Disabled = true
	}
	dash := menubar.Menu{Title: "&Dashboard", Items: []menubar.Item{
		pause, up,
		item("&Open in a panel", "o", "o"),
		item("&Logs around the cursor", "l", "l"),
		item("Set the investigation &window", "w", "w"),
		reread,
		menubar.Separator(),
		item("&Close", "F10", "f10"),
	}}
	var ranges []menubar.Item
	cur := v.span(m)
	for _, d := range m.spans(v) {
		it := item("Last "+longDuration(d), "", rangeKey+shortDuration(d))
		it.Checked = d == cur && !v.follow
		ranges = append(ranges, it)
	}
	ranges = append(ranges, menubar.Separator(), item("Zoom &in", "+", "+"), item("Zoom &out", "-", "-"))
	blocks := item("&Blocks, for fonts without braille", "b", "b")
	blocks.Checked = m.mode == chart.Blocks
	view := menubar.Menu{Title: "&View", Items: []menubar.Item{
		item("&Next widget", "Tab", "tab"),
		item("&Expand or restore the widget", "F4", "f4"),
		blocks,
	}}
	help := menubar.Menu{Title: "&Help", Items: []menubar.Item{item("&Dashboard", "F1", "f1")}}
	return []menubar.Menu{dash, {Title: "&Range", Items: ranges}, view, help}
}

// spans are the ranges v zooms through.
func (m *Model) spans(v *view) []time.Duration {
	if m.sessionShown(v) {
		return sessionSpans
	}
	return promSpans
}

// setRange shows the last d.
func (m *Model) setRange(v *view, d time.Duration) {
	m.detach(v)
	v.custom = 0
	for i, s := range sessionSpans {
		if s == d {
			v.sessionIdx = i
		}
	}
	for i, s := range promSpans {
		if s == d {
			v.promIdx = i
		}
	}
}

// parseShort reads what shortDuration writes.
func parseShort(s string) (time.Duration, bool) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		d, err := time.ParseDuration(days + "h")
		return d * 24, err == nil
	}
	d, err := time.ParseDuration(s)
	return d, err == nil
}

// longDuration writes 15 minutes, 1 hour, 7 days.
func longDuration(d time.Duration) string {
	s := shortDuration(d)
	n, unit := s[:len(s)-1], s[len(s)-1:]
	word := map[string]string{"s": "second", "m": "minute", "h": "hour", "d": "day"}[unit]
	if n != "1" {
		word += "s"
	}
	return n + " " + word
}
