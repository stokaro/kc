package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stokaro/kc/internal/logmux"
	"github.com/stokaro/kc/internal/logview"
	uistyles "github.com/stokaro/kc/internal/ui/styles"
)

// Keys of the logs viewer for JSON logs (see docs/adr/0071-json-logs.md).
const (
	logsToggleRawKey = "alt+v"
	logsFilterKey    = "alt+g"
)

// isCopyOriginalsKey reports M-A (Meta-Shift-a), which copies the original
// records. Terminals report it as alt+A or as alt+shift+a.
func isCopyOriginalsKey(msg tea.KeyMsg) bool {
	k := msg.Key()
	if !k.Mod.Contains(tea.ModAlt) {
		return false
	}
	return k.Code == 'A' || k.ShiftedCode == 'A' || (k.Code == 'a' && k.Mod.Contains(tea.ModShift))
}

// handleLogViewKey handles the JSON log keys: M-v raw/formatted, M-g filter,
// M-a copy shown lines, M-A copy original records.
func (v *LogsViewer) handleLogViewKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if v.inner.SearchMode() {
		return nil, false
	}
	if isCopyOriginalsKey(msg) {
		return viewerCopyRequest(strings.Join(v.log.Originals(), "\n"), v.copyWhatNote("original records")), true
	}
	switch msg.String() {
	case logsToggleRawKey:
		if v.log.Mode() == logview.ModeRaw {
			v.log.SetMode(logview.ModeFormatted)
		} else {
			v.log.SetMode(logview.ModeRaw)
		}
		v.refreshDisplay()
		return nil, true
	case logsFilterKey:
		if v.onFilter != nil {
			return v.onFilter(), true
		}
		return nil, true
	case logsSourcesKey:
		if v.onSources != nil {
			return v.onSources(), true
		}
		return nil, false
	case copyAllKey:
		return viewerCopyRequest(v.inner.PlainText(), v.copyWhatNote("shown lines")), true
	}
	return nil, false
}

// appendRecords keeps lines as records and shows those that pass the filter.
// In a merged log each line's prefix becomes its source.
// The log keeps only its recent records (docs/adr/0179-bounded-log-retention.md):
// the display lines of dropped records leave the widget too.
func (v *LogsViewer) appendRecords(lines []string) bool {
	var shown []string
	dropped := 0
	if v.merged {
		for _, line := range lines {
			src, text := mergedLineSource(line)
			s, d := v.log.Append(src, text)
			shown = append(shown, s...)
			dropped += d
		}
	} else {
		shown, dropped = v.log.Append(logview.Source{}, lines...)
	}
	if len(shown) > 0 {
		v.inner.AppendLines(shown)
	}
	v.dropLines(dropped)
	return len(shown) > 0
}

// dropLines removes the n oldest display lines. When the user reads older
// lines and the top one goes, the view moves to the oldest kept line and
// the footer says so until the next scroll.
func (v *LogsViewer) dropLines(n int) {
	if n > 0 && v.inner.DropFirstLines(n) && !v.autoFollow {
		v.lostPlace = true
	}
}

// SetLimits sets how many records the viewer keeps.
func (v *LogsViewer) SetLimits(limits logview.Limits) {
	v.dropLines(v.log.SetLimits(limits))
}

// historyNote describes what the kept records lack, for the footer and the
// copy messages: "" when nothing was dropped or truncated.
func (v *LogsViewer) historyNote() string {
	var parts []string
	if n := v.log.Dropped(); n > 0 {
		parts = append(parts, fmt.Sprintf("%d older dropped", n))
	}
	if n := v.log.Truncated(); n > 0 {
		parts = append(parts, fmt.Sprintf("%d truncated", n))
	}
	return strings.Join(parts, ", ")
}

// copyWhatNote names copied log text and says what it lacks.
func (v *LogsViewer) copyWhatNote(what string) string {
	if note := v.historyNote(); note != "" {
		return what + " (" + note + ")"
	}
	return what
}

// refreshDisplay rebuilds the shown lines from the kept records. The stream
// is not touched.
func (v *LogsViewer) refreshDisplay() {
	v.inner.SetLines(v.log.Lines())
	if v.autoFollow {
		v.inner.ScrollToEnd()
	}
}

// SetQuery filters the shown records with q; nil clears the filter.
func (v *LogsViewer) SetQuery(q *logview.Query) {
	v.log.SetQuery(q)
	v.refreshDisplay()
}

// Query returns the current filter query, or "".
func (v *LogsViewer) Query() string { return v.log.Query().String() }

// LogMode returns whether the viewer shows records raw or formatted.
func (v *LogsViewer) LogMode() logview.Mode { return v.log.Mode() }

// mergedLineSource splits a merged log line into its source and text. kc's
// own notices (`[kc] ...`) and lines without a prefix keep no source.
func mergedLineSource(line string) (logview.Source, string) {
	t, text, ok := logmux.ParseFormatted(line)
	if !ok {
		return logview.Source{}, line
	}
	return logview.Source{Pod: t.Pod, Container: t.Container}, text
}

// mergedSourceStyle colors a source prefix as logmux.Format does.
func mergedSourceStyle(src logview.Source) string {
	return logmux.StyledPrefix(logmux.Target{Pod: src.Pod, Container: src.Container})
}

// SetOnFilter sets what M-g does; the app opens the filter dialog.
func (v *LogsViewer) SetOnFilter(fn func() tea.Cmd) { v.onFilter = fn }

// logViewStatus describes the view for the footer, for example
// "formatted, 12 of 40 lines, filter: level=error".
func (v *LogsViewer) logViewStatus() string {
	text := v.log.Mode().String()
	if v.log.Query().Filters() {
		text += fmt.Sprintf(", %d of %d lines", v.log.Shown(), v.log.Len())
	} else {
		text += fmt.Sprintf(", %d lines", v.log.Len())
	}
	if note := v.historyNote(); note != "" {
		text += " (" + note + ")"
	}
	if v.lostPlace {
		text += ", view moved: its lines were dropped"
	}
	if q := v.log.Query().String(); q != "" {
		text += ", filter: " + q
	}
	return text
}

// logViewHints are the footer hints of the JSON log keys.
func (v *LogsViewer) logViewHints() []FooterHint {
	toggle := "Raw"
	if v.log.Mode() == logview.ModeRaw {
		toggle = "Format"
	}
	hints := []FooterHint{
		{Key: "M-v", Label: toggle, Enabled: true},
		{Key: "M-g", Label: "Filter", Enabled: v.onFilter != nil},
		{Key: "M-a", Label: "Copy shown", Enabled: true},
		{Key: "M-A", Label: "Copy orig", Enabled: true},
		{Key: "M-c", Label: "Copy cmd", Enabled: v.bar.command != ""},
	}
	if v.onSources != nil {
		hints = append(hints, FooterHint{Key: "M-s", Label: "Sources", Enabled: true})
	}
	return hints
}

// logsFilterModalName registers the filter dialog with the modal manager.
const logsFilterModalName = "logs_filter"

// logsFilterHint is the syntax summary under the filter field.
const logsFilterHint = `text "a b" /re/ key=v1,v2 key~re !exclude .field`

// logsFilterResultMsg is the filter dialog's answer.
type logsFilterResultMsg struct {
	dialog  *logsFilterDialog
	query   string
	confirm bool
	close   bool
}

// logsFilterDialog edits the query of a logs viewer. It sits on top of the
// viewer and passes the viewer's stream messages on, so the log keeps
// streaming behind it.
type logsFilterDialog struct {
	input  *TextInputModal
	viewer *LogsViewer
	done   func()
}

func newLogsFilterDialog(v *LogsViewer, done func()) *logsFilterDialog {
	d := &logsFilterDialog{viewer: v, done: done}
	confirmStyle := lipgloss.NewStyle().
		Padding(0, 3).
		Background(lipgloss.Color(uistyles.ColorModalButtonBg)).
		Foreground(lipgloss.Color(uistyles.ColorWhite))
	confirmFocused := confirmStyle.
		Background(lipgloss.Color(uistyles.ColorModalButtonSelBg)).
		Foreground(lipgloss.Color(uistyles.ColorBlack)).
		Bold(true)
	d.input = NewTextInputModal(TextInputModalConfig{
		Title:        "Log filter",
		Label:        "Filter (empty clears)",
		Description:  logsFilterHint,
		ConfirmLabel: "OK",
		CancelLabel:  "Cancel",
		Placeholder:  "level=error !healthz .msg",
		InitialValue: v.Query(),
		MinWidth:     40,
		MaxWidth:     72,
		MinHeight:    5,
		HideTitle:    true,
		Validate: func(s string) error {
			_, err := logview.ParseQuery(s)
			return err
		},
		ResultMsg: func(res TextInputModalResult) tea.Msg {
			return logsFilterResultMsg{dialog: d, query: res.Value, confirm: res.Confirm, close: res.Close}
		},
		ButtonStyles: TextInputModalButtonStyles{
			Confirm:        &confirmStyle,
			ConfirmFocused: &confirmFocused,
		},
	})
	return d
}

func (d *logsFilterDialog) Init() tea.Cmd             { return d.input.FocusInput() }
func (d *logsFilterDialog) SetDimensions(w, h int)    { d.input.SetDimensions(w, h) }
func (d *logsFilterDialog) View() tea.View            { return d.input.View() }
func (d *logsFilterDialog) RuleRows() []int           { return d.input.RuleRows() }
func (d *logsFilterDialog) FooterHints() []FooterHint { return d.input.FooterHints() }
func (d *logsFilterDialog) PreferredSize(maxW, maxH int) (width, height int) {
	return d.input.PreferredSize(maxW, maxH)
}

func (d *logsFilterDialog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m, ok := msg.(logsFilterResultMsg); ok {
		if m.dialog != d {
			return d, nil
		}
		if m.confirm {
			// Validate accepted the text, so it parses.
			q, _ := logview.ParseQuery(m.query)
			d.viewer.SetQuery(q)
		}
		if m.close && d.done != nil {
			d.done()
		}
		return d, nil
	}
	_, cmd := d.input.Update(msg)
	return d, cmd
}

// showLogsFilterModal opens the filter dialog over the logs viewer. The
// viewer stays drawn, and keeps streaming, behind it.
func (a *App) showLogsFilterModal(v *LogsViewer) tea.Cmd {
	if a.modalManager == nil || v == nil {
		return nil
	}
	behind := a.modalManager.GetActiveModal()
	dialog := newLogsFilterDialog(v, func() { a.hideModalName(logsFilterModalName) })
	modal := NewModal("Log filter", dialog)
	modal.SetCloseOnSingleEsc(true)
	a.modalManager.Register(logsFilterModalName, modal)
	background := ""
	if behind != nil {
		background = viewString(behind.View())
	}
	a.configureModalWindow(modal, dialog, -1, background, 60, 7)
	if behind != nil {
		modal.SetWindowedBackgroundProvider(func() string { return viewString(behind.View()) })
	}
	a.showModal(logsFilterModalName)
	return dialog.Init()
}
