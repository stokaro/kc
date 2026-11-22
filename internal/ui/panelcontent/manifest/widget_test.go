package manifest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"

	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

type viewableStub struct {
	body []string
	path []string
}

func (viewableStub) Columns() (string, []string, []*lipgloss.Style, bool) {
	return "stub", []string{"stub"}, nil, true
}

func (viewableStub) Details() string { return "" }

func (v viewableStub) Path() []string { return v.path }

func (v viewableStub) ViewContent() (string, string, string, string, string, error) {
	return "Manifest", strings.Join(v.body, "\n"), "yaml", "text/yaml", "stub.yaml", nil
}

type objectViewableStub struct {
	viewableStub
	gvr schema.GroupVersionResource
	ns  string
	nm  string
}

func (o objectViewableStub) GVR() schema.GroupVersionResource { return o.gvr }
func (o objectViewableStub) Namespace() string                { return o.ns }
func (o objectViewableStub) Name() string                     { return o.nm }
func (objectViewableStub) SupportsVerb(string) bool           { return false }

func TestFrameInfoProvidesIndicatorsAndStatus(t *testing.T) {
	ctx := context.Background()
	w := New(panelcontent.WidgetDeps{})
	_, _ = w.Update(ctx, tea.WindowSizeMsg{Width: 40, Height: 5})

	lines := make([]string, 10)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %02d", i+1)
	}
	item := objectViewableStub{
		viewableStub: viewableStub{
			body: lines,
			path: []string{"namespaces", "default", "pods", "stub"},
		},
		gvr: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"},
		ns:  "default",
		nm:  "stub",
	}
	w.SetFocus(ctx, true)
	cmd := w.OnSelectionChanged(ctx, panelcontent.Selection{
		ID:   "stub",
		Path: "/namespaces/default/pods/stub",
		Item: item,
	})
	if _, handled := w.Update(ctx, cmd()); !handled {
		t.Fatalf("expected the widget to take its load result")
	}

	info := w.FrameInfo(ctx, panelcontent.FrameInfoRequest{Width: 40})
	if info.SuppressFooter {
		t.Fatalf("expected footer to be rendered for manifest mode selection")
	}
	if info.TopIndicator != "─" {
		t.Fatalf("expected top indicator '─', got %q", info.TopIndicator)
	}
	if info.BottomIndicator != "v" {
		t.Fatalf("expected bottom indicator 'v', got %q", info.BottomIndicator)
	}
	if got := info.FooterStatus; got != "5/10" {
		t.Fatalf("expected footer status %q, got %q", "5/10", got)
	}
	expectedBreadcrumb := "/namespaces/default/pods/stub"
	if info.Breadcrumb != expectedBreadcrumb {
		t.Fatalf("expected breadcrumb %q, got %q", expectedBreadcrumb, info.Breadcrumb)
	}
}

// countingViewable returns body and counts ViewContent calls.
type countingViewable struct {
	viewableStub
	body  *string
	calls *int
}

func (c countingViewable) ViewContent() (string, string, string, string, string, error) {
	*c.calls++
	return "Manifest", *c.body, "yaml", "text/yaml", "stub.yaml", nil
}

func TestManifestLoadsInBackgroundAndRefreshKeepsScroll(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	w := New(panelcontent.WidgetDeps{})
	w.loader.After = func(_ time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
		return func() tea.Msg { return fn(time.Now()) }
	}
	_, _ = w.Update(ctx, tea.WindowSizeMsg{Width: 40, Height: 3})
	w.SetFocus(ctx, true)

	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %02d", i+1)
	}
	body := strings.Join(lines, "\n")
	calls := 0
	item := countingViewable{viewableStub: viewableStub{path: []string{"a"}}, body: &body, calls: &calls}

	cmd := w.OnSelectionChanged(ctx, panelcontent.Selection{Item: item})
	if calls != 0 {
		t.Fatalf("ViewContent ran on the UI goroutine")
	}
	if got := w.FrameInfo(ctx, panelcontent.FrameInfoRequest{Width: 40}).HeaderStatus; got != panelcontent.LoadingStatus {
		t.Fatalf("expected loading status, got %q", got)
	}
	_, _ = w.Update(ctx, cmd())
	_, _ = w.Update(ctx, tea.KeyPressMsg{Code: tea.KeyPgDown})
	before, _ := w.viewer.Position()
	if before <= 3 {
		t.Fatalf("expected PgDn to scroll, position %d", before)
	}

	refresh := func() {
		t.Helper()
		tick, _ := w.Update(ctx, w.Refresh(ctx)())
		_, _ = w.Update(ctx, tick())
	}
	lines[0] = "changed"
	body = strings.Join(lines, "\n")
	refresh()
	if calls != 2 {
		t.Fatalf("expected a second read, got %d", calls)
	}
	if !strings.Contains(w.viewer.PlainText(), "changed") {
		t.Fatalf("expected the refreshed manifest in the viewer")
	}
	if after, _ := w.viewer.Position(); after != before {
		t.Fatalf("refresh moved the view from %d to %d", before, after)
	}
}
