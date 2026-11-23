package ui

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// markGroupModal is the dialog of mc's Select group ("+") and Unselect
// group ("-"): a shell pattern that marks or unmarks the rows whose name
// matches it.
const markGroupModal = "mark_group"

// markGroupHint is the line under the pattern.
const markGroupHint = `shell pattern on the name: "*" any text, "?" one character, [a-z] a set; case is ignored`

// markGroupResultMsg is the answer of the pattern dialog.
type markGroupResultMsg struct {
	panel   int
	mark    bool
	pattern string
	confirm bool
	close   bool
}

// markGroupModel is the pattern dialog; one Esc cancels it.
type markGroupModel struct{ *TextInputModal }

func (m *markGroupModel) HandleModalEscape(tea.KeyMsg) (bool, tea.Cmd) {
	return true, m.emitResult(false, true)
}

// markGroupTitle names the dialog as mc does.
func markGroupTitle(mark bool) string {
	if mark {
		return "Select group"
	}
	return "Unselect group"
}

// showMarkGroupDialog opens the pattern dialog for the active panel. It
// works on lists only, as the "+" and "-" keys do.
func (a *App) showMarkGroupDialog(mark bool) tea.Cmd {
	idx := a.activePanel
	if !a.panelListActive(idx) || a.modalManager == nil {
		return nil
	}
	title := markGroupTitle(mark)
	model := &markGroupModel{NewTextInputModal(TextInputModalConfig{
		Title:        title,
		Label:        title,
		Description:  markGroupHint,
		ConfirmLabel: "OK",
		CancelLabel:  "Cancel",
		InitialValue: "*",
		MinWidth:     36,
		MaxWidth:     70,
		HideTitle:    true,
		Validate: func(v string) error {
			_, err := path.Match(strings.TrimSpace(v), "")
			return err
		},
		ResultMsg: func(res TextInputModalResult) tea.Msg {
			return markGroupResultMsg{panel: idx, mark: mark, pattern: res.Value, confirm: res.Confirm, close: res.Close}
		},
	})}
	modal := NewModal(title, model)
	modal.SetCloseOnSingleEsc(true)
	a.modalManager.Register(markGroupModal, modal)
	a.configureModalWindow(modal, model, idx, "", 50, 7)
	a.showModal(markGroupModal)
	return nil
}

// handleMarkGroupResult closes the dialog and applies the pattern.
func (a *App) handleMarkGroupResult(msg markGroupResultMsg) tea.Cmd {
	if msg.close {
		a.hideModalName(markGroupModal)
	}
	if !msg.confirm {
		return nil
	}
	return a.markGroup(msg.panel, msg.pattern, msg.mark)
}

// markGroup marks or unmarks the rows of panel idx that match pattern.
func (a *App) markGroup(idx int, pattern string, mark bool) tea.Cmd {
	panel := a.panelByIndex(idx)
	if panel == nil || panel.Mode() != PanelModeList {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	lw := panel.listWidget(ctx)
	if lw == nil {
		return nil
	}
	n, err := lw.MarkGroup(ctx, pattern, mark)
	if err != nil {
		return a.ShowToast(err.Error(), 3*time.Second)
	}
	ctrllog.FromContext(a.ctx).WithName("marks").V(1).Info("group", "panel", idx, "pattern", pattern, "mark", mark, "changed", n)
	panel.invalidateRenderCache()
	a.invalidateView("mark group")
	a.invalidateFunctionBar("mark group")
	if n == 0 {
		which := "marked"
		if mark {
			which = "unmarked"
		}
		return a.ShowToast(fmt.Sprintf("No %s row matches %q", which, strings.TrimSpace(pattern)), 2*time.Second)
	}
	return nil
}
