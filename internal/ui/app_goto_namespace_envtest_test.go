package ui

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kccluster "github.com/stokaro/kc/internal/cluster"
	models "github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

func TestGoToNamespaceEmptyStaysClusterScope(t *testing.T) {
	kcenvtest.Require(t, testCfg)
	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	ctx := t.Context()
	go cl.Start(ctx)
	t.Cleanup(func() {
		cl.Stop()
	})

	app := NewApp()
	app.cancel()
	app.ctx, app.cancel = context.WithCancel(ctx)
	t.Cleanup(app.cancel)
	app.cl = cl
	app.currentCtx = &kubeconfig.Context{Name: "env"}

	app.goToNamespace("")

	if app.leftNav == nil || app.rightNav == nil {
		t.Fatalf("expected navigators to be initialized")
	}
	if got := app.leftPanel.GetCurrentPath(); got != "/" {
		t.Fatalf("left panel path = %q, want /", got)
	}
	if got := app.rightPanel.GetCurrentPath(); got != "/" {
		t.Fatalf("right panel path = %q, want /", got)
	}
	if app.leftNav.HasBack() {
		t.Fatalf("expected left navigator to have no back history")
	}
	if app.rightNav.HasBack() {
		t.Fatalf("expected right navigator to have no back history")
	}
}

func TestGoToNamespaceExistingNavigatesPanels(t *testing.T) {
	kcenvtest.Require(t, testCfg)
	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	ctx := t.Context()
	go cl.Start(ctx)
	t.Cleanup(func() {
		cl.Stop()
	})

	nsName := "autonav"
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	if err := cl.GetClient().Create(ctx, namespace); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = cl.GetClient().Delete(cleanupCtx, namespace)
	})

	app := NewApp()
	app.cancel()
	app.ctx, app.cancel = context.WithCancel(ctx)
	t.Cleanup(app.cancel)
	app.cl = cl
	app.currentCtx = &kubeconfig.Context{Name: "env", Namespace: nsName}

	waitCtx, cancelWait := context.WithTimeout(ctx, 5*time.Second)
	defer cancelWait()
	for {
		if app.namespaceExists(nsName) {
			break
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-waitCtx.Done():
			t.Fatalf("namespace %q not observed: %v", nsName, waitCtx.Err())
		}
	}

	app.goToNamespace(nsName)

	// Panels stay at their root; the namespace is the panels' scope.
	for idx, panel := range []*Panel{app.leftPanel, app.rightPanel} {
		if got := panel.GetCurrentPath(); got != "/" {
			t.Fatalf("panel %d path = %q, want /", idx, got)
		}
		if app.panelScope[idx] != nsName {
			t.Fatalf("panel %d scope = %q, want %q", idx, app.panelScope[idx], nsName)
		}
	}
	root, ok := app.leftNav.Current().(*models.RootFolder)
	if !ok || root.NamespaceScope() != nsName {
		t.Fatalf("left root = %T, want a RootFolder scoped to %q", app.leftNav.Current(), nsName)
	}
}
