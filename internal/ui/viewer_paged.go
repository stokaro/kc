package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/pagedfile"
	"github.com/stokaro/kc/internal/ui/viewer"
)

// PagedViewer shows a file too large to read whole (F3): it reads blocks
// as the page needs them, without syntax highlighting. See
// pagedfile and viewer.PagedWidget.
type PagedViewer struct {
	inner                      *viewer.PagedWidget
	width, height              int
	onEdit, onOptions, onClose func() tea.Cmd
	bar                        viewerCommandBar
}

// NewPagedViewer shows view.Source. Reads run under a child of ctx; Close
// cancels them and closes the source.
func NewPagedViewer(ctx context.Context, view models.FileView, theme string, onEdit, onClose func() tea.Cmd) *PagedViewer {
	inner := viewer.NewPaged(ctx, view.Source, view.Size, viewer.Metadata{Title: view.Title, Filename: view.Filename}, pagedfile.Options{})
	inner.SetTheme(theme)
	v := &PagedViewer{inner: inner, onEdit: onEdit, onClose: onClose}
	inner.SetCallbacks(onEdit, nil, onClose)
	return v
}

// Init reads the first page.
func (v *PagedViewer) Init() tea.Cmd { return v.inner.Start() }

// Close cancels pending reads and closes the file. It is safe to call
// more than once.
func (v *PagedViewer) Close() { v.inner.Close() }

// Widget returns the paged widget.
func (v *PagedViewer) Widget() *viewer.PagedWidget { return v.inner }

// SetCommand sets the kubectl command shown under the text.
func (v *PagedViewer) SetCommand(cmd string) { v.bar.command = cmd }

func (v *PagedViewer) SetDimensions(w, h int) {
	v.width, v.height = w, h
	v.inner.Resize(w, h-v.bar.rows(h))
}

func (v *PagedViewer) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == copyAllKey && !v.inner.SearchMode() {
		// Copying a file of any size whole would hold it in memory.
		cmd, _ := v.bar.handle(copyResultMsg{text: "The file is too large to copy whole; F5 saves it", error: true}, pagedBarTarget{}, v.width, v.height)
		return v, cmd
	}
	if cmd, handled := v.bar.handle(msg, pagedBarTarget{searching: v.inner.SearchMode()}, v.width, v.height); handled {
		return v, cmd
	}
	cmd, _ := v.inner.Update(msg)
	return v, cmd
}

// pagedBarTarget is what the command bar asks of the text: a paged view
// has no selection and is never copied whole.
type pagedBarTarget struct{ searching bool }

func (t pagedBarTarget) SearchMode() bool  { return t.searching }
func (t pagedBarTarget) PlainText() string { return "" }
func (t pagedBarTarget) Selecting() bool   { return false }

func (v *PagedViewer) View() tea.View {
	if v.height <= 0 || v.width <= 0 {
		return tea.NewView("")
	}
	rows := v.bar.rows(v.height)
	body := v.inner.View(viewer.Frame{Width: v.width, Height: v.height - rows, Focused: true})
	if rows == 0 {
		return tea.NewView(body)
	}
	return tea.NewView(body + "\n" + v.bar.render(v.width))
}

func (v *PagedViewer) FooterHints() []FooterHint {
	if v.inner.SearchMode() {
		return []FooterHint{
			{Key: "Enter", Label: "OK", Enabled: true},
			{Key: "Tab", Label: "Next field", Enabled: true},
			{Key: "Space", Label: "Toggle", Enabled: true},
			{Key: "Esc", Label: "Cancel", Enabled: true},
		}
	}
	return []FooterHint{
		{Key: "F2", Label: "Options", Enabled: v.onOptions != nil},
		{Key: "F3", Label: "Close", Enabled: v.onClose != nil},
		{Key: "F4", Label: "Edit", Enabled: v.onEdit != nil},
		{Key: "F7", Label: "Search", Enabled: true},
		{Key: "F10", Label: "Close", Enabled: v.onClose != nil},
		{Key: "n", Label: "Next", Enabled: v.inner.HasSearchMatches()},
		{Key: "M-c", Label: "Copy cmd", Enabled: v.bar.command != ""},
	}
}

// FooterStatus shows the search state while there is one, else that the
// file is paged, its size and the position.
func (v *PagedViewer) FooterStatus(width int) string {
	if width <= 0 {
		return ""
	}
	if status := v.inner.FooterStatusText(width); status != "" {
		return status
	}
	return lipgloss.NewStyle().Width(width).Render(truncateStringToWidth(v.inner.StatusText(), width))
}

func (v *PagedViewer) SetTheme(theme string) { v.inner.SetTheme(theme) }

func (v *PagedViewer) Theme() string { return v.inner.Theme() }

func (v *PagedViewer) SetWrapMode(on bool) { v.inner.SetWrapMode(on) }

func (v *PagedViewer) WrapMode() bool { return v.inner.WrapMode() }

func (v *PagedViewer) SetOnOptions(fn func() tea.Cmd) {
	v.onOptions = fn
	v.inner.SetCallbacks(v.onEdit, v.onOptions, v.onClose)
}

func (v *PagedViewer) HandleModalEscape(key tea.KeyMsg) (bool, tea.Cmd) {
	if key.String() != "esc" {
		return false, nil
	}
	if cmd, handled := v.inner.HandleEscape(); handled {
		return true, cmd
	}
	return false, nil
}
