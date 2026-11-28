package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/pkg/appconfig"
)

// quitModal is the dialog that asks before kc exits.
const quitModal = "confirm_quit"

// quitListMax is how many sessions the quit dialog names before "and N
// more".
const quitListMax = 5

// liveSessions is what stops when kc exits: one count per kind, and a line
// per session for the dialog's list.
type liveSessions struct {
	forwards  int
	nodeShell int
	copies    int
	lines     []string
	// notes say what happens to what stops, when that is not obvious.
	notes []string
}

func (s liveSessions) empty() bool { return s.forwards+s.nodeShell+s.copies == 0 }

// liveSessions collects what quitting would stop: the port forwards, the
// node shell (also while its pod starts) and a running copy. kubectl edit,
// the editor of a local file and the kubectl exec and attach sessions take
// the terminal while they run, so kc gets no key to quit with then.
func (a *App) liveSessions() liveSessions {
	var s liveSessions
	if a.portForwards != nil {
		for _, f := range a.portForwards.List() {
			s.forwards++
			s.lines = append(s.lines, fmt.Sprintf("%s -> %s in %s (%s)", f.Local(), f.Spec.Target(), f.Spec.Namespace, f.Spec.Context))
		}
	}
	switch {
	case a.nodeShell != nil && a.nodeShell.pod != nil:
		s.nodeShell = 1
		pod := a.nodeShell.pod
		s.lines = append(s.lines, fmt.Sprintf("node shell on %s (pod %s/%s)", pod.Spec.NodeName, pod.Namespace, pod.Name))
		s.notes = append(s.notes, "kc deletes the pod of the node shell before it exits.")
	case a.nodeShellStarting:
		s.nodeShell = 1
		s.lines = append(s.lines, "node shell, its pod starting")
		s.notes = append(s.notes, "The pod of the starting node shell exits on its own a minute after it runs.")
	}
	if a.transfer != nil && a.transfer.running {
		s.copies = 1
		if a.transfer.upload != nil {
			s.lines = append(s.lines, "copy into a container")
		} else {
			s.lines = append(s.lines, "copy from a container")
		}
	}
	return s
}

// quitQuestion is the dialog's title: what stops, then the question, as in
// "2 port forwards will stop. Quit?".
func (s liveSessions) quitQuestion() string {
	var parts []string
	if s.forwards > 0 {
		parts = append(parts, countNoun(s.forwards, "port forward", "port forwards"))
	}
	if s.nodeShell > 0 {
		parts = append(parts, countNoun(s.nodeShell, "node shell", "node shells"))
	}
	if s.copies > 0 {
		parts = append(parts, countNoun(s.copies, "copy", "copies"))
	}
	if len(parts) == 0 {
		return "Quit kc?"
	}
	what := parts[0]
	if len(parts) > 1 {
		what = strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
	return what + " will stop. Quit?"
}

func countNoun(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// requestQuit is what F10, Esc 0, File > Exit and a click on Quit do. With
// nothing running it quits at once; while port forwards, a node shell or a
// copy would stop, or with confirm.quit: always, it asks first (#375).
func (a *App) requestQuit() tea.Cmd {
	sessions := a.liveSessions()
	always := a.cfg != nil && a.cfg.Confirm.Quit == appconfig.QuitConfirmAlways
	if sessions.empty() && !always {
		return tea.Quit
	}
	m := newQuestionModel(sessions.quitQuestion(), func(yes bool) tea.Cmd {
		a.hideModalName(quitModal)
		if yes {
			return tea.Quit
		}
		return nil
	})
	m.items = sessions.lines
	if len(m.items) > quitListMax {
		m.items = append(m.items[:quitListMax:quitListMax], fmt.Sprintf("and %d more", len(sessions.lines)-quitListMax))
	}
	m.notes = sessions.notes
	modal := newQuestionModal("Quit", m)
	a.modalManager.Register(quitModal, modal)
	a.showQuestion(quitModal, modal, m, -1)
	return nil
}
