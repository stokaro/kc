package help

// The pages the code opens by name. Each has a file topics/<id>.md; a test
// checks that every constant here has one and every file has a constant.
const (
	Index ID = "index"
	Help  ID = "help"

	// Panels and their modes.
	Panels       ID = "panels"
	PanelMode    ID = "panel-mode"
	ModeDescribe ID = "mode-describe"
	ModeManifest ID = "mode-manifest"
	ModeLogs     ID = "mode-logs"
	ModeInfo     ID = "mode-info"
	ModeCommand  ID = "mode-command"

	// Options, locations and navigation.
	PanelOptions     ID = "panel-options"
	ViewerOptions    ID = "viewer-options"
	ContextNamespace ID = "context-namespace"
	NamespaceCreate  ID = "namespace-create"
	Hotlist          ID = "hotlist"
	History          ID = "history"
	MenuBar          ID = "menu-bar"
	ReadOnly         ID = "read-only"
	Shell            ID = "shell"

	// Dialogs and actions.
	RowFilter           ID = "row-filter"
	Delete              ID = "delete"
	Copy                ID = "copy"
	Confirm             ID = "confirm"
	CustomCommands      ID = "custom-commands"
	Diagnostics         ID = "diagnostics"
	NodeActions         ID = "node-actions"
	WorkloadActions     ID = "workload-actions"
	PortForward         ID = "port-forward"
	Access              ID = "access"
	CompareObjects      ID = "compare-objects"
	CompareLists        ID = "compare-lists"
	PinFreeze           ID = "pin-freeze"
	InvestigationWindow ID = "investigation-window"

	// Special folders.
	Overview    ID = "overview"
	Attention   ID = "attention"
	QuietChecks ID = "quiet-checks"
	Diagnosis   ID = "diagnosis"
	Timeline    ID = "timeline"
	Related     ID = "related"
	ContainerFS ID = "container-fs"
	LocalFiles  ID = "local-files"
	Helm        ID = "helm"
	Explain     ID = "explain"

	// Full-screen viewers and the logs dialogs.
	Viewer      ID = "viewer"
	LogsViewer  ID = "logs-viewer"
	LogsSources ID = "logs-sources"
	LogsFields  ID = "logs-fields"
	LogsFilter  ID = "logs-filter"
	DiffViewer  ID = "diff-viewer"
)
