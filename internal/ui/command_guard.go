package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/cmdtemplate"
	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/pkg/appconfig"
)

// commandConfirmModal is the modal name of the red dialog before a
// command with `confirm:` runs.
const commandConfirmModal = "command_confirm"

// commandMenuModal is the modal name of the custom commands menu.
const commandMenuModal = "command_menu"

// guardCommand wraps run, the start of a command picked in the menu (or by
// its hotkey), with the steps the command asks for: the prompt dialog,
// then the red confirmation. run gets the command with the answers bound
// into its templates, so restarts (watch ticks, namespace follow) reuse
// them without asking again. item and namespace are what the menu was
// opened on; they fill the prompt defaults and the confirmation.
func (a *App) guardCommand(targets []models.Item, namespace string, run func(appconfig.CommandConfig) tea.Cmd) func(appconfig.CommandConfig) tea.Cmd {
	return func(cmd appconfig.CommandConfig) tea.Cmd {
		if err := appconfig.ValidateCommand(cmd); err != nil {
			a.hideModalName(commandMenuModal)
			return a.commandError(cmd, err)
		}
		if len(cmd.Prompts) == 0 && !cmd.Confirm.Enabled {
			return run(cmd)
		}
		a.hideModalName(commandMenuModal)
		items, gvr := commandPreviewTarget(cmd, targets, namespace)
		if len(cmd.Prompts) == 0 {
			return a.confirmCommand(cmd, items, gvr, run)
		}
		return a.promptCommand(cmd, items, gvr, func(bound appconfig.CommandConfig) tea.Cmd {
			return a.confirmCommand(bound, items, gvr, run)
		})
	}
}

// commandPreviewTarget returns the items and GVR the menu passes to cmd:
// the namespace for namespace commands, the marked rows (or the row under
// the cursor) for the others, as commandTargets picks them.
func commandPreviewTarget(cmd appconfig.CommandConfig, targets []models.Item, namespace string) ([]models.Item, schema.GroupVersionResource) {
	if cmd.Type == appconfig.CommandTypeNamespace {
		if namespace == "" || namespace == namespacePlaceholder {
			return nil, schema.GroupVersionResource{}
		}
		return []models.Item{namespaceCommandItem{namespace: namespace}}, schema.GroupVersionResource{}
	}
	if len(targets) == 0 {
		return nil, schema.GroupVersionResource{}
	}
	// The fallback GVR the run uses: the first target's.
	var gvr schema.GroupVersionResource
	if obj, ok := targets[0].(models.ObjectItem); ok {
		gvr = obj.GVR()
	}
	return targets, gvr
}

func (a *App) commandError(cmd appconfig.CommandConfig, err error) tea.Cmd {
	ctrllog.FromContext(a.ctx).WithName("commands").Error(err, "custom command refused", "name", cmd.Name)
	text := fmt.Sprintf("%s: %v", cmd.Name, err)
	if a.toastLogger != nil {
		return a.toastLogger.Errorf("%s", text)
	}
	return a.ShowToast(text, 5*time.Second)
}

// promptCommand shows the prompt dialog of cmd. On OK the answers are
// bound into the command and the confirmation templates and next runs.
func (a *App) promptCommand(cmd appconfig.CommandConfig, items []models.Item, gvr schema.GroupVersionResource, next func(appconfig.CommandConfig) tea.Cmd) tea.Cmd {
	defaults := make(map[string]string, len(cmd.Prompts))
	for _, p := range cmd.Prompts {
		def := p.Default
		// A default may itself be a template, e.g. "{{.Name}}-copy".
		if rendered, err := RenderCommand(def, items, gvr); err == nil {
			def = rendered
		}
		defaults[p.Name] = def
	}
	model := NewCommandPromptModel(cmd.Prompts, defaults, func(values map[string]string) tea.Cmd {
		a.hideModalName(commandPromptModal)
		bound, err := bindCommandInput(cmd, values)
		if err != nil {
			return a.commandError(cmd, err)
		}
		return next(bound)
	}, func() tea.Cmd {
		a.hideModalName(commandPromptModal)
		return nil
	})
	modal := NewModal(cmd.Name, model)
	a.modalManager.Register(commandPromptModal, modal)
	w, h := model.size()
	a.configureModalWindow(modal, model, a.activePanel, "", min(w+2, max(30, a.width-6)), h+2)
	a.showModal(commandPromptModal)
	return nil
}

// bindCommandInput returns cmd with the prompt answers substituted into
// its command and confirmation templates and its prompts cleared.
func bindCommandInput(cmd appconfig.CommandConfig, values map[string]string) (appconfig.CommandConfig, error) {
	command, err := cmdtemplate.Bind(cmd.Command, values)
	if err != nil {
		return cmd, err
	}
	message, err := cmdtemplate.Bind(cmd.Confirm.Message, values)
	if err != nil {
		return cmd, err
	}
	cmd.Command = command
	cmd.Confirm.Message = message
	cmd.Prompts = nil
	return cmd, nil
}

// commandConfirmation describes cmd for the red dialog. The command is
// rendered as it will run; a long one wraps over several lines.
func (a *App) commandConfirmation(cmd appconfig.CommandConfig, items []models.Item, gvr schema.GroupVersionResource) (models.Confirmation, error) {
	rendered, err := RenderCommand(cmd.Command, items, gvr)
	if err != nil {
		return models.Confirmation{}, err
	}
	title := "Run " + cmd.Name + "?"
	if cmd.Confirm.Message != "" {
		if title, err = RenderCommand(cmd.Confirm.Message, items, gvr); err != nil {
			return models.Confirmation{}, err
		}
	}
	c := models.Confirmation{Title: title, Action: "Run"}
	if a.currentCtx != nil && a.currentCtx.Name != "" {
		c.Details = append(c.Details, [2]string{"Context", a.currentCtx.Name})
	}
	if len(items) > 0 {
		if obj, ok := items[0].(models.ObjectItem); ok {
			if ns := obj.Namespace(); ns != "" {
				c.Details = append(c.Details, [2]string{"Namespace", ns})
			}
			if g := obj.GVR(); !g.Empty() {
				c.Details = append(c.Details, [2]string{"Object", g.Resource + "/" + obj.Name()})
			}
		}
	}
	width := max(30, a.width-12)
	c.Notes = append(c.Notes, "Command:")
	for _, line := range strings.Split(rendered, "\n") {
		c.Notes = append(c.Notes, wrapCommandLine(line, width)...)
	}
	return c, nil
}

// wrapCommandLine cuts line into pieces of at most width runes, at spaces
// where it can, and indents the pieces.
func wrapCommandLine(line string, width int) []string {
	const indent = "  "
	width = max(10, width-len(indent))
	var out []string
	rest := []rune(line)
	for len(rest) > width {
		cut := width
		for i := width; i > width/2; i-- {
			if rest[i] == ' ' {
				cut = i
				break
			}
		}
		out = append(out, indent+string(rest[:cut]))
		rest = []rune(strings.TrimLeft(string(rest[cut:]), " "))
	}
	return append(out, indent+string(rest))
}

// confirmCommand runs cmd, after the red dialog when it asks for one.
func (a *App) confirmCommand(cmd appconfig.CommandConfig, items []models.Item, gvr schema.GroupVersionResource, run func(appconfig.CommandConfig) tea.Cmd) tea.Cmd {
	if !cmd.Confirm.Enabled {
		return run(cmd)
	}
	c, err := a.commandConfirmation(cmd, items, gvr)
	if err != nil {
		return a.commandError(cmd, err)
	}
	inner := NewConfirmModel(c)
	model := &commandConfirmModel{ConfirmModel: inner, onAnswer: func(ok bool) tea.Cmd {
		a.hideModalName(commandConfirmModal)
		if !ok {
			return nil
		}
		return run(cmd)
	}}
	modal := NewModal("Confirm", model)
	modal.SetDanger(true)
	a.modalManager.Register(commandConfirmModal, modal)
	w, h := inner.size()
	a.configureModalWindow(modal, model, a.activePanel, "", min(w+2, max(30, a.width-6)), h+2)
	a.showModal(commandConfirmModal)
	return nil
}

// commandConfirmModel is the red ConfirmModel with its answer handled in
// place: the command starts as a tea.Cmd, which a models.ConfirmedAction
// (Confirm() error) cannot return.
type commandConfirmModel struct {
	*ConfirmModel
	onAnswer func(ok bool) tea.Cmd
}

func (m *commandConfirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := m.ConfirmModel.Update(msg)
	return m, m.resolve(cmd)
}

func (m *commandConfirmModel) HandleModalEscape(k tea.KeyMsg) (bool, tea.Cmd) {
	handled, cmd := m.ConfirmModel.HandleModalEscape(k)
	return handled, m.resolve(cmd)
}

// resolve turns the dialog's answer into the next step. ConfirmModel
// answers with a command that only returns a confirmResultMsg, so calling
// it here is cheap and keeps the message away from the app's own
// confirmation handler.
func (m *commandConfirmModel) resolve(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if res, ok := msg.(confirmResultMsg); ok {
		return m.onAnswer(res.ok)
	}
	return func() tea.Msg { return msg }
}
