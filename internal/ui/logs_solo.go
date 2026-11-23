package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/logview"
)

// logsSoloKey shows only the focused record's source in a merged log, and
// every source again (docs/adr/0271-logs-session-workspace.md).
const logsSoloKey = "alt+m"

// toggleSolo shows the focused record's source alone, or every source when
// one is shown alone. It only hides records: every stream keeps reading,
// and the hidden records stay kept within the limits. The sources dialog
// (M-s) is what stops streams.
func (v *LogsViewer) toggleSolo() tea.Cmd {
	if !v.merged {
		v.note = "Solo needs a merged log of several sources"
		return nil
	}
	// The focused record is read before Solo changes which records the
	// display rows map to.
	seq, focused := v.focusRecord()
	if src, on := v.log.Solo(); on {
		v.applySolo(logview.Source{}, false, seq, focused)
		v.note = "Showing every source again; " + src.String() + " was shown alone"
		return nil
	}
	if !focused {
		v.note = "No record to take the source from"
		return nil
	}
	r, err := v.log.Record(seq)
	if err != nil {
		v.note = err.Error()
		return nil
	}
	if r.Source == (logview.Source{}) {
		v.note = "This line belongs to no source"
		return nil
	}
	src := r.Source
	v.applySolo(src, true, seq, true)
	v.note = "Showing " + src.String() + " alone; M-m shows every source. The other sources keep streaming"
	return nil
}

// applySolo changes Solo, leaves the context view, and keeps the focused
// record in view unless the view follows the stream.
func (v *LogsViewer) applySolo(src logview.Source, on bool, seq logview.Seq, focused bool) {
	v.log.SetSolo(src, on)
	v.context, v.correlated = nil, ""
	v.refreshDisplay()
	if focused && !v.autoFollow {
		v.showRecord(seq)
	}
}

// soloStatus names the source shown alone, for the status line.
func (v *LogsViewer) soloStatus() string {
	if src, on := v.log.Solo(); on {
		return "solo: " + src.String()
	}
	return ""
}
