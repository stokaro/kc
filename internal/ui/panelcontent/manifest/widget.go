package manifest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	models "github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
	"github.com/stokaro/kc/internal/ui/panelcontent/reload"
	"github.com/stokaro/kc/internal/ui/viewer"
)

// Widget renders resource manifests using the shared viewer widget. The
// manifest is read in the background, because a cache read waits for the
// informer to sync, and it is reread when the shared cache changed.
type Widget struct {
	deps     panelcontent.WidgetDeps
	viewer   *viewer.Widget
	sel      panelcontent.Selection
	loader   *reload.Loader
	shownKey string // key of the content in the viewer
}

// New constructs a manifest widget backed by the generic viewer.
func New(deps panelcontent.WidgetDeps) *Widget {
	w := viewer.New("dracula")
	return &Widget{
		deps:   deps,
		viewer: w,
		loader: &reload.Loader{Paused: deps.Paused},
	}
}

func (w *Widget) Init(context.Context) tea.Cmd { return nil }

func (w *Widget) Update(ctx context.Context, msg tea.Msg) (tea.Cmd, bool) {
	if m, ok := msg.(tea.WindowSizeMsg); ok {
		width := m.Width
		height := m.Height
		if width < 1 {
			width = 1
		}
		if height < 1 {
			height = 1
		}
		w.viewer.Resize(width, height)
		return nil, true
	}
	if res, apply, cmd, handled := w.loader.Update(msg); handled {
		if apply {
			w.show(res)
		}
		if res.Reread {
			cmd = tea.Batch(cmd, panelcontent.RereadDone(res.Err, ""))
		}
		return cmd, true
	}
	return w.viewersUpdate(msg)
}

func (w *Widget) viewersUpdate(msg tea.Msg) (tea.Cmd, bool) {
	if cmd, handled := w.viewer.Update(msg); handled {
		return cmd, true
	}
	return nil, false
}

func (w *Widget) View(ctx context.Context, frame panelcontent.Frame) tea.View {
	return tea.NewView(w.viewer.View(viewer.Frame{
		Width:   frame.Size.Width,
		Height:  frame.Size.Height,
		Focused: frame.Focused,
	}))
}

func (w *Widget) SetFocus(_ context.Context, focused bool) { w.loader.SetActive(focused) }

func (w *Widget) Teardown(context.Context) {}

func (w *Widget) Footer(ctx context.Context, width int) string {
	if status := strings.TrimSpace(w.viewer.FooterStatusText(width)); status != "" {
		return status
	}
	return w.apiPathForSelection()
}

func (w *Widget) FrameInfo(ctx context.Context, req panelcontent.FrameInfoRequest) panelcontent.FrameInfo {
	info := panelcontent.FrameInfo{TopIndicator: "─", BottomIndicator: "─"}
	breadcrumb := ""
	if w.sel.Item != nil {
		if path := w.sel.Item.Path(); len(path) > 0 {
			breadcrumb = "/" + strings.Join(path, "/")
		}
		if breadcrumb == "" {
			breadcrumb = w.sel.Path
		}
	}
	if breadcrumb == "" && w.deps.Path != nil {
		breadcrumb = w.deps.Path()
	}
	if breadcrumb != "" {
		info.Breadcrumb = breadcrumb
	}
	if w.loader.Loading() || w.loader.Rereading() {
		info.HeaderStatus = panelcontent.LoadingStatus
	}
	above, below := w.viewer.ScrollIndicators()
	if above {
		info.TopIndicator = "^"
	}
	if below {
		info.BottomIndicator = "v"
	}
	api := w.apiPathForSelection()
	if api != "" {
		current, total := w.viewer.Position()
		if total > 0 {
			info.FooterStatus = fmt.Sprintf("%d/%d", current, total)
		}
		info.SuppressFooter = false
	} else {
		info.SuppressFooter = true
	}
	return info
}

func (w *Widget) apiPathForSelection() string {
	if obj, ok := w.sel.Item.(models.ObjectItem); ok && obj != nil {
		return apiPath(obj.GVR(), obj.Namespace(), obj.Name())
	}
	return ""
}

// OnSelectionChanged loads the manifest for a new selection, or refreshes it
// when the selection still names the same item.
func (w *Widget) OnSelectionChanged(ctx context.Context, sel panelcontent.Selection) tea.Cmd {
	w.sel = sel
	return w.load(ctx)
}

// Refresh rereads the manifest; the viewer changes only when the text did.
func (w *Widget) Refresh(context.Context) tea.Cmd {
	return w.loader.Refresh()
}

// Reread reads the manifest again at once (C-r), keeping the scroll
// position when the item is the same.
func (w *Widget) Reread(context.Context) tea.Cmd {
	if w.loader.Key() == "" {
		return panelcontent.RereadDone(nil, panelcontent.NothingToReread)
	}
	return w.loader.Reread()
}

type content struct {
	title, body, lang, mime, filename string
}

func (w *Widget) load(ctx context.Context) tea.Cmd {
	item := w.sel.Item
	if item == nil && w.deps.SelectedItem != nil {
		if i, ok := w.deps.SelectedItem(ctx); ok {
			item = i
		}
	}
	if item == nil {
		w.setStatic("Select a resource to view its manifest.")
		return nil
	}
	viewable, ok := item.(models.Viewable)
	if !ok {
		w.setStatic("Selected resource does not expose manifest content.")
		return nil
	}
	key := w.sel.Source + "|" + itemKey(item)
	if key != w.loader.Key() && w.shownKey == "" {
		w.viewer.SetContent("Loading...", viewer.Metadata{Title: "Manifest"})
	}
	return w.loader.Show(key, func(known string) reload.Result {
		title, body, lang, mime, filename, err := viewable.ViewContent()
		if err != nil {
			return reload.Result{Err: err}
		}
		version := bodyVersion(body)
		if version == known {
			return reload.Result{Version: version, Unchanged: true}
		}
		return reload.Result{
			Value:   content{title: title, body: body, lang: lang, mime: mime, filename: filename},
			Version: version,
		}
	})
}

func (w *Widget) show(res reload.Result) {
	body := ""
	meta := viewer.Metadata{Title: "Manifest"}
	if res.Err != nil {
		body = "Failed to load manifest: " + res.Err.Error()
	} else {
		c, _ := res.Value.(content)
		body = c.body
		meta = viewer.Metadata{Title: c.title, Language: c.lang, MimeType: c.mime, Filename: c.filename}
	}
	key := w.loader.Key()
	if key == w.shownKey {
		w.viewer.UpdateContent(body, meta)
		return
	}
	w.shownKey = key
	w.viewer.SetContent(body, meta)
}

func (w *Widget) setStatic(body string) {
	w.loader.Show("", nil)
	w.shownKey = ""
	w.viewer.SetContent(body, viewer.Metadata{Title: "Manifest"})
}

// itemKey identifies the shown item: the full GVR plus namespace and name for
// objects, the item path for anything else (keys, containers).
func itemKey(item models.Item) string {
	path := strings.Join(item.Path(), "/")
	if obj, ok := item.(models.ObjectItem); ok && obj != nil {
		gvr := obj.GVR()
		return gvr.GroupVersion().String() + "/" + gvr.Resource + "/" + obj.Namespace() + "/" + obj.Name() + "|" + path
	}
	return "path|" + path
}

// bodyVersion is a short fingerprint of the manifest text.
func bodyVersion(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:8])
}
