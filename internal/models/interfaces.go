package models

import (
	"context"

	"charm.land/lipgloss/v2"
	table "github.com/sttts/kc/internal/table"
	"github.com/sttts/kc/pkg/appconfig"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// Item matches the navigation item contract. Navigation will alias to this type.
type Item interface {
	table.Row
	Details() string
	Path() []string
}

// Confirmation describes a change to the cluster for the user to approve.
type Confirmation struct {
	Title   string
	Details [][2]string // label and value: context, namespace, object
	Notes   []string    // what the change does and what stays behind
	Command string      // the equivalent kubectl command, when there is one
	Action  string      // label of the confirming button
}

// ConfirmedAction is a row whose Enter changes the cluster. The UI shows
// its Confirmation and calls Confirm only after the user agrees.
type ConfirmedAction interface {
	Item
	Confirmation() Confirmation
	Confirm() error
}

// ConfirmRequest asks the UI to confirm and then run Action.
type ConfirmRequest struct{ Action ConfirmedAction }

// ObjectRef names one Kubernetes object by its full GVR.
type ObjectRef struct {
	GVR       schema.GroupVersionResource
	Namespace string
	Name      string
}

// ClusterAction is a change to the cluster that the UI confirms first.
// Unlike ConfirmedAction, Run calls the API server, so the UI runs it off
// the update goroutine.
type ClusterAction interface {
	Confirmation() Confirmation
	Run(ctx context.Context) (ActionResult, error)
}

// ActionResult reports what a ClusterAction did.
type ActionResult struct {
	// Message is shown on the command line.
	Message string
	// Created is an object the action created, for the UI to select.
	Created *ObjectRef
}

// ActionEnv is what a built-in object action needs from the folder that
// lists the object.
type ActionEnv struct {
	// Client reads from the shared cache and writes to the API server.
	Client crclient.Client
	// Context is the kubeconfig context name shown in the dialog.
	Context string
	// Kubectl formats the equivalent kubectl command, adding --context
	// when needed.
	Kubectl func(args ...string) string
}

// ActionEnvProvider is a folder whose objects can run built-in actions.
type ActionEnvProvider interface {
	ActionEnv() (ActionEnv, bool)
}

// ObjectItem identifies items backed by Kubernetes objects.
type ObjectItem interface {
	Item
	GVR() schema.GroupVersionResource
	Namespace() string
	Name() string
	SupportsVerb(string) bool
}

// Folder describes a navigable collection of rows.
type Folder interface {
	table.List
	Columns() []table.Column
	Path() []string
	ItemByID(context.Context, string) (Item, bool)
}

// Enterable identifies rows that can return a child folder when Enter is pressed.
type Enterable interface {
	Item
	Enter() (Folder, error)
}

// Viewable exposes focused content for viewer panes (F3).
type Viewable interface {
	ViewContent() (title, body, lang, mime, filename string, err error)
}

// LogsSpec describes a container log stream.
type LogsSpec struct {
	Namespace string
	Pod       string
	Container string
	Follow    bool
	TailLines int64
}

// DefaultLogsTailLines controls how many lines to fetch before following.
const DefaultLogsTailLines int64 = 200

// LogsProvider identifies rows that can open a streaming logs viewer.
type LogsProvider interface {
	LogsSpec() LogsSpec
}

// Countable reports aggregate information for list-style rows (resource groups, context lists).
type Countable interface {
	Count() int
	Empty() bool
}

// KeyFolder identifies key/value listings such as ConfigMap or Secret data folders.
type KeyFolder interface {
	Folder
	Parent() (schema.GroupVersionResource, string, string)
}

// Back identifies the synthetic ".." entry.
type Back interface {
	Item
	IsBack() bool
}

// ResourceViewConfigurable allows folders to customize resource view toggles at runtime.
type ResourceViewConfigurable interface {
	ApplyResourceViewOptions(showNonEmpty bool, order appconfig.ResourcesViewOrder, favorites []string)
}

// ObjectOrderConfigurable allows folders to override object ordering at runtime.
type ObjectOrderConfigurable interface {
	ApplyObjectOrder(order string)
}

// DirtyObservable folders notify listeners when their data changes.
type DirtyObservable interface {
	RegisterDirtyListener(func()) func()
}

// BackItem renders the synthetic ".." row. It is shown as "/.." like the
// parent directory entry in Midnight Commander.
type BackItem struct{}

var _ Item = BackItem{}
var _ Back = BackItem{}

func (BackItem) Columns() (string, []string, []*lipgloss.Style, bool) {
	style := GreenStyle()
	return "__back__", []string{"/.."}, []*lipgloss.Style{style}, true
}

func (BackItem) Details() string { return "Back" }
func (BackItem) Path() []string  { return nil }
func (BackItem) IsBack() bool    { return true }

// GreenStyle mirrors the navigation helper.
func GreenStyle() *lipgloss.Style {
	s := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	return &s
}

func WhiteStyle() *lipgloss.Style {
	s := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF"))
	return &s
}

// FavoriteResourceStyle highlights favorite resource rows.
func FavoriteResourceStyle() *lipgloss.Style {
	s := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF"))
	return &s
}

func DimStyle() *lipgloss.Style {
	s := lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("#7D7D7D"))
	return &s
}

var (
	deletingRowStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF7B7B"))
	notReadyStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5C5C"))
	jobSuccessStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#59B359"))
	jobFailedStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF5C5C"))
)

// DeletingStyle highlights rows representing objects pending finalization.
func DeletingStyle() *lipgloss.Style { return &deletingRowStyle }

// NotReadyStyle highlights rows where Ready=False.
func NotReadyStyle() *lipgloss.Style { return &notReadyStyle }

// JobSuccessStyle highlights jobs that completed successfully.
func JobSuccessStyle() *lipgloss.Style { return &jobSuccessStyle }

// JobFailedStyle highlights jobs that exhausted retries unsuccessfully.
func JobFailedStyle() *lipgloss.Style { return &jobFailedStyle }
