package panelcontent

import (
	"context"

	tea "charm.land/bubbletea/v2"

	models "github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/pkg/appconfig"
	"github.com/stokaro/kc/pkg/describe"
)

// Size describes the drawable rectangle granted to a widget.
type Size struct {
	Width  int
	Height int
}

// Frame augments Size with focus metadata for rendering.
type Frame struct {
	Size    Size
	Focused bool
}

// Widget is the shared contract implemented by each panel mode.
type Widget interface {
	Init(context.Context) tea.Cmd
	Update(context.Context, tea.Msg) (tea.Cmd, bool)
	View(context.Context, Frame) tea.View
	SetFocus(context.Context, bool)
	Teardown(context.Context)
}

// Factory constructs a widget instance with prepared dependencies.
type Factory func(WidgetDeps) Widget

// WidgetDeps contains cross-cutting helpers supplied by the panel shell.
type WidgetDeps struct {
	InvokeAction     func(context.Context, Action) tea.Cmd
	Path             func() string
	SelectionChanged func(context.Context, Selection) tea.Cmd
	SelectedItem     func(context.Context) (models.Item, bool)
	Describe         DescribeFunc
	// Paused reports whether background refreshes should wait, e.g. while
	// a dialog is open. Nil never pauses.
	Paused func() bool
	Post   func(tea.Msg)
	Config *appconfig.Config
}

// DescribeFunc prepares the reads behind a Describe view. It runs on the UI
// goroutine and must not do I/O; the returned functions run in a tea.Cmd.
type DescribeFunc func(describe.Target) DescribeReads

// DescribeReads are the background reads for one describe target.
type DescribeReads struct {
	// Describe renders kubectl describe output. It issues API requests.
	Describe func() (describe.Result, error)
	// Version returns the object's resourceVersion from the shared cache,
	// or "" when it is unknown. Nil means unknown.
	Version func() string
}

// BackgroundMsg is the result of a widget's background work. The app hands
// it to every widget of both panels, active or not and even behind a dialog,
// so a widget never waits for a result that was swallowed on the way.
type BackgroundMsg interface {
	WidgetBackground()
}

// TerminalMsg carries a message of the terminal image protocol to the
// widgets: the cell size the terminal reported, or a transmit that went
// out (kittygfx.IsMsg). The app hands it to every widget like a
// BackgroundMsg; a widget that draws images takes the ones it sent.
type TerminalMsg struct{ Msg tea.Msg }

// WidgetBackground makes TerminalMsg a BackgroundMsg.
func (TerminalMsg) WidgetBackground() {}

// ImageOwner widgets draw terminal images. ReleaseImages deletes them
// from the terminal and returns the commands to write as raw output, ""
// when there is nothing to delete. The panel calls it when it leaves the
// widget's mode; the widget draws text until it sends new pictures.
type ImageOwner interface {
	ReleaseImages() string
}

// TextViewer widgets that draw terminal images render the same content
// as text only. A still picture of the panel (C-x f) uses it: the widget
// goes on replacing its images, and a still picture must not show them.
type TextViewer interface {
	TextView(context.Context, Frame) tea.View
}

// Refresher widgets reload what they show when the shared cache changed.
// Refresh is cheap to call often; the widget limits how often it reloads.
type Refresher interface {
	Refresh(context.Context) tea.Cmd
}

// Rereader widgets read what they show again when the user asks (C-r). A
// reread starts at once and skips the checks that let Refresh keep the
// shown content. The returned command ends with a RereadDoneMsg.
type Rereader interface {
	Reread(context.Context) tea.Cmd
}

// RereadDoneMsg reports that a reread finished. Err is the read failure;
// Note, when set, replaces the default confirmation.
type RereadDoneMsg struct {
	Err  error
	Note string
}

// NothingToReread is the note of a reread with nothing selected to read.
const NothingToReread = "Nothing to reread"

// RereadDone returns a command that reports a finished reread.
func RereadDone(err error, note string) tea.Cmd {
	return func() tea.Msg { return RereadDoneMsg{Err: err, Note: note} }
}

// Selection represents the widget's current selection identity.
type Selection struct {
	ID   string
	Path string
	Item models.Item
	// Force notifies listeners and other panels even when the ID remains unchanged.
	Force bool
}

// SelectionProvider widgets expose the currently highlighted item.
type SelectionProvider interface {
	CurrentSelectionID(context.Context) string
}

// ItemProvider widgets expose selected row metadata.
type ItemProvider interface {
	CurrentItem(context.Context) (any, bool)
}

// FooterProvider widgets can render a footer row via the panel shell.
type FooterProvider interface {
	Footer(context.Context, int) string
}

// FrameInfoProvider allows widgets to customize panel frame rendering.
type FrameInfoProvider interface {
	FrameInfo(context.Context, FrameInfoRequest) FrameInfo
}

// FrameInfoRequest supplies frame rendering context.
type FrameInfoRequest struct {
	Width int
}

// LoadingStatus is the header status of a widget whose content for the
// current selection has not arrived yet.
const LoadingStatus = "loading..."

// FrameInfo describes breadcrumb overrides and status strings.
type FrameInfo struct {
	Breadcrumb      string
	HeaderStatus    string
	FooterStatus    string
	TopIndicator    string
	BottomIndicator string
	SuppressFooter  bool
}

// SelectionListener widgets opt in to selection change notifications.
// The returned command loads the new content in the background.
type SelectionListener interface {
	OnSelectionChanged(context.Context, Selection) tea.Cmd
}

// MarkGroupRequest asks for mc's Select group (Mark true, the "+" key) or
// Unselect group ("-") dialog on the active panel.
type MarkGroupRequest struct{ Mark bool }

// Action identifies panel-level shortcuts widgets may invoke.
type Action string

const (
	ActionHelp    Action = "help"
	ActionOptions Action = "options"
	ActionView    Action = "view"
	ActionEdit    Action = "edit"
	ActionCreate  Action = "create-namespace"
	ActionDelete  Action = "delete"
	ActionMenu    Action = "menu"
)

// MouseIntent categorizes routed mouse events.
type MouseIntent int

const (
	MouseIntentClick MouseIntent = iota
	MouseIntentWheel
)

// MouseMsg conveys panel-relative mouse details.
type MouseMsg struct {
	Intent MouseIntent
	Row    int
	Button tea.MouseButton
	DeltaY int
}
