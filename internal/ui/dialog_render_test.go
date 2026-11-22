package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/filexfer"
	"github.com/stokaro/kc/internal/nodeaction"
)

// dialogScreen renders the App as the terminal gets it and checks the
// invariants a windowed dialog depends on: a frame that is wider or taller
// than the screen is wrapped or cut by the renderer, which can push the
// dialog off the screen while its function key bar stays.
func dialogScreen(t *testing.T, a *App) string {
	t.Helper()
	frame := viewString(a.View())
	lines := strings.Split(frame, "\n")
	if len(lines) != a.height {
		t.Fatalf("frame has %d lines, screen has %d", len(lines), a.height)
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > a.width {
			t.Fatalf("line %d is %d columns wide, screen has %d", i, w, a.width)
		}
	}
	return ansi.Strip(frame)
}

func requireDrawn(t *testing.T, screen string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(screen, w) {
			t.Fatalf("the dialog is open but %q is not on the screen:\n%s", w, screen)
		}
	}
}

// Issue #134 saw open dialogs that took the keys but were not drawn: the
// copy dialog over the right panel at 140 columns, the copy result, and the
// drain summary after Esc. Each case opens the dialog from either panel at
// both widths the issue names and renders the whole App.
func TestOpenDialogsAreDrawn(t *testing.T) {
	t.Parallel()
	details := [][2]string{
		{"Context", "kind-kind"},
		{"Pod", "demo/bashpod"},
		{"Container", "bash"},
		{"From", "/data"},
		{"Entries", "a.txt"},
		{"To", shortenPath("/home/user/projects/kc/some/deeper/directory/for/the/copy")},
	}
	cases := []struct {
		name string
		run  func(t *testing.T, a *App) []string
	}{
		{
			name: "copy from a container",
			run: func(_ *testing.T, a *App) []string {
				a.openTransfer(&transferJob{
					model:     NewTransferModel(details, 0),
					req:       filexfer.Request{Dir: "/data", Entries: fileEntries("a.txt")},
					source:    &fakeTransferSource{sess: &transferFS{}},
					destPanel: 1 - a.activePanel,
				})
				return []string{"Copy from the container?", "[ Copy ]"}
			},
		},
		{
			name: "copy into a container",
			run: func(t *testing.T, a *App) []string {
				job, model := uploadJob(t, &uploadFS{}, 1, "a.txt")
				model.details = details
				model.commands = []string{"kubectl cp /home/user/projects/kc/some/deeper/directory/for/the/copy/a.txt demo/bashpod:/data/a.txt -c bash"}
				job.destPanel = 1 - a.activePanel
				a.openTransfer(job)
				return []string{"Copy into the container?", "[ Cancel ]"}
			},
		},
		{
			name: "copy result after a failed copy into a container",
			run: func(t *testing.T, a *App) []string {
				job, _ := uploadJob(t, &uploadFS{}, 0, "a.txt")
				job.destPanel = 1 - a.activePanel
				a.openTransfer(job)
				dialogScreen(t, a)
				job.running = true
				a.Update(transferDoneMsg{gen: job.gen, err: errors.New("listing /data: io: read/write on closed pipe")})
				return []string{"Copy finished: nothing copied", "read/write on closed pipe", "[ OK ]"}
			},
		},
		{
			name: "drain summary after Esc",
			run: func(t *testing.T, a *App) []string {
				view := newDrainProgressModel([]string{"kind-worker"}, func() {})
				modal := NewModal("Drain", view)
				a.modalManager.Register(nodeDrainModal, modal)
				w, h := view.PreferredSize(a.width, a.height)
				a.configureModalWindow(modal, view, a.activePanel, "", w+2, h+2)
				a.showModal(nodeDrainModal)
				a.Update(nodeDrainProgressMsg{view: view, ch: make(chan tea.Msg), p: nodeaction.Progress{Nodes: []nodeaction.NodeProgress{{
					Node: "kind-worker", Phase: nodeaction.PhaseEvicting, Remaining: 1,
					Blocked: []nodeaction.PodNote{{Namespace: "demo", Name: "web-5d8f7c9b6-abcde", Message: "Cannot evict pod as it would violate the pod's disruption budget."}},
				}}}})
				requireDrawn(t, dialogScreen(t, a), "Refused", "[ Cancel ]")
				a.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
				_, cmd := a.Update(nodeDrainDoneMsg{view: view, err: fmt.Errorf("drain node kind-worker canceled with 1 pods left: %w", context.Canceled)})
				if cmd != nil {
					// The error toast changes the function key bar under the dialog.
					if msg := cmd(); msg != nil {
						a.Update(msg)
					}
				}
				return []string{"canceled with 1 pods left", "[ Close ]"}
			},
		},
	}
	for _, tc := range cases {
		for _, width := range []int{140, 200} {
			for _, panel := range []int{0, 1} {
				t.Run(fmt.Sprintf("%s/%dx40/panel %d", tc.name, width, panel), func(t *testing.T) {
					t.Parallel()
					a := newTransferApp(t)
					a.Update(tea.WindowSizeMsg{Width: width, Height: 40})
					a.activePanel = panel
					want := tc.run(t, a)
					requireDrawn(t, dialogScreen(t, a), want...)
				})
			}
		}
	}
}
