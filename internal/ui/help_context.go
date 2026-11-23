package ui

import (
	"context"
	"reflect"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/help"
	"github.com/stokaro/kc/internal/models"
)

// F1 opens the help page of what has focus, as in mc: the dialog on top,
// else the panel's mode, else the row under the cursor or the folder the
// panel lists. Where nothing specific applies it opens the index. The maps
// below are the whole mapping; help_context_test.go fails when a dialog,
// a panel mode or a folder type has no entry.

// helpModalName is the help window's own dialog name.
const helpModalName = "help"

// modalHelpPages maps each registered dialog name to its page.
var modalHelpPages = map[string]help.ID{
	"view_options":           help.PanelOptions,
	"viewer_options":         help.ViewerOptions,
	"namespace_create":       help.NamespaceCreate,
	"delete_confirm":         help.Delete,
	"copy_local":             help.Copy,
	transferModalKey:         help.Copy,
	panelModeModalKey(0):     help.PanelMode,
	panelModeModalKey(1):     help.PanelMode,
	"yaml_viewer":            help.Viewer,
	"logs_viewer":            help.LogsViewer,
	"command_menu":           help.CustomCommands,
	commandPromptModal:       help.CustomCommands,
	commandConfirmModal:      help.CustomCommands,
	"confirm_action":         help.Confirm,
	"context_picker":         help.ContextNamespace,
	"namespace_picker":       help.ContextNamespace,
	diagnosticStopModal:      help.Diagnostics,
	diagnosticReportModal:    help.Diagnostics,
	hotlistModalKey:          help.Hotlist,
	viewNameModalKey:         help.Hotlist,
	historyModalKey:          help.History,
	logsFieldsModalName:      help.LogsFields,
	logsFilterModalName:      help.LogsFilter,
	logsSourcesModalName:     help.LogsSources,
	nodeMenuModal:            help.NodeActions,
	nodeOptionsModal:         help.NodeActions,
	nodeConfirmModal:         help.NodeActions,
	nodeDrainModal:           help.NodeActions,
	rowFilterModal:           help.RowFilter,
	portForwardModal:         help.PortForward,
	workloadInputModal:       help.WorkloadActions,
	workloadPickModal:        help.WorkloadActions,
	workloadConfirmModal:     help.WorkloadActions,
	accessViewModalName:      help.Access,
	windowModalKey:           help.InvestigationWindow,
	"compare_viewer":         help.CompareObjects,
	"compare_context_picker": help.CompareLists,
	"compare_lists":          help.CompareLists,
	"compare_lists_diff":     help.DiffViewer,
}

// modeHelpPages maps each panel mode to its page.
var modeHelpPages = map[PanelViewMode]help.ID{
	PanelModeList:     help.Panels,
	PanelModeDescribe: help.ModeDescribe,
	PanelModeManifest: help.ModeManifest,
	PanelModeCommand:  help.ModeCommand,
	PanelModeLogs:     help.ModeLogs,
	PanelModeInfo:     help.ModeInfo,
}

// folderHelpPages maps each folder type of internal/models, by type name,
// to the page of a List panel showing it. Plain lists open the panels page.
var folderHelpPages = map[string]help.ID{
	"BaseFolder":                help.Panels,
	"RootFolder":                help.ContextNamespace,
	"ContextRootFolder":         help.Panels,
	"ResourcesFolder":           help.Panels,
	"ClusterResourcesFolder":    help.Panels,
	"NamespacedResourcesFolder": help.Panels,
	"ObjectsFolder":             help.Panels,
	"ClusterObjectsFolder":      help.Panels,
	"NamespacedObjectsFolder":   help.Panels,
	"ChildResourceTypesFolder":  help.Panels,
	"NodeChildFolder":           help.Panels,
	"ConfigMapKeysFolder":       help.Viewer,
	"SecretKeysFolder":          help.Viewer,
	"PodContainersFolder":       help.Panels,
	"PodContainerListFolder":    help.Panels,
	"PodContainerDetailFolder":  help.Panels,
	"PodContainerFSFolder":      help.ContainerFS,
	"LocalDirFolder":            help.LocalFiles,
	"OverviewFolder":            help.Overview,
	"OverviewTargetsFolder":     help.Overview,
	"overviewFindingsFolder":    help.Attention, // or quiet checks, by path
	"RelatedObjectsFolder":      help.Related,   // or diagnosis, by kind
	"TimelineFolder":            help.Timeline,
	"HelmReleasesFolder":        help.Helm,
	"HelmReleaseFolder":         help.Helm,
	"HelmHistoryFolder":         help.Helm,
	"HelmResourcesFolder":       help.Helm,
	"PortForwardsFolder":        help.PortForward,
	"ExplainFolder":             help.Explain,
}

// quietChecksSuffix ends the path segment of a section's quiet checks.
const quietChecksSuffix = " quiet checks"

// rowHelpPages maps the last path segment of a row that enters a special
// folder to that folder's page: F1 on "/diagnosis" explains the diagnosis.
var rowHelpPages = map[string]help.ID{
	"attention":                    help.Attention,
	"overview":                     help.Overview,
	models.DiagnosisSegment:        help.Diagnosis,
	models.TimelineSegment:         help.Timeline,
	models.OwnersSegment:           help.Related,
	models.UsedBySegment:           help.Related,
	models.DependsOnSegment:        help.Related,
	models.PortForwardsPathSegment: help.PortForward,
}

// modalHelpPage returns the page of the dialog registered as name.
func modalHelpPage(name string) help.ID {
	if name == helpModalName {
		return ""
	}
	if page, ok := modalHelpPages[name]; ok {
		return page
	}
	return help.Index
}

// folderHelpPage returns the page of a List panel showing folder.
func folderHelpPage(folder models.Folder) help.ID {
	if folder == nil {
		return help.Panels
	}
	if f, ok := folder.(*models.RelatedObjectsFolder); ok {
		if f.Kind() == models.RelatedDiagnosis {
			return help.Diagnosis
		}
		return help.Related
	}
	t := reflect.TypeOf(folder)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	page, ok := folderHelpPages[t.Name()]
	if !ok {
		return help.Panels
	}
	if page == help.Attention {
		if p := folder.Path(); len(p) > 0 && strings.HasSuffix(p[len(p)-1], quietChecksSuffix) {
			return help.QuietChecks
		}
	}
	return page
}

// rowHelpPage returns the page of a row that is more than an object: the
// entry of a special folder or a logs row. It reports false for others.
func rowHelpPage(item models.Item) (help.ID, bool) {
	switch item.(type) {
	case nil:
		return "", false
	case *models.WorkloadLogItem:
		return help.LogsViewer, true
	case models.ObjectItem:
		return "", false // an object named "timeline" is just an object
	}
	if r, ok := item.(interface{ ID() string }); ok && r.ID() == models.OverviewRowID {
		return help.Overview, true
	}
	p := item.Path()
	if len(p) == 0 {
		return "", false
	}
	last := p[len(p)-1]
	if strings.HasSuffix(last, quietChecksSuffix) {
		return help.QuietChecks, true
	}
	page, ok := rowHelpPages[last]
	return page, ok
}

// panelHelpPage returns the page for F1 in panel.
func (a *App) panelHelpPage(panel *Panel) help.ID {
	if panel == nil {
		return help.Index
	}
	if panel.Mode() != PanelModeList {
		if page, ok := modeHelpPages[panel.Mode()]; ok {
			return page
		}
		return help.Index
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	if item, ok := panel.SelectedNavItem(ctx); ok {
		if page, ok := rowHelpPage(item); ok {
			return page
		}
	}
	return folderHelpPage(panel.Folder())
}

// showPanelHelp opens the page for the active panel.
func (a *App) showPanelHelp(panel *Panel) tea.Cmd {
	return a.showHelp(a.panelHelpPage(panel))
}

// showHelp opens page in the help window over whatever is on the screen.
// The dialogs below stay open and keep their state; closing the help
// returns to them.
func (a *App) showHelp(page help.ID) tea.Cmd {
	if a.helpViewer == nil || a.modalManager == nil {
		return nil
	}
	modal := a.modalManager.modals[helpModalName]
	if modal == nil {
		return nil
	}
	if modal.IsVisible() {
		a.helpViewer.Open(page)
		a.invalidateView("help page")
		return nil
	}
	// The screen as it is now, dialogs included, is the background.
	bg := viewString(a.View())
	a.helpViewer.Reset(page)
	modal.SetDimensions(a.width, a.height)
	winW, winH := helpWindowSize(a.width, a.height)
	modal.SetWindowed(winW, winH, bg)
	a.showModal(helpModalName)
	return nil
}

// helpWindowSize returns the help window for a screen: at most
// helpWindowWidth wide, else the screen less a margin, and most of the
// height.
func helpWindowSize(width, height int) (winW, winH int) {
	winW = min(helpWindowWidth, width-4)
	if winW < 30 {
		winW = width
	}
	winH = height - 4
	if winH < 10 {
		winH = height - 1
	}
	return max(1, winW), max(1, winH)
}
