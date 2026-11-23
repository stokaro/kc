package appconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ViewerConfig struct {
	Theme string           `json:"theme"`
	Mode  string           `json:"mode"`
	Logs  LogsViewerConfig `json:"logs"`
}

// LogsViewerConfig holds the options a logs viewer opens with.
type LogsViewerConfig struct {
	// Tail is the number of recent lines fetched before following. -1
	// fetches the whole log; 0 means the default of 200.
	Tail int64 `json:"tail"`
	// Since limits the log to lines newer than this. 0s means no limit.
	Since metav1.Duration `json:"since"`
	// Timestamps prefixes every line with its timestamp.
	Timestamps bool `json:"timestamps"`
	// MaxRequests limits the log streams a merged logs viewer (all pods of
	// a workload, all containers of a pod) runs at once, like kubectl's
	// --max-log-requests. Further streams wait for a free slot. Zero or
	// less means DefaultLogsMaxRequests.
	MaxRequests int `json:"maxRequests"`
	// MaxLines bounds the lines one logs viewer or Logs panel keeps; older
	// lines are dropped. Zero or less means DefaultLogsMaxLines.
	MaxLines int `json:"maxLines"`
	// MaxMemoryMiB bounds the estimated memory of the lines one logs viewer
	// or Logs panel keeps, in MiB; older lines are dropped. Zero or less
	// means DefaultLogsMaxMemoryMiB.
	MaxMemoryMiB int `json:"maxMemoryMiB"`
}

// Defaults of viewer.logs.maxLines and viewer.logs.maxMemoryMiB.
const (
	DefaultLogsMaxLines     = 100_000
	DefaultLogsMaxMemoryMiB = 64
)

// EffectiveMaxLines returns MaxLines, or the default when it is not
// positive.
func (c LogsViewerConfig) EffectiveMaxLines() int {
	if c.MaxLines <= 0 {
		return DefaultLogsMaxLines
	}
	return c.MaxLines
}

// EffectiveMaxMemoryBytes returns MaxMemoryMiB in bytes, or the default when
// it is not positive.
func (c LogsViewerConfig) EffectiveMaxMemoryBytes() int64 {
	mib := c.MaxMemoryMiB
	if mib <= 0 {
		mib = DefaultLogsMaxMemoryMiB
	}
	return int64(mib) << 20
}

// DefaultLogsTail is the default for viewer.logs.tail.
const DefaultLogsTail int64 = 200

// DefaultLogsMaxRequests is the default for viewer.logs.maxRequests. It
// matches stern's default for following logs.
const DefaultLogsMaxRequests = 50

// EffectiveMaxRequests returns MaxRequests, or the default when it is not
// positive.
func (c LogsViewerConfig) EffectiveMaxRequests() int {
	if c.MaxRequests <= 0 {
		return DefaultLogsMaxRequests
	}
	return c.MaxRequests
}

const (
	ViewerModeScroll = "scroll"
	ViewerModeWrap   = "wrap"
)

const (
	ColumnsModeNormal = "normal"
	ColumnsModeWide   = "wide"
)

const (
	ObjectsOrderName         = "name"
	ObjectsOrderNameDesc     = "-name"
	ObjectsOrderCreation     = "creation"
	ObjectsOrderCreationDesc = "-creation"
)

// ObjectsOrderKey returns the objects order key of a table column: the title
// in lower case with inner spaces replaced by dashes ("Nominated Node" gives
// "nominated-node").
func ObjectsOrderKey(title string) string {
	return strings.ToLower(strings.Join(strings.Fields(title), "-"))
}

// NormalizeObjectsOrder returns the canonical form of an objects order: a
// column key (see ObjectsOrderKey), or "creation", with a leading "-" for
// descending order. Values that name no key become "name". A key that a
// given table lacks is not an error here; that list falls back to name.
func NormalizeObjectsOrder(order string) string {
	order = strings.TrimSpace(order)
	desc := strings.HasPrefix(order, "-")
	if desc {
		order = order[1:]
	}
	key := ObjectsOrderKey(order)
	if key == "" || strings.HasPrefix(key, "-") {
		return ObjectsOrderName
	}
	if desc {
		return "-" + key
	}
	return key
}

type HorizontalConfig struct {
	Step int `json:"step"`
}

type ScrollingConfig struct {
	Horizontal HorizontalConfig `json:"horizontal"`
}

type PanelWidthConfig struct {
	LeftPercent  int `json:"leftPercent"`
	RightPercent int `json:"rightPercent"`
}

type PanelConfig struct {
	Scrolling ScrollingConfig  `json:"scrolling"`
	Table     TableConfig      `json:"table"`
	Width     PanelWidthConfig `json:"width"`
	// Skin names the panel skin: a built-in one ("mc", "dark") or
	// ~/.kc/skins/<name>.yaml. Empty means "mc".
	Skin string `json:"skin"`
}

// ContextColorConfig tints the panel frame and the context label of panels
// whose kubeconfig context matches. Set exactly one of Match and Regex.
type ContextColorConfig struct {
	// Match is a context name or a glob: "*" matches any run of characters
	// (including "/"), "?" one character, e.g. "*prod*".
	Match string `json:"match,omitempty"`
	// Regex is a Go regular expression matched against the context name.
	Regex string `json:"regex,omitempty"`
	// Color is an ANSI name, an ANSI 256 index or a hex value.
	Color string `json:"color"`
}

type MouseConfig struct {
	DoubleClickTimeout metav1.Duration `json:"doubleClickTimeout"`
}

type InputConfig struct {
	Mouse      MouseConfig     `json:"mouse"`
	EscTimeout metav1.Duration `json:"escTimeout"`
}

type ClustersConfig struct {
	TTL metav1.Duration `json:"ttl"` // duration, e.g. 2m, 30s
}

type DiscoveryConfig struct {
	Refresh metav1.Duration `json:"refresh"`
}

type KubernetesConfig struct {
	Clusters  ClustersConfig  `json:"clusters"`
	Discovery DiscoveryConfig `json:"discovery"`
}

// ResourcesViewOrder is the ordering mode for resource groups.
// Valid values: "alpha", "group", "favorites".
type ResourcesViewOrder string

const (
	OrderAlpha     ResourcesViewOrder = "alpha"
	OrderGroup     ResourcesViewOrder = "group"
	OrderFavorites ResourcesViewOrder = "favorites"
)

// ResourcesViewConfig controls how resource groups are displayed.
type ResourcesViewConfig struct {
	// ShowNonEmptyOnly toggles filtering of resource groups to those with >0 objects.
	ShowNonEmptyOnly bool `json:"showNonEmptyOnly"`
	// Order controls ordering of groups. Valid values are "alpha", "group", "favorites".
	Order ResourcesViewOrder `json:"order"`
	// Favorites lists resource plural names to prioritize when OrderFavorites is active.
	// Leave empty to auto-populate from discovery's "all" category.
	Favorites []string `json:"favorites"`
	// Columns controls which server-side table columns are shown. Valid values are "normal" and "wide".
	Columns string `json:"columns"`
	// ObjectsOrder controls ordering within object lists when drilling into resources. Valid values are "name", "-name", "creation", "-creation".
	ObjectsOrder string `json:"objectsOrder"`
	// PeekInterval throttles how often empty-resource peeks hit the API (default 10s).
	PeekInterval metav1.Duration `json:"peekInterval"`
}

// LayoutConfig holds the screen layout settings of mc's Options > Layout.
type LayoutConfig struct {
	// MenuBarVisible keeps the menu bar titles on the top row, as mc does by
	// default; the panels start one row lower. When false the top row
	// belongs to the panels and the bar shows only while F9 has it open.
	MenuBarVisible bool `json:"menuBarVisible"`
	// LeftPanelMode and RightPanelMode are the modes the panels start in:
	// list, describe, manifest, logs or info. At least one panel starts in
	// list mode. Panel modes are not saved between runs, so these apply on
	// every start.
	LeftPanelMode  PanelStartMode `json:"leftPanelMode"`
	RightPanelMode PanelStartMode `json:"rightPanelMode"`
}

type Config struct {
	Layout      LayoutConfig        `json:"layout"`
	Viewer      ViewerConfig        `json:"viewer"`
	Panel       PanelConfig         `json:"panel"`
	Input       InputConfig         `json:"input"`
	Kubernetes  KubernetesConfig    `json:"kubernetes"`
	Resources   ResourcesViewConfig `json:"resources"`
	Objects     ObjectsConfig       `json:"objects"`
	Terminal    TerminalConfig      `json:"terminal"`
	NodeShell   NodeShellConfig     `json:"nodeShell"`
	PortForward PortForwardConfig   `json:"portForward"`
	Commands    []CommandConfig     `json:"commands"`
	// Contexts color panels per kubeconfig context; the first matching
	// entry wins.
	Contexts []ContextColorConfig `json:"contexts"`
	// Columns adds and hides object list columns per resource type, keyed
	// by group/version/resource (see ColumnsKey). Empty by default.
	Columns  map[string][]CustomColumn `json:"columns,omitempty"`
	Overview OverviewConfig            `json:"overview"`
	// Timeline bounds the object timelines (/timeline inside an object).
	Timeline TimelineConfig `json:"timeline"`
	// ReadOnly opens contexts in read-only mode; see ReadOnlyConfig.
	ReadOnly ReadOnlyConfig `json:"readOnly"`
}

// OverviewConfig holds the thresholds of the /overview folder. A zero or
// negative value means the default; Normalized fills it in.
type OverviewConfig struct {
	// Window is how far back warning events and container restarts count.
	Window metav1.Duration `json:"window"`
	// RestartThreshold is how many restarts make a pod "restarting" when its
	// last restart is inside Window.
	RestartThreshold int `json:"restartThreshold"`
	// RequestsWarnPercent and RequestsFailPercent compare the CPU and memory
	// requests on a node with its allocatable.
	RequestsWarnPercent int `json:"requestsWarnPercent"`
	RequestsFailPercent int `json:"requestsFailPercent"`
	// LimitsWarnPercent marks a node overcommitted when the limits of its
	// pods add up to more than this percent of allocatable.
	LimitsWarnPercent int `json:"limitsWarnPercent"`
	// PodsWarnPercent compares the pods on a node with allocatable pods.
	PodsWarnPercent int `json:"podsWarnPercent"`
	// QuotaWarnPercent flags a ResourceQuota when any used value reaches
	// this percent of its hard limit.
	QuotaWarnPercent int `json:"quotaWarnPercent"`
	// UsageWarnPercent and UsageFailPercent judge the measured CPU and
	// memory usage (metrics.k8s.io): of the pods on a node against its
	// allocatable, and of the node itself against its capacity.
	UsageWarnPercent int `json:"usageWarnPercent"`
	UsageFailPercent int `json:"usageFailPercent"`
	// UnderRequestedFactor marks a node "under-requested" when the measured
	// usage of its pods is at least this many times what they request, and
	// that usage reaches UnderRequestedMinPercent of allocatable. The scheduler
	// then keeps placing pods on a node that is busier than it thinks.
	UnderRequestedFactor     float64 `json:"underRequestedFactor"`
	UnderRequestedMinPercent int     `json:"underRequestedMinPercent"`
	// MemoryLimitWarnPercent flags a container whose memory usage reaches
	// this percent of its limit: it is close to an OOM kill.
	MemoryLimitWarnPercent int `json:"memoryLimitWarnPercent"`
	// CPULimitWarnPercent flags a container whose CPU usage reaches this
	// percent of its limit: it is throttled.
	CPULimitWarnPercent int `json:"cpuLimitWarnPercent"`
	// DiskWarnPercent and DiskFailPercent compare the used node and image
	// filesystem of a node (kubelet stats summary) with its capacity.
	DiskWarnPercent int `json:"diskWarnPercent"`
	DiskFailPercent int `json:"diskFailPercent"`
	// VolumeWarnPercent flags a mounted PersistentVolumeClaim whose used
	// bytes reach this percent of its capacity.
	VolumeWarnPercent int `json:"volumeWarnPercent"`
	// StatsMaxNodes skips the kubelet stats summary (one request per node)
	// on clusters with more nodes than this.
	StatsMaxNodes int `json:"statsMaxNodes"`
	// CertificateExpiryDays flags a cert-manager Certificate whose
	// status.notAfter is less than this many days away.
	CertificateExpiryDays int `json:"certificateExpiryDays"`
}

// TimelineConfig bounds the revisions kc keeps while it records objects. A
// zero or negative value means the default; Normalized fills it in.
type TimelineConfig struct {
	// MaxEntries is how many revisions one object's timeline keeps; the
	// oldest are evicted first.
	MaxEntries int `json:"maxEntries"`
	// MaxBytes bounds the revisions of one object by their JSON size.
	MaxBytes int `json:"maxBytes"`
	// MaxObjects is how many objects kc records at once; starting one more
	// stops the oldest recording.
	MaxObjects int `json:"maxObjects"`
}

// DefaultTimeline returns the default timeline bounds.
func DefaultTimeline() TimelineConfig {
	return TimelineConfig{MaxEntries: 100, MaxBytes: 4 << 20, MaxObjects: 10}
}

// Normalized returns t with every unset or invalid value replaced by its
// default.
func (t TimelineConfig) Normalized() TimelineConfig {
	d := DefaultTimeline()
	if t.MaxEntries <= 0 {
		t.MaxEntries = d.MaxEntries
	}
	if t.MaxBytes <= 0 {
		t.MaxBytes = d.MaxBytes
	}
	if t.MaxObjects <= 0 {
		t.MaxObjects = d.MaxObjects
	}
	return t
}

// DefaultOverview returns the default overview thresholds.
func DefaultOverview() OverviewConfig {
	return OverviewConfig{
		Window:              metav1.Duration{Duration: time.Hour},
		RestartThreshold:    3,
		RequestsWarnPercent: 80,
		RequestsFailPercent: 95,
		LimitsWarnPercent:   100,
		PodsWarnPercent:     90,
		QuotaWarnPercent:    80,

		UsageWarnPercent:         80,
		UsageFailPercent:         90,
		UnderRequestedFactor:     2,
		UnderRequestedMinPercent: 25,
		MemoryLimitWarnPercent:   90,
		CPULimitWarnPercent:      90,
		DiskWarnPercent:          80,
		DiskFailPercent:          90,
		VolumeWarnPercent:        80,
		StatsMaxNodes:            50,
		CertificateExpiryDays:    14,
	}
}

// Normalized returns o with every unset or invalid value replaced by its
// default.
func (o OverviewConfig) Normalized() OverviewConfig {
	d := DefaultOverview()
	if o.Window.Duration <= 0 {
		o.Window = d.Window
	}
	positive := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	positive(&o.RestartThreshold, d.RestartThreshold)
	positive(&o.RequestsWarnPercent, d.RequestsWarnPercent)
	positive(&o.RequestsFailPercent, d.RequestsFailPercent)
	positive(&o.LimitsWarnPercent, d.LimitsWarnPercent)
	positive(&o.PodsWarnPercent, d.PodsWarnPercent)
	positive(&o.QuotaWarnPercent, d.QuotaWarnPercent)
	positive(&o.UsageWarnPercent, d.UsageWarnPercent)
	positive(&o.UsageFailPercent, d.UsageFailPercent)
	positive(&o.UnderRequestedMinPercent, d.UnderRequestedMinPercent)
	positive(&o.MemoryLimitWarnPercent, d.MemoryLimitWarnPercent)
	positive(&o.CPULimitWarnPercent, d.CPULimitWarnPercent)
	positive(&o.DiskWarnPercent, d.DiskWarnPercent)
	positive(&o.DiskFailPercent, d.DiskFailPercent)
	positive(&o.VolumeWarnPercent, d.VolumeWarnPercent)
	positive(&o.StatsMaxNodes, d.StatsMaxNodes)
	positive(&o.CertificateExpiryDays, d.CertificateExpiryDays)
	if o.UnderRequestedFactor <= 1 {
		// A factor of 1 or less would flag every node that uses what it
		// requests.
		o.UnderRequestedFactor = d.UnderRequestedFactor
	}
	if o.RequestsFailPercent < o.RequestsWarnPercent {
		o.RequestsFailPercent = o.RequestsWarnPercent
	}
	if o.UsageFailPercent < o.UsageWarnPercent {
		o.UsageFailPercent = o.UsageWarnPercent
	}
	if o.DiskFailPercent < o.DiskWarnPercent {
		o.DiskFailPercent = o.DiskWarnPercent
	}
	return o
}

// NodeShellConfig controls the node shell: a privileged pod pinned to a
// node that enters the host namespaces. It is off by default.
type NodeShellConfig struct {
	// Enabled turns the node shell on.
	Enabled bool `json:"enabled"`
	// Contexts limits the node shell to these kube contexts. Empty means
	// every context while Enabled is true.
	Contexts []string `json:"contexts,omitempty"`
	// Image runs the shell pod. It needs sh and nsenter.
	Image string `json:"image"`
	// Namespace holds the shell pod. Pod security admission must allow
	// privileged pods there.
	Namespace string `json:"namespace"`
	// MaxDuration is the pod's activeDeadlineSeconds: the pod is stopped
	// after this time even while the shell is in use.
	MaxDuration metav1.Duration `json:"maxDuration"`
}

// Default node shell values.
const (
	DefaultNodeShellImage       = "docker.io/library/busybox:1.36.1"
	DefaultNodeShellNamespace   = "default"
	DefaultNodeShellMaxDuration = 8 * time.Hour
)

// EnabledFor reports whether the node shell may run on context.
func (c NodeShellConfig) EnabledFor(context string) bool {
	if !c.Enabled {
		return false
	}
	if len(c.Contexts) == 0 {
		return true
	}
	for _, name := range c.Contexts {
		if strings.TrimSpace(name) == context {
			return true
		}
	}
	return false
}

func normalizeNodeShell(c *NodeShellConfig) {
	c.Image = strings.TrimSpace(c.Image)
	if c.Image == "" {
		c.Image = DefaultNodeShellImage
	}
	c.Namespace = strings.TrimSpace(c.Namespace)
	if c.Namespace == "" {
		c.Namespace = DefaultNodeShellNamespace
	}
	if c.MaxDuration.Duration <= 0 {
		c.MaxDuration = metav1.Duration{Duration: DefaultNodeShellMaxDuration}
	}
}

// PortForwardConfig controls port forwards started from kc.
type PortForwardConfig struct {
	// Address is the local address forwards listen on: an IP address or
	// "localhost" (both loopback addresses, as kubectl does).
	Address string `json:"address"`
}

// DefaultPortForwardAddress keeps forwards reachable from this machine only.
const DefaultPortForwardAddress = "127.0.0.1"

type CommandType string

const (
	CommandTypeSelector  CommandType = "selector"
	CommandTypeSticky    CommandType = "sticky"
	CommandTypeNamespace CommandType = "namespace"
	CommandTypeGlobal    CommandType = "global"
)

type CommandLocation string

const (
	CommandLocationPanel      CommandLocation = "panel"
	CommandLocationFullscreen CommandLocation = "fullscreen"
)

type CommandExitBehavior string

const (
	CommandExitKeepOpen CommandExitBehavior = "keep-open"
	CommandExitClose    CommandExitBehavior = "close"
	CommandExitRestore  CommandExitBehavior = "restore"
)

type CommandShowForConfig struct {
	Resources []string `json:"resources"`
	Groups    []string `json:"groups"`
}

type CommandConfig struct {
	Name                   string               `json:"name"`
	Command                string               `json:"command"`
	Type                   CommandType          `json:"type"`
	SupportsMultiSelection bool                 `json:"supportsMultiSelection"`
	ShowFor                CommandShowForConfig `json:"showFor"`
	Location               CommandLocation      `json:"location"`
	Interactive            bool                 `json:"interactive"`
	WatchInterval          metav1.Duration      `json:"watchInterval"`
	Debounce               metav1.Duration      `json:"debounce"`
	OnExit                 CommandExitBehavior  `json:"onExit"`
	// Hotkey runs the command without the menu, in mc notation ("M-1",
	// "C-x 1"). See ParseHotkey and ValidateCommands.
	Hotkey string `json:"hotkey,omitempty"`
	// Prompts are asked in one dialog before the command runs; the
	// template reads the answers as {{.Input.<name>}}.
	Prompts []CommandPrompt `json:"prompts,omitempty"`
	// Confirm shows the rendered command in a red dialog before it runs.
	Confirm CommandConfirm `json:"confirm,omitzero"`
	// Dangerous marks a command that changes the cluster. Read-only mode
	// hides and refuses dangerous commands; every other command still runs.
	Dangerous bool `json:"dangerous,omitempty"`
}

// ObjectsConfig controls object-list specific options.
type ObjectsConfig struct {
	// Order controls ordering within object lists: a column key (the column
	// title in lower case, spaces as dashes, e.g. "name", "age", "restarts")
	// or "creation", with a leading "-" for descending order. Lists without
	// that column sort by name.
	Order string `json:"order"`
	// Columns controls which columns are shown. Valid values are ColumnsModeNormal and ColumnsModeWide.
	Columns string `json:"columns"`
	// GroupByNamespace groups a list across namespaces by namespace and
	// sorts inside each namespace, as kubectl get -A prints it. When false
	// the sort column orders all rows at once, so the pod with the most
	// memory comes first whatever its namespace.
	GroupByNamespace bool `json:"groupByNamespace"`
}

type TerminalMode string

const (
	TerminalModeOverlay TerminalMode = "overlay"
	TerminalModeCopy    TerminalMode = "copy"
)

type TerminalConfig struct {
	Follow bool         `json:"follow"`
	Mode   TerminalMode `json:"mode"`
}

// TableMode selects how tables render horizontally.
// "scroll": horizontal panning across all columns.
// "fit": fit all columns within the viewport width.
type TableMode string

const (
	TableModeScroll TableMode = "scroll"
	TableModeFit    TableMode = "fit"
)

type TableConfig struct {
	Mode TableMode `json:"mode"`
}

func Default() *Config {
	return &Config{
		Layout: LayoutConfig{
			MenuBarVisible: true,
			LeftPanelMode:  PanelStartList,
			RightPanelMode: PanelStartInfo,
		},
		Viewer: ViewerConfig{
			Theme: "dracula",
			Mode:  ViewerModeScroll,
			Logs: LogsViewerConfig{
				Tail:         DefaultLogsTail,
				MaxRequests:  DefaultLogsMaxRequests,
				MaxLines:     DefaultLogsMaxLines,
				MaxMemoryMiB: DefaultLogsMaxMemoryMiB,
			},
		},
		Panel: PanelConfig{
			Scrolling: ScrollingConfig{Horizontal: HorizontalConfig{Step: 4}},
			Table:     TableConfig{Mode: TableModeScroll},
			Width:     PanelWidthConfig{LeftPercent: 50, RightPercent: 50},
			Skin:      "mc",
		},
		Input: InputConfig{
			Mouse:      MouseConfig{DoubleClickTimeout: metav1.Duration{Duration: 300 * time.Millisecond}},
			EscTimeout: metav1.Duration{Duration: time.Second},
		},
		Kubernetes: KubernetesConfig{
			Clusters:  ClustersConfig{TTL: metav1.Duration{Duration: 2 * time.Minute}},
			Discovery: DiscoveryConfig{Refresh: metav1.Duration{Duration: 30 * time.Second}},
		},
		Resources: ResourcesViewConfig{
			ShowNonEmptyOnly: true,
			Order:            OrderAlpha,
			Columns:          ColumnsModeNormal,
			PeekInterval:     metav1.Duration{Duration: 10 * time.Second},
			// Favorites empty by default so we can seed them from discovery's "all" category.
			Favorites: nil,
		},
		Objects:  ObjectsConfig{Order: ObjectsOrderName, Columns: ColumnsModeNormal, GroupByNamespace: true},
		Terminal: TerminalConfig{Follow: true, Mode: TerminalModeOverlay},
		NodeShell: NodeShellConfig{
			Enabled:     false,
			Image:       DefaultNodeShellImage,
			Namespace:   DefaultNodeShellNamespace,
			MaxDuration: metav1.Duration{Duration: DefaultNodeShellMaxDuration},
		},
		PortForward: PortForwardConfig{Address: DefaultPortForwardAddress},
		Overview:    DefaultOverview(),
		Timeline:    DefaultTimeline(),
		Commands: []CommandConfig{
			{
				Name:          "Top Nodes",
				Command:       "kubectl top nodes",
				Type:          CommandTypeGlobal,
				Location:      CommandLocationPanel,
				WatchInterval: metav1.Duration{Duration: 5 * time.Second},
				Interactive:   false,
				Debounce:      metav1.Duration{Duration: 500 * time.Millisecond},
				OnExit:        CommandExitKeepOpen,
			},
			{
				Name:        fmt.Sprintf("Shell (%s)", shellEnv()),
				Command:     "$SHELL",
				Type:        CommandTypeGlobal,
				Location:    CommandLocationPanel,
				Interactive: true,
				OnExit:      CommandExitRestore,
			},
			{
				Name:     "Top Pods",
				Command:  "kubectl top pods -n {{.Namespace}}",
				Type:     CommandTypeNamespace,
				Location: CommandLocationPanel,
				WatchInterval: metav1.Duration{
					Duration: 5 * time.Second,
				},
				Debounce: metav1.Duration{Duration: 500 * time.Millisecond},
				OnExit:   CommandExitKeepOpen,
			},
		},
	}
}

// DefaultPath returns the configuration file kc reads: ~/.kc/config.yaml.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating the home directory: %w", err)
	}
	return filepath.Join(home, ".kc", "config.yaml"), nil
}

func shellEnv() string {
	if val := os.Getenv("SHELL"); strings.TrimSpace(val) != "" {
		return val
	}
	return "sh"
}

// normalizeLoaded fills zero values and folds invalid values of a config
// read from a file to the values kc uses.
func normalizeLoaded(cfg *Config) {
	cfg.Layout.normalize()
	if cfg.Viewer.Theme == "" {
		cfg.Viewer.Theme = "dracula"
	}
	cfg.Viewer.Mode = strings.ToLower(cfg.Viewer.Mode)
	if cfg.Viewer.Mode != ViewerModeWrap {
		cfg.Viewer.Mode = ViewerModeScroll
	}
	if cfg.Panel.Scrolling.Horizontal.Step <= 0 {
		cfg.Panel.Scrolling.Horizontal.Step = 4
	}
	if cfg.Panel.Table.Mode != TableModeFit && cfg.Panel.Table.Mode != TableModeScroll {
		cfg.Panel.Table.Mode = TableModeScroll
	}
	normalizePanelWidth(&cfg.Panel.Width)
	if cfg.Terminal.Mode != TerminalModeCopy && cfg.Terminal.Mode != TerminalModeOverlay {
		cfg.Terminal.Mode = TerminalModeOverlay
	}
	if cfg.Kubernetes.Clusters.TTL.Duration == 0 {
		cfg.Kubernetes.Clusters.TTL = metav1.Duration{Duration: 2 * time.Minute}
	}
	if cfg.Kubernetes.Discovery.Refresh.Duration == 0 {
		cfg.Kubernetes.Discovery.Refresh = metav1.Duration{Duration: 30 * time.Second}
	}
	if cfg.Input.Mouse.DoubleClickTimeout.Duration == 0 {
		cfg.Input.Mouse.DoubleClickTimeout = metav1.Duration{Duration: 300 * time.Millisecond}
	}
	// Normalize resources settings
	switch cfg.Resources.Order {
	case OrderAlpha, OrderGroup, OrderFavorites:
	default:
		cfg.Resources.Order = OrderFavorites
	}
	if strings.EqualFold(cfg.Resources.Columns, ColumnsModeWide) {
		cfg.Resources.Columns = ColumnsModeWide
	} else {
		cfg.Resources.Columns = ColumnsModeNormal
	}
	cfg.Objects.Order = NormalizeObjectsOrder(cfg.Objects.Order)
	if strings.EqualFold(cfg.Objects.Columns, ColumnsModeWide) {
		cfg.Objects.Columns = ColumnsModeWide
	} else {
		cfg.Objects.Columns = ColumnsModeNormal
	}
	normalizeNodeShell(&cfg.NodeShell)
	cfg.PortForward.Address = strings.TrimSpace(cfg.PortForward.Address)
	if cfg.PortForward.Address == "" {
		cfg.PortForward.Address = DefaultPortForwardAddress
	}

	// Normalize commands
	for i := range cfg.Commands {
		cmd := &cfg.Commands[i]
		if cmd.Debounce.Duration == 0 {
			cmd.Debounce = metav1.Duration{Duration: 500 * time.Millisecond}
		}
		if cmd.OnExit == "" {
			cmd.OnExit = CommandExitKeepOpen
		}
		if cmd.Location == "" {
			cmd.Location = CommandLocationPanel
		}
	}
}

// normalizeForSave applies the canonical spelling of enumerated values
// before cfg is written to a file.
func normalizeForSave(cfg *Config) {
	// Enforce lower-case style names for consistency
	cfg.Viewer.Theme = strings.ToLower(cfg.Viewer.Theme)
	cfg.Viewer.Mode = strings.ToLower(cfg.Viewer.Mode)
	if cfg.Viewer.Mode != ViewerModeWrap {
		cfg.Viewer.Mode = ViewerModeScroll
	}
	// Normalize order value
	switch cfg.Resources.Order {
	case OrderAlpha, OrderGroup, OrderFavorites:
	default:
		cfg.Resources.Order = OrderFavorites
	}
	if strings.EqualFold(cfg.Resources.Columns, ColumnsModeWide) {
		cfg.Resources.Columns = ColumnsModeWide
	} else {
		cfg.Resources.Columns = ColumnsModeNormal
	}
	if cfg.Resources.PeekInterval.Duration <= 0 {
		cfg.Resources.PeekInterval = metav1.Duration{Duration: Default().Resources.PeekInterval.Duration}
	}
	cfg.Objects.Order = NormalizeObjectsOrder(cfg.Objects.Order)
	if strings.EqualFold(cfg.Objects.Columns, ColumnsModeWide) {
		cfg.Objects.Columns = ColumnsModeWide
	} else {
		cfg.Objects.Columns = ColumnsModeNormal
	}
	normalizePanelWidth(&cfg.Panel.Width)
	normalizeNodeShell(&cfg.NodeShell)
}

func normalizePanelWidth(width *PanelWidthConfig) {
	if width == nil {
		return
	}
	left := clampPercent(width.LeftPercent)
	right := clampPercent(width.RightPercent)

	switch {
	case left == 0 && right == 0:
		left = 50
		right = 50
	case left == 0 && right != 0:
		left = clampPercent(100 - right)
	case left != 0 && right == 0:
		right = clampPercent(100 - left)
	default:
		right = clampPercent(100 - left)
	}

	width.LeftPercent = clampPercent(left)
	width.RightPercent = clampPercent(right)
}

func clampPercent(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
