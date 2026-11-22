package ui

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/sttts/kc/internal/access"
	models "github.com/sttts/kc/internal/models"
	uistyles "github.com/sttts/kc/internal/ui/styles"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// ssarServer answers SelfSubjectAccessReviews from a fake clientset: verbs
// in deny are denied, everything else is allowed, and fail makes every
// review fail.
type ssarServer struct {
	mu      sync.Mutex
	deny    map[string]bool
	fail    error
	reviews []authorizationv1.ResourceAttributes
}

func (s *ssarServer) reviewer() access.Reviewer {
	cs := fake.NewClientset()
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		review := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview).DeepCopy()
		s.mu.Lock()
		defer s.mu.Unlock()
		s.reviews = append(s.reviews, *review.Spec.ResourceAttributes)
		if s.fail != nil {
			return true, nil, s.fail
		}
		if s.deny[review.Spec.ResourceAttributes.Verb] {
			review.Status.Reason = "RBAC: no rule for " + review.Spec.ResourceAttributes.Verb
			return true, review, nil
		}
		review.Status.Allowed = true
		review.Status.Reason = "RBAC: allowed"
		return true, review, nil
	})
	return access.SSARReviewer{Client: cs.AuthorizationV1()}
}

func (s *ssarServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reviews)
}

// drainCmd runs cmd and every command a batch holds, returning the messages.
func drainCmd(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, drainCmd(t, c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

func newAccessTestApp(t *testing.T, srv *ssarServer) (*App, stubObject) {
	t.Helper()
	app := NewApp()
	t.Cleanup(app.cancel)
	app.width, app.height = 120, 30
	model, _ := app.Update(tea.WindowSizeMsg{Width: app.width, Height: app.height})
	app = model.(*App)
	app.accessState().override = srv.reviewer()

	obj := stubObject{id: "group/v1/tests/web", namespace: "team-a", name: "web", verbs: []string{"get", "update", "patch", "delete"}}
	ctx := t.Context()
	app.leftPanel.SetEnvironmentSupplier(func() PanelEnvironment {
		// The real environment needs a live cluster for edit and delete;
		// keep its Permit wiring and force the rest on.
		env := app.panelEnvironment()
		env.AllowEditObjects = true
		env.AllowDeleteObjects = true
		return env
	})
	app.leftPanel.UseFolder(ctx, true)
	app.leftPanel.SetFolder(ctx, newStubFolder([]models.Item{obj}), false)
	app.leftPanel.SelectByRowID(ctx, obj.id)
	app.activePanel = 0
	return app, obj
}

// settleAccess runs prefetch rounds until no review is left to send.
func settleAccess(t *testing.T, app *App) {
	t.Helper()
	// Update starts the next round itself (a named review after a type-level
	// denial), so run what it returns as the program would.
	pending := []tea.Cmd{app.accessPrefetchCmd()}
	for round := 0; len(pending) > 0; round++ {
		if round > 5 {
			t.Fatal("access prefetch did not settle")
		}
		var next []tea.Cmd
		for _, cmd := range pending {
			for _, msg := range drainCmd(t, cmd) {
				if _, ok := msg.(accessResultMsg); !ok {
					continue // selection observers and the like
				}
				_, c := app.Update(msg)
				if c != nil {
					next = append(next, c)
				}
			}
		}
		pending = next
	}
	if cmd := app.accessPrefetchCmd(); cmd != nil {
		t.Fatal("reviews left after settling")
	}
}

func functionKeyEnabled(bar, key, label string) (enabled, found bool) {
	on := uistyles.FunctionBarKeyStyle.Render(key) + uistyles.FunctionBarLabelStyle.Render(label)
	off := uistyles.FunctionBarKeyStyle.Render(key) + uistyles.FunctionBarDisabledStyle.Render(label)
	switch {
	case strings.Contains(bar, on):
		return true, true
	case strings.Contains(bar, off):
		return false, true
	}
	return false, false
}

func TestAccessGatesEditAndDelete(t *testing.T) {
	tests := []struct {
		name       string
		srv        *ssarServer
		wantEdit   bool
		wantDelete bool
	}{
		{name: "all allowed", srv: &ssarServer{}, wantEdit: true, wantDelete: true},
		{name: "delete denied", srv: &ssarServer{deny: map[string]bool{"delete": true}}, wantEdit: true, wantDelete: false},
		{name: "patch denied", srv: &ssarServer{deny: map[string]bool{"patch": true}}, wantEdit: false, wantDelete: true},
		{name: "get denied", srv: &ssarServer{deny: map[string]bool{"get": true}}, wantEdit: false, wantDelete: true},
		{name: "review error stays enabled", srv: &ssarServer{fail: errors.New("boom")}, wantEdit: true, wantDelete: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, _ := newAccessTestApp(t, tt.srv)
			ctx := t.Context()

			// Before any answer the actions are Unknown, so enabled.
			if caps := app.leftPanel.Capabilities(ctx); !caps.CanEdit || !caps.CanDelete {
				t.Fatalf("unknown access disabled actions: %+v", caps)
			}

			settleAccess(t, app)
			if tt.srv.count() == 0 {
				t.Fatal("no access review was sent")
			}
			caps := app.leftPanel.Capabilities(ctx)
			if caps.CanEdit != tt.wantEdit || caps.CanDelete != tt.wantDelete {
				t.Fatalf("caps edit=%v delete=%v, want edit=%v delete=%v", caps.CanEdit, caps.CanDelete, tt.wantEdit, tt.wantDelete)
			}
			app.invalidateFunctionBar("test")
			bar := app.renderFunctionKeys()
			if on, ok := functionKeyEnabled(bar, " 4", "Edit"); !ok || on != tt.wantEdit {
				t.Fatalf("F4 enabled=%v found=%v, want %v in:\n%s", on, ok, tt.wantEdit, bar)
			}
			if on, ok := functionKeyEnabled(bar, " 8", "Delete"); !ok || on != tt.wantDelete {
				t.Fatalf("F8 enabled=%v found=%v, want %v in:\n%s", on, ok, tt.wantDelete, bar)
			}

			// A denied F8 does not run its handler.
			var deleted bool
			app.leftPanel.SetActionHandlers(PanelActionHandlers{PanelActionDelete: func(*Panel) tea.Cmd {
				deleted = true
				return nil
			}})
			app.leftPanel.invokeActionIfAllowed(ctx, PanelActionDelete)
			if deleted != tt.wantDelete {
				t.Fatalf("delete handler ran=%v, want %v", deleted, tt.wantDelete)
			}
		})
	}
}

func TestAccessPrefetchUsesCacheAndIdentity(t *testing.T) {
	srv := &ssarServer{deny: map[string]bool{"delete": true}}
	app, _ := newAccessTestApp(t, srv)
	settleAccess(t, app)
	first := srv.count()
	// get, patch and delete at type level, then delete by name.
	if first != 4 {
		t.Fatalf("reviews = %d, want 4: %+v", first, srv.reviews)
	}
	for _, r := range srv.reviews {
		if r.Namespace != "team-a" || r.Group != "group" || r.Version != "v1" || r.Resource != "tests" {
			t.Fatalf("review lacks namespace or full GVR: %+v", r)
		}
	}
	if last := srv.reviews[len(srv.reviews)-1]; last.Verb != "delete" || last.Name != "web" {
		t.Fatalf("named review = %+v, want delete web", last)
	}

	// Cached: nothing is asked again.
	if cmd := app.accessPrefetchCmd(); cmd != nil {
		t.Fatal("fresh answers asked again")
	}

	// Impersonating someone else is another identity: ask again.
	app.impersonateUser = "jane"
	if caps := app.leftPanel.Capabilities(t.Context()); !caps.CanDelete {
		t.Fatal("answers for the kubeconfig user were reused for the impersonated user")
	}
	settleAccess(t, app)
	if srv.count() <= first {
		t.Fatal("identity change did not send new reviews")
	}
}

func TestAccessViewShowsResultsAndCommand(t *testing.T) {
	srv := &ssarServer{deny: map[string]bool{"delete": true}}
	app, _ := newAccessTestApp(t, srv)
	app.kubectlCtx = ""

	_, cmd := app.Update(tea.KeyPressMsg{Code: 'k', Mod: tea.ModAlt})
	modal := app.modalManager.GetActiveModal()
	if modal == nil {
		t.Fatal("access view did not open")
	}
	view, ok := modal.content.(*accessViewModel)
	if !ok {
		t.Fatalf("modal content = %T", modal.content)
	}
	if got := view.View().Content; !strings.Contains(got, "checking") {
		t.Fatalf("view before results lacks pending state:\n%s", got)
	}
	for _, msg := range drainCmd(t, cmd) {
		app.Update(msg)
	}
	settleAccess(t, app)

	body := view.View().Content
	for _, want := range []string{"tests.group/web in namespace team-a", "as the kubeconfig user"} {
		if !strings.Contains(body, want) {
			t.Fatalf("view lacks %q:\n%s", want, body)
		}
	}
	// Move to delete and read its reason and command.
	for i, c := range view.checks {
		if c.Verb == "delete" && c.Subresource == "" {
			view.move(i - view.selected)
			break
		}
	}
	body = view.View().Content
	if !strings.Contains(body, "denied") || !strings.Contains(body, "RBAC: no rule for delete") {
		t.Fatalf("denied row lacks result or reason:\n%s", body)
	}
	if want := "kubectl auth can-i delete tests.group/web -n team-a"; view.CanICommand() != want || !strings.Contains(body, want) {
		t.Fatalf("can-i = %q, want %q", view.CanICommand(), want)
	}
}

func TestAccessViewStaleAndUnknown(t *testing.T) {
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	cache := access.NewCache(10*time.Second, clock)
	id := access.Identity{Context: "dev"}
	target := access.Target{Resource: podsResource, Namespace: "ns", Name: "p"}
	view := newAccessViewModel(target, id, access.Policy{}, cache)
	view.SetDimensions(90, 30)

	get := target.Check("get")
	exec := access.ExecChecks("ns", "p")[0]
	cache.Store(id, get, access.Result{Decision: access.Allowed, Reason: "ok", CheckedAt: now})
	cache.Store(id, exec, access.Result{Decision: access.Unknown, Reason: "access review failed: timeout", CheckedAt: now})

	body := view.View().Content
	if !strings.Contains(body, "allowed") || strings.Contains(body, "(stale)") {
		t.Fatalf("fresh view wrong:\n%s", body)
	}
	if !strings.Contains(body, "pods/exec") || !strings.Contains(body, "unknown") {
		t.Fatalf("exec row missing or not unknown:\n%s", body)
	}
	if !strings.Contains(body, "pods/log") || !strings.Contains(body, "pods/portforward") {
		t.Fatalf("pod subresources missing:\n%s", body)
	}
	now = now.Add(11 * time.Second)
	if body := view.View().Content; !strings.Contains(body, "allowed (stale)") {
		t.Fatalf("expired answer not marked stale:\n%s", body)
	}
}

func TestAccessTargetForItem(t *testing.T) {
	obj := stubObject{id: "x", namespace: "ns", name: "web"}
	if tgt, ok := accessTargetForItem(obj); !ok || tgt.Name != "web" || tgt.Namespace != "ns" || tgt.Resource.Resource != "tests" {
		t.Fatalf("object target = %+v ok=%v", tgt, ok)
	}
	logs := models.NewContainerLogItem(models.Deps{}, "l", []string{"logs"}, nil, models.LogsSpec{Namespace: "ns", Pod: "p", Container: "c"})
	if tgt, ok := accessTargetForItem(logs); !ok || tgt.Resource != podsResource || tgt.Name != "p" {
		t.Fatalf("logs target = %+v ok=%v", tgt, ok)
	}
	if _, ok := accessTargetForItem(mockItemFB{id: "plain"}); ok {
		t.Fatal("plain row got an access target")
	}
	if _, ok := accessTargetForItem(namespaceCommandItem{namespace: "ns"}); ok {
		t.Fatal("item without a GVR got an access target")
	}
}

func TestAccessGatesLogsView(t *testing.T) {
	logs := models.NewContainerLogItem(models.Deps{}, "logs", []string{"logs"}, nil, models.LogsSpec{Namespace: "ns", Pod: "p", Container: "c"})
	panel := NewPanel("logs")
	cache := access.NewCache(0, nil)
	id := access.Identity{Context: "dev"}
	panel.SetEnvironmentSupplier(func() PanelEnvironment {
		return PanelEnvironment{Permit: func(checks ...access.Check) bool {
			return access.Policy{}.Permits(func(c access.Check) access.Decision { return cache.Decide(id, c) }, checks...)
		}}
	})
	ctx := t.Context()
	panel.UseFolder(ctx, true)
	panel.SetFolder(ctx, newStubFolder([]models.Item{logs}), false)
	panel.SelectByRowID(ctx, "logs")
	if !panel.Capabilities(ctx).CanView {
		t.Fatal("logs view disabled before any answer")
	}
	for _, c := range access.LogsChecks("ns", "p") {
		cache.Store(id, c.TypeLevel(), access.Result{Decision: access.Denied, CheckedAt: time.Now()})
		cache.Store(id, c, access.Result{Decision: access.Denied, CheckedAt: time.Now()})
	}
	if caps := panel.Capabilities(ctx); caps.CanView || caps.CanCopy {
		t.Fatalf("denied pods/log kept F3/F5 enabled: %+v", caps)
	}
}
