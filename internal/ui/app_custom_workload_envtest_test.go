package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	models "github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/promhistory"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/internal/ui/dashboard"
	"github.com/stokaro/kc/internal/ui/panelcontent"
)

const uiWorkloadGroup = "workloads.kc-test.io"

// installUIWorkloadCRD installs a namespaced CRD of uiWorkloadGroup that
// keeps any field, with a scale subresource reading .spec.replicas when
// scale is true.
func installUIWorkloadCRD(t *testing.T, cfg *rest.Config, kind, plural string, scale bool) {
	t.Helper()
	ctx := t.Context()
	apiext, err := apiextclient.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	version := apiextensionsv1.CustomResourceDefinitionVersion{
		Name: "v1", Served: true, Storage: true,
		Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object", XPreserveUnknownFields: new(true)}},
	}
	if scale {
		version.Subresources = &apiextensionsv1.CustomResourceSubresources{Scale: &apiextensionsv1.CustomResourceSubresourceScale{
			SpecReplicasPath: ".spec.replicas", StatusReplicasPath: ".status.replicas",
		}}
	}
	crd := &apiextensionsv1.CustomResourceDefinition{
		Name: plural + "." + uiWorkloadGroup,
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group:    uiWorkloadGroup,
			Names:    apiextensionsv1.CustomResourceDefinitionNames{Plural: plural, Singular: strings.ToLower(kind), Kind: kind, ListKind: kind + "List"},
			Scope:    apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{version},
		},
	}
	if _, err := apiext.ApiextensionsV1().CustomResourceDefinitions().Create(ctx, crd, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create CRD %s: %v", crd.Name, err)
	}
	kctesting.Eventually(t, 10*time.Second, 100*time.Millisecond, func() bool {
		latest, err := apiext.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crd.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		for _, c := range latest.Status.Conditions {
			if c.Type == apiextensionsv1.Established && c.Status == apiextensionsv1.ConditionTrue {
				return true
			}
		}
		return false
	})
}

func uiCustomObject(kind, ns, name string, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	u.SetAPIVersion(uiWorkloadGroup + "/v1")
	u.SetKind(kind)
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

func uiOwnerRef(o crclient.Object) []metav1.OwnerReference {
	gvk := o.GetObjectKind().GroupVersionKind()
	return []metav1.OwnerReference{{APIVersion: gvk.GroupVersion().String(), Kind: gvk.Kind, Name: o.GetName(), UID: o.GetUID(), Controller: new(true)}}
}

// TestCustomWorkloadInfoAndDashboardEnvtest puts the cursor on a Rollout
// (a test CRD) whose ReplicaSet owns a pod: once the Info engine saw the
// pod's chain, Info shows the Rollout's pods and C-x g opens the dashboard
// on it, with the Prometheus target through its ReplicaSets. A Widget row,
// a kind that owns no pods, keeps the namespace scope.
func TestCustomWorkloadInfoAndDashboardEnvtest(t *testing.T) {
	t.Parallel()
	cfg := kcenvtest.Start(t)
	installUIWorkloadCRD(t, cfg, "Rollout", "rollouts", true)
	installUIWorkloadCRD(t, cfg, "Widget", "widgets", false)
	rollouts := schema.GroupVersionResource{Group: uiWorkloadGroup, Version: "v1", Resource: "rollouts"}
	widgets := schema.GroupVersionResource{Group: uiWorkloadGroup, Version: "v1", Resource: "widgets"}

	ns := kctesting.UniqueName("custom-wl")
	admin, err := kccluster.New(cfg)
	if err != nil {
		t.Fatalf("admin cluster: %v", err)
	}
	kctesting.RunInBackground(t, "admin", admin.Start)
	t.Cleanup(admin.Stop)
	adminCreate(t, admin, &corev1.Namespace{Name: ns})
	labels := map[string]string{"app": "web"}
	podSpec := corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}}
	web := uiCustomObject("Rollout", ns, "web", map[string]any{"replicas": int64(2)})
	adminCreate(t, admin, web)
	rs := &appsv1.ReplicaSet{
		Namespace: ns, Name: "web-r1", OwnerReferences: uiOwnerRef(web),
		Spec: appsv1.ReplicaSetSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{Labels: labels, Spec: podSpec}},
	}
	adminCreate(t, admin, rs)
	rs.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))
	adminCreate(t, admin, &corev1.Pod{Namespace: ns, Name: "web-r1-a", Labels: labels, OwnerReferences: uiOwnerRef(rs), Spec: podSpec})
	adminCreate(t, admin, uiCustomObject("Widget", ns, "w", make(map[string]any)))

	app := pooledTestAppOn(t, cfg, ns)
	app.goToNamespace(ns)
	_, _ = app.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	// The link opens in the other panel: the Rollout row in the left one.
	app.activePanel = 1
	runLinkCmds(t, app, app.openObjectRef(app.ownConn(1), 1, models.ObjectRef{GVR: rollouts, Namespaced: true, Namespace: ns, Name: "web"}, ""))
	_ = app.setPanelMode(t.Context(), app.rightPanel, PanelModeInfo, "test")
	app.activePanel = 0

	// The right panel's Info runs the overview engine of the namespace:
	// it learns the Rollout kind from the pod's chain and then shows it.
	// Its ticks do not run here, so the test asks it to read again.
	want := "Pods of rollouts." + uiWorkloadGroup + " " + ns + "/web"
	deadline := time.Now().Add(30 * time.Second)
	for {
		if l, ok := app.rightPanel.ensureActiveWidget(t.Context()).(panelcontent.SelectionListener); ok {
			_ = l.OnSelectionChanged(t.Context(), panelcontent.Selection{})
		}
		app.rightPanel.invalidateRenderCache()
		app.invalidateView("test")
		s := dashboardScreen(app)
		if strings.Contains(s, want) && strings.Contains(s, "0/2 ready, 1 pods") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Info never showed %q with the Rollout's pod; last screen:\n%s", want, s)
		}
		time.Sleep(100 * time.Millisecond)
	}
	key := app.ownConn(0).clusterKey()
	if !models.IsWorkloadTarget(key, rollouts) || models.IsWorkloadTarget(key, widgets) {
		t.Fatalf("workload targets: rollouts %v widgets %v, want only rollouts", models.IsWorkloadTarget(key, rollouts), models.IsWorkloadTarget(key, widgets))
	}
	wantHistory := promhistory.Target{Kind: "Rollout", Custom: true, Via: "ReplicaSet", Namespace: ns, Name: "web"}
	if got, err := workloadHistoryTarget(key, models.InfoWorkload{GVR: rollouts, Namespace: ns, Name: "web"}); err != nil || got != wantHistory {
		t.Fatalf("history target = %+v, %v; want %+v", got, err, wantHistory)
	}

	_, _ = app.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	_, _ = app.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
	if !app.dashboardOpen() {
		t.Fatal("C-x g did not open the dashboard")
	}
	if got := app.dash.model.Root(); got.Kind != dashboard.KindWorkload || got.Name != "web" || got.Workload != rollouts {
		t.Fatalf("target = %+v, want the Rollout under the cursor", got)
	}
	waitDashboard(t, app, "› rollouts."+uiWorkloadGroup+" web")
	_, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	// A row of a kind that owns no pods opens the namespace dashboard,
	// whose workload list has the Rollout.
	app.activePanel = 1
	runLinkCmds(t, app, app.openObjectRef(app.ownConn(1), 1, models.ObjectRef{GVR: widgets, Namespaced: true, Namespace: ns, Name: "w"}, ""))
	app.activePanel = 0
	_, _ = app.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	_, _ = app.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
	if got := app.dash.model.Root(); got.Kind != dashboard.KindScope || got.Namespace != ns {
		t.Fatalf("target = %+v, want the namespace %s", got, ns)
	}
	waitDashboard(t, app, "rollouts."+uiWorkloadGroup+" web")
}
