package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

// Logs keeps the pod it streams while the left cursor names no object,
// also after the left panel switched its context. It holds the connection
// it read the pod from, so its frame and its actions stay with that
// connection, and gives the connection up once a pod of the new context
// replaces the stream, or once it leaves the follower modes.
func TestLogsHoldsTheConnectionOfItsPod(t *testing.T) {
	app, dev, streams := newFollowFakeLogs(t)
	ctx := t.Context()

	// End skips b and c, which Logs would follow.
	sendKey(t, app, tea.KeyPressMsg{Code: tea.KeyEnd})
	if id := app.leftPanel.currentSelectionID(ctx); id != "sub" {
		t.Fatalf("the left cursor is on %q, want folder sub", id)
	}
	assertLogsStream(t, app, streams, dev, "shop", "a", 1, 0)

	prod := app.connFor(&kubeconfig.Context{Name: "prod"}, kccluster.Key{ContextName: "prod"}, nil, nil)
	app.setPanelConn(0, prod)
	_, cmd := app.Update(followNoopMsg{})
	settle(t, app, cmd)
	if n := dev.holders(); n != 1 {
		t.Fatalf("the left panel left dev; dev has %d holders, want Logs alone", n)
	}
	assertLogsStream(t, app, streams, dev, "shop", "a", 1, 0)

	sendKey(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if path := app.leftPanel.GetCurrentPath(); path != "/sub" {
		t.Fatalf("the left panel is in %s, want /sub", path)
	}
	assertLogsStream(t, app, streams, dev, "shop", "a", 1, 0)

	sendKey(t, app, tea.KeyPressMsg{Code: tea.KeyDown})
	assertLogsStream(t, app, streams, prod, "shop", "x", 2, 1)
	if n := dev.holders(); n != 0 {
		t.Fatalf("dev has %d holders after Logs left it", n)
	}

	// List mode shows the panel's own rows: the hold on prod goes.
	settle(t, app, app.setPanelMode(ctx, app.rightPanel, PanelModeList, "test"))
	if n := prod.holders(); n != 1 {
		t.Fatalf("prod has %d holders after the right panel left Logs, want the left panel alone", n)
	}
}

// newFollowFakeLogs is newFollowFakeApp with both panels in context dev,
// scope shop, and the right panel streaming pod a through fake streams.
func newFollowFakeLogs(t *testing.T) (*App, *panelConn, *logStreams) {
	t.Helper()
	app, _, _ := newFollowFakeApp(t)
	dev := useTestCluster(app, &kubeconfig.Context{Name: "dev"}, nil, 0)
	useTestCluster(app, &kubeconfig.Context{Name: "dev"}, nil, 1)
	app.panelScope = [2]string{"shop", "shop"}
	streams := useFakeLogStreams(t, app.rightPanel)
	settle(t, app, app.setPanelMode(t.Context(), app.rightPanel, PanelModeLogs, "test"))
	// The connections changed outside Update; the first message reports.
	_, cmd := app.Update(followNoopMsg{})
	settle(t, app, cmd)
	assertLogsStream(t, app, streams, dev, "shop", "a", 1, 0)
	return app, dev, streams
}

// A frozen Logs panel misses the moves of the left cursor. Thawing it
// follows the row the cursor is on, here a pod of another connection, and
// the frame names that connection in the same Update.
func TestThawedLogsFrameNamesTheNewConnection(t *testing.T) {
	app, _, streams := newFollowFakeLogs(t)
	freeze := func() {
		sendKey(t, app, tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
		sendKey(t, app, tea.KeyPressMsg{Code: 'f'})
	}
	freeze()
	if !app.rightPanel.Frozen() {
		t.Fatal("C-x f did not freeze the Logs panel")
	}

	prod := app.connFor(&kubeconfig.Context{Name: "prod"}, kccluster.Key{ContextName: "prod"}, nil, nil)
	app.setPanelConn(0, prod)
	sendKey(t, app, tea.KeyPressMsg{Code: tea.KeyDown})
	freeze()
	if app.rightPanel.Frozen() {
		t.Fatal("C-x f did not thaw the Logs panel")
	}
	assertLogsStream(t, app, streams, prod, "shop", "b", 2, 1)
}

// Logs keeps a pod only while it knows where the pod was read; without
// that its frame and its actions could not name the pod's context. A pod
// of unknown origin goes when the left cursor reaches a folder.
func TestLogsDropsAPodOfUnknownOrigin(t *testing.T) {
	app, dev, streams := newFollowFakeLogs(t)
	// As if the pod was read on a connection that is gone.
	app.rightPanel.follow(app.rightPanel.lastSelection, "gone", followOrigin{})

	sendKey(t, app, tea.KeyPressMsg{Code: tea.KeyEnd})
	if w := app.rightPanel.logsWidget(); w == nil || w.Streaming() {
		t.Fatal("Logs streams a pod of unknown origin after the left cursor left it")
	}
	if o, c := streams.counts(); o != 1 || c != 1 {
		t.Fatalf("Logs opened %d streams and canceled %d, want 1 and 1", o, c)
	}
	assertFrameNames(t, app, dev, "shop")
}
