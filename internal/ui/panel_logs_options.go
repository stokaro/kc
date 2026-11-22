package ui

import (
	"slices"

	tea "charm.land/bubbletea/v2"
	logswidget "github.com/sttts/kc/internal/ui/panelcontent/logs"
)

// panelLogsOptions adapts a Logs panel widget to the F2 viewer and log
// options of the fullscreen logs viewer (viewerOptionsTarget,
// logOptionsTarget and logContainerTarget).
type panelLogsOptions struct {
	w *logswidget.Widget
}

// showPanelLogsOptionsModal opens F2 for a panel in Logs mode: the Panel
// section of the other modes, then the Viewer and Logs sections of the
// fullscreen logs viewer. The commit applies the viewer and log options
// before the Panel section (see the ViewOptionsCommittedMsg case).
func (a *App) showPanelLogsOptionsModal(panel *Panel, panelIdx int, target panelLogsOptions) tea.Cmd {
	cfg := a.viewerOptionsConfig(target)
	cfg.PanelIndex = panelIdx
	cfg.SkipPanelSection = false
	cfg.PanelModes = panel.AvailableModes()
	cfg.ActivePanelMode = panel.Mode()
	cfg.PanelWidthPercent = a.panelWidthPercentFor(panelIdx)
	cfg.TableMode = panel.TableMode()
	content := NewViewOptionsModel(cfg)

	modal := a.modalManager.modals["view_options"]
	if modal == nil {
		modal = NewModal("View Options", content)
		a.modalManager.Register("view_options", modal)
	} else {
		modal.SetContent(content)
		modal.title = "View Options"
	}
	a.layoutViewOptionsModal(modal, content, panelIdx, "")
	modal.SetOnClose(func() tea.Cmd {
		a.viewerOptionsTarget = nil
		return nil
	})
	a.viewerOptionsTarget = target
	a.showModal("view_options")
	return nil
}

// logContainerTarget is a log view whose container F2 can choose.
type logContainerTarget interface {
	LogContainerChoices() []ViewOptionsChoice
}

func (o panelLogsOptions) SetTheme(theme string) { o.w.SetTheme(theme) }
func (o panelLogsOptions) Theme() string         { return o.w.Theme() }
func (o panelLogsOptions) SetWrapMode(on bool)   { o.w.SetWrapMode(on) }
func (o panelLogsOptions) WrapMode() bool        { return o.w.WrapMode() }

// LogOptions returns the stream options. A tail of 0 in the settings keeps
// the row's tail, which is returned instead.
func (o panelLogsOptions) LogOptions() LogOptions {
	s := o.w.Settings()
	tail := s.TailLines
	if tail == 0 {
		tail = o.w.Target().Spec.EffectiveTailLines()
	}
	if tail < 0 {
		tail = -1
	}
	return LogOptions{
		TailLines:    tail,
		SinceSeconds: s.SinceSeconds,
		Timestamps:   s.Timestamps,
		Previous:     s.Previous,
		Container:    s.Container,
	}
}

// SetLogOptions restarts the stream when the options change what it
// streams.
func (o panelLogsOptions) SetLogOptions(opts LogOptions) tea.Cmd {
	return o.w.SetSettings(logswidget.Settings{
		TailLines:    opts.TailLines,
		SinceSeconds: opts.SinceSeconds,
		Timestamps:   opts.Timestamps,
		Previous:     opts.Previous,
		Container:    opts.Container,
	})
}

// LogContainerChoices lists the Container row: the default, the pod's
// containers once a stream of the pod has opened, and all containers. It is
// empty while no pod is selected.
func (o panelLogsOptions) LogContainerChoices() []ViewOptionsChoice {
	target := o.w.Target()
	if target.Spec.Pod == "" {
		return nil
	}
	def := "Default"
	switch {
	case target.AllContainers:
		def = "Default (all containers)"
	case target.Spec.Container != "":
		def = "Default (" + target.Spec.Container + ")"
	}
	choices := []ViewOptionsChoice{{Value: logswidget.ContainerDefault, Label: def}}
	names := o.w.Containers()
	current := o.w.Settings().Container
	if current != logswidget.ContainerDefault && current != logswidget.ContainerAll && !slices.Contains(names, current) {
		names = append(names, current)
	}
	for _, name := range names {
		choices = append(choices, ViewOptionsChoice{Value: name, Label: name})
	}
	return append(choices, ViewOptionsChoice{Value: logswidget.ContainerAll, Label: "All containers"})
}
