package ui

import (
	models "github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
	infowidget "github.com/stokaro/kc/internal/ui/panelcontent/info"
	logswidget "github.com/stokaro/kc/internal/ui/panelcontent/logs"
)

// A follower shows the object under the other panel's cursor. Logs and
// Info keep the object they show while that cursor is on a row that names
// no object, and the frame of every follower names the connection and
// namespace its object was read in; see
// docs/adr/0403-logs-info-keep-their-object.md.

// followOrigin is where the object a follower shows was read: the
// connection, which the follower holds while it shows the object, and the
// namespaces of the panel the object was read in. The zero origin means
// the follower shows no object, or is pinned; its frame names the other
// panel or the pin then.
type followOrigin struct {
	conn *panelConn
	// scope is the namespace scope of that panel, which the frame names.
	scope string
	// overview is the namespace of the list that panel showed, which Info
	// summarizes (App.overviewNamespace).
	overview string
	// exec is the namespace the shell line had there
	// (App.panelExecNamespace).
	exec    string
	release func()
}

// holdOrigin takes a hold on c for a follower that shows an object read
// from c. Without a connection there is no origin.
func holdOrigin(c *panelConn, scope, overview, exec string) followOrigin {
	if c == nil {
		return followOrigin{}
	}
	return followOrigin{conn: c, scope: scope, overview: overview, exec: exec, release: c.hold()}
}

// drop gives up the origin's hold on its connection.
func (o followOrigin) drop() {
	if o.release != nil {
		o.release()
	}
}

// namesObject reports whether item, the row under a followed cursor,
// names an object: a Kubernetes object, a container or log row of a pod,
// or an /overview finding. "..", a folder row and an empty list (nil)
// name none.
func namesObject(item models.Item) bool {
	if item == nil {
		return false
	}
	if _, ok := item.(models.FindingPreviewer); ok {
		return true
	}
	if obj, ok := item.(models.ObjectItem); ok && obj.GVR().Resource != "" && obj.Name() != "" {
		return true
	}
	_, ok := logswidget.Target(item)
	return ok
}

// keepsShown reports whether follower panel keeps what it shows when the
// followed cursor moves to a row that names no object. Logs keeps the pod
// it streams, so browsing the other panel does not cut the stream; Info
// keeps the node, workload or finding it summarizes. Describe and Manifest
// keep nothing, and neither does a Logs or Info panel that shows a hint or
// a namespace, or an object of unknown origin: the frame could not name
// where it was read.
func keepsShown(panel *Panel) bool {
	item := panel.lastSelection
	if panel.origin.conn == nil {
		return false
	}
	switch panel.Mode() {
	case PanelModeLogs:
		_, ok := logswidget.Target(item)
		return ok
	case PanelModeInfo:
		var t infowidget.Target
		narrowInfoTarget(&t, item, panel.origin.conn.clusterKey())
		return t.Finding != nil || t.Node != "" || t.Workload != nil
	default:
		return false
	}
}

// followRule decides what follower panel does with sel, the other panel's
// selection or the pin's item. keep reports that the panel keeps what it
// shows (keepsShown) because sel names no object. Otherwise origin is
// where sel's object was read, held for the panel; it is zero when sel
// names no object, and while a pin holds the connection.
func (a *App) followRule(panel *Panel, sel panelcontent.Selection) (origin followOrigin, keep bool) {
	if panel.pinned() != nil {
		return followOrigin{}, false
	}
	if !namesObject(sel.Item) {
		return followOrigin{}, keepsShown(panel)
	}
	idx, ok := a.panelIndexFor(panel)
	if !ok {
		return followOrigin{}, false
	}
	other := 1 - idx
	conn := a.connByID(sel.Source)
	if conn == nil && sel.Source == "" {
		conn = a.ownConn(other)
	}
	return holdOrigin(conn, a.panelScope[other], a.overviewNamespace(other), a.panelExecNamespace(a.panelByIndex(other))), false
}

// unpinFollow hands the pinned object of panel and its connection back to
// the follower before the pin goes, so that a Logs or Info panel keeps it
// under its own frame while the other cursor names no object.
func unpinFollow(panel *Panel, pin *panelPin) {
	if pin.item == nil || pin.conn == nil {
		return
	}
	panel.follow(pin.item, pin.conn.connID(), holdOrigin(pin.conn, pin.scope, pin.scope, pin.scope))
}
