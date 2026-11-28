package ui

import (
	"context"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	navui "github.com/stokaro/kc/internal/navigation"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

// impactCluster starts a kc cluster on cfg and returns it with a client.
func impactCluster(t *testing.T, cfg *rest.Config) (*kccluster.Cluster, crclient.Client) {
	t.Helper()
	cl, err := kccluster.New(cfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	kctesting.RunInBackground(t, "cluster", cl.Start)
	t.Cleanup(cl.Stop)
	c, err := crclient.New(cfg, crclient.Options{})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return cl, c
}

// create creates obj and deletes it when the test ends.
func create(t *testing.T, c crclient.Client, obj crclient.Object) {
	t.Helper()
	if err := c.Create(t.Context(), obj); err != nil {
		t.Fatalf("create %s: %v", obj.GetName(), err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		_ = c.Delete(ctx, obj)
	})
}

func impactPod(ns, name, node string) *corev1.Pod {
	return &corev1.Pod{Namespace: ns, Name: name, Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "app", Image: "nginx"}}}}
}

// helmSecret is a Helm release revision as Helm stores it.
func helmSecret(ns, release string, revision int) *corev1.Secret {
	rev := strconv.Itoa(revision)
	return &corev1.Secret{
		Namespace: ns, Name: "sh.helm.release.v1." + release + ".v" + rev,
		Labels: map[string]string{"owner": "helm", "name": release, "version": rev, "status": "deployed"},
		Type:   "helm.sh/release.v1",
	}
}

// The namespace count reads the API server, not the cache: kinds kc has no
// informer for are counted as well, Helm releases once however many
// revisions they keep (#374).
func TestCountNamespaceImpactEnvtest(t *testing.T) {
	kcenvtest.Require(t, testCfg)
	cl, c := impactCluster(t, testCfg)
	ns := kctesting.UniqueName("impact-ns")
	create(t, c, &corev1.Namespace{Name: ns})
	for _, name := range []string{"web-config", "db-config"} {
		create(t, c, &corev1.ConfigMap{Namespace: ns, Name: name})
	}
	create(t, c, &corev1.Secret{Namespace: ns, Name: "token"})
	create(t, c, helmSecret(ns, "web", 1))
	create(t, c, helmSecret(ns, "web", 2))
	create(t, c, helmSecret(ns, "db", 1))
	create(t, c, &corev1.Service{Namespace: ns, Name: "web", Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}}})
	create(t, c, impactPod(ns, "web-0", ""))
	create(t, c, impactPod(ns, "web-1", ""))
	create(t, c, &corev1.PersistentVolumeClaim{Namespace: ns, Name: "data", Spec: corev1.PersistentVolumeClaimSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}},
	}})
	// A configmap in another namespace is not counted.
	other := kctesting.UniqueName("impact-other")
	create(t, c, &corev1.Namespace{Name: other})
	create(t, c, &corev1.ConfigMap{Namespace: other, Name: "web-config"})

	impact := countDeleteImpact(t.Context(), deleteTarget{gvr: gvrNamespaces, name: ns, cluster: cl, context: "envtest"})
	want := []string{
		"It holds 2 pods, 1 service, 1 PVC, 2 configmaps, 4 secrets and 2 Helm releases.",
		"Counted: deployments, statefulsets, daemonsets, cronjobs, jobs, pods, services, ingresses, PVCs, configmaps, secrets, Helm releases. Other kinds are not counted; everything in it is deleted.",
	}
	if !slices.Equal(impact.lines, want) {
		t.Fatalf("impact\n%q\nwant\n%q", impact.lines, want)
	}
}

// The node count finds the pods bound to the node in every namespace, and
// no other pod.
func TestCountNodeImpactEnvtest(t *testing.T) {
	kcenvtest.Require(t, testCfg)
	cl, c := impactCluster(t, testCfg)
	node := kctesting.UniqueName("impact-node")
	create(t, c, &corev1.Node{Name: node})
	for _, n := range []string{kctesting.UniqueName("impact-a"), kctesting.UniqueName("impact-b")} {
		create(t, c, &corev1.Namespace{Name: n})
		create(t, c, impactPod(n, "on-node", node))
		create(t, c, impactPod(n, "elsewhere", node+"-other"))
	}
	impact := countDeleteImpact(t.Context(), deleteTarget{gvr: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, name: node, cluster: cl, context: "envtest"})
	if want := []string{"2 pods bound to it are deleted with it."}; !slices.Equal(impact.lines, want) {
		t.Fatalf("impact %q, want %q", impact.lines, want)
	}
}

// A PersistentVolume names the claim bound to it and what its reclaim
// policy does to the storage.
func TestCountPersistentVolumeImpactEnvtest(t *testing.T) {
	kcenvtest.Require(t, testCfg)
	cl, c := impactCluster(t, testCfg)
	pv := func(name string, policy corev1.PersistentVolumeReclaimPolicy, claim *corev1.ObjectReference) *corev1.PersistentVolume {
		return &corev1.PersistentVolume{Name: name, Spec: corev1.PersistentVolumeSpec{
			Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			PersistentVolumeReclaimPolicy: policy,
			PersistentVolumeSource:        corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/data"}},
			ClaimRef:                      claim,
		}}
	}
	pvGVR := schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}
	bound, free := kctesting.UniqueName("impact-pv"), kctesting.UniqueName("impact-pv")
	create(t, c, pv(bound, corev1.PersistentVolumeReclaimDelete, &corev1.ObjectReference{Namespace: "shop", Name: "data"}))
	create(t, c, pv(free, corev1.PersistentVolumeReclaimRetain, nil))
	for name, want := range map[string][]string{
		bound: {"Claim shop/data is bound to it (phase Pending) and loses its volume.", "Reclaim policy Delete: the storage is deleted too."},
		free:  {"No claim is bound to it (phase Pending).", "Reclaim policy Retain: the storage stays."},
	} {
		impact := countDeleteImpact(t.Context(), deleteTarget{gvr: pvGVR, name: name, cluster: cl, context: "envtest"})
		if !slices.Equal(impact.lines, want) {
			t.Errorf("impact of %s\n%q\nwant\n%q", name, impact.lines, want)
		}
	}
}

// A CustomResourceDefinition counts its objects in all namespaces. It
// runs on its own control plane: the CRD must not show in the others'
// discovery.
func TestCountCRDImpactEnvtest(t *testing.T) {
	t.Parallel()
	cfg := kcenvtest.Start(t)
	cl, c := impactCluster(t, cfg)
	const group = "count.kc-test.io"
	apiext, err := apiextclient.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	crd := &apiextensionsv1.CustomResourceDefinition{
		Name: "gadgets." + group,
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "gadgets", Singular: "gadget", Kind: "Gadget", ListKind: "GadgetList"},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1alpha1", Served: true, Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object", XPreserveUnknownFields: new(true)}}},
				{Name: "v1", Served: true, Storage: true, Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object", XPreserveUnknownFields: new(true)}}},
			},
		},
	}
	if _, err := apiext.ApiextensionsV1().CustomResourceDefinitions().Create(t.Context(), crd, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create CRD: %v", err)
	}
	gadgets := schema.GroupVersionResource{Group: group, Version: "v1", Resource: "gadgets"}
	target := deleteTarget{gvr: schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}, name: crd.Name, cluster: cl, context: "envtest"}
	// The first list may come before the API server serves the new kind.
	var impact deleteImpact
	kctesting.Eventually(t, 15*time.Second, 100*time.Millisecond, func() bool {
		impact = countDeleteImpact(t.Context(), target)
		return slices.Equal(impact.lines, []string{"There are no gadgets." + group + " objects."})
	}, "the CRD never counted zero objects")

	create(t, c, &corev1.Namespace{Name: "shop"})
	create(t, c, &corev1.Namespace{Name: "team"})
	for _, ref := range [][2]string{{"shop", "a"}, {"shop", "b"}, {"team", "a"}} {
		u := &unstructured.Unstructured{}
		u.SetAPIVersion(group + "/v1")
		u.SetKind("Gadget")
		u.SetNamespace(ref[0])
		u.SetName(ref[1])
		if _, err := cl.Dynamic().Resource(gadgets).Namespace(ref[0]).Create(t.Context(), u, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create gadget %s/%s: %v", ref[0], ref[1], err)
		}
	}
	impact = countDeleteImpact(t.Context(), target)
	if want := []string{"3 gadgets." + group + " objects in all namespaces are deleted with it."}; !slices.Equal(impact.lines, want) {
		t.Fatalf("impact %q, want %q", impact.lines, want)
	}
}

// namespacesApp starts kc on the package control plane in context envtest
// with /namespaces open in the left panel, listing names.
func namespacesApp(t *testing.T, names ...string) *App {
	t.Helper()
	ctx := t.Context()
	kc := clientcmdapi.NewConfig()
	kc.Clusters["envtest"] = &clientcmdapi.Cluster{Server: testCfg.Host, CertificateAuthorityData: testCfg.CAData}
	kc.AuthInfos["envtest"] = &clientcmdapi.AuthInfo{ClientCertificateData: testCfg.CertData, ClientKeyData: testCfg.KeyData, Token: testCfg.BearerToken}
	kc.Contexts["envtest"] = &clientcmdapi.Context{Cluster: "envtest", AuthInfo: "envtest", Namespace: names[0]}
	kc.CurrentContext = "envtest"
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*kc, path); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	app := NewApp()
	app.cancel()
	app.ctx, app.cancel = context.WithCancel(ctx)
	t.Cleanup(app.cancel)
	app.width, app.height = 120, 40
	model, _ := app.Update(tea.WindowSizeMsg{Width: app.width, Height: app.height})
	app = model.(*App)
	app.clPool = kccluster.NewPool(time.Minute, 0)
	app.clPool.Start()
	t.Cleanup(app.clPool.Stop)
	usePoolContext(t, app, &kubeconfig.Context{Name: "envtest", Namespace: names[0], Kubeconfig: &kubeconfig.Kubeconfig{Path: path, Context: "envtest"}})
	app.goToNamespace(names[0])
	nav := app.navigatorFor(0)
	kctesting.Eventually(t, 15*time.Second, 50*time.Millisecond, func() bool {
		_, err := navui.GoTo(ctx, nav, []navui.GoToStep{{SelectionID: "namespaces", Enter: true}})
		return err == nil
	}, "the root never listed namespaces")
	kctesting.Eventually(t, 15*time.Second, 50*time.Millisecond, func() bool {
		for _, n := range names {
			if _, ok := nav.Current().ItemByID(ctx, n); !ok {
				return false
			}
		}
		return true
	}, "/namespaces never listed "+strings.Join(names, ", "))
	app.syncPanelWithNavigator(0)
	app.setActivePanel(0, "test")
	return app
}

// typeNameAndConfirm types name into the open delete dialog, presses M-y
// and returns the result of the delete it sends.
func typeNameAndConfirm(t *testing.T, app *App, name string) resourceDeletedMsg {
	t.Helper()
	for _, msg := range pressDialog(t, app, append(textKeys(name), keyAltY)...) {
		answer, ok := msg.(DeleteConfirmMsg)
		if !ok || !answer.Confirm {
			continue
		}
		for _, res := range sendApp(app, answer) {
			if r, ok := res.(resourceDeletedMsg); ok {
				return r
			}
		}
	}
	t.Fatalf("M-y with %s typed sent no delete", name)
	return resourceDeletedMsg{}
}

// F8 on a namespace in /namespaces, against the API server: the dialog
// counts what is in it, waits for the name and deletes the instance the
// row showed. A namespace deleted and created again while the dialog was
// open is not deleted (#374).
func TestDeleteNamespaceThroughTheDialogEnvtest(t *testing.T) {
	kcenvtest.Require(t, testCfg)
	ctx := t.Context()
	_, c := impactCluster(t, testCfg)
	cs, err := kubernetes.NewForConfig(testCfg)
	if err != nil {
		t.Fatal(err)
	}
	victim, replaced := kctesting.UniqueName("ns-victim"), kctesting.UniqueName("ns-replaced")
	victimNS := &corev1.Namespace{Name: victim}
	create(t, c, victimNS)
	create(t, c, &corev1.ConfigMap{Namespace: victim, Name: "web-config"})
	replacedNS := &corev1.Namespace{Name: replaced}
	create(t, c, replacedNS)
	app := namespacesApp(t, victim, replaced)

	app.leftPanel.SelectByRowID(ctx, victim)
	_, count := app.Update(keyF8)
	if app.modalManager.TopName() != "delete_confirm" || app.pendingDelete == nil || app.pendingDelete.uid != victimNS.UID {
		t.Fatalf("F8 opened %q for %+v, want the instance %s", app.modalManager.TopName(), app.pendingDelete, victimNS.UID)
	}
	deliverCount(t, app, count)
	if view := deleteDialogText(app); !strings.Contains(view, "It holds 1 configmap.") {
		t.Fatalf("dialog after the count:\n%s", view)
	}
	if got := deleteAnswer(t, app, keyAltY); got != nil {
		t.Fatalf("M-y before the name answered %+v", *got)
	}
	if res := typeNameAndConfirm(t, app, victim); res.err != nil {
		t.Fatalf("delete %s: %v", victim, res.err)
	}
	var got corev1.Namespace
	if err := c.Get(ctx, crclient.ObjectKey{Name: victim}, &got); err != nil || got.DeletionTimestamp == nil || got.UID != victimNS.UID {
		t.Fatalf("%s after the delete: %v, deleted at %v, uid %s", victim, err, got.DeletionTimestamp, got.UID)
	}

	// Another client deletes the namespace and creates it again while the
	// dialog is open. envtest runs no namespace controller: the test
	// finalizes the namespace itself.
	app.leftPanel.SelectByRowID(ctx, replaced)
	app.Update(keyF8)
	if app.pendingDelete == nil || app.pendingDelete.uid != replacedNS.UID {
		t.Fatalf("F8 on %s is pending %+v", replaced, app.pendingDelete)
	}
	if err := c.Delete(ctx, replacedNS); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, crclient.ObjectKey{Name: replaced}, &got); err != nil {
		t.Fatal(err)
	}
	got.Spec.Finalizers = nil
	if _, err := cs.CoreV1().Namespaces().Finalize(ctx, &got, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("finalize %s: %v", replaced, err)
	}
	// As the namespace controller does, delete again once finalized.
	if err := c.Delete(ctx, &got); err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("delete the finalized %s: %v", replaced, err)
	}
	kctesting.Eventually(t, 10*time.Second, 50*time.Millisecond, func() bool {
		return apierrors.IsNotFound(c.Get(ctx, crclient.ObjectKey{Name: replaced}, &corev1.Namespace{}))
	}, replaced+" never went away")
	replacement := &corev1.Namespace{Name: replaced}
	create(t, c, replacement)

	if res := typeNameAndConfirm(t, app, replaced); !apierrors.IsConflict(res.err) {
		t.Fatalf("delete of the replaced %s: %v, want a Conflict", replaced, res.err)
	}
	if err := c.Get(ctx, crclient.ObjectKey{Name: replaced}, &got); err != nil || got.UID != replacement.UID || got.DeletionTimestamp != nil {
		t.Fatalf("the replacement %s: %v, uid %s (want %s), deleted at %v", replaced, err, got.UID, replacement.UID, got.DeletionTimestamp)
	}
}
