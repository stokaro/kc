package describe

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	tea "charm.land/bubbletea/v2"

	models "github.com/stokaro/kc/internal/models"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
	"github.com/stokaro/kc/internal/ui/panelcontent/reload"
	"github.com/stokaro/kc/internal/ui/viewer"
	describe "github.com/stokaro/kc/pkg/describe"
)

// Widget renders kubectl describe output for the selected object. Describe
// issues API requests, so it loads in the background and refreshes when the
// object changes in the shared cache.
type Widget struct {
	deps     panelcontent.WidgetDeps
	viewer   *viewer.Widget
	sel      panelcontent.Selection
	loader   *reload.Loader
	target   describe.Target
	shownKey string // key of the content in the viewer
}

// New constructs a describe widget backed by the shared viewer.
func New(deps panelcontent.WidgetDeps) *Widget {
	return &Widget{
		deps:   deps,
		viewer: viewer.New("dracula"),
		loader: &reload.Loader{Paused: deps.Paused},
	}
}

func (w *Widget) Init(context.Context) tea.Cmd { return nil }

func (w *Widget) Update(_ context.Context, msg tea.Msg) (tea.Cmd, bool) {
	if res, apply, cmd, handled := w.loader.Update(msg); handled {
		if apply {
			w.show(res)
		}
		if res.Reread {
			cmd = tea.Batch(cmd, panelcontent.RereadDone(res.Err, ""))
		}
		return cmd, true
	}
	return w.viewer.Update(msg)
}

func (w *Widget) View(_ context.Context, frame panelcontent.Frame) tea.View {
	return tea.NewView(w.viewer.View(viewer.Frame{
		Width:   frame.Size.Width,
		Height:  frame.Size.Height,
		Focused: frame.Focused,
	}))
}

func (w *Widget) Resize(_ context.Context, size panelcontent.Size) {
	w.viewer.Resize(size.Width, size.Height)
}

func (w *Widget) SetFocus(_ context.Context, focused bool) { w.loader.SetActive(focused) }

func (w *Widget) Teardown(context.Context) {}

func (w *Widget) Footer(_ context.Context, width int) string {
	return w.viewer.Footer(width)
}

func (w *Widget) FrameInfo(ctx context.Context, req panelcontent.FrameInfoRequest) panelcontent.FrameInfo {
	info := panelcontent.FrameInfo{SuppressFooter: true, TopIndicator: "─", BottomIndicator: "─"}
	breadcrumb := ""
	if w.sel.Item != nil {
		if path := w.sel.Item.Path(); len(path) > 0 {
			breadcrumb = "/" + strings.Join(path, "/")
		}
	}
	if breadcrumb == "" {
		// Nothing to describe: the location of the followed row.
		breadcrumb = w.sel.Path
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
	current, total := w.viewer.Position()
	if total > 0 {
		above, below := w.viewer.ScrollIndicators()
		if above {
			info.TopIndicator = "^"
		}
		if below {
			info.BottomIndicator = "v"
		}
		percent := min(max(int(math.Round(float64(current)*100/float64(total))), 0), 100)
		info.FooterStatus = fmt.Sprintf("%d/%d • %d%%", current, total, percent)
	}
	return info
}

// OnSelectionChanged loads the describe output for a new selection, or
// refreshes it when the selection still names the same object.
func (w *Widget) OnSelectionChanged(_ context.Context, sel panelcontent.Selection) tea.Cmd {
	w.sel = sel
	return w.load()
}

// Refresh reloads the output when the object changed in the shared cache.
func (w *Widget) Refresh(context.Context) tea.Cmd {
	return w.loader.Refresh()
}

// Reread describes the object again at once (C-r), even when its
// resourceVersion did not change: the events in the output may have.
func (w *Widget) Reread(context.Context) tea.Cmd {
	if w.loader.Key() == "" {
		return panelcontent.RereadDone(nil, panelcontent.NothingToReread)
	}
	return w.loader.Reread()
}

// load describes the selected object. A selection that is not an object
// shows the hint: the widget never falls back to another row.
func (w *Widget) load() tea.Cmd {
	target, ok := w.selectionTarget()
	if !ok {
		w.loader.Show("", nil)
		w.setStatic("Select a resource to describe.", "Describe")
		return nil
	}
	if w.deps.Describe == nil {
		w.loader.Show("", nil)
		w.setStatic("Describe provider unavailable.", describeTitle(target, ""))
		return nil
	}
	key := panelcontent.SourceKey(w.sel.Source, targetKey(target))
	if key != w.loader.Key() {
		w.target = target
		// The output of another object stays until the new one arrives
		// (ADR 0047), but not the output read from another connection:
		// the frame already names the new one (ADR 0377).
		if panelcontent.KeySource(w.shownKey) != w.sel.Source {
			w.shownKey = ""
		}
		if w.shownKey == "" {
			w.viewer.SetContent("Loading...", viewer.Metadata{Title: describeTitle(target, "")})
		}
	}
	return w.loader.Show(key, describeFetch(w.deps.Describe(w.sel.Source, target)))
}

// describeFetch skips the describe requests when the cached object still
// has the resourceVersion of the shown output.
func describeFetch(reads panelcontent.DescribeReads) reload.Fetch {
	return func(known string) reload.Result {
		version := ""
		if reads.Version != nil {
			version = reads.Version()
		}
		if version != "" && version == known {
			return reload.Result{Version: version, Unchanged: true}
		}
		if reads.Describe == nil {
			return reload.Result{Err: errors.New("describe provider unavailable")}
		}
		res, err := reads.Describe()
		return reload.Result{Value: res, Version: version, Err: err}
	}
}

func (w *Widget) show(res reload.Result) {
	result, _ := res.Value.(describe.Result)
	title := describeTitle(w.target, result.Title)
	body := result.Body
	meta := viewer.Metadata{Title: title, MimeType: "text/plain"}
	switch {
	case res.Err != nil:
		body = fmt.Sprintf("Describe failed: %v", res.Err)
		meta = viewer.Metadata{Title: title}
	case strings.TrimSpace(body) == "":
		body = "No describe output available."
	}
	key := w.loader.Key()
	if key == w.shownKey {
		w.viewer.UpdateContent(body, meta)
		return
	}
	w.shownKey = key
	w.viewer.SetContent(body, meta)
}

func (w *Widget) setStatic(body, title string) {
	w.shownKey = ""
	w.viewer.SetContent(body, viewer.Metadata{Title: title})
}

// targetKey is the identity of a describe target, with the full GVR.
func targetKey(t describe.Target) string {
	return t.GVR.GroupVersion().String() + "/" + t.GVR.Resource + "/" + t.Namespace + "/" + t.Name
}

func (w *Widget) selectionTarget() (describe.Target, bool) {
	obj, ok := w.sel.Item.(models.ObjectItem)
	if !ok || obj == nil {
		return describe.Target{}, false
	}
	gvr := obj.GVR()
	if gvr.Resource == "" {
		return describe.Target{}, false
	}
	return describe.Target{
		GVR:       gvr,
		Namespace: obj.Namespace(),
		Name:      obj.Name(),
	}, true
}

func describeTitle(target describe.Target, override string) string {
	if override != "" {
		return override
	}
	name := target.Name
	if name == "" {
		name = target.GVR.Resource
	}
	if target.Namespace != "" {
		name = fmt.Sprintf("%s/%s", target.Namespace, name)
	}
	return fmt.Sprintf("Describe %s", name)
}
