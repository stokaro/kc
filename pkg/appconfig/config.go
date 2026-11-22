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
}

// DefaultLogsTail is the default for viewer.logs.tail.
const DefaultLogsTail int64 = 200

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

type Config struct {
	Viewer     ViewerConfig        `json:"viewer"`
	Panel      PanelConfig         `json:"panel"`
	Input      InputConfig         `json:"input"`
	Kubernetes KubernetesConfig    `json:"kubernetes"`
	Resources  ResourcesViewConfig `json:"resources"`
	Objects    ObjectsConfig       `json:"objects"`
	Terminal   TerminalConfig      `json:"terminal"`
	Commands   []CommandConfig     `json:"commands"`
}

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
}

// ObjectsConfig controls object-list specific options.
type ObjectsConfig struct {
	// Order controls ordering within object lists. Valid values conform to ObjectsOrder* constants (Name, NameDesc, Creation, CreationDesc).
	Order string `json:"order"`
	// Columns controls which columns are shown. Valid values are ColumnsModeNormal and ColumnsModeWide.
	Columns string `json:"columns"`
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
		Viewer: ViewerConfig{
			Theme: "dracula",
			Mode:  ViewerModeScroll,
			Logs:  LogsViewerConfig{Tail: DefaultLogsTail},
		},
		Panel: PanelConfig{
			Scrolling: ScrollingConfig{Horizontal: HorizontalConfig{Step: 4}},
			Table:     TableConfig{Mode: TableModeScroll},
			Width:     PanelWidthConfig{LeftPercent: 50, RightPercent: 50},
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
		Objects:  ObjectsConfig{Order: ObjectsOrderName, Columns: ColumnsModeNormal},
		Terminal: TerminalConfig{Follow: true, Mode: TerminalModeOverlay},
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
	switch {
	case strings.EqualFold(cfg.Objects.Order, ObjectsOrderName):
		cfg.Objects.Order = ObjectsOrderName
	case strings.EqualFold(cfg.Objects.Order, ObjectsOrderNameDesc):
		cfg.Objects.Order = ObjectsOrderNameDesc
	case strings.EqualFold(cfg.Objects.Order, ObjectsOrderCreation):
		cfg.Objects.Order = ObjectsOrderCreation
	case strings.EqualFold(cfg.Objects.Order, ObjectsOrderCreationDesc):
		cfg.Objects.Order = ObjectsOrderCreationDesc
	default:
		cfg.Objects.Order = ObjectsOrderName
	}
	if strings.EqualFold(cfg.Objects.Columns, ColumnsModeWide) {
		cfg.Objects.Columns = ColumnsModeWide
	} else {
		cfg.Objects.Columns = ColumnsModeNormal
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
	switch {
	case strings.EqualFold(cfg.Objects.Order, ObjectsOrderName):
		cfg.Objects.Order = ObjectsOrderName
	case strings.EqualFold(cfg.Objects.Order, ObjectsOrderNameDesc):
		cfg.Objects.Order = ObjectsOrderNameDesc
	case strings.EqualFold(cfg.Objects.Order, ObjectsOrderCreation):
		cfg.Objects.Order = ObjectsOrderCreation
	case strings.EqualFold(cfg.Objects.Order, ObjectsOrderCreationDesc):
		cfg.Objects.Order = ObjectsOrderCreationDesc
	default:
		cfg.Objects.Order = ObjectsOrderName
	}
	if strings.EqualFold(cfg.Objects.Columns, ColumnsModeWide) {
		cfg.Objects.Columns = ColumnsModeWide
	} else {
		cfg.Objects.Columns = ColumnsModeNormal
	}
	normalizePanelWidth(&cfg.Panel.Width)
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
