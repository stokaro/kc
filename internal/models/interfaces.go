package models

import (
	"context"

	"charm.land/lipgloss/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	table "github.com/stokaro/kc/internal/table"
	"github.com/stokaro/kc/pkg/appconfig"
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
	// Local is set when the action changes only what kc runs on this
	// machine, such as stopping a local port forward. Read-only mode
	// allows it.
	Local bool
}

// ConfirmedAction is a row whose Enter changes the cluster (or, with
// Confirmation.Local, only kc's own local state). The UI shows
// its Confirmation and calls Confirm only after the user agrees.
type ConfirmedAction interface {
	Item
	Confirmation() Confirmation
	Confirm() error
}

// ConfirmRequest asks the UI to confirm and then run Action.
type ConfirmRequest struct{ Action ConfirmedAction }

// ConfirmFollowUp is a ConfirmedAction whose approval hands the UI a
// message once Confirm succeeded, for work only the UI can do, such as
// opening a terminal.
type ConfirmFollowUp interface {
	FollowUp() any
}

// NodeShellRequest asks the UI to start a node shell: create Pod in
// Namespace on Node and open its shell.
type NodeShellRequest struct {
	Context   string
	Node      string
	Namespace string
	Pod       string
}

// ObjectRef names one Kubernetes object by its full GVR. Namespaced is
// set by callers that know the resource scope (Helm release manifests);
// others leave it false and use Namespace.
type ObjectRef struct {
	GVR        schema.GroupVersionResource
	Namespaced bool
	Namespace  string
	Name       string
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

// ObjectLink is a row that refers to a live object listed elsewhere, such
// as an object of a Helm release manifest. Enter opens the object in the
// other panel. Missing is non-empty when the object is known not to be in
// the cluster; the UI shows it as a message.
type ObjectLink interface {
	Item
	LinkTarget() (ref ObjectRef, missing string)
}

// OpenObjectRequest asks the UI to show the linked object in the other
// panel.
type OpenObjectRequest struct{ Link ObjectLink }

// ObjectItem identifies items backed by Kubernetes objects.
type ObjectItem interface {
	Item
	GVR() schema.GroupVersionResource
	Namespace() string
	Name() string
	SupportsVerb(string) bool
}

// ObjectOrigin names the cluster connection an object row was read from.
type ObjectOrigin struct {
	Cluster *kccluster.Cluster
	Context string // kubeconfig context name shown to the user
}

// InstanceItem is an ObjectItem that knows which object instance it shows:
// the UID the row was read with and the cluster it came from. A name alone
// also matches an object that was deleted and recreated after the read.
type InstanceItem interface {
	ObjectItem
	UID() types.UID
	Origin() ObjectOrigin
}

// Folder describes a navigable collection of rows.
type Folder interface {
	table.List
	Columns() []table.Column
	Path() []string
	ItemByID(context.Context, string) (Item, bool)
}

// Rereader is a folder the user can ask to read again (C-r, mc's Reread).
// Refresh only marks the rows dirty; Reread also drops what the folder keeps
// between reads and events do not keep current: the API discovery of a list
// of resource types, a list or watch given up after a forbidden answer, the
// spacing between two list loads, the overview's last report. A folder fed
// by the shared cache reads the cache again and starts no new watch.
type Rereader interface {
	Reread(ctx context.Context)
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

// ManifestSource is an object row that can read its full manifest, for
// example to compare it with another object.
type ManifestSource interface {
	ObjectItem
	// ContextName is the kubeconfig context the object lives in.
	ContextName() string
	// Manifest reads the object through the shared cache. It may block on
	// an informer sync, so call it off the UI goroutine. The caller owns
	// the returned object.
	Manifest(ctx context.Context) (*unstructured.Unstructured, error)
}

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
	Parent() (gvr schema.GroupVersionResource, namespace, name string)
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

// NamespaceGroupingConfigurable folders list objects across namespaces and
// can group the rows by namespace or sort them as one list.
type NamespaceGroupingConfigurable interface {
	ApplyNamespaceGrouping(on bool)
	// NamespaceGrouping reports the grouping and whether it applies: a list
	// of one namespace has nothing to group.
	NamespaceGrouping() (on, applies bool)
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

//revive:disable-next-line:function-result-limit the table.Row interface in internal/table fixes these four results
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
