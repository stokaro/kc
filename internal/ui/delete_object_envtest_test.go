package ui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	models "github.com/stokaro/kc/internal/models"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/pkg/appconfig"
)

// deleteEnv starts a kc cluster against envtest and creates a namespace.
func deleteEnv(t *testing.T, namespace string) (*kccluster.Cluster, crclient.Client) {
	t.Helper()
	if testCfg == nil {
		t.Skip("envtest not available")
	}
	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	ctx := t.Context()
	kctesting.RunInBackground(t, "cluster", cl.Start)
	t.Cleanup(cl.Stop)
	c := cl.GetClient()
	ns := &corev1.Namespace{Name: namespace}
	if err := c.Create(ctx, ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	t.Cleanup(func() {
		// t.Context() is canceled before cleanup functions run.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		_ = c.Delete(cleanupCtx, ns)
	})
	return cl, c
}

// selectedRow returns the configmap row the objects folder builds for name,
// the way the panel hands it to deleteResourceForPanel.
func selectedRow(t *testing.T, cl *kccluster.Cluster, namespace, name string) models.Item {
	t.Helper()
	ctx := t.Context()
	cfg := appconfig.Default()
	deps := models.Deps{
		Cl:         cl,
		Ctx:        ctx,
		CtxName:    "envtest",
		KubeConfig: clientcmdapi.Config{CurrentContext: "envtest", Contexts: map[string]*clientcmdapi.Context{"envtest": {}}},
		AppConfig:  cfg,
	}
	folder := models.NewNamespacedObjectsFolder(deps, gvrConfigMaps, namespace, []string{"namespaces", namespace, "configmaps"}, nil)
	var item models.Item
	kctesting.Eventually(t, 10*time.Second, 50*time.Millisecond, func() bool {
		var ok bool
		item, ok = folder.ItemByID(ctx, name)
		return ok
	})
	if item == nil {
		t.Fatalf("folder never listed configmap %s/%s", namespace, name)
	}
	return item
}

func createConfigMap(t *testing.T, c crclient.Client, namespace, name string) *corev1.ConfigMap {
	t.Helper()
	cm := &corev1.ConfigMap{Namespace: namespace, Name: name}
	if err := c.Create(t.Context(), cm); err != nil {
		t.Fatalf("create configmap %s/%s: %v", namespace, name, err)
	}
	return cm
}

// TestDeleteSparesReplacementEnvtest replaces the selected object between
// confirmation and execution, as another client could while F8 is open.
func TestDeleteSparesReplacementEnvtest(t *testing.T) {
	namespace, name := kctesting.UniqueName("delete-replaced"), "victim"
	cl, c := deleteEnv(t, namespace)
	ctx := t.Context()
	original := createConfigMap(t, c, namespace, name)

	target, err := deleteTargetFor(0, selectedRow(t, cl, namespace, name))
	if err != nil {
		t.Fatalf("deleteTargetFor() error = %v", err)
	}
	if target.uid != original.UID {
		t.Fatalf("target UID = %q, want the listed object's UID %q", target.uid, original.UID)
	}
	if target.cluster != cl || target.context != "envtest" {
		t.Fatalf("target origin = %p/%q, want %p/envtest", target.cluster, target.context, cl)
	}

	if err := c.Delete(ctx, original); err != nil {
		t.Fatalf("delete original: %v", err)
	}
	replacement := createConfigMap(t, c, namespace, name)
	if replacement.UID == original.UID {
		t.Fatalf("replacement reused UID %q", replacement.UID)
	}

	msg := executeDelete(ctx, target)
	if !apierrors.IsConflict(msg.err) {
		t.Fatalf("executeDelete() error = %v, want a Conflict", msg.err)
	}

	var got corev1.ConfigMap
	if err := c.Get(ctx, crclient.ObjectKeyFromObject(replacement), &got); err != nil {
		t.Fatalf("replacement is gone: %v", err)
	}
	if got.UID != replacement.UID {
		t.Fatalf("configmap UID = %q, want the replacement %q", got.UID, replacement.UID)
	}
}

func TestDeleteAlreadyGoneEnvtest(t *testing.T) {
	namespace, name := kctesting.UniqueName("delete-gone"), "victim"
	cl, c := deleteEnv(t, namespace)
	ctx := t.Context()
	original := createConfigMap(t, c, namespace, name)

	target, err := deleteTargetFor(0, selectedRow(t, cl, namespace, name))
	if err != nil {
		t.Fatalf("deleteTargetFor() error = %v", err)
	}
	if err := c.Delete(ctx, original); err != nil {
		t.Fatalf("delete original: %v", err)
	}

	msg := executeDelete(ctx, target)
	if !apierrors.IsNotFound(msg.err) {
		t.Fatalf("executeDelete() error = %v, want NotFound", msg.err)
	}
}

// TestConfirmedDeleteUsesOriginClusterEnvtest runs the confirmation through
// the App with no current cluster, as after a context switch: the delete
// must still go to the cluster the row was read from.
func TestConfirmedDeleteUsesOriginClusterEnvtest(t *testing.T) {
	namespace, name := kctesting.UniqueName("delete-origin"), "victim"
	cl, c := deleteEnv(t, namespace)
	ctx := t.Context()
	original := createConfigMap(t, c, namespace, name)

	target, err := deleteTargetFor(0, selectedRow(t, cl, namespace, name))
	if err != nil {
		t.Fatalf("deleteTargetFor() error = %v", err)
	}

	app := NewApp()
	app.cancel()
	app.ctx, app.cancel = context.WithCancel(ctx)
	t.Cleanup(app.cancel)
	app.cl = nil
	app.pendingDelete = &target

	_, cmd := app.Update(DeleteConfirmMsg{Confirm: true, Close: true})
	deleted, ok := findDeleteResult(cmd)
	if !ok {
		t.Fatalf("confirmation produced no delete result")
	}
	if deleted.err != nil {
		t.Fatalf("delete error = %v", deleted.err)
	}
	err = c.Get(ctx, crclient.ObjectKeyFromObject(original), &corev1.ConfigMap{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("get after delete error = %v, want NotFound", err)
	}
}

// findDeleteResult runs cmd and the commands it batches until one yields
// the delete result.
func findDeleteResult(cmd tea.Cmd) (resourceDeletedMsg, bool) {
	if cmd == nil {
		return resourceDeletedMsg{}, false
	}
	switch msg := cmd().(type) {
	case resourceDeletedMsg:
		return msg, true
	case busyDoneMsg:
		res, ok := msg.msg.(resourceDeletedMsg)
		return res, ok
	case tea.BatchMsg:
		for _, c := range msg {
			if res, ok := findDeleteResult(c); ok {
				return res, true
			}
		}
	}
	return resourceDeletedMsg{}, false
}
