package models

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

var deploymentGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}

func TestPodReferences(t *testing.T) {
	t.Parallel()
	cmEnv := func(name string) corev1.EnvVar {
		return corev1.EnvVar{Name: "A", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{Name: name, Key: "k"}}}
	}
	cases := []struct {
		name string
		pod  corev1.Pod
		want []PodReference
	}{
		{
			name: "empty pod names nothing",
			pod:  corev1.Pod{},
			want: nil,
		},
		{
			name: "service account and deprecated field",
			pod:  corev1.Pod{Spec: corev1.PodSpec{DeprecatedServiceAccount: "old"}},
			want: []PodReference{{GVR: serviceAccountGVR, Name: "old", Via: "serviceAccount"}},
		},
		{
			name: "serviceAccountName wins over the deprecated field",
			pod:  corev1.Pod{Spec: corev1.PodSpec{ServiceAccountName: "new", DeprecatedServiceAccount: "old"}},
			want: []PodReference{{GVR: serviceAccountGVR, Name: "new", Via: "serviceAccount"}},
		},
		{
			name: "volumes of every supported kind",
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "web"},
				Spec: corev1.PodSpec{Volumes: []corev1.Volume{
					{Name: "c", ConfigMap: &corev1.ConfigMapVolumeSource{Name: "cm"}},
					{Name: "s", Secret: &corev1.SecretVolumeSource{SecretName: "sec"}},
					{Name: "p", PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}},
					{Name: "scratch", Ephemeral: &corev1.EphemeralVolumeSource{}},
					{Name: "proj", Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{
						{ConfigMap: &corev1.ConfigMapProjection{Name: "pcm"}},
						{Secret: &corev1.SecretProjection{Name: "psec"}},
						{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token"}},
					}}},
					{Name: "e", EmptyDir: &corev1.EmptyDirVolumeSource{}},
				}},
			},
			want: []PodReference{
				{GVR: configMapGVR, Name: "cm", Via: "volume"},
				{GVR: secretGVR, Name: "sec", Via: "volume"},
				{GVR: pvcGVR, Name: "data", Via: "volume"},
				{GVR: pvcGVR, Name: "web-scratch", Via: "ephemeral volume"},
				{GVR: configMapGVR, Name: "pcm", Via: "projected volume"},
				{GVR: secretGVR, Name: "psec", Via: "projected volume"},
			},
		},
		{
			name: "env and envFrom in init, regular and ephemeral containers",
			pod: corev1.Pod{Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{Name: "init", EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{Name: "init-sec"}}}}},
				Containers: []corev1.Container{{Name: "app",
					EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{Name: "app-cm"}}},
					Env: []corev1.EnvVar{
						{Name: "PLAIN", Value: "x"},
						cmEnv("key-cm"),
						{Name: "S", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{Name: "key-sec", Key: "k"}}},
						{Name: "F", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
					},
				}},
				EphemeralContainers: []corev1.EphemeralContainer{{Name: "debug", Env: []corev1.EnvVar{cmEnv("debug-cm")}}},
			}},
			want: []PodReference{
				{GVR: secretGVR, Name: "init-sec", Via: "envFrom"},
				{GVR: configMapGVR, Name: "app-cm", Via: "envFrom"},
				{GVR: configMapGVR, Name: "key-cm", Via: "env"},
				{GVR: secretGVR, Name: "key-sec", Via: "env"},
				{GVR: configMapGVR, Name: "debug-cm", Via: "env"},
			},
		},
		{
			name: "imagePullSecrets",
			pod:  corev1.Pod{Spec: corev1.PodSpec{ImagePullSecrets: []corev1.LocalObjectReference{{Name: "regcred"}, {Name: ""}}}},
			want: []PodReference{{GVR: secretGVR, Name: "regcred", Via: "imagePullSecrets"}},
		},
		{
			name: "same object and way once, another way again",
			pod: corev1.Pod{Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{{Name: "c", ConfigMap: &corev1.ConfigMapVolumeSource{Name: "cm"}}},
				Containers: []corev1.Container{
					{Name: "a", Env: []corev1.EnvVar{cmEnv("cm")}},
					{Name: "b", Env: []corev1.EnvVar{cmEnv("cm")}},
				},
			}},
			want: []PodReference{
				{GVR: configMapGVR, Name: "cm", Via: "volume"},
				{GVR: configMapGVR, Name: "cm", Via: "env"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := PodReferences(&tc.pod)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("PodReferences:\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func testMapper() meta.RESTMapper {
	m := meta.NewDefaultRESTMapper(nil)
	m.Add(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, meta.RESTScopeNamespace)
	m.Add(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"}, meta.RESTScopeNamespace)
	m.Add(schema.GroupVersionKind{Version: "v1", Kind: "Node"}, meta.RESTScopeRoot)
	return m
}

func TestResolveOwners(t *testing.T) {
	t.Parallel()
	yes := true
	no := false
	cases := []struct {
		name      string
		mapper    meta.RESTMapper
		namespace string
		owners    []metav1.OwnerReference
		want      []RelatedRef
	}{
		{
			name:      "controller first, namespaced owner in the dependent's namespace",
			mapper:    testMapper(),
			namespace: "shop",
			owners: []metav1.OwnerReference{
				{APIVersion: "apps/v1", Kind: "Deployment", Name: "other", UID: "u1", Controller: &no},
				{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-1", UID: "u2", Controller: &yes},
			},
			want: []RelatedRef{
				{GVR: replicaSetGVRForTest(), Namespace: "shop", Name: "web-1", Relation: "controller", UID: "u2", APIVersion: "apps/v1", Kind: "ReplicaSet"},
				{GVR: deploymentGVR, Namespace: "shop", Name: "other", Relation: "owner", UID: "u1", APIVersion: "apps/v1", Kind: "Deployment"},
			},
		},
		{
			name:      "cluster-scoped owner has no namespace",
			mapper:    testMapper(),
			namespace: "kube-system",
			owners:    []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: "n1", UID: "u", Controller: &yes}},
			want:      []RelatedRef{{GVR: nodeGVR, Name: "n1", Relation: "controller", UID: "u", APIVersion: "v1", Kind: "Node"}},
		},
		{
			name:      "unknown kind stays listed with the problem",
			mapper:    testMapper(),
			namespace: "shop",
			owners:    []metav1.OwnerReference{{APIVersion: "example.com/v1", Kind: "Widget", Name: "w"}},
			want:      []RelatedRef{{Name: "w", Relation: "owner", APIVersion: "example.com/v1", Kind: "Widget", Problem: "unknown kind example.com/v1 Widget"}},
		},
		{
			name:   "invalid apiVersion",
			mapper: testMapper(),
			owners: []metav1.OwnerReference{{APIVersion: "a/b/c", Kind: "X", Name: "x"}},
			want:   []RelatedRef{{Name: "x", Relation: "owner", APIVersion: "a/b/c", Kind: "X", Problem: `invalid apiVersion "a/b/c"`}},
		},
		{
			name:   "namespaced owner of a cluster-scoped object",
			mapper: testMapper(),
			owners: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "d"}},
			want:   []RelatedRef{{GVR: deploymentGVR, Name: "d", Relation: "owner", APIVersion: "apps/v1", Kind: "Deployment", Problem: "namespaced owner of a cluster-scoped object"}},
		},
		{
			name:   "no mapper",
			owners: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "d"}},
			want:   []RelatedRef{{Name: "d", Relation: "owner", APIVersion: "apps/v1", Kind: "Deployment", Problem: "no RESTMapper"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ResolveOwners(tc.mapper, tc.namespace, tc.owners)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ResolveOwners:\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func replicaSetGVRForTest() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
}

// fakeReader serves objects like the cache: lists filter by namespace, a
// missing object is NotFound.
type fakeReader struct {
	objs    map[schema.GroupVersionResource][]*unstructured.Unstructured
	listErr error
	reads   []string
}

func newFakeReader(t *testing.T, objs map[schema.GroupVersionResource][]runtime.Object) *fakeReader {
	t.Helper()
	r := &fakeReader{objs: map[schema.GroupVersionResource][]*unstructured.Unstructured{}}
	for gvr, list := range objs {
		for _, o := range list {
			m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
			if err != nil {
				t.Fatalf("convert: %v", err)
			}
			r.objs[gvr] = append(r.objs[gvr], &unstructured.Unstructured{Object: m})
		}
	}
	return r
}

func (r *fakeReader) ListByGVR(_ context.Context, gvr schema.GroupVersionResource, namespace string) (*unstructured.UnstructuredList, error) {
	r.reads = append(r.reads, "list "+gvr.Resource+" "+namespace)
	if r.listErr != nil {
		return nil, r.listErr
	}
	out := &unstructured.UnstructuredList{}
	for _, u := range r.objs[gvr] {
		if namespace == "" || u.GetNamespace() == namespace {
			out.Items = append(out.Items, *u.DeepCopy())
		}
	}
	return out, nil
}

func (r *fakeReader) GetByGVR(_ context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	r.reads = append(r.reads, "get "+gvr.Resource+" "+namespace+"/"+name)
	for _, u := range r.objs[gvr] {
		if u.GetNamespace() == namespace && u.GetName() == name {
			return u.DeepCopy(), nil
		}
	}
	return nil, apierrors.NewNotFound(gvr.GroupResource(), name)
}

func TestCheckOwners(t *testing.T) {
	t.Parallel()
	r := newFakeReader(t, map[schema.GroupVersionResource][]runtime.Object{
		deploymentGVR: {
			&metav1.PartialObjectMetadata{Namespace: "shop", Name: "live", UID: "u-live"},
			&metav1.PartialObjectMetadata{Namespace: "shop", Name: "recreated", UID: "u-new"},
		},
	})
	in := []RelatedRef{
		{GVR: deploymentGVR, Namespace: "shop", Name: "live", UID: "u-live"},
		{GVR: deploymentGVR, Namespace: "shop", Name: "recreated", UID: "u-old"},
		{GVR: deploymentGVR, Namespace: "shop", Name: "gone", UID: "u-gone"},
		{Name: "unresolved", Problem: "unknown kind"},
	}
	got := checkOwners(t.Context(), r, in)
	want := []string{"", "not found (an object with another UID has the name)", "not found", "unknown kind"}
	for i := range want {
		if got[i].Problem != want[i] {
			t.Errorf("owner %s: problem %q, want %q", got[i].Name, got[i].Problem, want[i])
		}
	}
	// The unresolved reference is never read.
	if len(r.reads) != 3 {
		t.Errorf("reads = %v, want 3 gets", r.reads)
	}

	errReader := &errGetReader{err: errors.New("boom")}
	got = checkOwners(t.Context(), errReader, []RelatedRef{{GVR: deploymentGVR, Namespace: "shop", Name: "x"}})
	if got[0].Problem != "unreadable: boom" {
		t.Errorf("read error: problem %q", got[0].Problem)
	}
}

type errGetReader struct{ err error }

func (r *errGetReader) ListByGVR(context.Context, schema.GroupVersionResource, string) (*unstructured.UnstructuredList, error) {
	return nil, r.err
}
func (r *errGetReader) GetByGVR(context.Context, schema.GroupVersionResource, string, string) (*unstructured.Unstructured, error) {
	return nil, r.err
}

func pod(ns, name string, labels map[string]string, spec corev1.PodSpec) *corev1.Pod {
	return &corev1.Pod{Namespace: ns, Name: name, Labels: labels, Spec: spec}
}

func TestUsedByFinders(t *testing.T) {
	t.Parallel()
	cmVolume := corev1.PodSpec{Volumes: []corev1.Volume{{Name: "c", ConfigMap: &corev1.ConfigMapVolumeSource{Name: "cfg"}}},
		Containers: []corev1.Container{{Name: "a", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{Name: "cfg"}}}}}}
	pathType := networkingv1.PathTypePrefix
	ingressTo := func(name, svc string) *networkingv1.Ingress {
		return &networkingv1.Ingress{
			Namespace: "shop", Name: name,
			Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "shop.example", HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{
				{Path: "/api", PathType: &pathType, Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: svc}}},
			}}}}},
		}
	}
	defaultBackend := &networkingv1.Ingress{
		Namespace: "shop", Name: "fallback",
		Spec: networkingv1.IngressSpec{DefaultBackend: &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "web"}}},
	}
	objs := map[schema.GroupVersionResource][]runtime.Object{
		podGVR: {
			pod("shop", "uses-cfg", map[string]string{"app": "web"}, cmVolume),
			pod("shop", "plain", map[string]string{"app": "db"}, corev1.PodSpec{}),
			pod("other", "uses-cfg-elsewhere", nil, cmVolume),
		},
		serviceGVR: {
			&corev1.Service{Namespace: "shop", Name: "web", Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}}},
			&corev1.Service{Namespace: "shop", Name: "db", Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "db"}}},
			&corev1.Service{Namespace: "shop", Name: "manual"},
		},
		ingressGVR: {ingressTo("front", "web"), ingressTo("admin", "db"), defaultBackend},
		pvGVR: {
			&corev1.PersistentVolume{Name: "pv-bound", Spec: corev1.PersistentVolumeSpec{ClaimRef: &corev1.ObjectReference{Namespace: "shop", Name: "data", UID: "claim-uid"}}},
			&corev1.PersistentVolume{Name: "pv-stale", Spec: corev1.PersistentVolumeSpec{ClaimRef: &corev1.ObjectReference{Namespace: "shop", Name: "data", UID: "old-uid"}}},
			&corev1.PersistentVolume{Name: "pv-other", Spec: corev1.PersistentVolumeSpec{ClaimRef: &corev1.ObjectReference{Namespace: "shop", Name: "other"}}},
			&corev1.PersistentVolume{Name: "pv-free"},
		},
		pvcGVR: {
			&corev1.PersistentVolumeClaim{Namespace: "shop", Name: "data", UID: types.UID("claim-uid"), Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv-bound"}},
			&corev1.PersistentVolumeClaim{Namespace: "shop", Name: "other", Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv-else"}},
		},
	}
	cases := []struct {
		name      string
		target    schema.GroupVersionResource
		namespace string
		object    string
		want      []string // id + relation
	}{
		{name: "pods using a configmap, own namespace only", target: configMapGVR, namespace: "shop", object: "cfg",
			want: []string{"v1/pods/shop/uses-cfg volume, envFrom"}},
		{name: "configmap nobody uses", target: configMapGVR, namespace: "shop", object: "unused", want: nil},
		{name: "services selecting a pod, none without selector", target: podGVR, namespace: "shop", object: "uses-cfg",
			want: []string{"v1/services/shop/web selector"}},
		{name: "missing pod has no users", target: podGVR, namespace: "shop", object: "gone", want: nil},
		{name: "ingresses using a service", target: serviceGVR, namespace: "shop", object: "web",
			want: []string{"networking.k8s.io/v1/ingresses/shop/front rule shop.example/api", "networking.k8s.io/v1/ingresses/shop/fallback default backend"}},
		{name: "claim bound to a volume", target: pvGVR, object: "pv-bound",
			want: []string{"v1/persistentvolumeclaims/shop/data claim"}},
		{name: "claimRef with another UID", target: pvGVR, object: "pv-stale", want: nil},
		{name: "claim bound to another volume", target: pvGVR, object: "pv-other", want: nil},
		{name: "volume without claimRef", target: pvGVR, object: "pv-free", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			finder, ok := UsedByFor(tc.target)
			if !ok {
				t.Fatalf("no finder for %s", tc.target)
			}
			refs, err := finder.Find(t.Context(), newFakeReader(t, objs), tc.namespace, tc.object)
			if err != nil {
				t.Fatalf("Find: %v", err)
			}
			var got []string
			for _, r := range refs {
				got = append(got, RelatedRowID(r)+" "+r.Relation)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("users:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestUsedByRegistry(t *testing.T) {
	t.Parallel()
	for _, gvr := range []schema.GroupVersionResource{configMapGVR, secretGVR, pvcGVR, serviceAccountGVR, podGVR, serviceGVR, pvGVR} {
		if _, ok := UsedByFor(gvr); !ok {
			t.Errorf("no used-by finder for %s", gvr)
		}
	}
	if _, ok := UsedByFor(deploymentGVR); ok {
		t.Errorf("deployments must have no used-by finder")
	}
	// A list error reaches the caller instead of an empty result.
	finder, _ := UsedByFor(secretGVR)
	r := &fakeReader{listErr: errors.New("forbidden")}
	if _, err := finder.Find(t.Context(), r, "shop", "s"); err == nil {
		t.Errorf("list error swallowed")
	}
}

func TestRelatedRows(t *testing.T) {
	t.Parallel()
	refs := []RelatedRef{
		{GVR: replicaSetGVRForTest(), Namespace: "shop", Name: "web-1", Relation: "controller"},
		{GVR: ingressGVR, Namespace: "shop", Name: "front", Relation: "backend"},
		{GVR: replicaSetGVRForTest(), Namespace: "shop", Name: "web-1", Relation: "controller"},
		{Name: "w", Relation: "owner", APIVersion: "example.com/v1", Kind: "Widget", Problem: "unknown kind"},
		{GVR: deploymentGVR, Namespace: "shop", Name: "gone", Relation: "owner", Problem: "not found"},
	}
	rows := relatedRows(Deps{}, []string{"pods", "web-1-abc", OwnersSegment}, refs)
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4 (duplicate dropped)", len(rows))
	}
	type want struct {
		id         string
		title      string
		relation   string
		object     bool
		enterable  bool
		pathSuffix string
	}
	wants := []want{
		{id: "apps/v1/replicasets/shop/web-1", title: "/web-1", relation: "controller", object: true, enterable: true, pathSuffix: "owners/web-1"},
		{id: "networking.k8s.io/v1/ingresses/shop/front", title: "front", relation: "backend", object: true},
		{id: "unresolved/example.com/v1/Widget//w", title: "w", relation: "owner: unknown kind"},
		{id: "apps/v1/deployments/shop/gone", title: "gone", relation: "owner: not found"},
	}
	for i, w := range wants {
		id, cells, _, _ := rows[i].Columns()
		if id != w.id || cells[0] != w.title || cells[3] != w.relation {
			t.Errorf("row %d = %q %q, want %q %q %q", i, id, cells, w.id, w.title, w.relation)
		}
		obj, isObj := rows[i].(ObjectItem)
		if isObj != w.object {
			t.Errorf("row %d object item = %v, want %v", i, isObj, w.object)
		}
		if _, ok := rows[i].(Enterable); ok != w.enterable {
			t.Errorf("row %d enterable = %v, want %v", i, ok, w.enterable)
		}
		if isObj && w.pathSuffix != "" && !strings.HasSuffix(strings.Join(obj.Path(), "/"), w.pathSuffix) {
			t.Errorf("row %d path = %v, want suffix %s", i, obj.Path(), w.pathSuffix)
		}
	}
}
