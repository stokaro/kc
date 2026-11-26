package ui

import (
	"context"
	"strings"
	"sync"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	kccluster "github.com/stokaro/kc/internal/cluster"
	models "github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// A pinned panel keeps its target while the other panel browses: the
// other panel's selection no longer reaches it. A frozen panel keeps
// showing what it showed when it was frozen. Both are explained in
// docs/adr/0187-pin-freeze-window.md.

// followerMode reports whether a panel in mode follows the other panel's
// selection, and so can be pinned.
func followerMode(mode PanelViewMode) bool {
	switch mode {
	case PanelModeDescribe, PanelModeManifest, PanelModeLogs, PanelModeInfo:
		return true
	}
	return false
}

// pinIdentity names the object a pinned row stands for: the context, the
// full GVR, the namespace, the name and, when the row knows it, the UID.
type pinIdentity struct {
	cluster   kccluster.Key
	gvr       schema.GroupVersionResource
	namespace string
	name      string
	uid       types.UID
}

// label is the identity as the frame shows it.
func (id pinIdentity) label() string {
	what := id.gvr.Resource
	if id.gvr.Group != "" {
		what += "." + id.gvr.Group
	}
	name := id.name
	if id.namespace != "" {
		name = id.namespace + "/" + name
	}
	return strings.TrimSpace(what + " " + name)
}

// panelPin is the target of a pinned panel.
type panelPin struct {
	// item is replayed to the widget instead of the other panel's
	// selection; id is its row ID.
	item models.Item
	id   string
	// scope is the namespace an Info panel summarizes.
	scope string
	ident pinIdentity
	// watch reports the instances the shared cache sees under the name;
	// nil when the row names no object.
	watch *pinWatch
	// lost says why the pinned instance is no longer what the name shows:
	// deleted, or replaced by another UID. The panel is frozen then.
	lost string
	// conn is the connection the pinned item was read from; the pin
	// holds it until release (docs/adr/0362-independent-panel-contexts.md).
	conn    *panelConn
	release func()
}

// close stops the pin's watch and gives up its hold on the connection.
func (pin *panelPin) close() {
	pin.watch.close()
	if pin.release != nil {
		pin.release()
		pin.release = nil
	}
}

// pinWatch keeps the last thing the shared cache said about a pinned
// name. The informer's goroutine writes it; the UI goroutine reads it
// after a pinSignalMsg.
type pinWatch struct {
	mu     sync.Mutex
	last   models.ObjectSighting
	seen   bool
	stop   func()
	closed bool
}

// attach keeps the func that removes the handler; a watch closed before
// its handler was added removes it at once.
func (w *pinWatch) attach(stop func()) {
	w.mu.Lock()
	closed := w.closed
	if !closed {
		w.stop = stop
	}
	w.mu.Unlock()
	if closed {
		stop()
	}
}

func (w *pinWatch) set(s models.ObjectSighting) {
	w.mu.Lock()
	w.last, w.seen = s, true
	w.mu.Unlock()
}

func (w *pinWatch) get() (models.ObjectSighting, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last, w.seen
}

func (w *pinWatch) close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	stop := w.stop
	w.stop, w.closed = nil, true
	w.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// panelFreeze is the still picture of a frozen panel.
type panelFreeze struct {
	at time.Time
	// reason is why kc froze the panel itself (the pinned object went);
	// empty when the user froze it.
	reason  string
	content string
	info    panelcontent.FrameInfo
	// missed is set when a selection arrived while frozen.
	missed bool
}

// pinned returns the panel's pin, nil when it follows the other panel.
func (p *Panel) pinned() *panelPin { return p.pin }

// SetPin pins the panel to pin. The next selection the panel gets shows
// pin's item.
func (p *Panel) SetPin(pin *panelPin) {
	if p.pin != nil && p.pin != pin {
		p.pin.close()
	}
	p.pin = pin
	p.invalidateRenderCache()
}

// ClearPin returns the panel to following the other panel; the caller
// replays the other panel's selection.
func (p *Panel) ClearPin() {
	if p.pin == nil {
		return
	}
	p.pin.close()
	p.pin = nil
	p.invalidateRenderCache()
}

// Frozen reports whether the panel shows a still picture.
func (p *Panel) Frozen() bool { return p.freeze != nil }

// FrozenAt returns when the panel was frozen.
func (p *Panel) FrozenAt() (time.Time, bool) {
	if p.freeze == nil {
		return time.Time{}, false
	}
	return p.freeze.at, true
}

// Freeze keeps what the panel shows now, with the frame it has now, until
// Thaw. reason is set when kc froze the panel itself.
func (p *Panel) Freeze(ctx context.Context, at time.Time, reason string) {
	if p.freeze != nil {
		if reason != "" {
			p.freeze.reason = reason
			p.invalidateRenderCache()
		}
		return
	}
	info := p.widgetFrameInfo(ctx, p.width)
	var content string
	if tv, ok := p.ensureActiveWidget(ctx).(panelcontent.TextViewer); ok {
		// The widget goes on replacing its images; the still picture
		// keeps their text.
		content = viewString(tv.TextView(ctx, panelcontent.Frame{Size: panelcontent.Size{Width: p.width, Height: p.height}}))
	} else {
		content = viewString(p.renderContentFocused(ctx, false))
	}
	p.freeze = &panelFreeze{at: at, reason: reason, content: content, info: info}
	p.invalidateRenderCache()
}

// Thaw shows the live widget again. It reports whether a selection
// arrived while the panel was frozen.
func (p *Panel) Thaw() (missed bool) {
	if p.freeze == nil {
		return false
	}
	missed = p.freeze.missed
	p.freeze = nil
	p.invalidateRenderCache()
	return missed
}

// pinnedSelection replaces sel by the pinned item.
func (p *Panel) pinnedSelection(sel panelcontent.Selection) panelcontent.Selection {
	if p.pin == nil || p.pin.item == nil {
		return sel
	}
	return panelcontent.Selection{ID: p.pin.id, Path: sel.Path, Item: p.pin.item, Source: p.pin.conn.connID(), Force: sel.Force}
}

// decorateFrame marks a pinned or frozen panel in its frame. A frozen
// panel shows the frame of the moment it was frozen, with the live status
// replaced: nothing in it may read as current.
func (p *Panel) decorateFrame(info panelcontent.FrameInfo) panelcontent.FrameInfo {
	if p.freeze != nil {
		status := "FROZEN " + p.freeze.at.Format("15:04:05 -07:00")
		if p.freeze.reason != "" {
			status += ": " + p.freeze.reason
		}
		info.HeaderStatus = status
		info.FooterStatus = ""
		info.SuppressFooter = true
	}
	if p.pin != nil {
		mark := "[pinned"
		if p.pin.lost != "" {
			mark += ", " + p.pin.lost
		}
		info.Breadcrumb = mark + "] " + info.Breadcrumb
	}
	return info
}

// frozenView fits the still picture to the panel's current size.
func (p *Panel) frozenView(width, height int) string {
	lines := strings.Split(p.freeze.content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, l := range lines {
		if lipgloss.Width(l) > width {
			lines[i] = ansi.Truncate(l, width, "")
		}
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}
