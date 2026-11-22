package ui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sttts/kc/internal/models"
	"github.com/sttts/kc/internal/nodeaction"
)

func nodeKey(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	case "esc":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})
	case "down":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyDown})
	case "up":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyUp})
	case "backspace":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace})
	case "space":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "})
	case "left":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft})
	}
	r := []rune(s)
	return tea.KeyPressMsg(tea.Key{Code: r[0], Text: s})
}

func pressNode(t *testing.T, m tea.Model, keys ...string) tea.Msg {
	t.Helper()
	var last tea.Msg
	for _, k := range keys {
		var cmd tea.Cmd
		_, cmd = m.Update(nodeKey(k))
		if cmd != nil {
			last = cmd()
		}
	}
	return last
}

func TestNodeActionMenuChoosesAction(t *testing.T) {
	target := nodeTarget{nodes: []string{"a", "b"}}
	tests := []struct {
		keys []string
		want nodeActionKind
	}{
		{keys: []string{"enter"}, want: nodeActionCordon},
		{keys: []string{"down", "enter"}, want: nodeActionUncordon},
		{keys: []string{"down", "down", "enter"}, want: nodeActionDrain},
		{keys: []string{"up", "enter"}, want: nodeActionDrain},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.keys, "+"), func(t *testing.T) {
			menu := newNodeActionMenu(target)
			msg, ok := pressNode(t, menu, tt.keys...).(nodeMenuChosenMsg)
			if !ok {
				t.Fatalf("no choice message")
			}
			if msg.kind != tt.want || !slices.Equal(msg.target.nodes, target.nodes) {
				t.Errorf("chosen = %v for %v, want %v", msg.kind, msg.target.nodes, tt.want)
			}
		})
	}
	menu := newNodeActionMenu(target)
	menu.SetDimensions(40, 5)
	if view := ansi.Strip(menu.View().Content); !strings.Contains(view, "2 nodes: a, b") || !strings.Contains(view, "> Cordon...") {
		t.Errorf("menu view:\n%s", view)
	}
}

func TestDrainOptionsForm(t *testing.T) {
	target := nodeTarget{nodes: []string{"a"}}
	tests := []struct {
		name   string
		keys   []string
		wantOK bool
		want   nodeaction.DrainOptions
		errHas string
	}{
		{name: "defaults", keys: []string{"enter"}, wantOK: true, want: nodeaction.DefaultDrainOptions()},
		{name: "cancel button", keys: []string{"down", "enter"}, wantOK: false},
		{name: "toggle every box", keys: []string{"up", "space", "up", "space", "up", "space", "enter"}, wantOK: true,
			want: nodeaction.DrainOptions{GracePeriodSeconds: -1, Timeout: nodeaction.DefaultDrainTimeout, IgnoreDaemonSets: false, DeleteEmptyDirData: true, Force: true}},
		{name: "grace and timeout", keys: []string{
			"down", "down", // OK -> Cancel -> grace
			"backspace", "backspace", "3", "0",
			"down", "backspace", "backspace", "backspace", "backspace", "9", "0", "s",
			"enter",
		}, wantOK: true, want: nodeaction.DrainOptions{GracePeriodSeconds: 30, Timeout: 90 * time.Second, IgnoreDaemonSets: true}},
		{name: "timeout zero means none", keys: []string{"down", "down", "down", "backspace", "backspace", "backspace", "backspace", "0", "enter"}, wantOK: true,
			want: nodeaction.DrainOptions{GracePeriodSeconds: -1, IgnoreDaemonSets: true}},
		{name: "bad grace is refused", keys: []string{"down", "down", "backspace", "backspace", "x", "enter"}, errHas: "grace period"},
		{name: "negative grace below -1 is refused", keys: []string{"down", "down", "backspace", "2", "enter"}, errHas: "grace period"},
		{name: "bad timeout is refused", keys: []string{"down", "down", "down", "x", "enter"}, errHas: "timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			form := newDrainOptionsModel(target, nodeaction.DefaultDrainOptions())
			last := pressNode(t, form, tt.keys...)
			if tt.errHas != "" {
				if last != nil {
					t.Fatalf("invalid input must not submit, got %#v", last)
				}
				if !strings.Contains(form.err, tt.errHas) {
					t.Fatalf("error = %q, want it to mention %q", form.err, tt.errHas)
				}
				form.SetDimensions(70, 12)
				if view := ansi.Strip(form.View().Content); !strings.Contains(view, "! "+form.err) {
					t.Errorf("view does not show the error:\n%s", view)
				}
				return
			}
			msg, ok := last.(nodeDrainOptionsMsg)
			if !ok {
				t.Fatalf("last message = %#v, want nodeDrainOptionsMsg", last)
			}
			if msg.ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", msg.ok, tt.wantOK)
			}
			if tt.wantOK && msg.opts != tt.want {
				t.Errorf("opts = %+v, want %+v", msg.opts, tt.want)
			}
		})
	}
}

func TestDrainProgressCancelThenClose(t *testing.T) {
	canceled := 0
	view := newDrainProgressModel([]string{"a", "b"}, func() { canceled++ })
	view.SetDimensions(80, 16)
	view.setProgress(nodeaction.Progress{Nodes: []nodeaction.NodeProgress{
		{Node: "a", Phase: nodeaction.PhaseEvicting, Evicted: 2, Remaining: 1, Blocked: []nodeaction.PodNote{{Namespace: "app", Name: "db-0", Message: "The disruption budget db needs 1 healthy pods"}}},
		{Node: "b", Phase: nodeaction.PhaseWaiting},
	}})
	text := ansi.Strip(view.View().Content)
	for _, want := range []string{"evicting", "app/db-0: The disruption budget db", "Esc cancels", "[ Cancel ]"} {
		if !strings.Contains(text, want) {
			t.Errorf("view lacks %q:\n%s", want, text)
		}
	}

	// Esc while running cancels once and keeps the dialog open.
	for range 2 {
		handled, _ := view.HandleModalEscape(nodeKey("esc"))
		if !handled {
			t.Fatalf("Esc while running must keep the dialog open")
		}
	}
	if canceled != 1 {
		t.Fatalf("cancel called %d times, want 1", canceled)
	}
	if text := ansi.Strip(view.View().Content); !strings.Contains(text, "Canceling") {
		t.Errorf("view does not say it cancels:\n%s", text)
	}

	view.finish(nodeaction.Result{}, errors.New("drain node a canceled with 1 pod left"))
	if text := ansi.Strip(view.View().Content); !strings.Contains(text, "! drain node a canceled") || !strings.Contains(text, "[ Close ]") {
		t.Errorf("finished view:\n%s", text)
	}
	if handled, _ := view.HandleModalEscape(nodeKey("esc")); handled {
		t.Errorf("Esc after the drain must close the dialog")
	}
	if _, ok := pressNode(t, view, "enter").(nodeDrainCloseMsg); !ok {
		t.Errorf("Enter after the drain must close the dialog")
	}
}

func TestNodeConfirmCarriesTheAction(t *testing.T) {
	drain := &nodeaction.DrainAction{}
	m := &nodeConfirmModel{ConfirmModel: NewConfirmModel(models.Confirmation{Title: "Drain node?", Action: "Drain"}), drain: drain}

	// Cancel has the focus: Enter alone changes nothing.
	got, ok := pressNode(t, m, "enter").(nodeConfirmResultMsg)
	if !ok || got.ok {
		t.Fatalf("Enter on Cancel = %#v, want a refusal", got)
	}
	got, ok = pressNode(t, m, "left", "enter").(nodeConfirmResultMsg)
	if !ok || !got.ok || got.drain != drain || got.cordon != nil {
		t.Fatalf("confirm = %#v, want ok with the drain", got)
	}
	_, cmd := m.HandleModalEscape(nodeKey("esc"))
	if got, ok := cmd().(nodeConfirmResultMsg); !ok || got.ok {
		t.Fatalf("Esc = %#v, want a refusal", got)
	}
}

func TestWrapCommand(t *testing.T) {
	got := wrapCommand("kubectl --context prod drain node-a node-b --ignore-daemonsets --timeout=5m0s", 30)
	want := []string{"kubectl --context prod drain", "  node-a node-b", "  --ignore-daemonsets", "  --timeout=5m0s"}
	if !slices.Equal(got, want) {
		t.Errorf("wrap = %q, want %q", got, want)
	}
}
