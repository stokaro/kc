package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	readmedoc "github.com/sttts/kc"
	"github.com/sttts/kc/internal/access"
	"github.com/sttts/kc/internal/atomicfile"
	kccluster "github.com/sttts/kc/internal/cluster"
	"github.com/sttts/kc/internal/execctx"
	"github.com/sttts/kc/internal/explain"
	models "github.com/sttts/kc/internal/models"
	navui "github.com/sttts/kc/internal/navigation"
	"github.com/sttts/kc/internal/objectaction"
	"github.com/sttts/kc/internal/podfs"
	"github.com/sttts/kc/internal/portforward"
	"github.com/sttts/kc/internal/ui/menubar"
	panelcontent "github.com/sttts/kc/internal/ui/panelcontent"
	describewidget "github.com/sttts/kc/internal/ui/panelcontent/describe"
	manifestwidget "github.com/sttts/kc/internal/ui/panelcontent/manifest"
	"github.com/sttts/kc/internal/ui/skin"
	viewpkg "github.com/sttts/kc/internal/ui/viewer"
	"github.com/sttts/kc/pkg/appconfig"
	describe "github.com/sttts/kc/pkg/describe"
	"github.com/sttts/kc/pkg/kubeconfig"
	corev1 "k8s.io/api/core/v1"
	metamapper "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	clientcmd "k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// EscTimeoutMsg is sent when the escape sequence times out
type EscTimeoutMsg struct{}

// FolderDirtyMsg requests an immediate refresh for the given panel.
type FolderDirtyMsg struct {
	PanelIdx int
}

func (a *App) hasApplicableCommands(panel *Panel) bool {
	if panel == nil {
		return false
	}
	if len(a.objectActionsFor(panel).actions) > 0 {
		return true
	}
	if len(a.cfg.Commands) == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	item, _ := panel.SelectedNavItem(ctx)
	selected := panel.GetSelectedItems()
	activeNamespace := deriveNamespace(item, selected, panel.namespace(ctx))
	targets := commandTargets(item, selected)
	policy := a.accessPolicy()
	for _, cmd := range a.cfg.Commands {
		if policy.PermitsCommand(cmd.Dangerous) && isCommandApplicableToTargets(cmd, targets, activeNamespace) {
			return true
		}
	}
	return false
}

// DiscoveryRefreshedMsg is emitted when API discovery invalidates; resource folders should refresh.
type DiscoveryRefreshedMsg struct{}

// kubectlEditFinishedMsg notifies that a kubectl edit invocation exited.
type kubectlEditFinishedMsg struct {
	err         error
	panelIndex  int
	resourceRef string
}

type namespaceCreatedMsg struct {
	name string
	err  error
}

type commandWatchTickMsg struct {
	PanelIdx int
	Token    int
}

type panelNamespaceChangedMsg struct {
	PanelIdx  int
	Namespace string
}

type restartNamespaceCommandMsg struct {
	PanelIdx  int
	Namespace string
}

type startupIntentMsg struct{}

type cachedView struct {
	view   string
	cursor *tea.Cursor
	valid  bool
}

type resourceDeletedMsg struct {
	target deleteTarget
	err    error
}

type commandExitMsg struct {
	PanelIdx int
	OnExit   appconfig.CommandExitBehavior
}

func (a *App) setCommandWatchInterval(panelIdx int, interval time.Duration) tea.Cmd {
	if panelIdx < 0 || panelIdx > 1 {
		return nil
	}
	a.commandWatchInterval[panelIdx] = interval
	// Invalidate outstanding ticks.
	a.commandWatchToken[panelIdx]++
	if interval <= 0 {
		return nil
	}
	token := a.commandWatchToken[panelIdx]
	return tea.Tick(interval, func(time.Time) tea.Msg {
		return commandWatchTickMsg{PanelIdx: panelIdx, Token: token}
	})
}

func (a *App) scheduleCommandWatch(panelIdx int) tea.Cmd {
	if panelIdx < 0 || panelIdx > 1 {
		return nil
	}
	interval := a.commandWatchInterval[panelIdx]
	a.commandWatchToken[panelIdx]++
	if interval <= 0 {
		return nil
	}
	token := a.commandWatchToken[panelIdx]
	return tea.Tick(interval, func(time.Time) tea.Msg {
		return commandWatchTickMsg{PanelIdx: panelIdx, Token: token}
	})
}

type viewerOptionsTarget interface {
	SetTheme(string)
	Theme() string
	SetWrapMode(bool)
	WrapMode() bool
}

// logOptionsTarget is a viewer whose stream options F2 can change.
type logOptionsTarget interface {
	LogOptions() LogOptions
	SetLogOptions(LogOptions) tea.Cmd
}

// App represents the main application state
type App struct {
	leftPanel    *Panel
	rightPanel   *Panel
	terminal     *Terminal
	execFiles    *execctx.Files // kubeconfigs for subprocesses; see exec_context.go
	modalManager *ModalManager
	width        int
	height       int
	activePanel  int // 0 = left, 1 = right
	showTerminal bool
	allResources []schema.GroupVersionKind
	// Esc sequence tracking
	escPressed bool
	// menuBar is the F9 pull-down menu bar (see menu_bar.go).
	menuBar menubar.Bar
	// ctrlXPending is set after C-x, the prefix of mc's two-key commands.
	ctrlXPending bool
	// Data providers
	kubeMgr           *kubeconfig.Manager
	cl                *kccluster.Cluster
	clLease           *kccluster.Lease // holds cl in clPool; see docs/adr/0062-cache-ownership.md
	clPool            *kccluster.Pool
	ctx               context.Context
	cancel            context.CancelFunc
	currentCtx        *kubeconfig.Context
	kubectlCtx        string // context plain kubectl uses; see Deps.KubectlContext
	impersonateUser   string
	impersonateUID    string
	impersonateGroups []string
	access            *accessState // access review cache; see access_gate.go
	viewConfig        *ViewConfig
	cfg               *appconfig.Config

	// readOnlyOverride is the --readonly / --write flag; see readonly.go.
	readOnlyOverride ReadOnlyOverride

	// Command watch scheduling
	commandWatchToken    [2]int
	commandWatchInterval [2]time.Duration
	// New navigation (folder-backed) using a Navigator
	leftNav  *navui.Navigator
	rightNav *navui.Navigator
	// Mouse double-click detection
	lastClickTime  time.Time
	lastClickPanel int
	lastClickRowID string
	// Suppress forwarding of mouse to terminal immediately after toggling fullscreen
	suppressMouseUntil time.Time
	// Resources options dialog state
	prevResShowNonEmpty bool
	prevResOrder        string
	resOptsChanged      bool
	resOptsConfirmed    bool
	// Busy spinner state (lightweight, non-intrusive)
	busyActive bool
	busyLabel  string
	busyFrame  int
	busyToken  int
	// Toast notification state (auto-dismiss)
	toastActive bool
	toastText   string
	toastInfo   bool
	toastUntil  time.Time
	// Logger that emits toasts on errors with rate limiting
	toastLogger            *ToastLogger
	pendingCmdsMu          sync.Mutex
	pendingCmds            []tea.Cmd
	cfgStore               *appconfig.Store // ~/.kc/config.yaml; see configStore
	leftConfig             *appconfig.Config
	rightConfig            *appconfig.Config
	namespaceInput         *NamespaceCreateModel
	deleteConfirm          *DeleteConfirmModel
	copyInput              *CopyToLocalModel
	pendingCopy            *copyRequest
	pendingDelete          *deleteTarget
	lastCopyDir            string
	transfer               *transferJob // the open copy dialog, if any
	transferGen            int
	namespaceCreatePanel   int
	leftPanelWidthPercent  int
	rightPanelWidthPercent int
	leftFolderDirtyCancel  func()
	rightFolderDirtyCancel func()
	viewerOptionsTarget    viewerOptionsTarget
	// Discovery refresh notifications
	discoveryCh     chan struct{}
	discoveryCancel func()
	// Recent Bubble Tea messages for idle-loop diagnostics
	msgLog []string
	// Cached views
	mainViewCache         cachedView
	terminalAreaCache     cachedView
	activeCommandSelector *CommandSelectorModel
	functionBar           *functionBar
	// Namespace auto-navigation state
	// panelScope is each panel's namespace scope (models.AllNamespaces for
	// all). It is kc state; kubeconfig is never changed.
	panelScope         [2]string
	activity           *models.Activity     // pauses background refreshes while a dialog is open
	pendingConfirm     *pendingConfirmation // waits for the red confirmation
	objectActions      *objectaction.Registry
	nodeShell          *nodeShellSession     // open node shell, shown full screen
	nodeShellStarting  bool                  // a node shell pod is being created
	usage              *models.UsageMetrics  // polls metrics.k8s.io for usage columns
	portForwards       *portforward.Registry // forwards of this process, across contexts
	pendingPortForward *models.PortForwardPlan
	namespaceOverride  string
	// contextOverride is the kubeconfig context requested with --context.
	contextOverride string
	// allNamespaces is set by -A: the panels start in models.AllNamespaces.
	allNamespaces          bool
	startupIntent          StartupIntent
	startupIntentScheduled bool
	startupIntentApplied   bool
	helpViewer             *MarkdownHelpViewer
	podfsFactory           podfs.Factory
	folderDirtyCh          chan FolderDirtyMsg
	// describeRenderer is reused while describeRendererCluster is a.cl.
	describeRenderer        *describe.Renderer
	describeRendererCluster *kccluster.Cluster
	readErrCh               chan error // read failures from models, shown as red toasts
	panelNamespace          [2]string
	namespaceCommandTarget  [2]string
	// Navigation history per panel (M-y, M-u, M-H) and the hotlist (C-\).
	history     [2]*navui.History
	jumpToken   [2]int
	pendingJump *pendingJump
	hotlist     []navui.Location
	hotlistPath string // empty until Init; tests set it
	// panelNavs are the running NavigatePanel walks, one per panel.
	panelNavs   [2]*panelNavigation
	panelNavGen uint64
	// prevLocation is where each panel was before its last NavigatePanel
	// call; cd - goes there.
	prevLocation [2]*PanelPath
	explainDocs  *explain.Loader // OpenAPI v3 documents for M-e, per cluster and group-version
}

const (
	requestTimeout     = 10 * time.Second
	copyRequestTimeout = 30 * time.Second
	msgLogLimit        = 512
)

var panelWidthPercentOptions = []int{25, 33, 50, 66, 75, 100}

// Invariant: a.cfg is always non-nil. NewApp initializes it with defaults and
// Init() loads and overwrites with persisted config, never leaving it nil.

// NewApp creates a new application instance
func NewApp() *App {
	cfg := appconfig.Default()
	app := &App{
		cfg:                    cfg,
		terminal:               NewTerminal(),
		functionBar:            newFunctionBar(),
		modalManager:           NewModalManager(cfg),
		activePanel:            0,
		showTerminal:           false,
		allResources:           make([]schema.GroupVersionKind, 0),
		escPressed:             false,
		viewConfig:             NewViewConfig(),
		namespaceCreatePanel:   -1,
		leftPanelWidthPercent:  50,
		rightPanelWidthPercent: 50,
		folderDirtyCh:          make(chan FolderDirtyMsg, 64),
		readErrCh:              make(chan error, 16),
	}
	app.ctx, app.cancel = context.WithCancel(context.Background())
	app.activity = &models.Activity{}
	app.usage = models.NewUsageMetrics()
	app.leftPanel = NewPanel("", app.cfg)
	app.rightPanel = NewPanel("", app.cfg)
	app.terminal.SetLogger(ctrllog.Log.WithName("terminal"))
	app.toastLogger = NewToastLogger(app, 2*time.Second)
	app.leftPanel.SetCommandFocusHandler(func(f bool) tea.Cmd { return app.onCommandFocusChanged(0, f) })
	app.rightPanel.SetCommandFocusHandler(func(f bool) tea.Cmd { return app.onCommandFocusChanged(1, f) })
	app.leftPanel.SetMessagePoster(app.postFromPanel(0))
	app.rightPanel.SetMessagePoster(app.postFromPanel(1))
	app.leftPanel.RegisterSelectionObserver(func(ctx context.Context, sel panelcontent.Selection, ns string) tea.Msg {
		ns = deriveNamespace(sel.Item, app.leftPanel.GetSelectedItems(), ns)
		if ns == app.panelNamespace[0] {
			return nil
		}
		app.panelNamespace[0] = ns
		return panelNamespaceChangedMsg{PanelIdx: 0, Namespace: ns}
	})
	app.rightPanel.RegisterSelectionObserver(func(ctx context.Context, sel panelcontent.Selection, ns string) tea.Msg {
		ns = deriveNamespace(sel.Item, app.rightPanel.GetSelectedItems(), ns)
		if ns == app.panelNamespace[1] {
			return nil
		}
		app.panelNamespace[1] = ns
		return panelNamespaceChangedMsg{PanelIdx: 1, Namespace: ns}
	})

	// Register modals
	app.setupModals()
	app.setupPanelInputs()

	return app
}

// Init initializes the application
func (a *App) Init() tea.Cmd {
	loadToast := a.loadConfig()
	cfg := a.cfg
	a.modalManager.SyncEscTimeout()
	leftPercent, rightPercent := normalizePanelWidthPercents(cfg.Panel.Width.LeftPercent, cfg.Panel.Width.RightPercent)
	a.leftPanelWidthPercent = leftPercent
	a.rightPanelWidthPercent = rightPercent
	a.leftConfig = cloneConfig(cfg)
	a.rightConfig = cloneConfig(cfg)
	var lookErr tea.Cmd
	skinsDir, _ := skin.DefaultDir() // no home dir: only built-in skins
	if err := a.applyLook(cfg, skinsDir); err != nil {
		ctrllog.FromContext(a.ctx).WithName("skin").Error(err, "panel colors config")
		if a.toastLogger != nil {
			lookErr = a.toastLogger.Errorf("%s", strings.ReplaceAll(err.Error(), "\n", "; "))
		}
	}
	return tea.Batch(
		lookErr,
		a.leftPanel.Init(),
		a.rightPanel.Init(),
		a.terminal.Init(),
		func() tea.Msg {
			// Focus the terminal initially since it's the main input area
			a.terminal.Focus()
			return nil
		},
		a.waitFolderDirtyEvents(),
		a.waitReadErrors(),
		a.loadHotlist(),
		loadToast,
	)
}

// readErrorMsg carries a read failure reported by a folder or item.
type readErrorMsg struct{ err error }

// reportReadError is Deps.ReportError: models call it from any goroutine.
// It never blocks; when the channel is full the error is dropped, since a
// red message is already on its way.
func (a *App) reportReadError(err error) {
	if a == nil || a.readErrCh == nil || err == nil {
		return
	}
	select {
	case a.readErrCh <- err:
	default:
	}
}

func (a *App) waitReadErrors() tea.Cmd {
	if a == nil || a.readErrCh == nil {
		return nil
	}
	ctx := a.ctx
	ch := a.readErrCh
	return func() tea.Msg {
		select {
		case <-ctx.Done():
			return nil
		case err := <-ch:
			return readErrorMsg{err: err}
		}
	}
}

// readErrorText turns a read failure into a short message for the status line.
func readErrorText(err error) string {
	text := err.Error()
	if errors.Is(err, context.DeadlineExceeded) {
		text = strings.Replace(text, context.DeadlineExceeded.Error(), "timed out", 1)
	}
	return strings.ToUpper(text[:1]) + text[1:] + ". Try again."
}

func (a *App) waitFolderDirtyEvents() tea.Cmd {
	if a == nil || a.folderDirtyCh == nil {
		return nil
	}
	ctx := a.ctx
	ch := a.folderDirtyCh
	return func() tea.Msg {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			return msg
		}
	}
}

func (a *App) signalFolderDirty(panelIdx int) {
	if a == nil || a.folderDirtyCh == nil {
		return
	}
	msg := FolderDirtyMsg{PanelIdx: panelIdx}
	select {
	case a.folderDirtyCh <- msg:
	default:
		ctrllog.FromContext(a.ctx).WithName("folderDirty").Info("dropping folder dirty event", "panel", panelIdx)
	}
}

func cloneConfig(cfg *appconfig.Config) *appconfig.Config {
	if cfg == nil {
		return appconfig.Default()
	}
	clone := *cfg
	clone.Resources.Favorites = append([]string(nil), cfg.Resources.Favorites...)
	return &clone
}

func (a *App) watchDiscovery() tea.Cmd {
	if a.discoveryCh == nil {
		return nil
	}
	ctx := a.ctx
	ch := a.discoveryCh
	return func() tea.Msg {
		select {
		case <-ctx.Done():
			return nil
		case <-ch:
			return DiscoveryRefreshedMsg{}
		}
	}
}

func (a *App) handleDiscoveryRefresh() {
	if a.cl != nil {
		if infos, err := a.cl.GetResourceInfos(); err == nil {
			a.leftPanel.SetResourceCatalog(infos)
			a.rightPanel.SetResourceCatalog(infos)
		} else if a.toastLogger != nil {
			a.enqueueCmd(a.toastLogger.Errorf("Discovery refresh failed: %v", err))
		}
	}
	mark := func(nav *navui.Navigator) {
		if nav == nil {
			return
		}
		nav.ForEach(func(f models.Folder) {
			switch f.(type) {
			case *models.ClusterResourcesFolder, *models.NamespacedResourcesFolder, *models.RootFolder, *models.ContextRootFolder:
				if refresher, ok := f.(interface{ Refresh() }); ok {
					refresher.Refresh()
				}
			}
		})
	}
	mark(a.leftNav)
	mark(a.rightNav)
}

func (a *App) ensurePanelConfig(panel *Panel) *appconfig.Config {
	if panel == a.leftPanel {
		if a.leftConfig == nil {
			a.leftConfig = cloneConfig(a.cfg)
		}
		return a.leftConfig
	}
	if a.rightConfig == nil {
		a.rightConfig = cloneConfig(a.cfg)
	}
	return a.rightConfig
}

// activePanelRef returns the currently focused panel.
func (a *App) activePanelRef() *Panel {
	if a.activePanel == 1 {
		return a.rightPanel
	}
	return a.leftPanel
}

// panelIndex returns 0 for the left panel and 1 for the right panel.
func (a *App) panelIndex(panel *Panel) int {
	if panel == nil {
		return -1
	}
	if panel == a.rightPanel {
		return 1
	}
	return 0
}

func applyResourceOptionsToFolder(folder models.Folder, show bool, order appconfig.ResourcesViewOrder, favorites []string) {
	if folder == nil {
		return
	}
	if configurable, ok := folder.(models.ResourceViewConfigurable); ok {
		configurable.ApplyResourceViewOptions(show, order, favorites)
	}
}

func (a *App) navigatorForPanel(panel *Panel) *navui.Navigator {
	if panel == a.rightPanel {
		return a.rightNav
	}
	return a.leftNav
}

func (a *App) panelByIndex(idx int) *Panel {
	if idx == 1 {
		return a.rightPanel
	}
	return a.leftPanel
}

func (a *App) setActivePanel(idx int, reason string) {
	if idx < 0 || idx > 1 {
		return
	}
	if a.activePanel == idx {
		return
	}
	a.activePanel = idx
	a.invalidateView(reason)
	a.invalidateFunctionBar("active panel change")
}

func (a *App) setPanelFolder(ctx context.Context, panelIdx int, folder models.Folder, hasBack bool) {
	panel := a.panelByIndex(panelIdx)
	if panel == nil {
		return
	}
	panel.SetFolder(ctx, folder, hasBack)
	panel.RefreshFolder(ctx)
	a.attachFolderDirtyListener(panelIdx, folder)
	a.invalidateView(fmt.Sprintf("panel %d folder assigned", panelIdx))
	a.invalidateFunctionBar(fmt.Sprintf("panel %d folder assigned", panelIdx))
}

func (a *App) showModal(key string) {
	if a.modalManager == nil {
		return
	}
	// A dialog always covers the menu bar; it must not come back under it.
	a.menuBar.Close()
	a.modalManager.Show(key)
	a.invalidateView("show modal " + key)
}

func (a *App) hideModal() {
	if a.modalManager == nil {
		return
	}
	a.modalManager.Hide()
	a.activeCommandSelector = nil
	a.invalidateView("hide modal")
}

func (a *App) hideModalName(name string) {
	if a.modalManager == nil {
		return
	}
	a.modalManager.HideName(name)
	if name == "command_menu" {
		a.activeCommandSelector = nil
	}
	a.invalidateView("hide modal " + name)
}

func (a *App) attachFolderDirtyListener(panelIdx int, folder models.Folder) {
	var cancelPrev func()
	switch panelIdx {
	case 0:
		cancelPrev = a.leftFolderDirtyCancel
	case 1:
		cancelPrev = a.rightFolderDirtyCancel
	default:
		return
	}
	if cancelPrev != nil {
		cancelPrev()
	}
	assign := func(func()) {}
	switch panelIdx {
	case 0:
		assign = func(fn func()) { a.leftFolderDirtyCancel = fn }
	case 1:
		assign = func(fn func()) { a.rightFolderDirtyCancel = fn }
	}
	if folder == nil {
		assign(nil)
		return
	}
	obs, ok := folder.(models.DirtyObservable)
	if !ok {
		ctrllog.FromContext(a.ctx).WithName("folderDirty").Info("folder not dirty observable", "panel", panelIdx)
		assign(nil)
		return
	}
	cancel := obs.RegisterDirtyListener(func() {
		log := ctrllog.FromContext(a.ctx).WithName("folderDirty")
		log.Info("folder marked dirty", "panel", panelIdx)
		idx := panelIdx
		a.signalFolderDirty(idx)
	})
	assign(cancel)
}

func (a *App) panelIndexFor(panel *Panel) (int, bool) {
	if panel == nil {
		return -1, false
	}
	if panel == a.leftPanel {
		return 0, true
	}
	if panel == a.rightPanel {
		return 1, true
	}
	return -1, false
}

func (a *App) inlineTerminalHeight() int {
	if a == nil || a.terminal == nil {
		return 0
	}
	if a.showTerminal {
		return 0
	}
	return inlineTerminalLines
}

func (a *App) panelAreaMetrics() (leftWidth int, rightWidth int, panelHeight int, headerOffset int) {
	// A toast replaces the function key bar; it takes no row of its own.
	reserved := 1 + a.inlineTerminalHeight() + a.commandLineHeight()
	panelHeight = a.height - reserved
	if panelHeight < 0 {
		panelHeight = 0
	}
	leftWidth, rightWidth = a.panelWidthsFor(a.width)
	headerOffset = 1
	return
}

func (a *App) panelWidthPercentFor(panelIdx int) int {
	if panelIdx == 1 {
		return clampPercent(a.rightPanelWidthPercent)
	}
	return clampPercent(a.leftPanelWidthPercent)
}

func (a *App) panelWidthsFor(total int) (int, int) {
	if total <= 0 {
		return 0, 0
	}
	leftPercent, rightPercent := normalizePanelWidthPercents(a.leftPanelWidthPercent, a.rightPanelWidthPercent)
	if leftPercent >= 100 {
		return total, 0
	}
	if rightPercent >= 100 {
		return 0, total
	}
	left := 0
	if leftPercent > 0 {
		left = (total*leftPercent + 50) / 100
		if left <= 0 {
			left = 1
		}
		if left >= total {
			left = total - 1
		}
	}
	right := total - left
	if rightPercent == 0 {
		right = 0
	} else if right <= 0 {
		right = 1
		if left > 0 {
			left = total - right
		}
	}
	if left < 0 {
		left = 0
	}
	if right < 0 {
		right = 0
	}
	return left, total - left
}

func (a *App) setPanelWidthPercent(panelIdx int, percent int) []tea.Cmd {
	percent = clampPercent(percent)
	switch panelIdx {
	case 1:
		left, right := normalizePanelWidthPercents(100-percent, percent)
		a.leftPanelWidthPercent = left
		a.rightPanelWidthPercent = right
	default:
		left, right := normalizePanelWidthPercents(percent, 100-percent)
		a.leftPanelWidthPercent = left
		a.rightPanelWidthPercent = right
	}
	if a.leftConfig != nil {
		a.leftConfig.Panel.Width.LeftPercent = a.leftPanelWidthPercent
		a.leftConfig.Panel.Width.RightPercent = a.rightPanelWidthPercent
	}
	if a.rightConfig != nil {
		a.rightConfig.Panel.Width.LeftPercent = a.leftPanelWidthPercent
		a.rightConfig.Panel.Width.RightPercent = a.rightPanelWidthPercent
	}
	if a.leftPanelWidthPercent == 0 && a.rightPanelWidthPercent == 100 {
		a.setActivePanel(1, "panel width forced right")
	} else if a.leftPanelWidthPercent == 100 && a.rightPanelWidthPercent == 0 {
		a.setActivePanel(0, "panel width forced left")
	}
	cmds := a.resizePanelsForCurrentLayout()
	a.invalidateView("panel width percent")
	return cmds
}

func (a *App) resizePanelsForCurrentLayout() []tea.Cmd {
	if a == nil {
		return nil
	}
	leftWidth, rightWidth, panelHeight, _ := a.panelAreaMetrics()
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	log := ctrllog.FromContext(ctx).WithName("ui").WithName("panelResize")
	var cmds []tea.Cmd
	if a.leftPanel != nil {
		model, cmd := a.leftPanel.Update(tea.WindowSizeMsg{Width: leftWidth, Height: panelHeight})
		if model != nil {
			a.leftPanel = model.(*Panel)
		}
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if a.rightPanel != nil {
		model, cmd := a.rightPanel.Update(tea.WindowSizeMsg{Width: rightWidth, Height: panelHeight})
		if model != nil {
			a.rightPanel = model.(*Panel)
		}
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	log.V(1).Info("panels resized", "leftWidth", leftWidth, "rightWidth", rightWidth, "panelHeight", panelHeight)
	return cmds
}

var panelWidthCycle = panelWidthPercentOptions

func nextPanelWidthPercent(current int) int {
	if len(panelWidthCycle) == 0 {
		return current
	}
	for _, candidate := range panelWidthCycle {
		if candidate > current {
			return candidate
		}
	}
	return panelWidthCycle[len(panelWidthCycle)-1]
}

func (a *App) cyclePanelWidth(panelIdx int) []tea.Cmd {
	current := a.panelWidthPercentFor(panelIdx)
	next := nextPanelWidthPercent(current)
	if next == current {
		return nil
	}
	return a.setPanelWidthPercent(panelIdx, next)
}

func normalizePanelWidthPercents(left, right int) (int, int) {
	left = clampPercent(left)
	right = clampPercent(right)
	switch {
	case left == 0 && right == 0:
		left, right = 50, 50
	case left == 0:
		left = clampPercent(100 - right)
		right = clampPercent(right)
	default:
		if left >= 100 {
			left, right = 100, 0
		} else {
			right = clampPercent(100 - left)
		}
	}
	if right >= 100 {
		left = 0
		right = 100
	}
	return left, right
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

func (a *App) syncPanelConfig(panel *Panel) {
	cfg := a.ensurePanelConfig(panel)
	showNonEmpty, order := panel.ResourceViewOptions()
	cfg.Resources.ShowNonEmptyOnly = showNonEmpty
	cfg.Resources.Order = appconfig.ResourcesViewOrder(order)
	columns := panel.ColumnsMode()
	cfg.Resources.Columns = columns
	cfg.Objects.Order = panel.ObjectOrder()
	cfg.Objects.Columns = columns
	cfg.Panel.Table.Mode = appconfig.TableMode(panel.TableMode())
}

func (a *App) applyResourceOptions(panel *Panel) {
	if panel == nil {
		return
	}
	show, orderStr := panel.ResourceViewOptions()
	order := appconfig.ResourcesViewOrder(orderStr)
	cfg := a.ensurePanelConfig(panel)
	var favorites []string
	if cfg != nil {
		favorites = cfg.Resources.Favorites
	}
	applyResourceOptionsToFolder(panel.Folder(), show, order, favorites)
	if nav := a.navigatorForPanel(panel); nav != nil {
		if cur := nav.Current(); cur != nil {
			applyResourceOptionsToFolder(cur, show, order, favorites)
		}
	}
}

func (a *App) aggregatedKubeConfig(current string) clientcmdapi.Config {
	contexts := make(map[string]*clientcmdapi.Context)
	if a.kubeMgr != nil {
		for _, ctx := range a.kubeMgr.GetContexts() {
			if ctx == nil {
				continue
			}
			contexts[ctx.Name] = &clientcmdapi.Context{
				Cluster:   ctx.Cluster,
				AuthInfo:  ctx.User,
				Namespace: ctx.Namespace,
			}
		}
	}
	return clientcmdapi.Config{
		CurrentContext: current,
		Contexts:       contexts,
	}
}

func (a *App) makeDeps(cl *kccluster.Cluster, cfg *appconfig.Config, key kccluster.Key) models.Deps {
	if cfg == nil {
		cfg = a.cfg
	}
	return models.Deps{
		Cl:               cl,
		Ctx:              a.ctx,
		CtxName:          key.ContextName,
		KubectlContext:   a.kubectlCtx,
		KubeConfig:       a.aggregatedKubeConfig(key.ContextName),
		AppConfig:        cfg,
		ClusterKey:       key,
		NamespaceFactory: a.namespaceClusterFactory(),
		PodFSFactory:     a.podfsFactory,
		ReportError:      a.reportReadError,
		Activity:         a.activity,
		ReadOnly:         a.readOnlyFor(key.ContextName),
		PortForwards:     a.portForwards,
		Usage:            a.usage,
	}
}

func (a *App) currentClusterKey() kccluster.Key {
	if a.currentCtx == nil || a.currentCtx.Kubeconfig == nil {
		return kccluster.Key{}
	}
	return kccluster.Key{
		KubeconfigPath:       a.currentCtx.Kubeconfig.Path,
		ContextName:          a.currentCtx.Name,
		ImpersonateUser:      strings.TrimSpace(a.impersonateUser),
		ImpersonateUID:       strings.TrimSpace(a.impersonateUID),
		ImpersonateGroupsKey: a.impersonationGroupsKey(),
	}
}

func (a *App) namespaceClusterFactory() models.NamespaceClusterFactory {
	return func(ctx context.Context, key kccluster.Key, selectors map[schema.GroupVersionResource]crcache.ByObject) (*kccluster.Cluster, func(), error) {
		if a.clPool == nil {
			return nil, nil, fmt.Errorf("cluster pool unavailable")
		}
		lease, err := a.clPool.Acquire(ctx, key, selectors)
		if err != nil {
			return nil, nil, err
		}
		return lease.Cluster(), lease.Release, nil
	}
}

func (a *App) navigatorPath(nav *navui.Navigator) string {
	if nav == nil {
		return "/"
	}
	if dp, ok := nav.Current().(interface{ DisplayPath() string }); ok {
		return dp.DisplayPath()
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	return nav.Path(ctx)
}

func (a *App) impersonationGroupsKey() string {
	if len(a.impersonateGroups) == 0 {
		return ""
	}
	groups := append([]string(nil), a.impersonateGroups...)
	for i := range groups {
		groups[i] = strings.TrimSpace(groups[i])
	}
	sort.Strings(groups)
	return strings.Join(groups, ",")
}

func (a *App) navigatorNamespace(nav *navui.Navigator) string {
	if nav == nil {
		return ""
	}
	ns := ""
	type namespaceProvider interface {
		Namespace() string
	}
	nav.ForEach(func(folder models.Folder) {
		if provider, ok := folder.(namespaceProvider); ok {
			if name := strings.TrimSpace(provider.Namespace()); name != "" {
				ns = name
			}
		}
	})
	return ns
}

func (a *App) syncTerminalNamespaceForNavigator(nav *navui.Navigator) {
	if a.cfg == nil || !a.cfg.Terminal.Follow {
		return
	}
	ns := strings.TrimSpace(a.navigatorNamespace(nav))
	a.updateTermContextNamespace(ns)
}

// setupModals sets up the modal dialogs

func (a *App) setupModals() {
	// Resources options modal (content set dynamically on open)
	viewOpts := NewViewOptionsModel(ViewOptionsConfig{
		PanelIndex:        0,
		PanelModes:        []PanelViewMode{PanelModeList},
		ActivePanelMode:   PanelModeList,
		PanelWidthPercent: a.leftPanelWidthPercent,
		TableMode:         "scroll",
	})
	viewModal := NewModal("View Options", viewOpts)
	a.modalManager.Register("view_options", viewModal)

	// Namespace creation modal (configured on open)
	nsModel := NewNamespaceCreateModel()
	nsModal := NewModal("Create Namespace", nsModel)
	nsModal.SetCloseOnSingleEsc(true)
	a.modalManager.Register("namespace_create", nsModal)
	a.namespaceInput = nsModel

	// Delete confirmation modal
	delModel := NewDeleteConfirmModel()
	delModal := NewModal("Confirm Delete", delModel)
	delModal.SetCloseOnSingleEsc(true)
	a.modalManager.Register("delete_confirm", delModal)
	a.deleteConfirm = delModel

	// Copy-to-local modal
	copyModel := NewCopyToLocalModel()
	copyModal := NewModal("Copy to Local File", copyModel)
	copyModal.SetCloseOnSingleEsc(true)
	a.modalManager.Register("copy_local", copyModal)
	a.copyInput = copyModel

	for idx := 0; idx < 2; idx++ {
		modeModel := NewPanelModeModel(idx, []PanelViewMode{PanelModeList}, PanelModeList)
		modeModal := NewModal("Panel Mode", modeModel)
		modeModal.SetCloseOnSingleEsc(true)
		a.modalManager.Register(panelModeModalKey(idx), modeModal)
	}

	helpContent := helpMarkdownContent(strings.TrimSpace(readmedoc.README))
	helpViewer := NewMarkdownHelpViewer(helpContent)
	helpModal := NewModal("Kubernetes Commander Help", helpViewer)
	helpModal.SetCloseOnSingleEsc(true)
	a.modalManager.Register("help", helpModal)
	a.helpViewer = helpViewer
}

func helpMarkdownContent(raw string) string {
	if raw == "" {
		return ""
	}
	lines := strings.Split(raw, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.Contains(line, "docs/screenshot.png") {
			continue
		}
		if strings.HasPrefix(trim, "![") && strings.Contains(trim, "](") {
			continue
		}
		filtered = append(filtered, line)
	}
	return strings.Join(filtered, "\n")
}

func (a *App) setupPanelInputs() {
	envSupplier := func() PanelEnvironment { return a.panelEnvironment() }
	registerModes := func(panel *Panel, name string) {
		if panel == nil {
			return
		}
		panel.RegisterMode(PanelModeDescribe, func(p *Panel) PanelWidget {
			deps := p.manifestWidgetDeps()
			deps.Describe = a.describeFunc()
			deps.Paused = a.activity.Paused
			return describewidget.New(deps)
		})
		panel.RegisterMode(PanelModeManifest, func(p *Panel) PanelWidget {
			deps := p.manifestWidgetDeps()
			deps.Paused = a.activity.Paused
			return manifestwidget.New(deps)
		})
	}
	if a.leftPanel != nil {
		a.leftPanel.SetEnvironmentSupplier(envSupplier)
		a.leftPanel.SetCommandExecSupplier(panelCommandExec{app: a, panel: a.leftPanel})
		a.leftPanel.SetActionHandlers(a.panelActionHandlers())
		registerModes(a.leftPanel, "Left panel")
	}
	if a.rightPanel != nil {
		a.rightPanel.SetEnvironmentSupplier(envSupplier)
		a.rightPanel.SetCommandExecSupplier(panelCommandExec{app: a, panel: a.rightPanel})
		a.rightPanel.SetActionHandlers(a.panelActionHandlers())
		registerModes(a.rightPanel, "Right panel")
	}
}

// describeFunc prepares describe reads on the UI goroutine: it takes the
// cluster and the cached renderer now, so the background reads never touch
// App fields that a context switch replaces.
func (a *App) describeFunc() panelcontent.DescribeFunc {
	return func(target describe.Target) panelcontent.DescribeReads {
		cl, root := a.cl, a.ctx
		renderer, err := a.describeRendererForCluster()
		return panelcontent.DescribeReads{
			Describe: func() (describe.Result, error) {
				if err != nil {
					return describe.Result{}, err
				}
				ctrllog.FromContext(root).WithName("describe").Info("describe",
					"gvr", target.GVR.String(), "namespace", target.Namespace, "name", target.Name)
				return renderer.Describe(target)
			},
			Version: func() string {
				if cl == nil {
					return ""
				}
				ctx, cancel := context.WithTimeout(root, requestTimeout)
				defer cancel()
				obj, err := cl.GetByGVR(ctx, target.GVR, target.Namespace, target.Name)
				if err != nil || obj == nil {
					return ""
				}
				return obj.GetResourceVersion()
			},
		}
	}
}

// describeRendererForCluster returns one renderer per cluster connection,
// so a describe does not rebuild the REST client plumbing.
func (a *App) describeRendererForCluster() (*describe.Renderer, error) {
	if a.describeRenderer != nil && a.describeRendererCluster == a.cl {
		return a.describeRenderer, nil
	}
	renderer, err := a.newDescribeRenderer()
	if err != nil {
		return nil, err
	}
	a.describeRenderer, a.describeRendererCluster = renderer, a.cl
	return renderer, nil
}

func (a *App) newDescribeRenderer() (*describe.Renderer, error) {
	if a == nil || a.cl == nil {
		return nil, fmt.Errorf("cluster client unavailable")
	}
	cfg := rest.CopyConfig(a.cl.GetConfig())
	if cfg.Timeout == 0 {
		// The describer takes no context; the timeout bounds each request so
		// a hung server cannot hold the panel's only load in flight.
		cfg.Timeout = requestTimeout
	}
	mapper := a.cl.RESTMapper()
	disco := a.cl.DiscoveryClient()
	var loader clientcmd.ClientConfig
	if a.currentCtx != nil && a.currentCtx.Kubeconfig != nil && a.currentCtx.Kubeconfig.Config != nil {
		overrides := &clientcmd.ConfigOverrides{CurrentContext: a.currentCtx.Name}
		loader = clientcmd.NewDefaultClientConfig(*a.currentCtx.Kubeconfig.Config, overrides)
	}
	return describe.NewRenderer(cfg, mapper, disco, loader)
}

func (a *App) postFromPanel(idx int) func(tea.Msg) {
	return func(msg tea.Msg) {
		if msg == nil {
			return
		}
		switch m := msg.(type) {
		case commandExitMsg:
			m.PanelIdx = idx
			a.enqueueCmd(func() tea.Msg { return m })
		default:
			a.enqueueCmd(func() tea.Msg { return msg })
		}
	}
}

func (a *App) onCommandFocusChanged(panelIdx int, focused bool) tea.Cmd {
	if a.terminal != nil {
		if focused {
			a.terminal.Blur()
		} else {
			a.terminal.Focus()
		}
	}
	a.invalidateFunctionBar("command focus change")
	a.invalidateView("command focus change")
	return nil
}

func (a *App) panelActionHandlers() PanelActionHandlers {
	handlers := PanelActionHandlers{
		PanelActionHelp: func(*Panel) tea.Cmd {
			return a.showHelp()
		},
		PanelActionOptions: func(p *Panel) tea.Cmd {
			return a.showViewOptionsModalForPanel(p)
		},
		PanelActionView: func(p *Panel) tea.Cmd {
			return a.openViewerForPanel(p)
		},
		PanelActionEdit: func(p *Panel) tea.Cmd {
			return a.editSelectionForPanel(p)
		},
		PanelActionCopy: func(p *Panel) tea.Cmd {
			return a.copyItemForPanel(p)
		},
		PanelActionCreateNamespace: func(p *Panel) tea.Cmd {
			return a.createNamespaceForPanel(p)
		},
		PanelActionDelete: func(p *Panel) tea.Cmd {
			if cmd, ok := a.stopSelectionForPanel(p); ok {
				return cmd
			}
			return a.deleteResourceForPanel(p)
		},
		PanelActionMenu: func(p *Panel) tea.Cmd {
			return a.showContextMenuForPanel(p)
		},
	}
	// Help (F1) is intentionally omitted until implemented.
	return handlers
}

func (a *App) panelEnvironment() PanelEnvironment {
	env := PanelEnvironment{}
	if a.currentCtx != nil {
		env.AllowCreateNamespaces = true
		if a.currentCtx.Kubeconfig != nil {
			env.AllowEditObjects = true
		}
	}
	if a.cl != nil {
		env.AllowDeleteObjects = true
	}
	env.Permit = a.permitAccess
	env.ReadOnly = a.readOnly()
	return env
}

func (a *App) capabilitiesForPanel(panel *Panel) PanelCapabilities {
	if panel == nil {
		return PanelCapabilities{}
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	caps := panel.Capabilities(ctx)
	if caps.HasContextMenu {
		caps.HasContextMenu = a.hasApplicableCommands(panel)
	}
	return maskCapabilitiesForMode(panel.Mode(), panel.HasCommandFocus(), caps)
}

func maskCapabilitiesForMode(mode PanelViewMode, commandFocused bool, caps PanelCapabilities) PanelCapabilities {
	switch mode {
	case PanelModeList:
		return caps
	case PanelModeDescribe, PanelModeManifest:
		// Read-only; allow help/options/menu only.
		caps.CanView = false
		caps.CanCopy = false
		caps.CanEdit = false
		caps.CanDelete = false
		caps.CanCreateNS = false
		return caps
	case PanelModeCommand:
		// Command mode: no resource actions. Keep help/options/menu when the widget
		// isn't holding focus so they can be used from the bar.
		caps.CanView = false
		caps.CanCopy = false
		caps.CanEdit = false
		caps.CanDelete = false
		caps.CanCreateNS = false
		if commandFocused {
			caps.HasHelp = false
			caps.HasOptions = false
			caps.HasContextMenu = false
		}
		return caps
	default:
		return caps
	}
}

func (a *App) setPanelMode(ctx context.Context, panel *Panel, mode PanelViewMode, reason string) tea.Cmd {
	if panel == nil {
		return nil
	}
	prev := panel.Mode()
	cmd := panel.SetMode(ctx, mode)
	if panel.Mode() != prev {
		a.invalidateView(reason)
		a.invalidateFunctionBar(reason)
	}
	return cmd
}

func (a *App) updatePanelsWithMsg(msg tea.Msg) []tea.Cmd {
	var cmds []tea.Cmd
	if a.leftPanel != nil {
		model, cmd := a.leftPanel.Update(msg)
		if model != nil {
			a.leftPanel = model.(*Panel)
		}
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if a.rightPanel != nil {
		model, cmd := a.rightPanel.Update(msg)
		if model != nil {
			a.rightPanel = model.(*Panel)
		}
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}

func (a *App) cyclePanelMode(idx int) tea.Cmd {
	panel := a.panelByIndex(idx)
	if panel == nil {
		return nil
	}
	next := NextPanelMode(panel.Mode())
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	return a.setPanelMode(ctx, panel, next, fmt.Sprintf("cycle panel %d mode", idx))
}

func (a *App) cyclePanelModeIfVisible(idx int) tea.Cmd {
	leftWidth, rightWidth, _, _ := a.panelAreaMetrics()
	// Only cycle when both panels are visible to avoid acting on hidden panes.
	if leftWidth <= 0 || rightWidth <= 0 {
		return nil
	}
	switch idx {
	case 0:
		if leftWidth <= 0 {
			return nil
		}
	case 1:
		if rightWidth <= 0 {
			return nil
		}
	default:
		return nil
	}
	return a.cyclePanelMode(idx)
}

func panelModeModalKey(idx int) string {
	if idx == 0 {
		return "panel_mode_left"
	}
	return "panel_mode_right"
}

func (a *App) showPanelModeModal(panelIdx int) tea.Cmd {
	panel := a.panelByIndex(panelIdx)
	if panel == nil {
		return nil
	}
	modes := panel.AvailableModes()
	if len(modes) == 0 {
		modes = []PanelViewMode{PanelModeList}
	}
	model := NewPanelModeModel(panelIdx, modes, panel.Mode())
	key := panelModeModalKey(panelIdx)
	modal := a.modalManager.modals[key]
	if modal == nil {
		modal = NewModal("Panel Mode", model)
		modal.SetCloseOnSingleEsc(true)
		a.modalManager.Register(key, modal)
	} else {
		modal.SetContent(model)
	}
	leftPanelWidth, rightPanelWidth, panelHeight, headerOffset := a.panelAreaMetrics()
	panelWidth := leftPanelWidth
	panelOffset := 0
	if panelIdx == 1 {
		panelWidth = rightPanelWidth
		panelOffset = leftPanelWidth
	}
	if panelWidth <= 0 {
		panelWidth = max(20, max(a.width/2, a.width))
		panelOffset = 0
	}
	width := panelWidth / 2
	if width < 24 {
		width = 24
	}
	width = min(width, panelWidth-2)
	width = min(width, a.width-4)
	maxContentHeight := max(len(modes)+4, 6)
	targetHeight := panelHeight / 2
	if targetHeight < 6 {
		targetHeight = 6
	}
	height := min(maxContentHeight, targetHeight)
	if height < 6 {
		height = 6
	}
	model.SetDimensions(max(1, width-4), max(1, height-3))
	modal.SetDimensions(a.width, a.height)
	bg, _ := a.renderMainView()
	modal.SetWindowed(width, height, bg)
	offsetX := panelOffset + max(0, (panelWidth-width)/2)
	offsetY := headerOffset
	modal.SetWindowOffset(offsetX, offsetY)
	modal.SetOnClose(func() tea.Cmd { return nil })
	a.showModal(key)
	return nil
}

// Message handlers for function keys
// showViewOptionsModal opens the appropriate View Options dialog (Resources or Objects)
// depending on the active view context.
func (a *App) showViewOptionsModal() tea.Cmd {
	return a.showViewOptionsModalForPanel(a.activePanelRef())
}

func (a *App) showViewOptionsModalForPanel(panel *Panel) tea.Cmd {
	if panel == nil {
		panel = a.activePanelRef()
	}
	if panel == nil {
		return nil
	}

	panelIdx, ok := a.panelIndexFor(panel)
	if !ok {
		panelIdx = a.activePanel
	}

	// Determine folder context for contextual options.
	var curFolder models.Folder
	if nav := a.navigatorForPanel(panel); nav != nil {
		curFolder = nav.Current()
	}
	if curFolder == nil {
		curFolder = panel.Folder()
	}

	showInclude := false
	showOrder := false
	resourceSection := false
	commandSection := panel.Mode() == PanelModeCommand
	if curFolder != nil {
		switch curFolder.(type) {
		case *models.RootFolder,
			*models.ClusterResourcesFolder,
			*models.NamespacedResourcesFolder,
			*models.ContextRootFolder,
			*models.ResourcesFolder:
			showInclude = true
			showOrder = true
			resourceSection = true
		}
	}
	if commandSection {
		resourceSection = false
	}

	objectSection := false
	if curFolder != nil {
		if _, ok := curFolder.(interface {
			ObjectListMeta() (schema.GroupVersionResource, string, bool)
		}); ok {
			objectSection = true
		}
	}
	if panel.Mode() != PanelModeList {
		objectSection = false
	}
	if commandSection {
		objectSection = false
	}

	// Panel-derived defaults.
	showNonEmpty, order := panel.ResourceViewOptions()
	tableMode := panel.TableMode()
	columns := panel.ColumnsMode()
	objOrder := panel.ObjectOrder()

	var resConfig *ViewOptionsResourcesConfig
	if resourceSection {
		resConfig = &ViewOptionsResourcesConfig{
			ShowInclude:   showInclude,
			IncludeEmpty:  !showNonEmpty,
			ShowOrder:     showOrder,
			Order:         order,
			ShowTableMode: true,
		}
	}

	var objConfig *ViewOptionsObjectsConfig
	if objectSection {
		objConfig = &ViewOptionsObjectsConfig{
			ShowTableMode: true,
			Columns:       columns,
			Order:         objOrder,
		}
		ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
		if st, ok := panel.SortState(ctx); ok {
			objConfig.Order = st.Applied
			objConfig.SortKeys = st.Keys
			objConfig.SortTitles = st.Titles
		}
		cancel()
		if filter, ok := panel.RowFilter(); ok {
			objConfig.ShowFaultsOnly = true
			objConfig.FaultsOnly = filter.FaultsOnly
		}
	}

	var cmdConfig *ViewOptionsCommandConfig
	if commandSection {
		ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
		defer cancel()
		cmdConfig = &ViewOptionsCommandConfig{
			WatchInterval: panel.CommandWatchInterval(ctx),
		}
	}

	content := NewViewOptionsModel(ViewOptionsConfig{
		PanelIndex:        panelIdx,
		PanelModes:        panel.AvailableModes(),
		ActivePanelMode:   panel.Mode(),
		PanelWidthPercent: a.panelWidthPercentFor(panelIdx),
		TableMode:         tableMode,
		Resources:         resConfig,
		Objects:           objConfig,
		Command:           cmdConfig,
	})

	modal := a.modalManager.modals["view_options"]
	if modal == nil {
		modal = NewModal("View Options", content)
		a.modalManager.Register("view_options", modal)
	} else {
		modal.SetContent(content)
		modal.title = "View Options"
	}

	a.layoutViewOptionsModal(modal, content, panelIdx, "")
	modal.SetOnClose(func() tea.Cmd { return nil })
	a.showModal("view_options")
	return nil
}

func (a *App) showViewerOptionsModal(target viewerOptionsTarget) tea.Cmd {
	if target == nil {
		return nil
	}
	themes := viewpkg.AvailableThemes()
	currentTheme := target.Theme()
	if currentTheme == "" {
		currentTheme = a.viewerTheme()
	}
	wrapMode := appconfig.ViewerModeScroll
	if target.WrapMode() {
		wrapMode = appconfig.ViewerModeWrap
	}
	optsCfg := ViewOptionsConfig{
		PanelIndex:       -1,
		SkipPanelSection: true,
		Viewer: &ViewOptionsViewerConfig{
			ThemeNames: themes,
			Theme:      currentTheme,
			WrapMode:   wrapMode,
		},
	}
	if lt, ok := target.(logOptionsTarget); ok {
		o := lt.LogOptions()
		optsCfg.Logs = &ViewOptionsLogsConfig{
			TailLines:    o.TailLines,
			SinceSeconds: o.SinceSeconds,
			Timestamps:   o.Timestamps,
			Previous:     o.Previous,
		}
	}
	content := NewViewOptionsModel(optsCfg)
	modal := a.modalManager.modals["viewer_options"]
	if modal == nil {
		modal = NewModal("Viewer Options", content)
		modal.SetCloseOnSingleEsc(true)
		a.modalManager.Register("viewer_options", modal)
	} else {
		modal.SetContent(content)
		modal.title = "Viewer Options"
	}
	modal.SetOnClose(func() tea.Cmd {
		a.viewerOptionsTarget = nil
		return nil
	})
	base := ""
	if active := a.modalManager.GetActiveModal(); active != nil {
		if view := active.View(); viewString(view) != "" {
			base = viewString(view)
		}
	}
	a.layoutViewOptionsModal(modal, content, -1, base)
	a.viewerOptionsTarget = target
	a.showModal("viewer_options")
	return nil
}

func (a *App) applyViewerOptions(msg ViewOptionsCommittedMsg) tea.Cmd {
	target := a.viewerOptionsTarget
	if msg.Viewer == nil {
		if msg.Close {
			a.hideModal()
		}
		return nil
	}
	if target == nil {
		if msg.Close {
			a.hideModal()
		}
		return nil
	}
	var restart tea.Cmd
	saveLogs := false
	apply := msg.Accept || msg.SaveDefault
	if apply {
		theme := msg.Viewer.Theme
		if theme == "" {
			theme = target.Theme()
		}
		target.SetTheme(theme)
		wrap := strings.EqualFold(msg.Viewer.WrapMode, appconfig.ViewerModeWrap)
		target.SetWrapMode(wrap)
		if a.cfg == nil {
			a.cfg = appconfig.Default()
		}
		a.cfg.Viewer.Theme = theme
		if wrap {
			a.cfg.Viewer.Mode = appconfig.ViewerModeWrap
		} else {
			a.cfg.Viewer.Mode = appconfig.ViewerModeScroll
		}
		if lt, ok := target.(logOptionsTarget); ok && msg.Logs != nil {
			opts := LogOptions{
				TailLines:    msg.Logs.TailLines,
				SinceSeconds: msg.Logs.SinceSeconds,
				Timestamps:   msg.Logs.Timestamps,
				Previous:     msg.Logs.Previous,
			}
			restart = lt.SetLogOptions(opts)
			if msg.SaveDefault {
				// Previous belongs to one container's history, not a default.
				a.cfg.Viewer.Logs = appconfig.LogsViewerConfig{
					Tail:       opts.TailLines,
					Since:      metav1.Duration{Duration: time.Duration(opts.SinceSeconds) * time.Second},
					Timestamps: opts.Timestamps,
				}
				saveLogs = true
			}
		}
	}
	var cmd tea.Cmd
	switch {
	case msg.SaveDefault:
		theme, mode, logs := a.cfg.Viewer.Theme, a.cfg.Viewer.Mode, a.cfg.Viewer.Logs
		cmd = a.saveDefaults(func(c *appconfig.Config) {
			c.Viewer.Theme = theme
			c.Viewer.Mode = mode
			if saveLogs {
				c.Viewer.Logs = logs
			}
		})
	case msg.Accept:
		cmd = a.sessionOnly()
	}
	if msg.Close {
		a.hideModal()
	}
	a.viewerOptionsTarget = nil
	return tea.Batch(restart, cmd)
}

func (a *App) layoutViewOptionsModal(modal *Modal, content *ViewOptionsModel, panelIdx int, background string) {
	leftPanelWidth, rightPanelWidth, _, _ := a.panelAreaMetrics()
	panelWidth := leftPanelWidth
	if panelIdx == 1 {
		panelWidth = rightPanelWidth
	}
	if panelWidth <= 0 {
		panelWidth = max(24, max(a.width/2, a.width))
	}
	fallbackWidth := panelWidth / 2
	if fallbackWidth < 36 {
		fallbackWidth = 36
	}
	fallbackHeight := content.ContentHeight() + 2
	if fallbackHeight < 6 {
		fallbackHeight = 6
	}
	a.configureModalWindow(modal, content, panelIdx, background, fallbackWidth, fallbackHeight)
}

func (a *App) modalPanelMetrics(panelIdx int) (panelWidth, panelHeight, headerOffset, panelOffset int, centered bool) {
	leftWidth, rightWidth, panelHeight, headerOffset := a.panelAreaMetrics()
	panelOffset = 0
	panelWidth = leftWidth
	centered = panelIdx < 0
	if centered {
		panelWidth = a.width
		panelOffset = 0
	} else if panelIdx == 1 {
		panelWidth = rightWidth
		panelOffset = leftWidth
	}
	if panelWidth <= 0 {
		panelWidth = max(24, max(a.width/2, a.width))
		panelOffset = 0
	}
	return panelWidth, panelHeight, headerOffset, panelOffset, centered
}

func (a *App) configureModalWindow(modal *Modal, content interface{}, panelIdx int, background string, fallbackWinW, fallbackWinH int) {
	panelWidth, panelHeight, headerOffset, panelOffset, centered := a.modalPanelMetrics(panelIdx)
	maxFrameWidth := min(panelWidth-2, a.width-4)
	if maxFrameWidth < 6 {
		maxFrameWidth = 6
	}
	maxFrameHeight := min(panelHeight-2, a.height-4)
	if maxFrameHeight < 6 {
		maxFrameHeight = 6
	}
	maxContentWidth := max(1, maxFrameWidth-2)
	maxContentHeight := max(1, maxFrameHeight-2)

	innerW := max(1, fallbackWinW-2)
	innerH := max(1, fallbackWinH-2)
	if fallbackWinW <= 0 || innerW <= 0 {
		innerW = maxContentWidth
	}
	if fallbackWinH <= 0 || innerH <= 0 {
		innerH = maxContentHeight
	}
	if sizer, ok := content.(ModalSizer); ok {
		if prefW, prefH := sizer.PreferredSize(maxContentWidth, maxContentHeight); prefW > 0 || prefH > 0 {
			if prefW > 0 {
				innerW = min(prefW, maxContentWidth)
			}
			if prefH > 0 {
				innerH = min(prefH, maxContentHeight)
			}
		}
	}
	if innerW > maxContentWidth {
		innerW = maxContentWidth
	}
	if innerH > maxContentHeight {
		innerH = maxContentHeight
	}
	winW := innerW + 2
	winH := innerH + 2
	if winW > maxFrameWidth {
		winW = maxFrameWidth
		innerW = max(1, winW-2)
	}
	if winH > maxFrameHeight {
		winH = maxFrameHeight
		innerH = max(1, winH-2)
	}
	if setter, ok := content.(interface{ SetDimensions(int, int) }); ok {
		setter.SetDimensions(innerW, innerH)
	}
	bg := background
	if bg == "" {
		bg, _ = a.renderMainView()
	}
	modal.SetDimensions(a.width, a.height)
	modal.SetWindowed(winW, winH, bg)

	offsetX := panelOffset + max(0, (panelWidth-winW)/2)
	panelUsable := max(0, panelHeight-headerOffset-1)
	offsetY := headerOffset
	if panelUsable > winH {
		offsetY += (panelUsable - winH) / 2
	}
	if offsetY < headerOffset {
		offsetY = headerOffset
	}
	maxOffsetY := max(0, (a.height-1)-winH)
	if offsetY > maxOffsetY {
		offsetY = maxOffsetY
	}
	if centered {
		offsetX = max(0, (a.width-winW)/2)
		offsetY = headerOffset + max(0, (panelHeight-winH)/2)
		if offsetY > maxOffsetY {
			offsetY = maxOffsetY
		}
	}
	modal.SetWindowOffset(offsetX, offsetY)
}

func (a *App) viewResource() tea.Cmd {
	// TODO: Implement resource viewer
	return nil
}

func (a *App) editResource() tea.Cmd {
	// TODO: Implement resource editor
	return nil
}

func (a *App) createNamespace() tea.Cmd {
	return a.createNamespaceForPanel(a.activePanelRef())
}

func (a *App) createNamespaceForPanel(panel *Panel) tea.Cmd {
	if cmd := a.refuseReadOnly("Create namespace", access.CreateNamespaceChecks()...); cmd != nil {
		return cmd
	}
	if a.currentCtx == nil {
		if a.toastLogger != nil {
			a.enqueueCmd(a.toastLogger.Errorf("No active context for namespace creation"))
		}
		return nil
	}
	if panel == nil {
		panel = a.activePanelRef()
	}
	if panel == nil {
		return nil
	}
	if panel.GetCurrentPath() != "/namespaces" {
		if a.toastLogger != nil {
			a.enqueueCmd(a.toastLogger.Errorf("Create namespace is only available at /namespaces"))
		}
		return nil
	}
	a.namespaceCreatePanel = a.panelIndex(panel)
	modal := a.modalManager.modals["namespace_create"]
	if modal == nil {
		a.namespaceCreatePanel = -1
		return nil
	}
	if a.namespaceInput == nil {
		if model, ok := modal.content.(*NamespaceCreateModel); ok {
			a.namespaceInput = model
		} else {
			return nil
		}
	}
	a.namespaceInput.Reset()
	modal.SetContent(a.namespaceInput)
	a.configureModalWindow(modal, a.namespaceInput, a.namespaceCreatePanel, "", 30, 6)
	modal.SetOnClose(func() tea.Cmd {
		a.namespaceInput.Reset()
		return nil
	})
	a.showModal("namespace_create")
	return nil
}

func (a *App) deleteResource() tea.Cmd {
	return a.deleteResourceForPanel(a.activePanelRef())
}

func (a *App) deleteResourceForPanel(panel *Panel) tea.Cmd {
	panelIdx := a.activePanel
	if panel == nil {
		panel = a.activePanelRef()
	} else {
		if idx := a.panelIndex(panel); idx >= 0 {
			panelIdx = idx
		}
	}
	if panel == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	item, ok := panel.SelectedNavItem(ctx)
	cancel()
	if !ok || item == nil {
		return nil
	}
	obj, ok := item.(models.ObjectItem)
	if !ok {
		return nil
	}
	if cmd := a.refuseReadOnly("Delete", access.DeleteChecks(objectAccessTarget(obj))...); cmd != nil {
		return cmd
	}
	target, err := deleteTargetFor(panelIdx, obj)
	if err != nil {
		return a.ShowToast(fmt.Sprintf("Cannot delete %s: %v", kubectlResourceRef(obj.GVR(), obj.Name()), err), 5*time.Second)
	}
	a.pendingDelete = &target

	modal := a.modalManager.modals["delete_confirm"]
	if modal == nil {
		return nil
	}
	if a.deleteConfirm == nil {
		if model, ok := modal.content.(*DeleteConfirmModel); ok {
			a.deleteConfirm = model
		} else {
			return nil
		}
	}
	resourceLabel := kubectlResourceRef(target.gvr, target.name)
	a.deleteConfirm.Configure(resourceLabel, target.namespace, target.context)
	a.deleteConfirm.SetDimensions(max(20, a.width-4), max(5, a.height-6))
	modal.SetContent(a.deleteConfirm)
	modal.SetDimensions(a.width, a.height)
	bg, _ := a.renderMainView()
	winW := min(max(50, a.width/2), a.width-4)
	winH := min(9, a.height-4)
	if winW < 40 {
		winW = 40
	}
	if winH < 6 {
		winH = 6
	}
	modal.SetWindowed(winW, winH, bg)
	modal.SetOnClose(func() tea.Cmd {
		a.pendingDelete = nil
		return nil
	})
	a.showModal("delete_confirm")
	return nil
}

func (a *App) showContextMenu() tea.Cmd {
	return a.showContextMenuForPanel(a.activePanelRef())
}

func (a *App) createNamespaceWithName(name string) tea.Cmd {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if cmd := a.refuseReadOnly("Create namespace", access.CreateNamespaceChecks()...); cmd != nil {
		return cmd
	}
	return a.withBusy("Create namespace", 300*time.Millisecond, func() tea.Msg {
		if a.cl == nil {
			return namespaceCreatedMsg{name: name, err: fmt.Errorf("cluster not ready")}
		}
		ctx, cancel := context.WithTimeout(a.ctx, requestTimeout)
		defer cancel()
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
		if err := a.cl.GetClient().Create(ctx, ns); err != nil {
			return namespaceCreatedMsg{name: name, err: err}
		}
		return namespaceCreatedMsg{name: name}
	})
}

// performDelete deletes the confirmed object instance on the cluster it
// was read from; see executeDelete.
func (a *App) performDelete(target deleteTarget) tea.Cmd {
	checks := access.DeleteChecks(access.Target{Resource: target.gvr, Namespace: target.namespace, Name: target.name})
	if cmd := a.refuseReadOnly("Delete", checks...); cmd != nil {
		return cmd
	}
	root := a.ctx
	return a.withBusy("Delete", 300*time.Millisecond, func() tea.Msg {
		ctx, cancel := context.WithTimeout(root, requestTimeout)
		defer cancel()
		return executeDelete(ctx, target)
	})
}

// Function key action methods
func (a *App) showHelp() tea.Cmd {
	if a.helpViewer == nil || a.modalManager == nil {
		return nil
	}
	modal := a.modalManager.modals["help"]
	if modal == nil {
		return nil
	}
	a.helpViewer.ScrollTop()
	modal.SetDimensions(a.width, a.height)
	bg, _ := a.renderMainView()
	winW := a.width - 8
	if winW < 40 {
		winW = max(20, a.width-2)
	}
	if winW > a.width {
		winW = a.width
	}
	winH := a.height - 4
	if winH < 18 {
		winH = max(10, a.height-2)
	}
	if winH > a.height {
		winH = a.height
	}
	if winW <= 0 {
		winW = max(1, a.width)
	}
	if winH <= 0 {
		winH = max(1, a.height-1)
	}
	modal.SetWindowed(winW, winH, bg)
	offsetX := max(0, (a.width-winW)/2)
	offsetY := max(0, (a.height-winH)/2)
	modal.SetWindowOffset(offsetX, offsetY)
	modal.SetOnClose(func() tea.Cmd {
		a.helpViewer.ScrollTop()
		return nil
	})
	a.showModal("help")
	return nil
}

func (a *App) viewItem() tea.Cmd {
	return a.openViewerForPanel(a.activePanelRef())
}

func (a *App) editItem() tea.Cmd {
	return a.editSelectionForPanel(a.activePanelRef())
}

// openViewerForSelection opens the focused item's viewer when available.
func (a *App) openViewerForSelection() tea.Cmd {
	return a.openViewerForPanel(a.activePanelRef())
}

func (a *App) openViewerForPanel(panel *Panel) tea.Cmd {
	if panel == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	item, ok := panel.SelectedNavItem(ctx)
	if !ok || item == nil {
		return nil
	}
	if _, isBack := item.(models.Back); isBack {
		return nil
	}
	if logs, ok := item.(models.LogsProvider); ok {
		return a.openLogsViewer(item, logs.LogsSpec())
	}
	viewable, ok := item.(models.Viewable)
	if !ok {
		type vc interface {
			ViewContent() (string, string, string, string, string, error)
		}
		if alt, okAlt := item.(vc); okAlt {
			viewable = alt
		} else {
			return nil
		}
	}
	title, body, lang, mime, filename, err := viewable.ViewContent()
	if err != nil {
		if errors.Is(err, models.ErrNoViewContent) {
			return nil
		}
		if a.toastLogger != nil {
			a.enqueueCmd(a.toastLogger.Errorf("View failed: %v", err))
		}
		return nil
	}
	if filename == "" {
		filename = title
	}
	theme := a.viewerTheme()
	var onEdit func() tea.Cmd
	if _, ok := item.(models.ObjectItem); ok {
		onEdit = func() tea.Cmd { return a.editSelectionForPanel(panel) }
	}
	view := NewTextViewer(title, body, lang, mime, filename, theme, onEdit, nil, func() tea.Cmd {
		a.hideModal()
		return nil
	})
	view.SetWrapMode(a.cfg != nil && strings.EqualFold(a.cfg.Viewer.Mode, appconfig.ViewerModeWrap))
	view.SetCommand(itemViewCommand(item))
	view.SetOnOptions(func() tea.Cmd { return a.showViewerOptionsModal(view) })
	modalTitle := a.modalTitleFromItem(item, title)
	modal := NewModal(modalTitle, view)
	modal.SetMode(ModalModeFullscreen)
	modal.SetDimensions(a.width, a.height)
	modal.SetCloseOnSingleEsc(false)
	modal.SetCloseOnEscTimeout(true)
	a.modalManager.Register("yaml_viewer", modal)
	a.showModal("yaml_viewer")
	return nil
}

func (a *App) openLogsViewer(item models.Item, spec models.LogsSpec) tea.Cmd {
	if a.cl == nil {
		if a.toastLogger != nil {
			a.enqueueCmd(a.toastLogger.Errorf("Logs unavailable: no active cluster"))
		}
		return nil
	}
	cfg := rest.CopyConfig(a.cl.GetConfig())
	clientset, err := kubernetes.NewForConfig(rest.AddUserAgent(cfg, "kc-log-viewer"))
	if err != nil {
		if a.toastLogger != nil {
			a.enqueueCmd(a.toastLogger.Errorf("Logs client: %v", err))
		}
		return nil
	}
	// The viewer always follows; the configured defaults fill in the rest.
	// Previous comes from the row.
	spec.Follow = true
	if a.cfg != nil {
		spec = logsSpecWithDefaults(spec, a.cfg.Viewer.Logs)
	}
	parent := a.ctx
	open := func(spec models.LogsSpec) (io.ReadCloser, context.CancelFunc, error) {
		ctx, cancel := context.WithCancel(parent)
		opts := spec.PodLogOptions()
		ctrllog.FromContext(ctx).WithName("logsViewer").Info("client stream logs",
			"gvr", "v1/pods", "namespace", spec.Namespace, "pod", spec.Pod, "container", spec.Container,
			"tailLines", spec.EffectiveTailLines(), "sinceSeconds", spec.SinceSeconds,
			"timestamps", spec.Timestamps, "previous", spec.Previous, "follow", spec.Follow)
		stream, err := clientset.CoreV1().Pods(spec.Namespace).GetLogs(spec.Pod, opts).Stream(ctx)
		if err != nil {
			cancel()
			return nil, nil, err
		}
		return stream, cancel, nil
	}
	title := fmt.Sprintf("logs:%s/%s/%s", spec.Namespace, spec.Pod, spec.Container)
	logsViewer := NewLogsViewer(title, spec, open, a.viewerTheme())
	if lc, ok := item.(models.LogsCommander); ok {
		logsViewer.SetCommandBuilder(lc.LogsCommand)
	} else {
		logsViewer.SetCommand(itemViewCommand(item))
	}
	logsViewer.SetWrapMode(a.cfg != nil && strings.EqualFold(a.cfg.Viewer.Mode, appconfig.ViewerModeWrap))
	logsViewer.SetOnOptions(func() tea.Cmd { return a.showViewerOptionsModal(logsViewer) })
	logsViewer.SetOnClose(func() tea.Cmd {
		a.hideModal()
		return nil
	})
	modalTitle := a.modalTitleFromItem(item, title)
	modal := NewModal(modalTitle, logsViewer)
	modal.SetMode(ModalModeFullscreen)
	modal.SetDimensions(a.width, a.height)
	modal.SetCloseOnSingleEsc(false)
	modal.SetCloseOnEscTimeout(true)
	modal.SetOnClose(func() tea.Cmd {
		logsViewer.Close()
		return nil
	})
	a.modalManager.Register("logs_viewer", modal)
	a.showModal("logs_viewer")
	return logsViewer.Init()
}

// logsSpecWithDefaults applies the configured viewer.logs defaults to spec.
func logsSpecWithDefaults(spec models.LogsSpec, cfg appconfig.LogsViewerConfig) models.LogsSpec {
	if cfg.Tail != 0 {
		spec.TailLines = cfg.Tail
	}
	if secs := int64(cfg.Since.Seconds()); secs > 0 {
		spec.SinceSeconds = secs
	}
	spec.Timestamps = cfg.Timestamps
	return spec
}

func (a *App) modalTitleFromItem(item models.Item, fallback string) string {
	if item != nil {
		if pa, ok := item.(interface{ Path() []string }); ok {
			if segs := pa.Path(); len(segs) > 0 {
				return "/" + strings.Join(segs, "/")
			}
		}
	}
	if fallback != "" {
		return "/" + fallback
	}
	return "/"
}

func (a *App) viewerTheme() string {
	theme := "dracula"
	if a.cfg != nil && a.cfg.Viewer.Theme != "" {
		theme = a.cfg.Viewer.Theme
	}
	return theme
}

// editSelection triggers kubectl edit for the selected object.
func (a *App) editSelection() tea.Cmd {
	return a.editSelectionForPanel(a.activePanelRef())
}

func (a *App) editSelectionForPanel(panel *Panel) tea.Cmd {
	panelIdx := a.activePanel
	if panel == nil {
		panel = a.activePanelRef()
	} else {
		if idx := a.panelIndex(panel); idx >= 0 {
			panelIdx = idx
		}
	}
	if panel == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	item, ok := panel.SelectedNavItem(ctx)
	cancel()
	if !ok || item == nil {
		return nil
	}
	obj, ok := item.(models.ObjectItem)
	if !ok {
		return nil
	}

	path := panel.GetCurrentPath()
	return a.runKubectlEdit(panelIdx, path, obj)
}

func (a *App) runKubectlEdit(panelIdx int, panelPath string, obj models.ObjectItem) tea.Cmd {
	if cmd := a.refuseReadOnly("Edit", access.EditChecks(objectAccessTarget(obj))...); cmd != nil {
		return cmd
	}
	if a.currentCtx == nil || a.currentCtx.Kubeconfig == nil {
		if a.toastLogger != nil {
			return a.toastLogger.Errorf("kubectl edit: no active kubeconfig")
		}
		return a.ShowToast("kubectl edit unavailable: no active kubeconfig", 5*time.Second)
	}

	log := ctrllog.FromContext(a.ctx).WithName("kubectl_edit")

	namespace := strings.TrimSpace(obj.Namespace())
	if namespace == "" {
		namespace = strings.TrimSpace(a.currentCtx.Namespace)
	}
	ec := a.execContext(namespace)
	if ec.Kubeconfig == "" || ec.Context == "" {
		if a.toastLogger != nil {
			return a.toastLogger.Errorf("kubectl edit: active context has no kubeconfig file")
		}
		return a.ShowToast("kubectl edit failed: active context has no kubeconfig file", 5*time.Second)
	}
	resourceRef := kubectlResourceRef(obj.GVR(), obj.Name())
	cmd, commandStr := kubectlEditCommand(ec, resourceRef)

	log.Info("launching kubectl edit", "command", commandStr)
	a.logCommandToTerminal(commandStr)

	// Ensure the UI returns to the primary panel view before launching the editor.
	if a.modalManager != nil {
		for a.modalManager.IsModalVisible() {
			a.hideModal()
		}
	}
	if a.showTerminal {
		a.showTerminal = false
		if a.terminal != nil {
			a.terminal.SetShowPanels(true)
		}
		a.invalidateView("kubectl edit exit terminal")
		a.invalidateFunctionBar("terminal exit")
		a.invalidateTerminalArea("terminal exit")
	}

	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return kubectlEditFinishedMsg{
			err:         err,
			panelIndex:  panelIdx,
			resourceRef: resourceRef,
		}
	})
}

func kubectlResourceRef(gvr schema.GroupVersionResource, name string) string {
	resource := strings.Join([]string{gvr.Resource, gvr.Version, gvr.Group}, ".")
	return fmt.Sprintf("%s/%s", resource, name)
}

func (a *App) applyStartupIntent() tea.Cmd {
	if a.startupIntentApplied || a.startupIntent.Verb == KubectlVerbNone {
		return nil
	}
	a.startupIntentApplied = true
	defer a.recordHistory(0)
	switch a.startupIntent.Verb {
	case KubectlVerbGet:
		return a.applyStartupIntentGet()
	case KubectlVerbLogs:
		return a.applyStartupIntentLogs()
	default:
		return nil
	}
}

// startupSelectMsg retries selecting the objects named on the command line.
type startupSelectMsg struct {
	ids     []string
	path    string
	attempt int
}

const (
	startupSelectRetry    = 250 * time.Millisecond
	startupSelectAttempts = 20
)

// selectStartupRows selects ids in the left panel. The list may still be
// loading, so it retries until all ids are selected, the attempts run out,
// or the user has left the folder.
func (a *App) selectStartupRows(ids []string, path string, attempt int) tea.Cmd {
	if a.leftPanel == nil || a.leftPanel.GetCurrentPath() != path {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	a.leftPanel.SelectRowIDs(ctx, ids)
	cancel()
	if len(a.leftPanel.GetSelectedItems()) >= len(ids) || attempt+1 >= startupSelectAttempts {
		return nil
	}
	return tea.Tick(startupSelectRetry, func(time.Time) tea.Msg {
		return startupSelectMsg{ids: ids, path: path, attempt: attempt + 1}
	})
}

func (a *App) applyStartupIntentGet() tea.Cmd {
	intent := a.startupIntent.Get
	if intent == nil || a.leftNav == nil {
		return nil
	}
	order, groups, err := groupGetTargets(intent)
	if err != nil {
		a.notifyIntentError("kubectl get: %v", err)
		return nil
	}
	ns := a.startupIntentNamespace()
	resolved, warn := a.resolveGetResources(ns, order)
	if len(resolved) == 0 {
		if warn != "" {
			a.notifyIntentError("kubectl get: %s", warn)
		}
		return nil
	}
	if warn != "" {
		a.notifyIntentError("kubectl get: %s", warn)
	}
	if len(order) > 1 {
		idList := make([]string, 0, len(order))
		for _, resName := range order {
			if res, ok := resolved[resName]; ok {
				idList = append(idList, res.rowID)
			}
		}
		if len(idList) == 0 {
			return nil
		}
		return a.selectStartupRows(idList, a.leftPanel.GetCurrentPath(), 0)
	}

	res := resolved[order[0]]
	names := groups[order[0]].Names
	yaml := strings.EqualFold(intent.OutputFormat, "yaml")
	a.setActivePanel(0, "startup get focus left")
	// The type row and the object row may not be there yet on a fresh
	// start; NavigatePanel waits for each of them.
	target := startupTypePath(res)
	if len(names) == 1 {
		target.Elems = append(target.Elems, PathElem{Name: names[0]})
		target.SelectLast = true
	}
	return a.NavigatePanel(0, target, func(err error) tea.Cmd {
		if err != nil {
			a.notifyIntentError("kubectl get %s: %v", res.name, err)
			return nil
		}
		var cmds []tea.Cmd
		if len(names) > 1 {
			cmds = append(cmds, a.selectStartupRows(names, a.leftPanel.GetCurrentPath(), 0))
		}
		if yaml && len(names) > 0 {
			cmds = append(cmds, a.ensureManifestPreview(res, names[0]))
		}
		return tea.Batch(cmds...)
	})
}

// startupLogsTargetMsg carries the container a kc logs intent opens.
type startupLogsTargetMsg struct {
	res    resolvedResource
	intent *LogsIntent
	target logsContainerTarget
	err    error
}

func (a *App) applyStartupIntentLogs() tea.Cmd {
	intent := a.startupIntent.Logs
	if intent == nil || a.leftNav == nil {
		return nil
	}
	ns := strings.TrimSpace(a.startupIntent.Namespace)
	if ns == "" {
		ns = a.currentNamespace()
	}
	if ns == "" {
		ns = corev1.NamespaceDefault
	}
	order := []string{"pods"}
	resolved, warn := a.resolveGetResources(ns, order)
	if warn != "" {
		a.notifyIntentError("kubectl logs: %s", warn)
	}
	res, ok := resolved["pods"]
	if !ok {
		return nil
	}
	if a.cl == nil {
		a.notifyIntentError("kubectl logs %s: cluster not ready", intent.Pod)
		return nil
	}
	// Choosing the container reads the pod from the API, so it runs off
	// the update goroutine; the navigation starts when it is known.
	cl, parent := a.cl, a.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, requestTimeout)
		defer cancel()
		target, err := resolveLogsContainer(ctx, cl, res.namespace, intent)
		return startupLogsTargetMsg{res: res, intent: intent, target: target, err: err}
	}
}

// startStartupLogs walks the left panel to the logs row of the chosen
// container and opens the logs viewer.
func (a *App) startStartupLogs(msg startupLogsTargetMsg) tea.Cmd {
	intent := msg.intent
	if msg.err != nil {
		a.notifyIntentError("kubectl logs %s: %v", intent.Pod, msg.err)
		return nil
	}
	a.setActivePanel(0, "startup logs focus left")
	target := startupTypePath(msg.res)
	target.Elems = append(target.Elems,
		PathElem{Name: intent.Pod},
		PathElem{ID: msg.target.SectionID},
		PathElem{ID: msg.target.Container},
		PathElem{ID: "logs_latest"},
	)
	target.SelectLast = true
	return a.NavigatePanel(0, target, func(err error) tea.Cmd {
		if err != nil {
			a.notifyIntentError("kubectl logs %s: %v", intent.Pod, err)
			return nil
		}
		return a.openLogsViewerForIntent(intent)
	})
}

// startupIntentNamespace is the namespace a kc get intent opens: the
// all-namespaces scope with -A, else -n, else the context's namespace, else
// default.
func (a *App) startupIntentNamespace() string {
	if a.allNamespaces {
		return models.AllNamespaces
	}
	if ns := strings.TrimSpace(a.startupIntent.Namespace); ns != "" {
		return ns
	}
	if ns := a.currentNamespace(); ns != "" {
		return ns
	}
	return corev1.NamespaceDefault
}

func (a *App) currentNamespace() string {
	if a.currentCtx != nil {
		return strings.TrimSpace(a.currentCtx.Namespace)
	}
	return ""
}

func (a *App) resolveResource(resource string) (schema.GroupVersionResource, bool, error) {
	var zero schema.GroupVersionResource
	if a.cl == nil {
		return zero, false, fmt.Errorf("cluster not ready")
	}
	mapper := a.cl.RESTMapper()
	gvr, err := mapper.ResourceFor(schema.GroupVersionResource{Resource: resource})
	if err != nil {
		return zero, false, err
	}
	namespaced := true
	if kind, err := mapper.KindFor(gvr); err == nil {
		if mapping, err := mapper.RESTMapping(kind.GroupKind(), gvr.Version); err == nil && mapping.Scope != nil {
			namespaced = mapping.Scope.Name() == metamapper.RESTScopeNameNamespace
		}
	}
	return gvr, namespaced, nil
}

func resourceRowID(namespace string, gvr schema.GroupVersionResource, namespaced bool) string {
	if namespaced {
		return fmt.Sprintf("%s/%s/%s/%s", namespace, gvr.Group, gvr.Version, gvr.Resource)
	}
	return fmt.Sprintf("%s/%s/%s", gvr.Group, gvr.Version, gvr.Resource)
}

type resolvedResource struct {
	name       string
	gvr        schema.GroupVersionResource
	namespaced bool
	rowID      string
	namespace  string
}

func (a *App) resolveGetResources(namespace string, order []string) (map[string]resolvedResource, string) {
	resolved := make(map[string]resolvedResource, len(order))
	var warn string
	for _, res := range order {
		gvr, namespaced, err := a.resolveResource(res)
		if err != nil {
			if warn == "" {
				warn = fmt.Sprintf("%s: %v", res, err)
			}
			continue
		}
		// namespace is models.AllNamespaces for -A; the callers default
		// an unset namespace.
		resolved[res] = resolvedResource{
			name:       res,
			gvr:        gvr,
			namespaced: namespaced,
			rowID:      resourceRowID(namespace, gvr, namespaced),
			namespace:  namespace,
		}
	}
	return resolved, warn
}

// startupTypePath is the path to the object list of res: the root of its
// namespace (for namespaced types), then the type row. res.namespace is
// models.AllNamespaces for -A, so it is not defaulted here; the intents
// default an unset namespace before they resolve res.
func startupTypePath(res resolvedResource) PanelPath {
	p := PanelPath{Anchor: AnchorRoot, Elems: []PathElem{{Resource: res.gvr.GroupResource()}}}
	if res.namespaced {
		p.Rescope = true
		p.Namespace = res.namespace
	}
	return p
}

// goToResourceFolderIn opens the object list of res in panel idx.
func (a *App) goToResourceFolderIn(idx int, res resolvedResource) error {
	if a.navigatorFor(idx) == nil {
		return fmt.Errorf("navigation not ready")
	}
	ctx, cancel := context.WithTimeout(a.ctx, requestTimeout)
	defer cancel()
	// Namespaced types live in the root of the panel's namespace scope, so
	// rescope the panel when the target is in another namespace.
	// res.namespace is models.AllNamespaces for -A.
	if res.namespaced {
		nsName := res.namespace
		if nsName == "" {
			nsName = corev1.NamespaceDefault
		}
		if a.panelScope[idx] != nsName {
			a.panelScope[idx] = nsName
			a.replaceNavigator(idx, a.newScopedNavigator(idx))
		}
	}
	nav := a.navigatorFor(idx)
	for nav.HasBack() {
		nav.Back()
	}
	steps := []navui.GoToStep{{SelectionID: res.rowID, Enter: true}}
	if _, err := navui.GoTo(ctx, nav, steps); err != nil {
		return err
	}
	a.syncPanelWithNavigator(idx)
	return nil
}

// ensureManifestPreview shows the manifest of object name in the right
// panel.
func (a *App) ensureManifestPreview(res resolvedResource, name string) tea.Cmd {
	if a.rightNav == nil || name == "" {
		return nil
	}
	target := startupTypePath(res)
	target.Elems = append(target.Elems, PathElem{Name: name})
	target.SelectLast = true
	return a.NavigatePanel(1, target, func(err error) tea.Cmd {
		if err != nil {
			a.notifyIntentError("manifest %s/%s: %v", res.name, name, err)
			return nil
		}
		ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
		defer cancel()
		return a.setPanelMode(ctx, a.rightPanel, PanelModeManifest, "manifest preview mode")
	})
}

func (a *App) notifyIntentError(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if a.toastLogger != nil {
		a.enqueueCmd(a.toastLogger.Errorf("%s", msg))
	}
}

type logsContainerTarget struct {
	Container string
	SectionID string
}

const (
	logSectionContainers = "containers"
	logSectionInit       = "init"
	logSectionEphemeral  = "ephemeral"
)

func resolveLogsContainer(ctx context.Context, cl *kccluster.Cluster, namespace string, intent *LogsIntent) (logsContainerTarget, error) {
	var zero logsContainerTarget
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	obj, err := cl.GetByGVR(ctx, gvr, namespace, intent.Pod)
	if err != nil {
		return zero, err
	}
	var pod corev1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &pod); err != nil {
		return zero, err
	}
	return choosePodContainer(&pod, intent.Container)
}

func choosePodContainer(pod *corev1.Pod, requested string) (logsContainerTarget, error) {
	var zero logsContainerTarget
	req := strings.TrimSpace(requested)
	if req != "" {
		if containsContainer(pod.Spec.Containers, req) {
			return logsContainerTarget{Container: req, SectionID: logSectionContainers}, nil
		}
		if containsContainer(pod.Spec.InitContainers, req) {
			return logsContainerTarget{Container: req, SectionID: logSectionInit}, nil
		}
		if containsEphemeral(pod.Spec.EphemeralContainers, req) {
			return logsContainerTarget{Container: req, SectionID: logSectionEphemeral}, nil
		}
		return zero, fmt.Errorf("container %q not found in pod %s", req, pod.Name)
	}
	if len(pod.Spec.Containers) == 1 {
		return logsContainerTarget{Container: pod.Spec.Containers[0].Name, SectionID: logSectionContainers}, nil
	}
	if len(pod.Spec.Containers) == 0 && len(pod.Spec.InitContainers) == 1 {
		return logsContainerTarget{Container: pod.Spec.InitContainers[0].Name, SectionID: logSectionInit}, nil
	}
	if len(pod.Spec.Containers) == 0 && len(pod.Spec.EphemeralContainers) == 1 {
		return logsContainerTarget{Container: pod.Spec.EphemeralContainers[0].Name, SectionID: logSectionEphemeral}, nil
	}
	if len(pod.Spec.Containers) == 0 && len(pod.Spec.InitContainers) == 0 && len(pod.Spec.EphemeralContainers) == 0 {
		return zero, fmt.Errorf("pod %s has no containers", pod.Name)
	}
	names := make([]string, 0, len(pod.Spec.Containers))
	for _, c := range pod.Spec.Containers {
		names = append(names, c.Name)
	}
	return zero, fmt.Errorf("pod %s has multiple containers (%s); specify -c", pod.Name, strings.Join(names, ", "))
}

func containsContainer(list []corev1.Container, name string) bool {
	for _, c := range list {
		if c.Name == name {
			return true
		}
	}
	return false
}

func containsEphemeral(list []corev1.EphemeralContainer, name string) bool {
	for _, c := range list {
		if c.Name == name {
			return true
		}
	}
	return false
}

func (a *App) openLogsViewerForIntent(intent *LogsIntent) tea.Cmd {
	if intent == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()
	item, ok := a.leftPanel.SelectedNavItem(ctx)
	if !ok || item == nil {
		return nil
	}
	logsProvider, ok := item.(models.LogsProvider)
	if !ok {
		return nil
	}
	spec := logsProvider.LogsSpec()
	spec.Follow = intent.Follow
	return a.openLogsViewer(item, spec)
}

func (a *App) logCommandToTerminal(command string) {
	if command == "" {
		return
	}
	fmt.Fprintf(os.Stdout, "\n[kc] %s\n", command)
}

func (a *App) refreshPanelAfterEdit(panelIdx int) {
	var panel *Panel
	var nav *navui.Navigator
	if panelIdx == 0 {
		panel = a.leftPanel
		nav = a.leftNav
	} else {
		panel = a.rightPanel
		nav = a.rightNav
	}

	if nav != nil {
		if rf, ok := nav.Current().(interface{ Refresh() }); ok {
			rf.Refresh()
		}
	}

	if panel != nil {
		ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
		panel.RefreshFolder(ctx)
		cancel()
	}
}

func (a *App) copyItem() tea.Cmd {
	return a.copyItemForPanel(a.activePanelRef())
}

func (a *App) renameMoveItem() tea.Cmd {
	// TODO: Implement rename/move functionality (F6)
	return nil
}

func (a *App) copyItemForPanel(panel *Panel) tea.Cmd {
	if panel == nil {
		panel = a.activePanelRef()
	}
	if panel == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	item, ok := panel.SelectedNavItem(ctx)
	cancel()
	if !ok || item == nil {
		return nil
	}
	if _, isBack := item.(models.Back); isBack {
		return nil
	}
	if cmd, ok := a.startTransfer(panel); ok {
		return cmd
	}
	if _, ok := item.(*models.PodFSDirItem); ok {
		return a.ShowToast("To copy a directory, open a local directory in the other panel with M-l", 4*time.Second)
	}

	if logs, ok := item.(models.LogsProvider); ok {
		if a.cl == nil {
			if a.toastLogger != nil {
				a.enqueueCmd(a.toastLogger.Errorf("Copy unavailable: cluster not ready"))
			}
			return nil
		}
		spec := logs.LogsSpec()
		if spec.TailLines <= 0 {
			spec.TailLines = models.DefaultLogsTailLines
		}
		subject := fmt.Sprintf("logs %s/%s/%s", spec.Namespace, spec.Pod, spec.Container)
		filename := sanitizeFilename(fmt.Sprintf("%s-%s.log", spec.Pod, spec.Container))
		if filename == "" {
			filename = "logs.txt"
		}
		req := &copyRequest{
			subject:  subject,
			filename: filename,
			fetch: func(ctx context.Context) (io.ReadCloser, error) {
				specCopy := spec
				specCopy.Follow = false
				return a.fetchLogsSnapshot(ctx, specCopy)
			},
		}
		return a.showCopyDialog(req)
	}

	viewable, ok := item.(models.Viewable)
	if !ok {
		type viewContentProvider interface {
			ViewContent() (string, string, string, string, string, error)
		}
		if alt, ok := item.(viewContentProvider); ok {
			viewable = alt
		} else {
			return nil
		}
	}
	title, body, _, _, filename, err := viewable.ViewContent()
	if err != nil {
		if errors.Is(err, models.ErrNoViewContent) {
			return nil
		}
		if a.toastLogger != nil {
			a.enqueueCmd(a.toastLogger.Errorf("Copy failed: %v", err))
		}
		return nil
	}
	if filename == "" {
		filename = filepath.Base(a.modalTitleFromItem(item, title))
	}
	filename = sanitizeFilename(filename)
	if filename == "" {
		filename = "resource.txt"
	}
	return a.showCopyDialog(viewContentCopyRequest(a.modalTitleFromItem(item, title), filename, body))
}

// viewContentCopyRequest copies a viewer body as is. The body may be raw
// bytes (binary ConfigMap and Secret keys), so it must not be transformed.
func viewContentCopyRequest(subject, filename, body string) *copyRequest {
	data := []byte(body)
	return &copyRequest{
		subject:  subject,
		filename: filename,
		fetch: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(data)), nil
		},
	}
}

func (a *App) showCopyDialog(req *copyRequest) tea.Cmd {
	if req == nil || req.fetch == nil || a.copyInput == nil || a.modalManager == nil {
		return nil
	}
	modal := a.modalManager.modals["copy_local"]
	if modal == nil {
		return nil
	}
	dir := a.defaultCopyDir()
	target := filepath.Join(dir, req.filename)
	a.copyInput.Configure(req.subject, target)
	a.configureModalWindow(modal, a.copyInput, -1, "", 0, 0)
	modal.SetOnClose(func() tea.Cmd {
		a.pendingCopy = nil
		if a.copyInput != nil {
			a.copyInput.BlurInputs()
		}
		return nil
	})
	a.pendingCopy = req
	a.showModal("copy_local")
	return a.copyInput.FocusPath()
}

func (a *App) defaultCopyDir() string {
	if strings.TrimSpace(a.lastCopyDir) != "" {
		return a.lastCopyDir
	}
	if wd, err := os.Getwd(); err == nil {
		if abs, err := filepath.Abs(wd); err == nil {
			return abs
		}
		return wd
	}
	if abs, err := filepath.Abs("."); err == nil {
		return abs
	}
	return "."
}

func (a *App) executeCopyRequest(path string) tea.Cmd {
	req := a.pendingCopy
	a.pendingCopy = nil
	if req == nil {
		return nil
	}
	target := strings.TrimSpace(path)
	if target == "" {
		return nil
	}
	return a.withBusy("Copy", 300*time.Millisecond, func() tea.Msg {
		err := a.copyToPath(req, target)
		return copyFinishedMsg{path: target, subject: req.subject, err: err}
	})
}

func (a *App) copyToPath(req *copyRequest, dest string) error {
	dir := filepath.Dir(dest)
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("destination %q is not a directory", dir)
	}
	ctx, cancel := context.WithTimeout(a.ctx, copyRequestTimeout)
	defer cancel()
	reader, err := req.fetch(ctx)
	if err != nil {
		return err
	}
	defer reader.Close()
	// The destination changes only after the whole stream was written, and a
	// new export is private: it may hold decoded Secret values.
	return atomicfile.Write(ctx, dest, reader)
}

type copyRequest struct {
	subject  string
	filename string
	fetch    func(ctx context.Context) (io.ReadCloser, error)
}

type copyFinishedMsg struct {
	path    string
	subject string
	err     error
}

func sanitizeFilename(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "." || base == string(filepath.Separator) || base == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range base {
		switch {
		case r == '.' || r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "._")
	if out == "" {
		return ""
	}
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}

func (a *App) fetchLogsSnapshot(ctx context.Context, spec models.LogsSpec) (io.ReadCloser, error) {
	if a.cl == nil {
		return nil, fmt.Errorf("cluster not ready")
	}
	cfg := rest.CopyConfig(a.cl.GetConfig())
	clientset, err := kubernetes.NewForConfig(rest.AddUserAgent(cfg, "kc-copy-logs"))
	if err != nil {
		return nil, err
	}
	if spec.Namespace == "" || spec.Pod == "" {
		return nil, fmt.Errorf("logs spec incomplete")
	}
	opts := spec.PodLogOptions()
	opts.Follow = false
	req := clientset.CoreV1().Pods(spec.Namespace).GetLogs(spec.Pod, opts)
	return req.Stream(ctx)
}

// ErrUnknownContext reports a --context name that no loaded kubeconfig
// defines.
var ErrUnknownContext = errors.New("unknown context")

// RunConfig allows callers to customize the UI startup.
type RunConfig struct {
	// Context, when non-empty, is the kubeconfig context kc starts in
	// instead of the current context. kubeconfig is not changed.
	Context string
	// Namespace is the namespace both panels start in (-n).
	Namespace string
	// AllNamespaces starts both panels in the all-namespaces scope (-A). It
	// wins over Namespace, as in kubectl.
	AllNamespaces bool
	StartupIntent StartupIntent
	// DebugLogPath, when non-empty, points to the UI log file (e.g., ~/.kc/debug.log).
	DebugLogPath string
	// SwitchToFileLogger is called right before the UI program starts. Allows callers
	// to stop logging to stderr once bubbletea takes over the terminal.
	SwitchToFileLogger func()
	ImpersonateUser    string
	ImpersonateUID     string
	ImpersonateGroups  []string
	// ReadOnly is the command line's read-only choice: --readonly forces it
	// on, --write forces it off, neither leaves it to the config.
	ReadOnly ReadOnlyOverride
}

// Run starts the application
func Run(ctx context.Context, cfg RunConfig) error {
	ctx = ctrllog.IntoContext(ctx, ctrllog.Log.WithName("startup"))
	log := ctrllog.FromContext(ctx)
	app := NewApp()
	app.namespaceOverride = strings.TrimSpace(cfg.Namespace)
	app.contextOverride = strings.TrimSpace(cfg.Context)
	app.allNamespaces = cfg.AllNamespaces
	if app.allNamespaces {
		app.namespaceOverride = ""
	}
	app.startupIntent = cfg.StartupIntent
	app.impersonateUser = strings.TrimSpace(cfg.ImpersonateUser)
	app.impersonateUID = strings.TrimSpace(cfg.ImpersonateUID)
	app.impersonateGroups = append([]string(nil), cfg.ImpersonateGroups...)
	app.readOnlyOverride = cfg.ReadOnly

	// Initialize data model (best-effort; UI can still run without it)
	log.Info("initializing data")
	if err := app.initData(ctx); err != nil {
		if errors.Is(err, ErrUnknownContext) {
			// A wrong --context is a usage error: stop before the UI
			// takes over the terminal.
			app.cancel()
			return err
		}
		log.Error(err, "initialization warning")
		fmt.Printf("Data init warning: %v\n", err)
	}
	if cfg.DebugLogPath != "" {
		log.Info("initialization complete, launching UI; check debug.log for further log output while the UI is running", "debugLogPath", cfg.DebugLogPath)
	} else {
		log.Info("initialization complete, launching UI")
	}

	// Create program with proper options
	p := tea.NewProgram(
		app,
		tea.WithoutSignalHandler(), // Handle signals ourselves
	)

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		// Send quit message to the program
		p.Quit()
	}()

	if cfg.SwitchToFileLogger != nil {
		cfg.SwitchToFileLogger()
		// Refresh contexts so logs use the file-only, unnamed logger (no lingering "startup" name).
		baseLog := ctrllog.Log
		ctx = ctrllog.IntoContext(ctx, baseLog)
		app.ctx = ctrllog.IntoContext(app.ctx, baseLog)
		log = ctrllog.FromContext(ctx)
	}

	// Ensure terminal is reset on exit
	defer func() {
		// Delete the pod of an open node shell while the context still
		// carries the logger.
		app.closeNodeShell()
		// Close the embedded terminal/PTY to restore its raw mode
		if app.terminal != nil {
			_ = app.terminal.Close()
		}
		// Stop background resources
		if app.discoveryCancel != nil {
			app.discoveryCancel()
			app.discoveryCancel = nil
		}
		app.stopPortForwards()
		if app.clPool != nil {
			app.clPool.Stop()
		}
		app.closeKubeconfigFiles()
		app.cancel()
	}()

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running application: %v\n", err)
		return err
	}

	return nil
}

// initData discovers kubeconfigs, selects current context, starts cluster/cache and wires navigation.
func (a *App) initData(ctx context.Context) error {
	log := ctrllog.FromContext(ctx).WithName("init")
	// Kubeconfig manager and discovery
	a.kubeMgr = kubeconfig.NewManager()
	log.Info("discovering kubeconfigs")
	if err := a.kubeMgr.DiscoverKubeconfigs(); err != nil {
		// Log and show toast
		if a.toastLogger != nil {
			a.enqueueCmd(a.toastLogger.Errorf("Kubeconfig discovery failed: %v", err))
		}
		log.Error(err, "failed to discover kubeconfigs")
		return fmt.Errorf("discover kubeconfigs: %w", err)
	}
	log.Info("kubeconfigs discovered", "count", len(a.kubeMgr.GetKubeconfigs()), "contexts", len(a.kubeMgr.GetContexts()))
	if raw, err := clientcmd.NewDefaultClientConfigLoadingRules().Load(); err == nil {
		a.kubectlCtx = raw.CurrentContext
	}
	// Select the --context, else the current context (prefer env KUBECONFIG
	// first path).
	if name := strings.TrimSpace(a.contextOverride); name != "" {
		c, err := a.startupContext(name)
		if err != nil {
			log.Error(err, "requested context not found", "context", name)
			return err
		}
		a.currentCtx = c
	} else {
		a.currentCtx = a.selectCurrentContext()
	}
	if a.currentCtx == nil {
		log.Error(nil, "no current context found")
		return fmt.Errorf("no current context found")
	}
	ctxNamespace := strings.TrimSpace(a.currentCtx.Namespace)
	override := strings.TrimSpace(a.namespaceOverride)
	if override != "" {
		log.Info("namespace override requested", "requested", override, "original", ctxNamespace)
		ctxNamespace = override
		a.currentCtx.Namespace = override
	}
	nsLog := ctxNamespace
	if nsLog == "" {
		nsLog = "(cluster)"
	}
	log.Info("selected context", "name", a.currentCtx.Name, "cluster", a.currentCtx.Cluster, "namespace", nsLog)
	// Prepare app context and cluster pool; cluster will be started via pool.Acquire
	a.cancel()
	a.ctx, a.cancel = context.WithCancel(ctx)
	// Forwards live as long as the app context; StopAll ends them on exit.
	a.portForwards = a.newPortForwardRegistry()
	a.clPool = kccluster.NewPool(
		a.cfg.Kubernetes.Clusters.TTL.Duration,
		a.cfg.Kubernetes.Discovery.Refresh.Duration,
	)
	log.Info("starting cluster pool")
	a.clPool.Start()
	k := kccluster.Key{
		KubeconfigPath:       a.currentCtx.Kubeconfig.Path,
		ContextName:          a.currentCtx.Name,
		ImpersonateUser:      strings.TrimSpace(a.impersonateUser),
		ImpersonateUID:       strings.TrimSpace(a.impersonateUID),
		ImpersonateGroupsKey: a.impersonationGroupsKey(),
	}
	log.Info("acquiring cluster", "key", k)
	lease, err := a.clPool.Acquire(a.ctx, k, nil)
	if err != nil {
		log.Error(err, "cluster acquisition failed")
		return fmt.Errorf("cluster pool acquire: %w", err)
	}
	// The app holds its cluster for as long as it is the current one:
	// evicting it would cancel the informers the panels still read.
	a.clLease = lease
	a.cl = lease.Cluster()
	if factory, err := podfs.NewFactory(a.cl.GetConfig()); err != nil {
		log.Error(err, "failed to initialize pod filesystem factory")
		if a.toastLogger != nil {
			a.enqueueCmd(a.toastLogger.Errorf("Pod filesystem unavailable: %v", err))
		}
	} else {
		a.podfsFactory = factory
	}
	log.Info("cluster ready, fetching resource info")
	// Discovery-backed catalog (for panel displays)
	if infos, err := a.cl.GetResourceInfos(); err == nil {
		log.Info("resource infos fetched", "count", len(infos))
		a.leftPanel.SetResourceCatalog(infos)
		a.rightPanel.SetResourceCatalog(infos)
	} else {
		if a.toastLogger != nil {
			a.enqueueCmd(a.toastLogger.Errorf("Discovery resources failed: %v", err))
		}
		log.Error(err, "failed to fetch resource infos")
	}
	a.ensureTermContext()
	// Subscribe to discovery refresh events so resource folders can update dynamically.
	if a.discoveryCh == nil {
		a.discoveryCh = make(chan struct{}, 1)
	}
	if a.discoveryCancel != nil {
		a.discoveryCancel()
	}
	a.discoveryCancel = a.cl.AddDiscoveryListener(func() {
		select {
		case a.discoveryCh <- struct{}{}:
		default:
		}
	})
	a.enqueueCmd(a.watchDiscovery())
	// Legacy generic data sources removed; folders provide data directly
	a.leftPanel.SetViewConfig(a.viewConfig)
	a.rightPanel.SetViewConfig(a.viewConfig)
	// Provide contexts count to panels for root display
	a.leftPanel.SetContextCountProvider(func() int { return len(a.kubeMgr.GetContexts()) })
	a.rightPanel.SetContextCountProvider(func() int { return len(a.kubeMgr.GetContexts()) })
	// Initialize per-panel view options from config defaults
	if a.cfg != nil {
		a.leftPanel.SetResourceViewOptions(a.cfg.Resources.ShowNonEmptyOnly, string(a.cfg.Resources.Order))
		a.rightPanel.SetResourceViewOptions(a.cfg.Resources.ShowNonEmptyOnly, string(a.cfg.Resources.Order))
		a.applyResourceOptions(a.leftPanel)
		a.applyResourceOptions(a.rightPanel)
		// Initialize table mode from config defaults
		ctxLeft, cancelLeft := context.WithTimeout(a.ctx, panelContextTimeout)
		a.leftPanel.SetTableMode(ctxLeft, string(a.cfg.Panel.Table.Mode))
		a.leftPanel.SetColumnsMode(ctxLeft, a.cfg.Objects.Columns)
		a.leftPanel.SetObjectOrder(ctxLeft, a.cfg.Objects.Order)
		cancelLeft()
		ctxRight, cancelRight := context.WithTimeout(a.ctx, panelContextTimeout)
		a.rightPanel.SetTableMode(ctxRight, string(a.cfg.Panel.Table.Mode))
		a.rightPanel.SetColumnsMode(ctxRight, a.cfg.Objects.Columns)
		a.rightPanel.SetObjectOrder(ctxRight, a.cfg.Objects.Order)
		cancelRight()
		// Initialize columns mode and objects order from config defaults
		a.syncPanelConfig(a.leftPanel)
		a.syncPanelConfig(a.rightPanel)
	}
	// Preview: Use folder-backed rendering starting at root (not contexts listing)
	{
		// Programmatic navigation to current namespace for both panels
		ns := a.initialNamespace()
		if strings.TrimSpace(a.namespaceOverride) == "" {
			ctxNs := ""
			if a.currentCtx != nil {
				ctxNs = strings.TrimSpace(a.currentCtx.Namespace)
			}
			if ctxNs == "" && ns == corev1.NamespaceDefault {
				log.Info("no namespace set in context; defaulting", "namespace", ns)
			}
		}
		log.Info("initial navigation", "namespace", ns)
		a.goToNamespace(ns)
	}
	log.Info("panel initialization complete")
	return nil
}

// Legacy builder helpers removed (replaced by self-sufficient folders).

// goToNamespace scopes both panels to ns and shows their roots. An empty ns
// is models.AllNamespaces.
func (a *App) goToNamespace(ns string) {
	log := ctrllog.FromContext(a.ctx).WithName("gotoNamespace")
	log.Info("scoping panels", "namespace", ns)
	a.panelScope = [2]string{ns, ns}
	a.replaceNavigator(0, a.newScopedNavigator(0))
	a.replaceNavigator(1, a.newScopedNavigator(1))
	if a.clPool != nil {
		// Keep at most the cluster-scoped cache plus the current namespace
		// cache. Prune after the old navigators are closed, so the caches
		// their folders held are released and can go now.
		a.clPool.PruneNamespaces(ns)
	}
	a.updateTermContextNamespace(ns)
	a.showNavigatorRoot(0)
	a.showNavigatorRoot(1)
	a.leftPanel.SetFolderNavHandler(func(back bool, selID string, next models.Folder) {
		a.setActivePanel(0, "left panel folder nav focus")
		a.handleFolderNav(back, selID, next)
	})
	a.rightPanel.SetFolderNavHandler(func(back bool, selID string, next models.Folder) {
		a.setActivePanel(1, "right panel folder nav focus")
		a.handleFolderNav(back, selID, next)
	})
	a.recordHistory(0)
	a.recordHistory(1)
	a.scheduleStartupIntent()
}

func (a *App) scheduleStartupIntent() {
	if a.startupIntentScheduled || a.startupIntent.Verb == KubectlVerbNone {
		return
	}
	a.startupIntentScheduled = true
	a.enqueueCmd(func() tea.Msg { return startupIntentMsg{} })
}

func (a *App) initialNamespace() string {
	if a.allNamespaces {
		return models.AllNamespaces
	}
	if ns := strings.TrimSpace(a.namespaceOverride); ns != "" {
		return ns
	}
	if a.currentCtx != nil {
		if ns := strings.TrimSpace(a.currentCtx.Namespace); ns != "" {
			return ns
		}
	}
	return corev1.NamespaceDefault
}

// handleFolderNav processes back/forward navigation from panels and updates both panels.
// currentNav returns the navigator for the active panel (left=0, right=1).
func (a *App) currentNav() *navui.Navigator {
	if a.activePanel == 0 {
		return a.leftNav
	}
	return a.rightNav
}

func (a *App) navigatorDepth(nav *navui.Navigator) int {
	if nav == nil {
		return 0
	}
	count := 0
	nav.ForEach(func(models.Folder) { count++ })
	return count
}

func (a *App) syncPanelWithNavigator(panelIdx int) {
	var nav *navui.Navigator
	var panel *Panel
	switch panelIdx {
	case 0:
		nav = a.leftNav
		panel = a.leftPanel
	case 1:
		nav = a.rightNav
		panel = a.rightPanel
	}
	if nav == nil || panel == nil {
		return
	}
	cur := nav.Current()
	hasBack := nav.HasBack()
	ctxSet, cancelSet := context.WithTimeout(a.ctx, panelContextTimeout)
	a.setPanelFolder(ctxSet, panelIdx, cur, hasBack)
	cancelSet()
	panel.SetCurrentPath(a.navigatorPath(nav))
	ctxUse, cancelUse := context.WithTimeout(a.ctx, panelContextTimeout)
	panel.UseFolder(ctxUse, true)
	cancelUse()
}

func (a *App) handleFolderNav(back bool, selID string, next models.Folder) {
	var nav *navui.Navigator
	var panelSet func(context.Context, models.Folder, bool)
	var panelSelectByID func(context.Context, string)
	var panelReset func(context.Context)
	var panelRef *Panel
	if a.activePanel == 0 {
		if a.leftNav == nil {
			a.leftNav = a.newScopedNavigator(0)
		}
		nav = a.leftNav
		panelSet = func(ctx context.Context, folder models.Folder, hasBack bool) {
			a.setPanelFolder(ctx, 0, folder, hasBack)
		}
		panelSelectByID = func(ctx context.Context, id string) { a.leftPanel.SelectByRowID(ctx, id) }
		panelReset = func(ctx context.Context) { a.leftPanel.ResetSelectionTop(ctx) }
		panelRef = a.leftPanel
	} else {
		if a.rightNav == nil {
			a.rightNav = a.newScopedNavigator(1)
		}
		nav = a.rightNav
		panelSet = func(ctx context.Context, folder models.Folder, hasBack bool) {
			a.setPanelFolder(ctx, 1, folder, hasBack)
		}
		panelSelectByID = func(ctx context.Context, id string) { a.rightPanel.SelectByRowID(ctx, id) }
		panelReset = func(ctx context.Context) { a.rightPanel.ResetSelectionTop(ctx) }
		panelRef = a.rightPanel
	}
	a.cancelJump(a.activePanel)
	if back {
		nav.Back()
	} else if next != nil {
		// Pre-warm the next folder in background to trigger informer/lister start.
		// This shows a spinner if it takes longer than the delay and avoids UI freeze.
		a.enqueueCmd(a.withBusy("Loading", 800*time.Millisecond, func() tea.Msg {
			ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
			defer cancel()
			_ = next.Len(ctx)
			return nil
		}))
		nav.SetSelectionID(selID)
		nav.Push(next)
	}
	cur := nav.Current()
	hasBack := nav.HasBack()
	ctxPanel, cancelPanel := context.WithTimeout(a.ctx, panelContextTimeout)
	panelSet(ctxPanel, cur, hasBack)
	cancelPanel()
	// Update breadcrumbs from navigator state
	if a.activePanel == 0 {
		a.leftPanel.SetCurrentPath(a.navigatorPath(nav))
	} else {
		a.rightPanel.SetCurrentPath(a.navigatorPath(nav))
	}
	if back {
		id := nav.CurrentSelectionID()
		if id != "" {
			ctxSel, cancelSel := context.WithTimeout(a.ctx, panelContextTimeout)
			panelSelectByID(ctxSel, id)
			cancelSel()
		} else {
			ctxReset, cancelReset := context.WithTimeout(a.ctx, panelContextTimeout)
			panelReset(ctxReset)
			cancelReset()
		}
	} else {
		ctxReset, cancelReset := context.WithTimeout(a.ctx, panelContextTimeout)
		panelReset(ctxReset)
		cancelReset()
	}
	a.applyResourceOptions(panelRef)
	a.syncTerminalNamespaceForNavigator(nav)
	a.recordHistory(a.activePanel)
}

// namespaceExists returns true if the namespace exists in the current cluster.
func (a *App) namespaceExists(ns string) bool {
	if ns == "" {
		return false
	}
	if a.cl == nil {
		return false
	}
	gvk := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Namespace"}
	gvr, err := a.cl.GVKToGVR(gvk)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(a.ctx, requestTimeout)
	defer cancel()
	if _, err := a.cl.GetByGVR(ctx, gvr, "", ns); err != nil {
		return false
	}
	return true
}

// startupContext returns the context called name. When several kubeconfig
// files define it, the first one wins, as when kubectl merges $KUBECONFIG.
func (a *App) startupContext(name string) (*kubeconfig.Context, error) {
	if c := a.kubeMgr.GetContextByName(name); c != nil {
		return c, nil
	}
	paths := make([]string, 0, len(a.kubeMgr.GetKubeconfigs()))
	for _, kc := range a.kubeMgr.GetKubeconfigs() {
		paths = append(paths, kc.Path)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("%w %q (no kubeconfig loaded)", ErrUnknownContext, name)
	}
	return nil, fmt.Errorf("%w %q (kubeconfig: %s)", ErrUnknownContext, name, strings.Join(paths, string(os.PathListSeparator)))
}

// selectCurrentContext prefers $KUBECONFIG current-context, else any current-context, else first discovered.
func (a *App) selectCurrentContext() *kubeconfig.Context {
	if env := os.Getenv("KUBECONFIG"); env != "" {
		for _, p := range strings.Split(env, string(os.PathListSeparator)) {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			for _, kc := range a.kubeMgr.GetKubeconfigs() {
				if sameFilepath(kc.Path, p) {
					if c := a.kubeMgr.GetCurrentContext(kc); c != nil {
						return c
					}
				}
			}
		}
	}
	for _, kc := range a.kubeMgr.GetKubeconfigs() {
		if c := a.kubeMgr.GetCurrentContext(kc); c != nil {
			return c
		}
	}
	cs := a.kubeMgr.GetContexts()
	if len(cs) > 0 {
		return cs[0]
	}
	return nil
}

func sameFilepath(a, b string) bool {
	ap, err1 := filepath.Abs(a)
	bp, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	return ap == bp
}

func (a *App) GetObject(gvk schema.GroupVersionKind, namespace, name string) (map[string]interface{}, error) {
	gvr, err := a.cl.GVKToGVR(gvk)
	if err != nil {
		return nil, err
	}
	obj, err := a.cl.GetByGVR(a.ctx, gvr, namespace, name)
	if err != nil {
		return nil, err
	}
	unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")
	return obj.Object, nil
}

// RESTMapper exposes the app's RESTMapper to viewers for resource→GVK resolution.
func (a *App) RESTMapper() metamapper.RESTMapper { return a.cl.RESTMapper() }

// logMsg appends Bubble Tea messages to a bounded buffer for later inspection.
func (a *App) logMsg(msg tea.Msg) {
	if a == nil {
		return
	}
	if msgLogLimit == 0 {
		return
	}
	entry := describeTeaMsg(msg)
	a.msgLog = append(a.msgLog, entry)
	if overflow := len(a.msgLog) - msgLogLimit; overflow > 0 {
		copy(a.msgLog, a.msgLog[overflow:])
		a.msgLog = a.msgLog[:len(a.msgLog)-overflow]
	}
}

// resetMsgLog clears the diagnostic buffer.
func (a *App) resetMsgLog() {
	if a == nil {
		return
	}
	a.msgLog = nil
}

// msgLogSnapshot copies the current diagnostic log for debugging or tests.
func (a *App) msgLogSnapshot() []string {
	if len(a.msgLog) == 0 {
		return nil
	}
	out := make([]string, len(a.msgLog))
	copy(out, a.msgLog)
	return out
}

func describeTeaMsg(msg tea.Msg) string {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		return fmt.Sprintf("tea.WindowSizeMsg(%dx%d)", m.Width, m.Height)
	case tea.KeyMsg:
		return fmt.Sprintf("tea.KeyMsg(%s)", m.String())
	case tea.MouseMsg:
		return fmt.Sprintf("tea.MouseMsg(%s)", m.String())
	case tea.BatchMsg:
		return fmt.Sprintf("tea.BatchMsg(%d cmds)", len(m))
	case fmt.Stringer:
		return fmt.Sprintf("%T(%s)", msg, m.String())
	case error:
		return fmt.Sprintf("error(%v)", m)
	default:
		return fmt.Sprintf("%T", msg)
	}
}

func (c *cachedView) invalidate() { c.valid = false }

func (c *cachedView) set(view string, cursor *tea.Cursor) {
	c.view = view
	if cursor != nil {
		copy := *cursor
		c.cursor = &copy
	} else {
		c.cursor = nil
	}
	c.valid = true
}

func (c *cachedView) get() (string, *tea.Cursor, bool) {
	if !c.valid {
		return "", nil, false
	}
	var cursorCopy *tea.Cursor
	if c.cursor != nil {
		copy := *c.cursor
		cursorCopy = &copy
	}
	return c.view, cursorCopy, true
}

func (a *App) invalidateView(reason string) {
	if a == nil {
		return
	}
	if reason != "" {
		ctrllog.FromContext(a.ctx).WithName("ui").Info("view invalidated", "reason", reason)
	}
	a.mainViewCache.invalidate()
}

func (a *App) invalidateFunctionBar(reason string) {
	if a == nil {
		return
	}
	if reason != "" {
		ctrllog.FromContext(a.ctx).WithName("ui").Info("function bar invalidated", "reason", reason)
	}
	if a.functionBar != nil {
		a.functionBar.Invalidate()
	}
}

func (a *App) invalidateTerminalArea(reason string) {
	if a == nil {
		return
	}
	if reason != "" {
		ctrllog.FromContext(a.ctx).WithName("ui").Info("terminal area invalidated", "reason", reason)
	}
	a.terminalAreaCache.invalidate()
}

const namespacePlaceholder = "(no namespace)"

func (a *App) swapPanels() {
	// Swap panel pointers
	a.leftPanel, a.rightPanel = a.rightPanel, a.leftPanel

	// Swap navigators
	a.leftNav, a.rightNav = a.rightNav, a.leftNav

	// Swap configs
	a.leftConfig, a.rightConfig = a.rightConfig, a.leftConfig

	// The namespace scope, the history and any jump belong to the content.
	a.panelScope[0], a.panelScope[1] = a.panelScope[1], a.panelScope[0]
	a.history[0], a.history[1] = a.history[1], a.history[0]
	a.cancelJump(0)
	a.cancelJump(1)
	a.syncPanelScopeLabels()

	// Detach old listeners
	if a.leftFolderDirtyCancel != nil {
		a.leftFolderDirtyCancel()
		a.leftFolderDirtyCancel = nil
	}
	if a.rightFolderDirtyCancel != nil {
		a.rightFolderDirtyCancel()
		a.rightFolderDirtyCancel = nil
	}

	// Re-attach to new positions
	if a.leftPanel != nil {
		a.attachFolderDirtyListener(0, a.leftPanel.Folder())
	}
	if a.rightPanel != nil {
		a.attachFolderDirtyListener(1, a.rightPanel.Folder())
	}

	// Re-wire command focus/message handlers to the new sides.
	if a.leftPanel != nil {
		a.leftPanel.SetCommandFocusHandler(func(f bool) tea.Cmd { return a.onCommandFocusChanged(0, f) })
		a.leftPanel.SetMessagePoster(a.postFromPanel(0))
	}
	if a.rightPanel != nil {
		a.rightPanel.SetCommandFocusHandler(func(f bool) tea.Cmd { return a.onCommandFocusChanged(1, f) })
		a.rightPanel.SetMessagePoster(a.postFromPanel(1))
	}

	// Invalidate everything
	a.invalidateView("swap panels")
	a.invalidateFunctionBar("swap panels")

	// Toggle active panel so focus follows the content
	a.activePanel = (a.activePanel + 1) % 2
}

func (a *App) panelNamespaceContext(ctx context.Context, p *Panel) (models.Item, []Item, string, string) {
	if p == nil {
		return nil, nil, "", ""
	}

	item, _ := p.SelectedNavItem(ctx)
	selectedItems := p.GetSelectedItems()
	folderNamespace := p.namespace(ctx)
	activeNamespace := deriveNamespace(item, selectedItems, folderNamespace)

	return item, selectedItems, folderNamespace, activeNamespace
}

func (a *App) startNamespaceCommand(panelIdx int, cfg appconfig.CommandConfig, namespace string) tea.Cmd {
	// A namespace change restarts the command; the context may be read-only
	// by now.
	if refuse := a.refuseCommand(cfg); refuse != nil {
		return refuse
	}
	panel := a.panelByIndex(panelIdx)
	if panel == nil {
		return nil
	}

	panel.InvalidateRenderCache()

	leftWidth, rightWidth, panelHeight, _ := a.panelAreaMetrics()
	frameWidth := leftWidth
	if panelIdx == 1 {
		frameWidth = rightWidth
	}

	a.namespaceCommandTarget[panelIdx] = namespace

	if namespace == "" {
		// Disable watch ticks while out of scope.
		a.commandWatchInterval[panelIdx] = 0
		a.commandWatchToken[panelIdx]++
		bc := cfg.Name
		if cfg.Type == appconfig.CommandTypeNamespace {
			bc = fmt.Sprintf("%s (ns: -)", cfg.Name)
		}
		return panel.ShowCommandPlaceholder(a.ctx, cfg, "Not in namespace scope", bc, frameWidth, panelHeight)
	}

	items := []models.Item{namespaceCommandItem{namespace: namespace}}
	start := panel.StartCommand(a.ctx, cfg, items, schema.GroupVersionResource{}, frameWidth, panelHeight)
	var sched tea.Cmd
	ctxCmd, cancelCmd := context.WithTimeout(a.ctx, panelContextTimeout)
	_ = panel.SetCommandWatchInterval(ctxCmd, cfg.WatchInterval.Duration)
	cancelCmd()
	if cfg.WatchInterval.Duration > 0 {
		sched = a.setCommandWatchInterval(panelIdx, cfg.WatchInterval.Duration)
	}
	return tea.Batch(start, sched)
}

func (a *App) maybeRestartNamespaceCommand(panelIdx int, namespace string) tea.Cmd {
	panel := a.panelByIndex(panelIdx)
	if panel == nil {
		ctrllog.FromContext(a.ctx).WithName("namespaceCommand").V(1).Info("ns-follow: skip (no panel)", "panel", panelIdx)
		return nil
	}
	cfg := panel.CommandConfig()
	if cfg == nil || cfg.Type != appconfig.CommandTypeNamespace {
		ctrllog.FromContext(a.ctx).WithName("namespaceCommand").V(1).Info("ns-follow: skip (no namespaced command)", "panel", panelIdx, "mode", panel.Mode())
		return nil
	}
	if panel.Mode() != PanelModeCommand {
		ctrllog.FromContext(a.ctx).WithName("namespaceCommand").V(1).Info("ns-follow: skip (not in command mode)", "panel", panelIdx, "mode", panel.Mode())
		return nil
	}

	ctrllog.FromContext(a.ctx).WithName("namespaceCommand").V(1).Info("ns-follow: namespace change detected",
		"panel", panelIdx,
		"incoming", namespace,
		"currentTarget", a.namespaceCommandTarget[panelIdx],
		"mode", panel.Mode(),
		"cfg", cfg.Name,
	)

	if cfg.Debounce.Duration > 0 {
		ctrllog.FromContext(a.ctx).WithName("namespaceCommand").V(1).Info("ns-follow: debouncing restart",
			"panel", panelIdx,
			"namespace", namespace,
			"delay", cfg.Debounce.Duration,
			"cfg", cfg.Name,
		)
		return tea.Tick(cfg.Debounce.Duration, func(time.Time) tea.Msg {
			return restartNamespaceCommandMsg{PanelIdx: panelIdx, Namespace: namespace}
		})
	}

	return a.startNamespaceCommand(panelIdx, *cfg, namespace)
}

func (a *App) showContextMenuForPanel(p *Panel) tea.Cmd {
	ctx, cancel := context.WithTimeout(a.ctx, panelContextTimeout)
	defer cancel()

	item, selectedItems, folderNamespace, activeNamespace := a.panelNamespaceContext(ctx, p)

	var otherPanel *Panel
	if a.panelIndex(p) == 0 {
		otherPanel = a.rightPanel
	} else {
		otherPanel = a.leftPanel
	}
	_, _, otherFolderNamespace, otherActiveNamespace := a.panelNamespaceContext(ctx, otherPanel)

	targetNamespace := otherActiveNamespace
	if targetNamespace == "" {
		targetNamespace = otherFolderNamespace
	}
	if targetNamespace == "" {
		// Fall back to the current panel's namespace so the arrow reflects a concrete target
		// when the opposite panel isn't namespaced.
		targetNamespace = activeNamespace
	}
	if targetNamespace == "" {
		targetNamespace = namespacePlaceholder
	}

	ctrllog.FromContext(a.ctx).WithName("commandMenu").V(1).Info("building command list",
		"panel", a.panelIndex(p),
		"folderNamespace", folderNamespace,
		"selectedNamespace", activeNamespace,
		"targetNamespace", targetNamespace,
		"selectedCount", len(selectedItems),
		"activeNamespace", activeNamespace,
		"itemType", fmt.Sprintf("%T", item),
	)

	namespaceForApplicability := targetNamespace
	if namespaceForApplicability == "" {
		namespaceForApplicability = activeNamespace
	}

	// Marked rows win over the highlighted row; see docs/adr/0063.
	targets := commandTargets(item, selectedItems)

	// Filter commands
	// Read-only mode hides dangerous commands.
	policy := a.accessPolicy()
	var available []appconfig.CommandConfig
	for _, cmd := range a.cfg.Commands {
		if !policy.PermitsCommand(cmd.Dangerous) {
			continue
		}
		if isCommandApplicableToTargets(cmd, targets, namespaceForApplicability) {
			available = append(available, cmd)
		}
	}

	builtin := a.objectActionsFor(p)
	if len(available) == 0 && len(builtin.actions) == 0 {
		return nil
	}

	// Create selector
	targetPanel := "right panel"
	targetLeft := false
	if a.panelIndex(p) == 1 {
		targetPanel = "left panel"
		targetLeft = true
	}

	selector := NewCommandSelectorModel(available, targetPanel, targetNamespace, targetLeft, func(cmd appconfig.CommandConfig) tea.Cmd {
		a.hideModal()
		if refuse := a.refuseCommand(cmd); refuse != nil {
			return refuse
		}
		// The menu only offers commands that accept len(targets) rows.
		items := targets

		// Fallback GVR for items that carry none; each object item
		// renders with its own GVR.
		var gvr schema.GroupVersionResource
		if len(items) > 0 {
			if obj, ok := items[0].(models.ObjectItem); ok {
				gvr = obj.GVR()
			}
		}

		runPanel := p
		runPanelIdx := a.panelIndex(p)
		if cmd.Type == appconfig.CommandTypeNamespace && otherPanel != nil {
			runPanel = otherPanel
			runPanelIdx = a.panelIndex(otherPanel)
		}

		if cmd.Type == appconfig.CommandTypeNamespace {
			ns := targetNamespace
			if ns == namespacePlaceholder {
				ns = ""
			}
			return a.startNamespaceCommand(runPanelIdx, cmd, ns)
		}

		leftWidth, rightWidth, panelHeight, _ := a.panelAreaMetrics()
		frameWidth := leftWidth
		if idx := a.panelIndex(runPanel); idx == 1 {
			frameWidth = rightWidth
		}

		start := runPanel.StartCommand(a.ctx, cmd, items, gvr, frameWidth, panelHeight)
		var sched tea.Cmd
		if panelIdx := a.panelIndex(runPanel); panelIdx >= 0 {
			ctxCmd, cancelCmd := context.WithTimeout(a.ctx, panelContextTimeout)
			_ = runPanel.SetCommandWatchInterval(ctxCmd, cmd.WatchInterval.Duration)
			cancelCmd()
			if cmd.WatchInterval.Duration > 0 {
				sched = a.setCommandWatchInterval(panelIdx, cmd.WatchInterval.Duration)
			}
		}
		return tea.Batch(start, sched)
	}, func() tea.Cmd {
		a.hideModal()
		return nil
	})

	selector.AddObjectActions(builtin.actions, func(action objectaction.Action) tea.Cmd {
		a.hideModal()
		return a.prepareObjectAction(builtin, action)
	})

	modal := NewModal("Commands", selector)
	modal.SetCloseOnSingleEsc(true)
	panelIdx := a.panelIndex(p)
	// Rebuild each time so we pick up fresh command lists and selection state.
	a.modalManager.Register("command_menu", modal)
	a.activeCommandSelector = selector
	// Size and position like other panel-scoped modals.
	a.configureModalWindow(modal, selector, panelIdx, "", 48, len(selector.entries)+3)
	modal.SetOnClose(func() tea.Cmd { return nil })
	a.showModal("command_menu")
	return nil
}

type namespaceCommandItem struct {
	namespace string
}

func (n namespaceCommandItem) Columns() (string, []string, []*lipgloss.Style, bool) {
	return "namespace-placeholder", nil, nil, true
}

func (n namespaceCommandItem) Details() string { return "" }

func (n namespaceCommandItem) Path() []string { return nil }

func (n namespaceCommandItem) GVR() schema.GroupVersionResource { return schema.GroupVersionResource{} }

func (n namespaceCommandItem) Namespace() string { return n.namespace }

func (n namespaceCommandItem) Name() string { return n.namespace }

func (n namespaceCommandItem) SupportsVerb(string) bool { return true }

// itemViewCommand returns the kubectl command that prints what the viewer
// shows for item, or "" when the item has none.
func itemViewCommand(item models.Item) string {
	if vc, ok := item.(interface{ ViewCommand() string }); ok {
		return vc.ViewCommand()
	}
	return ""
}
