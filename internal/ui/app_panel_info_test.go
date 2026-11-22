package ui

import (
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	models "github.com/stokaro/kc/internal/models"
)

func TestInfoModeIsAvailable(t *testing.T) {
	app := NewApp()
	if !slices.Contains(app.rightPanel.AvailableModes(), PanelModeInfo) {
		t.Fatalf("modes = %v, want Info", app.rightPanel.AvailableModes())
	}
	if got := modeLabel(PanelModeInfo); got != "Info" {
		t.Fatalf("modeLabel(Info) = %q", got)
	}
	if got := NextPanelMode(PanelModeInfo); got != PanelModeCommand {
		t.Fatalf("NextPanelMode(Info) = %v, want Command", got)
	}
	found := false
	for _, item := range app.panelMenuItems(1) {
		found = found || item.Label == "&Info"
	}
	if !found {
		t.Fatal("the panel menu has no Info item")
	}
	if tgt := app.infoTarget(t.Context(), app.rightPanel); tgt.Open != nil || !strings.Contains(tgt.Hint, "Not connected") {
		t.Fatalf("target without a cluster = %+v", tgt)
	}
}

// TestInfoTargetFollowsTheOtherPanelEnvtest checks that the Info mode of
// the right panel shows the left panel's namespace scope.
func TestInfoTargetFollowsTheOtherPanelEnvtest(t *testing.T) {
	app, cl := setupAppWithCluster(t)
	ns := "info-target"
	t.Cleanup(createNamespace(t, cl, ns))
	waitForNamespaceReady(t, app, ns)
	app.goToNamespace(ns)

	tgt := app.infoTarget(t.Context(), app.rightPanel)
	if tgt.Open == nil || !strings.HasSuffix(tgt.Key, "|"+ns) || !strings.Contains(tgt.Label, ns) {
		t.Fatalf("target = %+v, want the left panel's namespace %q", tgt, ns)
	}
	src := tgt.Open()
	defer src.Close()
	if s := src.Summary(models.InfoFocus{}); s.Err != "" {
		t.Fatalf("summary error: %s", s.Err)
	}
}

// TestInfoTargetNamesTheWorkloadUnderTheCursorEnvtest puts the left panel's
// cursor on a Deployment (as kc get deployments web does): the Info mode of
// the right panel narrows to that workload by its full GVR.
func TestInfoTargetNamesTheWorkloadUnderTheCursorEnvtest(t *testing.T) {
	app, cl := setupAppWithCluster(t)
	ns := "info-workload"
	t.Cleanup(createNamespace(t, cl, ns))
	labels := map[string]string{"app": "web"}
	dep := &appsv1.Deployment{
		Namespace: ns, Name: "web",
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}},
			},
		},
	}
	if err := cl.GetClient().Create(t.Context(), dep); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	waitForNamespaceReady(t, app, ns)
	app.goToNamespace(ns)

	app.startupIntent = StartupIntent{Verb: KubectlVerbGet, Namespace: ns, Get: &GetIntent{Tokens: []GetToken{
		{Value: "deployments", ExplicitResource: true},
		{Value: "web"},
	}}}
	app.startupIntentApplied = false
	runStartupSelect(app, app.applyStartupIntent())

	tgt := app.infoTarget(t.Context(), app.rightPanel)
	want := models.InfoWorkload{GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, Namespace: ns, Name: "web"}
	if tgt.Workload == nil || *tgt.Workload != want || tgt.Node != "" {
		t.Fatalf("target workload = %+v node %q, want %+v (left panel %q)", tgt.Workload, tgt.Node, want, app.leftPanel.GetCurrentPath())
	}
}
