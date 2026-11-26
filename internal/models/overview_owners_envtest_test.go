package models

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
)

const ownersTestGroup = "workloads.kc-test.io"

func ownersTestGVR(resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: ownersTestGroup, Version: "v1", Resource: resource}
}

// installWorkloadCRD installs a namespaced CRD of ownersTestGroup that
// keeps any field; with scale it has a scale subresource reading
// .spec.replicas.
func installWorkloadCRD(t *testing.T, cfg *rest.Config, kind, plural string, scale bool) {
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
		Name: plural + "." + ownersTestGroup,
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group:    ownersTestGroup,
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

// ownersTestClient is an admin client of cfg for the test's objects.
func ownersTestClient(t *testing.T, cfg *rest.Config) crclient.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	cli, err := crclient.New(cfg, crclient.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return cli
}

// customObject is an object of kind in ownersTestGroup; replicas < 0
// leaves spec.replicas out.
func customObject(kind, ns, name string, replicas int64) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": make(map[string]any)}}
	u.SetAPIVersion(ownersTestGroup + "/v1")
	u.SetKind(kind)
	u.SetNamespace(ns)
	u.SetName(name)
	if replicas >= 0 {
		u.Object["spec"] = map[string]any{"replicas": replicas}
	}
	return u
}

// ownerRefTo is the controller reference to o, which carries its kind.
func ownerRefTo(o crclient.Object) []metav1.OwnerReference {
	gvk := o.GetObjectKind().GroupVersionKind()
	return []metav1.OwnerReference{{APIVersion: gvk.GroupVersion().String(), Kind: gvk.Kind, Name: o.GetName(), UID: o.GetUID(), Controller: new(true)}}
}

var ownersTestLabels = map[string]string{"app": "web"}

var ownersTestPodSpec = corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}}

// createOwnedPod creates a pod controlled by owner; envtest runs no
// kubelet, so it stays pending and not ready.
func createOwnedPod(t *testing.T, cli crclient.Client, ns, name string, owner crclient.Object) {
	t.Helper()
	mustCreate(t, cli, &corev1.Pod{Namespace: ns, Name: name, Labels: ownersTestLabels, OwnerReferences: ownerRefTo(owner), Spec: ownersTestPodSpec})
}

// createOwnedReplicaSet creates a ReplicaSet controlled by owner and
// returns it with its kind set, for the references of its pods.
func createOwnedReplicaSet(t *testing.T, cli crclient.Client, ns, name string, owner crclient.Object) *appsv1.ReplicaSet {
	t.Helper()
	rs := &appsv1.ReplicaSet{
		Namespace: ns, Name: name, OwnerReferences: ownerRefTo(owner),
		Spec: appsv1.ReplicaSetSpec{Selector: &metav1.LabelSelector{MatchLabels: ownersTestLabels}, Template: corev1.PodTemplateSpec{Labels: ownersTestLabels, Spec: ownersTestPodSpec}},
	}
	mustCreate(t, cli, rs)
	rs.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))
	return rs
}

func infoPodsBar(s InfoSummary) InfoBar {
	for _, b := range s.Bars {
		if b.Label == "Pods" {
			return b
		}
	}
	return InfoBar{}
}

// waitInfoPods focuses Info on w until its Pods bar satisfies ok.
func waitInfoPods(t *testing.T, info *OverviewInfo, what string, w InfoWorkload, ok func(InfoBar) bool) InfoSummary {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last InfoSummary
	for time.Now().Before(deadline) {
		last = info.Summary(InfoFocus{Workload: &w})
		if ok(infoPodsBar(last)) {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("waited 30s for %s: last summary %+v", what, last)
	return last
}

// watchedOwnerResources are the resources of the owner kinds that the
// engine of info has informers for.
func watchedOwnerResources(info *OverviewInfo) []string {
	var out []string
	for _, g := range info.engine.owners.watched() {
		out = append(out, g.Resource)
	}
	slices.Sort(out)
	return out
}

// TestOverviewCustomWorkloadsOnARealCache runs Info against an API server
// of its own with test CRDs and pods that carry owner references (envtest
// runs no controllers): a Rollout owns its pods through a ReplicaSet and
// has a scale subresource, a PodSet owns its pod and has none, a Widget
// owns no pods. The engine watches only the two kinds the pods' chains
// reach, a Scaler scaled to 0 is read once Info focuses it, and a pod of a
// deleted Rollout does not count for the new one of the same name.
func TestOverviewCustomWorkloadsOnARealCache(t *testing.T) {
	t.Parallel()
	cfg := kcenvtest.Start(t)
	ctx := t.Context()
	installWorkloadCRD(t, cfg, "Rollout", "rollouts", true)
	installWorkloadCRD(t, cfg, "Scaler", "scalers", true)
	installWorkloadCRD(t, cfg, "PodSet", "podsets", false)
	installWorkloadCRD(t, cfg, "Widget", "widgets", false)
	rollouts, scalers, podSets, widgets := ownersTestGVR("rollouts"), ownersTestGVR("scalers"), ownersTestGVR("podsets"), ownersTestGVR("widgets")

	cli := ownersTestClient(t, cfg)
	const ns = "shop"
	mustCreate(t, cli, &corev1.Namespace{Name: ns})
	web := customObject("Rollout", ns, "web", 3)
	mustCreate(t, cli, web)
	createOwnedPod(t, cli, ns, "web-r1-a", createOwnedReplicaSet(t, cli, ns, "web-r1", web))
	mustCreate(t, cli, customObject("Rollout", ns, "idle", 0))
	ps := customObject("PodSet", ns, "ps", -1)
	mustCreate(t, cli, ps)
	createOwnedPod(t, cli, ns, "ps-1", ps)
	mustCreate(t, cli, customObject("Widget", ns, "w", -1))
	mustCreate(t, cli, customObject("Scaler", ns, "s", 0))
	// A Rollout recreated with the same name: the pod names the old UID.
	old := customObject("Rollout", ns, "old", 1)
	mustCreate(t, cli, old)
	createOwnedPod(t, cli, ns, "stale", old)
	if err := cli.Delete(ctx, old); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, cli, customObject("Rollout", ns, "old", 1))

	cl, err := kccluster.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	kctesting.RunInBackground(t, "cluster", cl.Start)
	key := kccluster.Key{ContextName: kctesting.UniqueName("custom-workloads")}
	info := NewOverviewInfo(Deps{Cl: cl, Ctx: ctx, ClusterKey: key}, ns)
	t.Cleanup(info.Close)

	sum := waitInfoPods(t, info, "the Rollout's pod through its ReplicaSet", InfoWorkload{GVR: rollouts, Namespace: ns, Name: "web"}, func(b InfoBar) bool {
		return b.Detail == "0/3 ready, 1 pods"
	})
	if want := "Pods of rollouts." + ownersTestGroup + " shop/web"; sum.BarsTitle != want {
		t.Errorf("title = %q, want %q", sum.BarsTitle, want)
	}
	waitInfoPods(t, info, "the PodSet's pod and why it has no desired count", InfoWorkload{GVR: podSets, Namespace: ns, Name: "ps"}, func(b InfoBar) bool {
		return b.Detail == "0/1 ready" && slices.Equal(b.More, []string{"no desired count: podsets." + ownersTestGroup + " declares no scale subresource"})
	})
	waitInfoPods(t, info, "the idle Rollout scaled to 0", InfoWorkload{GVR: rollouts, Namespace: ns, Name: "idle"}, func(b InfoBar) bool {
		return b.Detail == "scaled to 0, 0 pods"
	})
	waitInfoPods(t, info, "the recreated Rollout without the old one's pod", InfoWorkload{GVR: rollouts, Namespace: ns, Name: "old"}, func(b InfoBar) bool {
		return b.Detail == "0/1 ready, 0 pods"
	})

	// Only the kinds the pods' chains reach have informers; the Widget and
	// the Scaler, which own no pods, have none.
	if got := watchedOwnerResources(info); !slices.Equal(got, []string{"podsets", "rollouts"}) {
		t.Fatalf("watched owner kinds = %v, want podsets and rollouts", got)
	}
	r, err := info.engine.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range r.pods {
		if p.name == "stale" && len(p.owners) > 0 {
			t.Errorf("the pod of the deleted Rollout has owners %v", p.owners)
		}
	}
	for _, tt := range []struct {
		gvr  schema.GroupVersionResource
		want bool
	}{{rollouts, true}, {podSets, true}, {scalers, true}, {widgets, false}} {
		if got := IsWorkloadTarget(key, tt.gvr); got != tt.want {
			t.Errorf("IsWorkloadTarget(%v) = %v, want %v", tt.gvr, got, tt.want)
		}
	}
	if chain, ok := WorkloadChainOf(key, rollouts); !ok || chain.Kind != "Rollout" || len(chain.Vias) != 1 || !slices.Equal(chain.Vias[0], []schema.GroupKind{{Group: "apps", Kind: "ReplicaSet"}}) {
		t.Errorf("rollouts chain = %+v, %v; want Rollout through a ReplicaSet", chain, ok)
	}

	// A focused kind with a scale subresource is read although it owns no
	// pods; one without is not watched.
	waitInfoPods(t, info, "the Scaler scaled to 0", InfoWorkload{GVR: scalers, Namespace: ns, Name: "s"}, func(b InfoBar) bool {
		return b.Detail == "scaled to 0, 0 pods"
	})
	info.Summary(InfoFocus{Workload: &InfoWorkload{GVR: widgets, Namespace: ns, Name: "w"}})
	kctesting.Eventually(t, 10*time.Second, 100*time.Millisecond, func() bool {
		info.engine.owners.mu.Lock()
		defer info.engine.owners.mu.Unlock()
		return info.engine.owners.wanted[widgets] >= 0
	})
	time.Sleep(2 * overviewInterval)
	if got := watchedOwnerResources(info); !slices.Equal(got, []string{"podsets", "rollouts", "scalers"}) {
		t.Fatalf("watched owner kinds = %v, want podsets, rollouts and scalers", got)
	}
}

// TestOverviewCustomWorkloadsNamespaceOnlyUser runs Info as a user who may
// read pods, ReplicaSets and Rollouts in one namespace only: the Rollout's
// informer runs in the overview's namespace cache, a kind the user may not
// list (PodSet) is trusted from the owner references, and the CRD the user
// may not read says why the Rollout's desired count is unknown.
func TestOverviewCustomWorkloadsNamespaceOnlyUser(t *testing.T) {
	t.Parallel()
	cfg := kcenvtest.Start(t)
	installWorkloadCRD(t, cfg, "Rollout", "rollouts", true)
	installWorkloadCRD(t, cfg, "PodSet", "podsets", false)
	user, ns := kctesting.UniqueName("owners-user"), kctesting.UniqueName("owners-team")
	e := newNamespaceUserEnvOn(t, cfg, user, ns)
	cli := ownersTestClient(t, cfg)
	grantReads(t, cli, ns, user, rbacv1.PolicyRule{APIGroups: []string{"apps"}, Resources: []string{"replicasets"}, Verbs: []string{"get", "list", "watch"}},
		rbacv1.PolicyRule{APIGroups: []string{ownersTestGroup}, Resources: []string{"rollouts"}, Verbs: []string{"get", "list", "watch"}})
	kctesting.WaitForAccess(t, e.admin.GetClient(), user, ns, "apps", "replicasets", "list", "watch")
	kctesting.WaitForAccess(t, e.admin.GetClient(), user, ns, ownersTestGroup, "rollouts", "list", "watch")

	web := customObject("Rollout", ns, "web", 2)
	mustCreate(t, cli, web)
	createOwnedPod(t, cli, ns, "web-r1-a", createOwnedReplicaSet(t, cli, ns, "web-r1", web))
	ps := customObject("PodSet", ns, "ps", -1)
	mustCreate(t, cli, ps)
	createOwnedPod(t, cli, ns, "ps-1", ps)

	info := NewOverviewInfo(e.deps, ns)
	t.Cleanup(info.Close)
	crd := "customresourcedefinitions.apiextensions.k8s.io rollouts." + ownersTestGroup
	waitInfoPods(t, info, "the Rollout's pod and why its desired count is unknown", InfoWorkload{GVR: ownersTestGVR("rollouts"), Namespace: ns, Name: "web"}, func(b InfoBar) bool {
		return b.Detail == "0/1 ready" && slices.Equal(b.More, []string{"desired count unknown: " + crd + ": " + problemForbidden})
	})
	waitInfoPods(t, info, "the PodSet trusted from the reference", InfoWorkload{GVR: ownersTestGVR("podsets"), Namespace: ns, Name: "ps"}, func(b InfoBar) bool {
		return b.Detail == "0/1 ready"
	})
	if got := watchedOwnerResources(info); !slices.Equal(got, []string{"rollouts"}) {
		t.Fatalf("watched owner kinds = %v, want rollouts only", got)
	}
	if errs := e.errors(); len(errs) > 0 {
		t.Fatalf("reported read errors: %v", errs)
	}
}

// grantReads binds a Role with rules in namespace to user.
func grantReads(t *testing.T, cli crclient.Client, namespace, user string, rules ...rbacv1.PolicyRule) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	role := &rbacv1.Role{Name: "owner-reader", Namespace: namespace, Rules: rules}
	if err := cli.Create(ctx, role); err != nil {
		t.Fatalf("create role: %v", err)
	}
	binding := &rbacv1.RoleBinding{
		Name: "owner-reader", Namespace: namespace,
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: user}},
	}
	if err := cli.Create(ctx, binding); err != nil {
		t.Fatalf("create rolebinding: %v", err)
	}
}
