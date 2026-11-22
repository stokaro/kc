package ui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/models"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/pkg/appconfig"
)

// Test that entering a namespaced endpoints list yields server-side columns
// beyond just Name when at least one endpoints object exists.
func TestPanelEndpointsColumnsEnvtest(t *testing.T) {
	kcenvtest.Require(t, testCfg)

	// Seed kube-system/kubernetes Endpoints
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	cli, err := crclient.New(testCfg, crclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	ctx := t.Context()
	// Ensure kube-system exists
	_ = cli.Create(ctx, &corev1.Namespace{Name: "kube-system"})
	ep := &corev1.Endpoints{Name: "kubernetes", Namespace: "kube-system"}
	// One dummy subset to ensure a non-empty row
	ep.Subsets = []corev1.EndpointSubset{{
		Addresses: []corev1.EndpointAddress{{IP: "10.0.0.1"}},
		Ports:     []corev1.EndpointPort{{Port: 443}},
	}}
	_ = cli.Create(ctx, ep)

	// Build kc cluster and folder
	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	go cl.Start(ctx)
	cfg := appconfig.Default()
	cfg.Resources.ShowNonEmptyOnly = false
	cfg.Resources.Columns = "normal"
	cfg.Objects.Order = "name"
	cfg.Objects.Columns = "normal"
	deps := models.Deps{
		Cl:         cl,
		Ctx:        ctx,
		CtxName:    "env",
		KubeConfig: clientcmdapi.Config{CurrentContext: "env", Contexts: map[string]*clientcmdapi.Context{"env": {}}},
		AppConfig:  cfg,
	}
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "endpoints"}
	folder := models.NewNamespacedObjectsFolder(deps, gvr, "kube-system", []string{"namespaces", "kube-system", gvr.Resource}, nil)

	// Wait until at least one row appears
	kctesting.Eventually(t, 5*time.Second, 50*time.Millisecond, func() bool { return folder.Len(ctx) > 0 })

	// Panel should pick up server columns on SetFolder
	p := NewPanel("")
	p.UseFolder(ctx, true)
	if model, _ := p.Update(tea.WindowSizeMsg{Width: 120, Height: 20}); model != nil {
		p = model.(*Panel)
	}
	p.SetFolder(ctx, folder, false)
	cols := p.ColumnTitles(ctx)
	if len(cols) < 2 {
		t.Fatalf("expected >=2 columns for endpoints, got %v", cols)
	}
}
