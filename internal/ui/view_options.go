package ui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stokaro/kc/internal/models"
	uistyles "github.com/stokaro/kc/internal/ui/styles"
	"github.com/stokaro/kc/pkg/appconfig"
)

// ViewOptionsCommittedMsg aggregates the selections made in the sectioned view
// options dialog.
type ViewOptionsCommittedMsg struct {
	PanelIndex        int
	PanelMode         PanelViewMode
	SetPanelMode      bool
	PanelWidthPercent int
	SetPanelWidth     bool
	TableMode         string
	HasTableMode      bool
	Resources         *ViewOptionsResourcesPayload
	Objects           *ViewOptionsObjectsPayload
	Command           *ViewOptionsCommandPayload
	Viewer            *ViewOptionsViewerPayload
	Logs              *ViewOptionsLogsPayload
	SaveDefault       bool
	Accept            bool
	Close             bool
}

// ViewOptionsResourcesPayload carries Resources section toggles.
type ViewOptionsResourcesPayload struct {
	HasInclude      bool
	IncludeEmpty    bool
	HasOrder        bool
	Order           string
	ShowTableOption bool
}

// ViewOptionsObjectsPayload carries Objects section toggles.
type ViewOptionsObjectsPayload struct {
	ShowTableOption bool
	Columns         string
	Order           string
	HasFaultsOnly   bool
	FaultsOnly      bool
}

// ViewOptionsCommandPayload carries command-specific options.
type ViewOptionsCommandPayload struct {
	WatchInterval time.Duration
}

// ViewOptionsViewerPayload carries viewer-specific options.
type ViewOptionsViewerPayload struct {
	Theme    string
	WrapMode string
}

// ViewOptionsLogsPayload carries the logs viewer stream options.
type ViewOptionsLogsPayload struct {
	TailLines    int64
	SinceSeconds int64
	Timestamps   bool
	Previous     bool
	// Container is the chosen value of the Container row, or the
	// configured Container when the dialog has no such row.
	Container string
}

// ViewOptionsConfig seeds the sectioned view options model.
type ViewOptionsConfig struct {
	PanelIndex       int
	SkipPanelSection bool

	PanelModes        []PanelViewMode
	ActivePanelMode   PanelViewMode
	PanelWidthPercent int

	TableMode string

	Resources *ViewOptionsResourcesConfig
	Objects   *ViewOptionsObjectsConfig
	Command   *ViewOptionsCommandConfig
	Viewer    *ViewOptionsViewerConfig
	Logs      *ViewOptionsLogsConfig
}

// ViewOptionsLogsConfig configures the Logs section. TailLines -1 means the
// whole log, SinceSeconds 0 no limit.
type ViewOptionsLogsConfig struct {
	TailLines    int64
	SinceSeconds int64
	Timestamps   bool
	Previous     bool
	// Containers are the choices of the Container row; the row is shown
	// when there are two or more. Container is the current value.
	Containers []ViewOptionsChoice
	Container  string
}

// ViewOptionsChoice is one value of a choice row and its label.
type ViewOptionsChoice struct {
	Value string
	Label string
}

// ViewOptionsResourcesConfig configures the Resources section.
type ViewOptionsResourcesConfig struct {
	ShowInclude   bool
	IncludeEmpty  bool
	ShowOrder     bool
	Order         string
	ShowTableMode bool
}

// ViewOptionsObjectsConfig configures the Objects section.
type ViewOptionsObjectsConfig struct {
	ShowTableMode bool
	Columns       string
	// Order is the order the list is sorted by, e.g. "-age".
	Order string
	// SortKeys and SortTitles list the columns "Sort by" offers. Without
	// them it offers Name only.
	SortKeys   []string
	SortTitles []string
	// ShowFaultsOnly adds the "Faults only" toggle; it is shown for
	// folders that can filter rows.
	ShowFaultsOnly bool
	FaultsOnly     bool
}

// ViewOptionsCommandConfig configures the Command section.
type ViewOptionsCommandConfig struct {
	WatchInterval time.Duration
}

// ViewOptionsViewerConfig configures the Viewer section.
type ViewOptionsViewerConfig struct {
	ThemeNames []string
	Theme      string
	WrapMode   string
}

var commandWatchDurations = []time.Duration{
	0,
	time.Second,
	2 * time.Second,
	3 * time.Second,
	4 * time.Second,
	5 * time.Second,
	6 * time.Second,
	7 * time.Second,
	8 * time.Second,
	9 * time.Second,
	10 * time.Second,
	20 * time.Second,
	30 * time.Second,
	40 * time.Second,
	50 * time.Second,
	time.Minute,
}

var commandWatchLabels = []string{
	"Off",
	"1s",
	"2s",
	"3s",
	"4s",
	"5s",
	"6s",
	"7s",
	"8s",
	"9s",
	"10s",
	"20s",
	"30s",
	"40s",
	"50s",
	"1m",
}

// logsTailChoices are the tail options; -1 fetches the whole log.
var logsTailChoices = []int64{50, 100, 200, 500, 1000, 5000, -1}

// logsSinceChoices are the since windows in seconds; 0 means no limit.
var logsSinceChoices = []int64{0, 60, 300, 900, 1800, 3600}

// withChoice returns choices with current added in order when it is
// missing, so a configured value outside the list shows and survives a
// commit. Negative values sort last.
func withChoice(choices []int64, current int64) ([]int64, int) {
	for i, c := range choices {
		if c == current {
			return choices, i
		}
	}
	less := func(a, b int64) bool {
		if a < 0 {
			return false
		}
		if b < 0 {
			return true
		}
		return a < b
	}
	out := make([]int64, 0, len(choices)+1)
	idx := -1
	for _, c := range choices {
		if idx < 0 && less(current, c) {
			idx = len(out)
			out = append(out, current)
		}
		out = append(out, c)
	}
	if idx < 0 {
		idx = len(out)
		out = append(out, current)
	}
	return out, idx
}

func cycleIndex(idx, n, delta int) int {
	if n == 0 {
		return 0
	}
	if delta < 0 {
		return (idx - 1 + n) % n
	}
	return (idx + 1) % n
}

func onOff(on bool) string {
	if on {
		return "On"
	}
	return "Off"
}

type viewOptionEntryKind int

const (
	viewOptionEntryOption viewOptionEntryKind = iota
	viewOptionEntrySection
	viewOptionEntrySpacer
)

type viewOptionKind int

const (
	viewOptionPanelMode viewOptionKind = iota
	viewOptionPanelWidth
	viewOptionIncludeEmpty
	viewOptionResourceOrder
	viewOptionResourceTableMode
	viewOptionObjectTableMode
	viewOptionObjectColumns
	viewOptionObjectOrder
	viewOptionObjectReverse
	viewOptionCommandWatch
	viewOptionViewerTheme
	viewOptionViewerWrapMode
	viewOptionLogsTail
	viewOptionLogsSince
	viewOptionLogsTimestamps
	viewOptionLogsPrevious
	viewOptionLogsContainer
	viewOptionObjectFaultsOnly
)

type viewOptionEntry struct {
	kind   viewOptionEntryKind
	title  string
	option viewOptionKind
}

// ViewOptionsModel renders the sectioned View Options dialog, covering general
// panel mode settings plus contextual Resources/Objects options.
type ViewOptionsModel struct {
	panelIdx int

	width  int
	height int

	entries []viewOptionEntry
	focus   int
	scroll  int

	panelModes      []PanelViewMode
	panelModeIndex  int
	hasPanelWidth   bool
	panelWidthIndex int

	tableModeIndex int
	hasTableMode   bool

	resources struct {
		enabled    bool
		hasInclude bool
		include    bool
		hasOrder   bool
		orderIndex int
		showTable  bool
	}

	objects struct {
		enabled    bool
		showTable  bool
		columnsIdx int
		sortKeys   []string
		sortTitles []string
		sortIdx    int
		reverse    bool
		hasFaults  bool
		faults     bool
	}

	viewer struct {
		enabled    bool
		themeNames []string
		themeIdx   int
		wrapMode   string
	}

	command struct {
		enabled  bool
		watchIdx int
	}

	logs struct {
		enabled    bool
		tails      []int64
		tailIdx    int
		sinces     []int64
		sinceIdx   int
		timestamps bool
		previous   bool
		containers []ViewOptionsChoice
		// containerIdx indexes containers; container is used without them.
		containerIdx int
		container    string
	}
}

// NewViewOptionsModel constructs a sectioned view options dialog model.
//
//nolint:gocognit,gocyclo // builds each dialog section from its optional config in order
func NewViewOptionsModel(cfg ViewOptionsConfig) *ViewOptionsModel {
	model := &ViewOptionsModel{
		panelIdx:     cfg.PanelIndex,
		hasTableMode: cfg.Resources != nil && cfg.Resources.ShowTableMode || cfg.Objects != nil && cfg.Objects.ShowTableMode,
	}
	if !cfg.SkipPanelSection {
		model.panelModes = append([]PanelViewMode(nil), cfg.PanelModes...)
		if len(model.panelModes) == 0 {
			model.panelModes = []PanelViewMode{PanelModeList}
		}
		model.panelModeIndex = indexOfPanelMode(model.panelModes, cfg.ActivePanelMode)
		model.entries = append(model.entries, viewOptionEntry{
			kind:   viewOptionEntryOption,
			option: viewOptionPanelMode,
		})
		model.hasPanelWidth = cfg.PanelWidthPercent >= 0
		if model.hasPanelWidth {
			model.panelWidthIndex = viewWidthIndexFor(cfg.PanelWidthPercent)
			model.entries = append(model.entries, viewOptionEntry{
				kind:   viewOptionEntryOption,
				option: viewOptionPanelWidth,
			})
		}
	} else {
		model.panelModes = append([]PanelViewMode(nil), cfg.PanelModes...)
		model.panelModeIndex = indexOfPanelMode(model.panelModes, cfg.ActivePanelMode)
		model.hasPanelWidth = false
	}
	model.tableModeIndex = tableModeIndexFor(cfg.TableMode)

	if cfg.Resources != nil {
		model.resources.enabled = true
		model.resources.hasInclude = cfg.Resources.ShowInclude
		model.resources.include = cfg.Resources.IncludeEmpty
		model.resources.hasOrder = cfg.Resources.ShowOrder
		model.resources.orderIndex = resourceOrderIndexFor(cfg.Resources.Order)
		model.resources.showTable = cfg.Resources.ShowTableMode

		model.appendSectionSpacerIfNeeded()
		model.entries = append(model.entries, viewOptionEntry{
			kind:  viewOptionEntrySection,
			title: "Resources:",
		})
		if model.resources.hasInclude {
			model.entries = append(model.entries, viewOptionEntry{
				kind:   viewOptionEntryOption,
				option: viewOptionIncludeEmpty,
			})
		}
		if model.resources.hasOrder {
			model.entries = append(model.entries, viewOptionEntry{
				kind:   viewOptionEntryOption,
				option: viewOptionResourceOrder,
			})
		}
		if model.resources.showTable {
			model.entries = append(model.entries, viewOptionEntry{
				kind:   viewOptionEntryOption,
				option: viewOptionResourceTableMode,
			})
		}
	}

	if cfg.Objects != nil {
		model.objects.enabled = true
		model.objects.showTable = cfg.Objects.ShowTableMode
		model.objects.columnsIdx = objectColumnsIndexFor(cfg.Objects.Columns)
		choice := objectSortChoices(cfg.Objects.SortKeys, cfg.Objects.SortTitles, cfg.Objects.Order)
		model.objects.sortKeys, model.objects.sortTitles = choice.keys, choice.labels
		model.objects.sortIdx, model.objects.reverse = choice.idx, choice.desc
		model.objects.hasFaults = cfg.Objects.ShowFaultsOnly
		model.objects.faults = cfg.Objects.FaultsOnly

		model.appendSectionSpacerIfNeeded()
		model.entries = append(model.entries, viewOptionEntry{
			kind:  viewOptionEntrySection,
			title: "Objects:",
		})
		if model.objects.showTable {
			model.entries = append(model.entries, viewOptionEntry{
				kind:   viewOptionEntryOption,
				option: viewOptionObjectTableMode,
			})
		}
		model.entries = append(model.entries,
			viewOptionEntry{kind: viewOptionEntryOption, option: viewOptionObjectColumns},
			viewOptionEntry{kind: viewOptionEntryOption, option: viewOptionObjectOrder},
			viewOptionEntry{kind: viewOptionEntryOption, option: viewOptionObjectReverse},
		)
		if model.objects.hasFaults {
			model.entries = append(model.entries, viewOptionEntry{kind: viewOptionEntryOption, option: viewOptionObjectFaultsOnly})
		}
	}

	if cfg.Command != nil {
		model.command.enabled = true
		model.command.watchIdx = commandWatchIndexFor(cfg.Command.WatchInterval)

		model.appendSectionSpacerIfNeeded()
		model.entries = append(model.entries, viewOptionEntry{
			kind:  viewOptionEntrySection,
			title: "Command:",
		})
		model.entries = append(model.entries, viewOptionEntry{
			kind:   viewOptionEntryOption,
			option: viewOptionCommandWatch,
		})
	}

	if cfg.Viewer != nil {
		model.viewer.enabled = true
		model.viewer.themeNames = append(make([]string, 0), cfg.Viewer.ThemeNames...)
		if len(model.viewer.themeNames) == 0 {
			model.viewer.themeNames = []string{cfg.Viewer.Theme}
		}
		model.viewer.themeIdx = viewerThemeIndexFor(model.viewer.themeNames, cfg.Viewer.Theme)
		model.viewer.wrapMode = cfg.Viewer.WrapMode
		if model.viewer.wrapMode != appconfig.ViewerModeWrap {
			model.viewer.wrapMode = appconfig.ViewerModeScroll
		}
		model.appendSectionSpacerIfNeeded()
		model.entries = append(model.entries, viewOptionEntry{
			kind:  viewOptionEntrySection,
			title: "Viewer:",
		})
		model.entries = append(model.entries, viewOptionEntry{
			kind:   viewOptionEntryOption,
			option: viewOptionViewerTheme,
		})
		model.entries = append(model.entries, viewOptionEntry{
			kind:   viewOptionEntryOption,
			option: viewOptionViewerWrapMode,
		})
	}

	if cfg.Logs != nil {
		model.logs.enabled = true
		tail := cfg.Logs.TailLines
		if tail < 0 {
			tail = -1
		}
		model.logs.tails, model.logs.tailIdx = withChoice(logsTailChoices, tail)
		model.logs.sinces, model.logs.sinceIdx = withChoice(logsSinceChoices, nonNegative(cfg.Logs.SinceSeconds))
		model.logs.timestamps = cfg.Logs.Timestamps
		model.logs.previous = cfg.Logs.Previous
		model.logs.container = cfg.Logs.Container
		model.appendSectionSpacerIfNeeded()
		model.entries = append(model.entries, viewOptionEntry{kind: viewOptionEntrySection, title: "Logs:"})
		if len(cfg.Logs.Containers) > 1 {
			model.logs.containers = append([]ViewOptionsChoice(nil), cfg.Logs.Containers...)
			for i, c := range model.logs.containers {
				if c.Value == cfg.Logs.Container {
					model.logs.containerIdx = i
				}
			}
			model.entries = append(model.entries, viewOptionEntry{kind: viewOptionEntryOption, option: viewOptionLogsContainer})
		}
		model.entries = append(model.entries,
			viewOptionEntry{kind: viewOptionEntryOption, option: viewOptionLogsTail},
			viewOptionEntry{kind: viewOptionEntryOption, option: viewOptionLogsSince},
			viewOptionEntry{kind: viewOptionEntryOption, option: viewOptionLogsTimestamps},
			viewOptionEntry{kind: viewOptionEntryOption, option: viewOptionLogsPrevious},
		)
	}

	if len(model.entries) == 0 {
		// Should never happen, but keep at least the panel mode row.
		model.entries = append(model.entries, viewOptionEntry{
			kind:   viewOptionEntryOption,
			option: viewOptionPanelMode,
		})
	}

	model.focus = model.firstOptionIndex()
	model.scroll = 0

	return model
}

func (m *ViewOptionsModel) appendSectionSpacerIfNeeded() {
	if len(m.entries) == 0 {
		return
	}
	last := m.entries[len(m.entries)-1]
	if last.kind == viewOptionEntrySpacer {
		return
	}
	if last.kind == viewOptionEntryOption {
		m.entries = append(m.entries, viewOptionEntry{kind: viewOptionEntrySpacer})
	}
}

func indexOfPanelMode(modes []PanelViewMode, mode PanelViewMode) int {
	for i, candidate := range modes {
		if candidate == mode {
			return i
		}
	}
	return 0
}

func tableModeIndexFor(mode string) int {
	for i, key := range tableModeKeys {
		if strings.EqualFold(key, mode) {
			return i
		}
	}
	return 0
}

func resourceOrderIndexFor(order string) int {
	for i, key := range orderKeys {
		if strings.EqualFold(key, order) {
			return i
		}
	}
	return 0
}

func viewWidthIndexFor(percent int) int {
	options := panelWidthPercentOptions
	if len(options) == 0 {
		return 0
	}
	best := 0
	bestDelta := absInt(options[0] - percent)
	for i := 1; i < len(options); i++ {
		delta := absInt(options[i] - percent)
		if delta < bestDelta {
			best = i
			bestDelta = delta
		}
	}
	return best
}

func viewerThemeIndexFor(names []string, current string) int {
	if len(names) == 0 {
		return 0
	}
	cur := strings.ToLower(current)
	for i, name := range names {
		if strings.ToLower(name) == cur {
			return i
		}
	}
	return 0
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func objectColumnsIndexFor(columns string) int {
	for i, key := range objColumnsKeys {
		if strings.EqualFold(key, columns) {
			return i
		}
	}
	return 0
}

// objectSortChoices returns the columns "Sort by" cycles through, the one
// order sorts by, and whether order is descending, like mc's Sort order
// dialog with its Reverse box. A key the columns lack (a wide column in
// normal mode) is kept as an extra choice, so the dialog does not change
// the order unasked.
func objectSortChoices(keys, titles []string, order string) objectSortChoice {
	order = appconfig.NormalizeObjectsOrder(order)
	desc := strings.HasPrefix(order, "-")
	key := strings.TrimPrefix(order, "-")
	if key == appconfig.ObjectsOrderCreation {
		key, desc = "age", !desc
	}
	if len(keys) == 0 {
		keys, titles = []string{appconfig.ObjectsOrderName}, []string{"Name"}
	}
	keys = append([]string(nil), keys...)
	labels := make([]string, len(keys))
	for i := range keys {
		labels[i] = keys[i]
		if i < len(titles) && titles[i] != "" {
			labels[i] = titles[i]
		}
	}
	idx := slices.Index(keys, key)
	if idx < 0 {
		keys = append(keys, key)
		labels = append(labels, key)
		idx = len(keys) - 1
	}
	return objectSortChoice{keys: keys, labels: labels, idx: idx, desc: desc}
}

// objectSortChoice is what objectSortChoices returns: the sort keys and
// their labels, the index of the current key, and whether the order is
// descending.
type objectSortChoice struct {
	keys, labels []string
	idx          int
	desc         bool
}

// objectOrder composes the order the Objects section selects.
func (m *ViewOptionsModel) objectOrder() string {
	key := m.objects.sortKeys[m.objects.sortIdx]
	if m.objects.reverse {
		return "-" + key
	}
	return key
}

func commandWatchIndexFor(interval time.Duration) int {
	if len(commandWatchDurations) == 0 {
		return 0
	}
	best := 0
	bestDelta := absDuration(commandWatchDurations[0] - interval)
	for i := 1; i < len(commandWatchDurations); i++ {
		if delta := absDuration(commandWatchDurations[i] - interval); delta < bestDelta {
			best = i
			bestDelta = delta
		}
	}
	return best
}

// Init implements tea.Model.
func (m *ViewOptionsModel) Init() tea.Cmd { return nil }

// SetDimensions updates the available width and height.
func (m *ViewOptionsModel) SetDimensions(w, h int) {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	m.width = w
	m.height = h
	m.ensureFocusVisible()
}

// ContentHeight returns the number of content rows required.
func (m *ViewOptionsModel) ContentHeight() int { return len(m.entries) }

func (m *ViewOptionsModel) firstOptionIndex() int {
	for i, entry := range m.entries {
		if entry.kind == viewOptionEntryOption {
			return i
		}
	}
	return 0
}

func (m *ViewOptionsModel) currentEntry() (viewOptionEntry, bool) {
	if m.focus < 0 || m.focus >= len(m.entries) {
		return viewOptionEntry{}, false
	}
	return m.entries[m.focus], true
}

func (m *ViewOptionsModel) ensureFocusVisible() {
	if m.height <= 0 {
		return
	}
	if m.focus < m.scroll {
		m.scroll = m.focus
	} else if m.focus >= m.scroll+m.height {
		m.scroll = m.focus - m.height + 1
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
	maxScroll := max(len(m.entries)-m.height, 0)
	if m.scroll > maxScroll {
		m.scroll = maxScroll
	}
}

func (m *ViewOptionsModel) moveFocus(delta int) {
	if len(m.entries) == 0 {
		return
	}
	idx := m.focus
	for {
		idx += delta
		if idx < 0 {
			idx = 0
			break
		}
		if idx >= len(m.entries) {
			idx = len(m.entries) - 1
			break
		}
		if m.entries[idx].kind == viewOptionEntryOption {
			break
		}
	}
	m.focus = idx
	m.ensureFocusVisible()
}

// Update handles key navigation and edits.
func (m *ViewOptionsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if step := wheelStep(msg); step != 0 {
		m.moveFocus(step)
		return m, nil
	}
	if t, ok := msg.(tea.KeyMsg); ok {
		switch t.String() {
		case "up", "k":
			m.moveFocus(-1)
		case "down", "j":
			m.moveFocus(1)
		case "left":
			m.adjustCurrent(-1)
		case "right":
			m.adjustCurrent(1)
		case " ", "space":
			m.adjustCurrent(0)
		case "ctrl+s":
			return m, func() tea.Msg {
				return m.commit(false, true)
			}
		case "enter":
			return m, func() tea.Msg {
				return m.commit(true, false)
			}
		}
	}
	return m, nil
}

//nolint:gocognit,gocyclo // one case per option, each cycling its own value list
func (m *ViewOptionsModel) adjustCurrent(delta int) {
	entry, ok := m.currentEntry()
	if !ok || entry.kind != viewOptionEntryOption {
		return
	}
	switch entry.option {
	case viewOptionPanelMode:
		if len(m.panelModes) == 0 {
			return
		}
		if delta < 0 {
			m.panelModeIndex = (m.panelModeIndex - 1 + len(m.panelModes)) % len(m.panelModes)
		} else {
			m.panelModeIndex = (m.panelModeIndex + 1) % len(m.panelModes)
		}
	case viewOptionPanelWidth:
		options := panelWidthPercentOptions
		if !m.hasPanelWidth || len(options) == 0 {
			return
		}
		switch {
		case delta < 0:
			if m.panelWidthIndex > 0 {
				m.panelWidthIndex--
			}
		case delta > 0:
			if m.panelWidthIndex < len(options)-1 {
				m.panelWidthIndex++
			}
		default:
			if m.panelWidthIndex < len(options)-1 {
				m.panelWidthIndex++
			}
		}
	case viewOptionIncludeEmpty:
		m.resources.include = !m.resources.include
	case viewOptionResourceOrder:
		if len(orderKeys) == 0 {
			return
		}
		if delta < 0 {
			m.resources.orderIndex--
			if m.resources.orderIndex < 0 {
				m.resources.orderIndex = len(orderKeys) - 1
			}
		} else {
			m.resources.orderIndex = (m.resources.orderIndex + 1) % len(orderKeys)
		}
	case viewOptionResourceTableMode, viewOptionObjectTableMode:
		if len(tableModeKeys) == 0 {
			return
		}
		if delta < 0 {
			m.tableModeIndex--
			if m.tableModeIndex < 0 {
				m.tableModeIndex = len(tableModeKeys) - 1
			}
		} else {
			m.tableModeIndex = (m.tableModeIndex + 1) % len(tableModeKeys)
		}
	case viewOptionObjectColumns:
		if len(objColumnsKeys) == 0 {
			return
		}
		m.objects.columnsIdx++
		if m.objects.columnsIdx >= len(objColumnsKeys) {
			m.objects.columnsIdx = 0
		}
	case viewOptionObjectOrder:
		n := len(m.objects.sortKeys)
		if n == 0 {
			return
		}
		if delta < 0 {
			m.objects.sortIdx = (m.objects.sortIdx - 1 + n) % n
		} else {
			m.objects.sortIdx = (m.objects.sortIdx + 1) % n
		}
	case viewOptionObjectReverse:
		m.objects.reverse = !m.objects.reverse
	case viewOptionObjectFaultsOnly:
		m.objects.faults = !m.objects.faults
	case viewOptionViewerTheme:
		if !m.viewer.enabled || len(m.viewer.themeNames) == 0 {
			return
		}
		if delta < 0 {
			m.viewer.themeIdx--
			if m.viewer.themeIdx < 0 {
				m.viewer.themeIdx = len(m.viewer.themeNames) - 1
			}
		} else {
			m.viewer.themeIdx = (m.viewer.themeIdx + 1) % len(m.viewer.themeNames)
		}
	case viewOptionViewerWrapMode:
		if !m.viewer.enabled {
			return
		}
		if m.viewer.wrapMode == appconfig.ViewerModeWrap {
			m.viewer.wrapMode = appconfig.ViewerModeScroll
		} else {
			m.viewer.wrapMode = appconfig.ViewerModeWrap
		}
	case viewOptionLogsTail:
		m.logs.tailIdx = cycleIndex(m.logs.tailIdx, len(m.logs.tails), delta)
	case viewOptionLogsSince:
		m.logs.sinceIdx = cycleIndex(m.logs.sinceIdx, len(m.logs.sinces), delta)
	case viewOptionLogsTimestamps:
		m.logs.timestamps = !m.logs.timestamps
	case viewOptionLogsPrevious:
		m.logs.previous = !m.logs.previous
	case viewOptionLogsContainer:
		m.logs.containerIdx = cycleIndex(m.logs.containerIdx, len(m.logs.containers), delta)
	case viewOptionCommandWatch:
		if len(commandWatchDurations) == 0 {
			return
		}
		if delta < 0 {
			m.command.watchIdx--
			if m.command.watchIdx < 0 {
				m.command.watchIdx = len(commandWatchDurations) - 1
			}
		} else {
			m.command.watchIdx = (m.command.watchIdx + 1) % len(commandWatchDurations)
		}
	}
}

func (m *ViewOptionsModel) commit(accept, save bool) ViewOptionsCommittedMsg {
	panelMode := PanelModeList
	setPanelMode := false
	if len(m.panelModes) > 0 {
		panelMode = m.panelModes[m.panelModeIndex]
		setPanelMode = true
	}
	panelWidthPercent := 0
	if m.hasPanelWidth {
		options := panelWidthPercentOptions
		if len(options) > 0 {
			panelWidthPercent = options[m.panelWidthIndex]
		}
	}
	tableMode := "scroll"
	if len(tableModeKeys) > 0 {
		tableMode = tableModeKeys[m.tableModeIndex]
	}
	msg := ViewOptionsCommittedMsg{
		PanelIndex:        m.panelIdx,
		SetPanelMode:      setPanelMode,
		PanelMode:         panelMode,
		SetPanelWidth:     m.hasPanelWidth,
		PanelWidthPercent: panelWidthPercent,
		TableMode:         tableMode,
		HasTableMode:      m.hasTableMode,
		SaveDefault:       save,
		Accept:            accept,
		Close:             accept,
	}
	if !save {
		// Save keeps the dialog open in parity with previous behaviour.
		msg.Close = accept
	}
	if m.resources.enabled {
		msg.Resources = &ViewOptionsResourcesPayload{
			HasInclude:      m.resources.hasInclude,
			IncludeEmpty:    m.resources.include,
			HasOrder:        m.resources.hasOrder,
			Order:           orderKeys[m.resources.orderIndex],
			ShowTableOption: m.resources.showTable,
		}
	}
	if m.objects.enabled {
		msg.Objects = &ViewOptionsObjectsPayload{
			ShowTableOption: m.objects.showTable,
			Columns:         objColumnsKeys[m.objects.columnsIdx],
			Order:           m.objectOrder(),
			HasFaultsOnly:   m.objects.hasFaults,
			FaultsOnly:      m.objects.faults,
		}
	}
	if m.viewer.enabled {
		theme := ""
		if len(m.viewer.themeNames) > 0 {
			theme = m.viewer.themeNames[m.viewer.themeIdx]
		}
		mode := appconfig.ViewerModeScroll
		if strings.EqualFold(m.viewer.wrapMode, appconfig.ViewerModeWrap) {
			mode = appconfig.ViewerModeWrap
		}
		msg.Viewer = &ViewOptionsViewerPayload{
			Theme:    theme,
			WrapMode: mode,
		}
	}
	if m.logs.enabled {
		msg.Logs = &ViewOptionsLogsPayload{
			TailLines:    m.logs.tails[m.logs.tailIdx],
			SinceSeconds: m.logs.sinces[m.logs.sinceIdx],
			Timestamps:   m.logs.timestamps,
			Previous:     m.logs.previous,
			Container:    m.logs.container,
		}
		if len(m.logs.containers) > 0 {
			msg.Logs.Container = m.logs.containers[m.logs.containerIdx].Value
		}
	}
	if m.command.enabled {
		interval := time.Duration(0)
		if len(commandWatchDurations) > 0 && m.command.watchIdx >= 0 && m.command.watchIdx < len(commandWatchDurations) {
			interval = commandWatchDurations[m.command.watchIdx]
		}
		msg.Command = &ViewOptionsCommandPayload{
			WatchInterval: interval,
		}
	}
	return msg
}

//nolint:gocyclo // one short case per option
func (m *ViewOptionsModel) optionLabelAndValue(opt viewOptionKind) (label, value string) {
	switch opt {
	case viewOptionPanelMode:
		return "Panel mode", modeLabel(m.panelModes[m.panelModeIndex])
	case viewOptionPanelWidth:
		options := panelWidthPercentOptions
		if !m.hasPanelWidth || len(options) == 0 {
			return "Panel width", ""
		}
		return "Panel width", fmt.Sprintf("%d%%", options[m.panelWidthIndex])
	case viewOptionIncludeEmpty:
		val := "No"
		if m.resources.include {
			val = "Yes"
		}
		return "Include empty", val
	case viewOptionResourceOrder:
		return "Order", orderLabels[m.resources.orderIndex]
	case viewOptionResourceTableMode, viewOptionObjectTableMode:
		return "Table mode", tableModeLabels[m.tableModeIndex]
	case viewOptionObjectColumns:
		return "Columns", objColumnsLabels[m.objects.columnsIdx]
	case viewOptionObjectOrder:
		return "Sort by", m.objects.sortTitles[m.objects.sortIdx]
	case viewOptionObjectReverse:
		if m.objects.reverse {
			return "Reverse", "Yes"
		}
		return "Reverse", "No"
	case viewOptionObjectFaultsOnly:
		val := "No"
		if m.objects.faults {
			val = "Yes"
		}
		return "Faults only", val
	case viewOptionCommandWatch:
		if len(commandWatchLabels) == 0 {
			return "Watch", "-"
		}
		return "Watch", commandWatchLabels[m.command.watchIdx]
	case viewOptionViewerTheme:
		if len(m.viewer.themeNames) == 0 {
			return "Theme", "-"
		}
		return "Theme", m.viewer.themeNames[m.viewer.themeIdx]
	case viewOptionViewerWrapMode:
		val := "Scroll"
		if strings.EqualFold(m.viewer.wrapMode, appconfig.ViewerModeWrap) {
			val = "Wrap"
		}
		return "Line mode", val
	case viewOptionLogsTail:
		if tail := m.logs.tails[m.logs.tailIdx]; tail > 0 {
			return "Tail lines", fmt.Sprintf("%d", tail)
		}
		return "Tail lines", "All"
	case viewOptionLogsSince:
		if since := models.FormatLogsSince(m.logs.sinces[m.logs.sinceIdx]); since != "" {
			return "Since", since
		}
		return "Since", "Off"
	case viewOptionLogsTimestamps:
		return "Timestamps", onOff(m.logs.timestamps)
	case viewOptionLogsPrevious:
		return "Previous", onOff(m.logs.previous)
	case viewOptionLogsContainer:
		if len(m.logs.containers) == 0 {
			return "Container", "-"
		}
		return "Container", m.logs.containers[m.logs.containerIdx].Label
	default:
		return "", ""
	}
}

func (m *ViewOptionsModel) maxLabelWidth() int {
	maxWidth := 0
	for _, entry := range m.entries {
		if entry.kind != viewOptionEntryOption {
			continue
		}
		label, _ := m.optionLabelAndValue(entry.option)
		if w := lipgloss.Width(label); w > maxWidth {
			maxWidth = w
		}
	}
	return maxWidth
}

// View renders the dialog body (without frame).
func (m *ViewOptionsModel) View() tea.View {
	if m.height <= 0 {
		m.height = len(m.entries)
		if m.height == 0 {
			m.height = 1
		}
	}
	rowStyle := lipgloss.NewStyle().
		Background(lipgloss.Color(uistyles.ColorModalBg)).
		Foreground(lipgloss.Color(uistyles.ColorModalFg))
	focusStyle := lipgloss.NewStyle().
		Background(lipgloss.Color(uistyles.ColorModalSelBg)).
		Foreground(lipgloss.Color(uistyles.ColorModalFg)).
		Bold(true)
	sectionStyle := lipgloss.NewStyle().
		Background(lipgloss.Color(uistyles.ColorModalBg)).
		Foreground(lipgloss.Color(uistyles.ColorModalFg)).
		Bold(true)

	maxLabel := m.maxLabelWidth()
	lines := make([]string, 0, m.height)
	for i := 0; i < m.height; i++ {
		idx := m.scroll + i
		if idx < 0 || idx >= len(m.entries) {
			lines = append(lines, rowStyle.Width(m.width).Render(""))
			continue
		}
		entry := m.entries[idx]
		switch entry.kind {
		case viewOptionEntrySpacer:
			lines = append(lines, rowStyle.Width(m.width).Render(""))
		case viewOptionEntrySection:
			lines = append(lines, sectionStyle.Width(m.width).Render(entry.title))
		case viewOptionEntryOption:
			label, value := m.optionLabelAndValue(entry.option)
			marker := " "
			st := rowStyle
			if idx == m.focus {
				marker = ">"
				st = focusStyle
			}
			if w := lipgloss.Width(label); w < maxLabel {
				label += strings.Repeat(" ", maxLabel-w)
			}
			line := marker + " " + label + ": " + value
			lines = append(lines, st.Width(m.width).Render(line))
		}
	}
	return tea.NewView(strings.Join(lines, "\n"))
}

// FooterHints exposes the keyboard hints for the modal footer.
func (m *ViewOptionsModel) FooterHints() []FooterHint {
	return []FooterHint{
		{Key: "Up/Down", Label: "Move", Enabled: true},
		{Key: "Left/Right/Space", Label: "Toggle", Enabled: true},
		{Key: "C-s", Label: "Save as defaults", Enabled: true},
		{Key: "Enter", Label: "Apply & Close", Enabled: true},
		{Key: "Esc", Label: "Cancel", Enabled: true},
	}
}

// PanelIndex identifies the panel the dialog was opened for.
func (m *ViewOptionsModel) PanelIndex() int { return m.panelIdx }
