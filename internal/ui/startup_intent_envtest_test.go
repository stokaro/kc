package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/models"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

func setupAppWithCluster(t *testing.T) (*App, *kccluster.Cluster) {
	kcenvtest.Require(t, testCfg)
	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	ctx := t.Context()
	kctesting.RunInBackground(t, "cluster", cl.Start)
	t.Cleanup(cl.Stop)

	app := NewApp()
	app.cancel()
	app.ctx, app.cancel = context.WithCancel(ctx)
	t.Cleanup(app.cancel)
	app.cl = cl
	app.currentCtx = &kubeconfig.Context{Name: "env"}
	return app, cl
}

func createNamespace(t *testing.T, cl *kccluster.Cluster, name string) func() {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ns := &corev1.Namespace{Name: name}
	if err := cl.GetClient().Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace: %v", err)
	}
	return func() {
		delCtx, cancelDel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancelDel()
		_ = cl.GetClient().Delete(delCtx, ns)
	}
}

func createPod(t *testing.T, cl *kccluster.Cluster, namespace, name string, containers []corev1.Container) func() {
	t.Helper()
	if len(containers) == 0 {
		containers = []corev1.Container{{Name: "app", Image: "busybox"}}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	pod := &corev1.Pod{
		Name:      name,
		Namespace: namespace,
		Spec: corev1.PodSpec{
			Containers: containers,
		},
	}
	if err := cl.GetClient().Create(ctx, pod); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create pod %s/%s: %v", namespace, name, err)
	}
	return func() {
		delCtx, cancelDel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancelDel()
		_ = cl.GetClient().Delete(delCtx, &corev1.Pod{Name: name, Namespace: namespace})
	}
}

func waitForNamespaceReady(t *testing.T, app *App, ns string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		if app.namespaceExists(ns) {
			return
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			t.Fatalf("namespace %q not observed: %v", ns, ctx.Err())
		}
	}
}

func TestStartupIntentGetMultiSelectEnvtest(t *testing.T) {
	app, cl := setupAppWithCluster(t)
	ns := kctesting.UniqueName("intent-multi")
	t.Cleanup(createNamespace(t, cl, ns))
	t.Cleanup(createPod(t, cl, ns, "pod-a", nil))
	t.Cleanup(createPod(t, cl, ns, "pod-b", nil))

	waitForNamespaceReady(t, app, ns)
	app.goToNamespace(ns)

	app.startupIntent = StartupIntent{
		Verb:      KubectlVerbGet,
		Namespace: ns,
		Get: &GetIntent{
			Tokens: []GetToken{
				{Value: "pods", ExplicitResource: true},
				{Value: "pod-a"},
				{Value: "pod-b"},
			},
		},
	}
	app.startupIntentApplied = false
	runStartupSelect(app, app.applyStartupIntent())

	wantPath := "/pods"
	if got := app.leftPanel.GetCurrentPath(); got != wantPath {
		t.Fatalf("left panel path = %q, want %q", got, wantPath)
	}
	items := app.leftPanel.GetSelectedItems()
	names := selectedItemNames(items)
	if !equalStringSets(names, []string{"pod-a", "pod-b"}) {
		t.Fatalf("selected pods = %v, want [pod-a pod-b]", names)
	}
	if app.leftPanel.Mode() != PanelModeList {
		t.Fatalf("left panel mode = %v, want list", app.leftPanel.Mode())
	}
}

func TestStartupIntentGetOutputYAMLEnvtest(t *testing.T) {
	app, cl := setupAppWithCluster(t)
	ns := kctesting.UniqueName("intent-yaml")
	podName := "pod-yaml"
	t.Cleanup(createNamespace(t, cl, ns))
	t.Cleanup(createPod(t, cl, ns, podName, nil))

	waitForNamespaceReady(t, app, ns)
	app.goToNamespace(ns)

	app.startupIntent = StartupIntent{
		Verb:      KubectlVerbGet,
		Namespace: ns,
		Get: &GetIntent{
			OutputFormat: "yaml",
			Tokens: []GetToken{
				{Value: "pods", ExplicitResource: true},
				{Value: podName},
			},
		},
	}
	app.startupIntentApplied = false
	runStartupSelect(app, app.applyStartupIntent())

	wantPath := "/pods"
	if got := app.leftPanel.GetCurrentPath(); got != wantPath {
		t.Fatalf("left panel path = %q, want %q", got, wantPath)
	}
	if app.rightPanel.Mode() != PanelModeManifest {
		t.Fatalf("right panel mode = %v, want manifest", app.rightPanel.Mode())
	}
	if app.activePanel != 0 {
		t.Fatalf("expected active panel to remain left (0), got %d", app.activePanel)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if item, ok := app.rightPanel.SelectedNavItem(ctx); !ok || item == nil {
		t.Fatalf("expected right panel selection")
	} else if path := "/" + strings.Join(item.Path(), "/"); path != "/pods/"+podName {
		t.Fatalf("right panel selection path = %q, want namespace pod path", path)
	}
}

// The intent runs right after the panels got a fresh root, before the root
// knows whether pods is empty and shows its row (#84).
func TestStartupIntentGetFreshRootEnvtest(t *testing.T) {
	app, cl := setupAppWithCluster(t)
	ns := kctesting.UniqueName("intent-fresh")
	podName := "pod-fresh"
	t.Cleanup(createNamespace(t, cl, ns))
	t.Cleanup(createPod(t, cl, ns, podName, nil))
	waitForNamespaceReady(t, app, ns)

	hideEmptyTypes(app)
	app.goToNamespace(ns)
	app.startupIntent = StartupIntent{
		Verb:      KubectlVerbGet,
		Namespace: ns,
		Get: &GetIntent{Tokens: []GetToken{
			{Value: "po", ExplicitResource: true},
			{Value: podName},
		}},
	}
	app.startupIntentApplied = false
	runStartupSelect(app, app.applyStartupIntent())

	if got := app.leftPanel.GetCurrentPath(); got != "/pods" {
		t.Fatalf("left panel path = %q, want /pods", got)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	item, ok := app.leftPanel.SelectedNavItem(ctx)
	if !ok || item == nil {
		t.Fatalf("no selection in the left panel")
	}
	if path := "/" + strings.Join(item.Path(), "/"); path != "/pods/"+podName {
		t.Fatalf("selected %q, want /pods/%s", path, podName)
	}
}

// An empty type has no row in the root at all (resources.showNonEmptyOnly);
// kc get still opens its list.
func TestStartupIntentGetEmptyTypeEnvtest(t *testing.T) {
	app, cl := setupAppWithCluster(t)
	ns := kctesting.UniqueName("intent-empty")
	t.Cleanup(createNamespace(t, cl, ns))
	waitForNamespaceReady(t, app, ns)

	hideEmptyTypes(app)
	app.goToNamespace(ns)
	app.startupIntent = StartupIntent{
		Verb:      KubectlVerbGet,
		Namespace: ns,
		Get:       &GetIntent{Tokens: []GetToken{{Value: "svc", ExplicitResource: true}}},
	}
	app.startupIntentApplied = false
	runStartupSelect(app, app.applyStartupIntent())

	if got := app.leftPanel.GetCurrentPath(); got != "/services" {
		t.Fatalf("left panel path = %q, want /services", got)
	}
}

func TestStartupIntentLogsEnvtest(t *testing.T) {
	app, cl := setupAppWithCluster(t)
	ns := kctesting.UniqueName("intent-logs")
	podName := "pod-logs"
	container := "main"
	t.Cleanup(createNamespace(t, cl, ns))
	t.Cleanup(createPod(t, cl, ns, podName, []corev1.Container{
		{Name: container, Image: "busybox"},
	}))

	waitForNamespaceReady(t, app, ns)
	app.goToNamespace(ns)

	app.startupIntent = StartupIntent{
		Verb:      KubectlVerbLogs,
		Namespace: ns,
		Logs: &LogsIntent{
			Pod:    podName,
			Follow: true,
		},
	}
	app.startupIntentApplied = false
	runStartupSelect(app, app.applyStartupIntent())

	wantPath := fmt.Sprintf("/pods/%s/containers/%s", podName, container)
	if got := app.leftPanel.GetCurrentPath(); got != wantPath {
		t.Fatalf("left panel path = %q, want %q", got, wantPath)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if item, ok := app.leftPanel.SelectedNavItem(ctx); !ok || item == nil {
		t.Fatalf("expected left panel selection on logs entry")
	} else if path := "/" + strings.Join(item.Path(), "/"); !strings.HasSuffix(path, "/logs/latest") {
		t.Fatalf("selected item path = %q, want logs/latest entry", path)
	}
}

// TestStartupIntentGetAllNamespacesEnvtest mirrors kc -A get pods: the
// left panel lists pods across all namespaces and keeps that scope.
func TestStartupIntentGetAllNamespacesEnvtest(t *testing.T) {
	app, cl := setupAppWithCluster(t)
	ns := kctesting.UniqueName("intent-all")
	t.Cleanup(createNamespace(t, cl, ns))
	t.Cleanup(createPod(t, cl, ns, "pod-all", nil))

	waitForNamespaceReady(t, app, ns)
	app.allNamespaces = true
	app.goToNamespace(app.initialNamespace())

	app.startupIntent = StartupIntent{
		Verb: KubectlVerbGet,
		Get: &GetIntent{
			Tokens: []GetToken{{Value: "pods", ExplicitResource: true}},
		},
	}
	app.startupIntentApplied = false
	runStartupSelect(app, app.applyStartupIntent())

	if got := app.leftPanel.GetCurrentPath(); got != "/pods" {
		t.Fatalf("left panel path = %q, want /pods", got)
	}
	for idx := range 2 {
		if got := app.panelScope[idx]; got != models.AllNamespaces {
			t.Fatalf("panel %d scope = %q, want all namespaces", idx, got)
		}
	}
	nav, ok := app.leftNav.Current().(interface{ Namespace() string })
	if !ok || nav.Namespace() != models.AllNamespaces {
		t.Fatalf("left folder %T is not the all-namespaces pod list", app.leftNav.Current())
	}
}

func selectedItemNames(items []Item) []string {
	names := make([]string, 0, len(items))
	for _, it := range items {
		names = append(names, strings.TrimPrefix(it.Name, "/"))
	}
	sort.Strings(names)
	return names
}

func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aCopy := append([]string(nil), a...)
	bCopy := append([]string(nil), b...)
	sort.Strings(aCopy)
	sort.Strings(bCopy)
	for i := range aCopy {
		if aCopy[i] != bCopy[i] {
			return false
		}
	}
	return true
}

// hideEmptyTypes sets the panels to resources.showNonEmptyOnly, as App.Init
// does with the default config.
func hideEmptyTypes(app *App) {
	app.leftPanel.SetResourceViewOptions(true, "alpha")
	app.rightPanel.SetResourceViewOptions(true, "alpha")
}

// runStartupSelect runs cmd and delivers the messages of the startup flow
// it produces (navigation waits, selection retries, the logs container),
// as the program loop would; other messages are dropped.
func runStartupSelect(app *App, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			runStartupSelect(app, c)
		}
	case startupSelectMsg, panelWalkMsg, startupLogsTargetMsg:
		_, next := app.Update(msg)
		runStartupSelect(app, next)
	}
}
