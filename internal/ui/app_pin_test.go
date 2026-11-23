package ui

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	models "github.com/stokaro/kc/internal/models"
	modeltesting "github.com/stokaro/kc/internal/models/testing"
	navui "github.com/stokaro/kc/internal/navigation"
	"github.com/stokaro/kc/internal/table"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

var pinPodsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

func pinPod(name string, uid types.UID) *models.ObjectRow {
	row := models.NewObjectRow(name, []string{name}, []string{"namespaces", "ns", "pods", name}, pinPodsGVR, "ns", name, nil)
	row.SetInstance(uid, models.ObjectOrigin{})
	return row
}

// pinApp has two pods in the left list and the right panel in mode,
// following the left panel's cursor on web.
func pinApp(t *testing.T, mode PanelViewMode) (*App, map[string]*models.ObjectRow) {
	t.Helper()
	ctx := t.Context()
	a := NewApp()
	t.Cleanup(a.cancel)
	a.width, a.height = 120, 30
	pods := map[string]*models.ObjectRow{"web": pinPod("web", "uid-web"), "api": pinPod("api", "uid-api")}
	folder := modeltesting.NewStaticFolder("pods", []table.Column{{Title: " Name"}}, []table.Row{pods["web"], pods["api"]})
	a.leftNav = navui.NewNavigator(folder)
	a.rightNav = navui.NewNavigator(modeltesting.NewStaticFolder("/", nil, nil))
	a.leftPanel.UseFolder(ctx, true)
	a.rightPanel.UseFolder(ctx, true)
	a.syncPanelWithNavigator(0)
	a.syncPanelWithNavigator(1)
	a.leftPanel.SelectByRowID(ctx, "web")
	a.setPanelMode(ctx, a.rightPanel, mode, "test")
	a.activePanel = 0
	return a, pods
}

// moveLeft puts the left panel's cursor on name, as the list reports it.
func moveLeft(t *testing.T, a *App, item *models.ObjectRow) {
	t.Helper()
	a.leftPanel.SelectByRowID(t.Context(), item.Name())
	a.Update(PanelSelectionChangedMsg{Panel: a.leftPanel, Selection: panelcontent.Selection{ID: item.Name(), Item: item}})
}

func shownName(t *testing.T, p *Panel) string {
	t.Helper()
	sel := p.CurrentSelection(t.Context())
	if sel.Item == nil {
		return ""
	}
	return selectionRowID(sel.Item)
}

func frameOf(t *testing.T, p *Panel) panelcontent.FrameInfo {
	t.Helper()
	return p.frameInfo(t.Context(), 100)
}

func TestPinnedPanelKeepsItsTarget(t *testing.T) {
	t.Parallel()
	for _, mode := range []PanelViewMode{PanelModeDescribe, PanelModeManifest, PanelModeLogs} {
		t.Run(modeLabel(mode), func(t *testing.T) {
			t.Parallel()
			a, pods := pinApp(t, mode)
			moveLeft(t, a, pods["web"])
			if got := shownName(t, a.rightPanel); got != "web" {
				t.Fatalf("before pinning the right panel shows %q", got)
			}
			a.togglePin()
			pin := a.rightPanel.pinned()
			if pin == nil {
				t.Fatal("C-x p did not pin the follower panel")
			}
			if pin.ident.gvr != pinPodsGVR || pin.ident.namespace != "ns" || pin.ident.name != "web" || pin.ident.uid != "uid-web" {
				t.Fatalf("identity = %+v", pin.ident)
			}
			if got := frameOf(t, a.rightPanel).Breadcrumb; !strings.HasPrefix(got, "[pinned] ") {
				t.Fatalf("breadcrumb = %q, want the pinned mark", got)
			}

			moveLeft(t, a, pods["api"])
			if got := shownName(t, a.rightPanel); got != "web" {
				t.Fatalf("browsing changed the pinned panel to %q", got)
			}
			// A mode change between follower modes keeps the pin and
			// shows the pinned row, not the other panel's.
			next := PanelModeDescribe
			if mode == PanelModeDescribe {
				next = PanelModeManifest
			}
			a.Update(PanelModeSelectedMsg{PanelIndex: 1, Mode: next})
			if a.rightPanel.pinned() == nil || shownName(t, a.rightPanel) != "web" {
				t.Fatalf("after a mode change: pin %v, shows %q", a.rightPanel.pinned(), shownName(t, a.rightPanel))
			}

			a.togglePin()
			if a.rightPanel.pinned() != nil {
				t.Fatal("C-x p did not unpin")
			}
			if got := shownName(t, a.rightPanel); got != "api" {
				t.Fatalf("after unpinning the panel shows %q, want the other panel's api", got)
			}
			if got := frameOf(t, a.rightPanel).Breadcrumb; strings.Contains(got, "[pinned") {
				t.Fatalf("breadcrumb still pinned: %q", got)
			}
		})
	}
}

func TestPinIsDroppedOutsideFollowerModes(t *testing.T) {
	t.Parallel()
	a, pods := pinApp(t, PanelModeDescribe)
	moveLeft(t, a, pods["web"])
	a.togglePin()
	a.setPanelMode(t.Context(), a.rightPanel, PanelModeList, "test")
	if a.rightPanel.pinned() != nil {
		t.Fatal("a list panel kept a pin")
	}
	// No follower panel: the key says so instead of pinning a list.
	a.setPanelMode(t.Context(), a.rightPanel, PanelModeList, "test")
	if cmd := a.togglePin(); cmd == nil || a.leftPanel.pinned() != nil || a.rightPanel.pinned() != nil {
		t.Fatal("C-x p with no follower panel must only explain")
	}
}

// The cache reports another UID, then a deletion, under the pinned name:
// the panel freezes with the reason and cannot be thawed into the other
// object; unpinning returns to following.
func TestPinnedObjectReplacedOrDeleted(t *testing.T) {
	t.Parallel()
	a, pods := pinApp(t, PanelModeDescribe)
	moveLeft(t, a, pods["web"])
	a.togglePin()
	pin := a.rightPanel.pinned()
	if pin.watch == nil {
		t.Fatal("an object pin has no watch")
	}

	// The current instance changes nothing.
	pin.watch.set(models.ObjectSighting{UID: "uid-web"})
	a.checkPins()
	if a.rightPanel.Frozen() {
		t.Fatal("a sighting of the pinned UID froze the panel")
	}

	pin.watch.set(models.ObjectSighting{UID: "uid-web-2"})
	a.checkPins()
	if !a.rightPanel.Frozen() || pin.lost != "replaced by uid uid-web-" {
		t.Fatalf("frozen %v, lost %q", a.rightPanel.Frozen(), pin.lost)
	}
	info := frameOf(t, a.rightPanel)
	if !strings.Contains(info.HeaderStatus, "FROZEN ") || !strings.Contains(info.HeaderStatus, "pinned object replaced by uid") {
		t.Fatalf("header = %q", info.HeaderStatus)
	}
	if !strings.HasPrefix(info.Breadcrumb, "[pinned, replaced by uid uid-web-] ") {
		t.Fatalf("breadcrumb = %q", info.Breadcrumb)
	}
	a.toggleFreeze()
	if !a.rightPanel.Frozen() {
		t.Fatal("C-x f showed live data of another object under the pinned name")
	}

	a.togglePin()
	if a.rightPanel.Frozen() || a.rightPanel.pinned() != nil {
		t.Fatal("unpinning did not return to live following")
	}

	// A deletion of the pinned UID.
	a.togglePin()
	pin = a.rightPanel.pinned()
	pin.watch.set(models.ObjectSighting{UID: "uid-web", Gone: true})
	a.checkPins()
	if pin.lost != "deleted" || !a.rightPanel.Frozen() {
		t.Fatalf("lost %q, frozen %v", pin.lost, a.rightPanel.Frozen())
	}
}

func TestPinLoss(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		uid     types.UID
		s       models.ObjectSighting
		want    string
		learned types.UID
	}{
		{name: "same instance", uid: "a", s: models.ObjectSighting{UID: "a"}},
		{name: "unknown uid is learned", s: models.ObjectSighting{UID: "b"}, learned: "b"},
		{name: "replaced", uid: "a", s: models.ObjectSighting{UID: "b"}, want: "replaced by uid b"},
		{name: "deleted", uid: "a", s: models.ObjectSighting{UID: "a", Gone: true}, want: "deleted"},
		{name: "deleted before the uid was known", s: models.ObjectSighting{UID: "z", Gone: true}, want: "deleted"},
		{name: "an old instance going is not this one", uid: "a", s: models.ObjectSighting{UID: "old", Gone: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id := pinIdentity{uid: tt.uid}
			if got := pinLoss(&id, tt.s); got != tt.want {
				t.Fatalf("pinLoss = %q, want %q", got, tt.want)
			}
			if tt.learned != "" && id.uid != tt.learned {
				t.Fatalf("uid = %q, want %q", id.uid, tt.learned)
			}
		})
	}
}

// A frozen panel is labeled with its capture time, does not follow the
// other panel, and catches up when thawed. TestFrozenPictureIsTheOldContent
// proves the picture itself.
func TestFrozenPanelHoldsItsPicture(t *testing.T) {
	t.Parallel()
	a, pods := pinApp(t, PanelModeDescribe)
	moveLeft(t, a, pods["web"])
	a.toggleFreeze()
	at, ok := a.rightPanel.FrozenAt()
	if !ok || time.Since(at) > time.Minute {
		t.Fatalf("frozen at %v, %v", at, ok)
	}
	if h := frameOf(t, a.rightPanel).HeaderStatus; !strings.HasPrefix(h, "FROZEN "+at.Format("15:04:05 -07:00")) {
		t.Fatalf("header = %q", h)
	}

	moveLeft(t, a, pods["api"])
	if got := shownName(t, a.rightPanel); got != "web" {
		t.Fatalf("a frozen panel followed the selection to %q", got)
	}

	a.toggleFreeze()
	if a.rightPanel.Frozen() {
		t.Fatal("C-x f did not thaw")
	}
	if got := shownName(t, a.rightPanel); got != "api" {
		t.Fatalf("after thawing the panel shows %q, want the selection that arrived meanwhile", got)
	}
}

// The window reaches the Logs panels, also ones created later, and Info
// panels read it.
func TestWindowReachesLogsPanels(t *testing.T) {
	t.Parallel()
	a, _ := pinApp(t, PanelModeLogs)
	a.handleWindowResult(windowResultMsg{Value: "30m", Confirm: true})
	if a.window.Last != 30*time.Minute {
		t.Fatalf("window = %+v", a.window)
	}
	if w := a.rightPanel.logsWidget(); w == nil || w.Span().Window != a.window {
		t.Fatalf("right Logs panel span = %+v", w.Span())
	}
	a.setPanelMode(t.Context(), a.leftPanel, PanelModeLogs, "test")
	if w := a.leftPanel.logsWidget(); w == nil || w.Span().Window != a.window {
		t.Fatal("a Logs panel opened later does not use the window")
	}
	if cmd := a.handleWindowResult(windowResultMsg{Value: "soon", Confirm: true}); cmd == nil || a.window.Last != 30*time.Minute {
		t.Fatal("a bad window replaced the good one")
	}
	a.handleWindowResult(windowResultMsg{Value: "", Confirm: true})
	if !a.window.Off() || !a.rightPanel.logsWidget().Span().Window.Off() {
		t.Fatal("an empty window did not turn it off")
	}
}
