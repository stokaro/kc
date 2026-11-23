package ui

import (
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// A message that changes state must not leave the cached frame on screen.
// Update drops the cache for every message except the timers that keep it
// on purpose, so a handler that returns early cannot forget the redraw.
func TestResultMessagesDropTheCachedFrame(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		msg  tea.Msg
	}{
		// The panel rereads its folder after a delete; before the fix the
		// frame kept the deleted row until another event came.
		{"delete result", resourceDeletedMsg{target: deleteTarget{panelIdx: 0}}},
		{"delete failure", resourceDeletedMsg{target: deleteTarget{panelIdx: 1}, err: errors.New("forbidden")}},
		{"kubectl edit finished", kubectlEditFinishedMsg{panelIndex: 0}},
		{"namespace created", namespaceCreatedMsg{name: "team-a"}},
		{"discovery refreshed", DiscoveryRefreshedMsg{}},
		{"escape timeout", EscTimeoutMsg{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := NewApp()
			t.Cleanup(a.cancel)
			a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			a.mainViewCache.set("stale", nil)
			a.Update(tt.msg)
			if _, _, ok := a.mainViewCache.get(); ok {
				t.Fatalf("%T kept the cached frame", tt.msg)
			}
		})
	}
}

// The busy and toast timers fire several times a second and change the
// screen only when they say so; between those ticks the frame stays cached.
func TestIdleTimersKeepTheCachedFrame(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(a *App)
		msg   tea.Msg
	}{
		{"busy tick while idle", func(*App) {}, BusyTickMsg{}},
		{"busy show for an old token", func(a *App) { a.busyToken = 2 }, BusyShowMsg{token: 1}},
		{"toast tick before the toast ends", func(a *App) {
			a.toastActive = true
			a.toastUntil = time.Now().Add(time.Hour)
		}, toastTickMsg{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := NewApp()
			t.Cleanup(a.cancel)
			a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			tt.setup(a)
			a.mainViewCache.set("cached", nil)
			a.Update(tt.msg)
			if view, _, ok := a.mainViewCache.get(); !ok || view != "cached" {
				t.Fatalf("%T dropped the cached frame", tt.msg)
			}
		})
	}
}
