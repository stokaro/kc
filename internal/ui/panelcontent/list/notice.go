package list

import (
	"errors"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/kubeerr"
	models "github.com/stokaro/kc/internal/models"
	uistyles "github.com/stokaro/kc/internal/ui/styles"
)

// Notice is what a list says instead of looking empty: why it lists
// nothing, in plain words, and what the user can do about it.
type Notice struct {
	// Problem is the reason, drawn in red; empty means no notice.
	Problem string
	// Hint names the keys or menu entries that help; may be empty.
	Hint string
	// Pending marks a reason that is not a failure, such as a read that
	// has not ended; it is drawn in the panel's text color.
	Pending bool
}

// Hints of the notices, by what helps.
const (
	hintConnection = "C-r retries · Context... (Left/Right menu) picks another"
	hintForbidden  = "C-r reads again"
	hintNamespace  = "Namespace... (Left/Right menu) picks another"
	hintRetry      = "C-r retries"
)

// NoticeFor is the notice for err, the reason a folder lists nothing.
func NoticeFor(err error) Notice {
	if err == nil {
		return Notice{}
	}
	kind := kubeerr.KindOf(err)
	if kind == kubeerr.KindOther && errors.Is(err, kccluster.ErrDiscoveryPending) {
		return Notice{Problem: "Reading the API discovery…", Pending: true}
	}
	n := Notice{Problem: kubeerr.Describe(err)}
	switch {
	case kind.Connection():
		n.Hint = hintConnection
	case kind == kubeerr.KindForbidden:
		n.Hint = hintForbidden
	case kind == kubeerr.KindNamespaceMissing:
		n.Hint = hintNamespace
	default:
		n.Hint = hintRetry
	}
	return n
}

// SetNotice sets what the list says while it shows no folder and has no
// items, such as why kc has no connection; the zero Notice clears it.
// A folder's own problem (models.ProblemFolder) takes its place once the
// list shows a folder.
func (w *Widget) SetNotice(n Notice) { w.notice = n }

// folderNotice is the notice of the folder the list shows, if any.
func (w *Widget) folderNotice() Notice {
	pf, ok := w.folder.(models.ProblemFolder)
	if !ok {
		return Notice{}
	}
	return NoticeFor(pf.ListProblem())
}

// noticeStyles are the styles of the problem and the hint lines.
func noticeStyles() (problem, hint lipgloss.Style) {
	pal := uistyles.Current()
	base := lipgloss.NewStyle().Background(pal.Background)
	return base.Foreground(models.NotReadyStyle().GetForeground()).Bold(true), base.Foreground(pal.Foreground)
}

// noticeLines wraps n to width cells, one space in from the left edge
// like the rows, and styles each line padded to width: the problem lines
// first, then the hint lines.
func noticeLines(n Notice, width int) []string {
	if n.Problem == "" || width <= 1 {
		return nil
	}
	problemStyle, hintStyle := noticeStyles()
	if n.Pending {
		problemStyle = hintStyle
	}
	var out []string
	add := func(text string, st lipgloss.Style) {
		if text == "" {
			return
		}
		for line := range strings.SplitSeq(ansi.Wrap(text, width-1, ""), "\n") {
			out = append(out, st.Width(width).Render(" "+strings.TrimRight(line, " ")))
		}
	}
	add(n.Problem, problemStyle)
	add(n.Hint, hintStyle)
	return out
}

// withNotice draws n into the table view below its rows, on the lines the
// table fills with empty rows. A notice that does not fit is cut at the
// bottom of the panel; with no free line it is not drawn.
func (w *Widget) withNotice(view string, n Notice) string {
	lines := noticeLines(n, w.width)
	if len(lines) == 0 || w.bt == nil {
		return view
	}
	rows := 0
	for {
		if _, ok := w.bt.VisibleRowID(rows); !ok {
			break
		}
		rows++
	}
	body := strings.Split(view, "\n")
	from := tableHeaderLines + rows
	if from >= len(body) {
		return view
	}
	for i, line := range lines {
		if from+i >= len(body) {
			break
		}
		body[from+i] = line
	}
	return strings.Join(body, "\n")
}
