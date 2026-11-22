package models

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func toUnstructured(t *testing.T, obj runtime.Object) *unstructured.Unstructured {
	t.Helper()
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	return &unstructured.Unstructured{Object: m}
}

func TestDependsOnFinders(t *testing.T) {
	t.Parallel()
	spec := corev1.PodSpec{
		ServiceAccountName: "runner",
		Volumes: []corev1.Volume{
			{Name: "cfg", ConfigMap: &corev1.ConfigMapVolumeSource{Name: "app-config"}},
			{Name: "scratch", Ephemeral: &corev1.EphemeralVolumeSource{}},
		},
		Containers: []corev1.Container{{Name: "app", EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{Name: "creds"}}}}},
	}
	scheduled := spec
	scheduled.NodeName = "node-1"
	cases := []struct {
		name string
		gvr  schema.GroupVersionResource
		obj  runtime.Object
		want []Dependency
	}{
		{
			name: "pod: spec references, the ephemeral claim and the node",
			gvr:  podGVR,
			obj:  &corev1.Pod{Namespace: "shop", Name: "web-1", Spec: scheduled},
			want: []Dependency{
				{GVR: serviceAccountGVR, Namespace: "shop", Name: "runner", Via: "serviceAccountName"},
				{GVR: configMapGVR, Namespace: "shop", Name: "app-config", Via: "volumes[cfg]"},
				{GVR: pvcGVR, Namespace: "shop", Name: "web-1-scratch", Via: "volumes[scratch].ephemeral"},
				{GVR: secretGVR, Namespace: "shop", Name: "creds", Via: "containers[app].envFrom"},
				{GVR: relNodesGVR, Name: "node-1", Via: "spec.nodeName"},
			},
		},
		{
			name: "unscheduled pod names no node",
			gvr:  podGVR,
			obj:  &corev1.Pod{Namespace: "shop", Name: "p", Spec: corev1.PodSpec{ServiceAccountName: "runner"}},
			want: []Dependency{{GVR: serviceAccountGVR, Namespace: "shop", Name: "runner", Via: "serviceAccountName"}},
		},
		{
			name: "deployment: the pod template, without ephemeral claims",
			gvr:  relDeploymentsGVR,
			obj:  &appsv1.Deployment{Namespace: "shop", Name: "web", Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: spec}}},
			want: []Dependency{
				{GVR: serviceAccountGVR, Namespace: "shop", Name: "runner", Via: "template.serviceAccountName"},
				{GVR: configMapGVR, Namespace: "shop", Name: "app-config", Via: "template.volumes[cfg]"},
				{GVR: secretGVR, Namespace: "shop", Name: "creds", Via: "template.containers[app].envFrom"},
			},
		},
		{
			name: "cronjob: the job's pod template",
			gvr:  relCronJobsGVR,
			obj: &batchv1.CronJob{Namespace: "shop", Name: "nightly", Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{ServiceAccountName: "batch"}}},
			}}},
			want: []Dependency{{GVR: serviceAccountGVR, Namespace: "shop", Name: "batch", Via: "template.serviceAccountName"}},
		},
		{
			name: "bound claim names its volume",
			gvr:  pvcGVR,
			obj:  &corev1.PersistentVolumeClaim{Namespace: "shop", Name: "data", Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv-1"}},
			want: []Dependency{{GVR: pvGVR, Name: "pv-1", Via: "spec.volumeName"}},
		},
		{
			name: "pending claim names nothing",
			gvr:  pvcGVR,
			obj:  &corev1.PersistentVolumeClaim{Namespace: "shop", Name: "data"},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			finder, ok := DependsOnFor(tc.gvr)
			if !ok {
				t.Fatalf("no depends-on finder for %s", tc.gvr)
			}
			got, err := finder(toUnstructured(t, tc.obj))
			if err != nil {
				t.Fatalf("finder: %v", err)
			}
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("dependencies:\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
	for _, gvr := range []schema.GroupVersionResource{configMapGVR, secretGVR, serviceGVR} {
		if _, ok := DependsOnFor(gvr); ok {
			t.Errorf("%s must have no depends-on finder", gvr)
		}
	}
}

// denyingReader serves fakeReader's objects but denies some types, like a
// scopedReader for a user who may not read them.
type denyingReader struct {
	*fakeReader
	denied map[schema.GroupVersionResource]bool
}

func (r *denyingReader) ListByGVR(ctx context.Context, gvr schema.GroupVersionResource, namespace string) (*unstructured.UnstructuredList, error) {
	if r.denied[gvr] {
		return nil, deniedError(gvr, namespace, "")
	}
	return r.fakeReader.ListByGVR(ctx, gvr, namespace)
}

func (r *denyingReader) GetByGVR(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	if r.denied[gvr] {
		return nil, deniedError(gvr, namespace, name)
	}
	return r.fakeReader.GetByGVR(ctx, gvr, namespace, name)
}

func TestDependencyRefs(t *testing.T) {
	t.Parallel()
	r := &denyingReader{
		fakeReader: newFakeReader(t, map[schema.GroupVersionResource][]runtime.Object{
			configMapGVR: {&corev1.ConfigMap{Namespace: "shop", Name: "app-config", UID: "cm-uid"}},
		}),
		denied: map[schema.GroupVersionResource]bool{relNodesGVR: true},
	}
	in := []Dependency{
		{GVR: configMapGVR, Namespace: "shop", Name: "app-config", Via: "volumes[cfg]"},
		{GVR: secretGVR, Namespace: "shop", Name: "creds", Via: "containers[app].envFrom"},
		{GVR: configMapGVR, Namespace: "shop", Name: "app-config", Via: "containers[app].env[A]"},
		{GVR: relNodesGVR, Name: "node-1", Via: "spec.nodeName"},
	}
	got := dependencyRefs(t.Context(), r, in)
	want := []RelatedRef{
		{GVR: configMapGVR, Namespace: "shop", Name: "app-config", UID: "cm-uid", Relation: "volumes[cfg], containers[app].env[A]"},
		{GVR: secretGVR, Namespace: "shop", Name: "creds", Relation: "containers[app].envFrom", Problem: problemNotFound},
		{GVR: relNodesGVR, Name: "node-1", Relation: "spec.nodeName", Problem: problemForbidden},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("refs:\n got %+v\nwant %+v", got, want)
	}
	// One read per target, not per field.
	if len(r.reads) != 2 {
		t.Errorf("reads = %v, want the configmap and the secret once each", r.reads)
	}
}

func appsControllerRef(kind, name string, uid types.UID) []metav1.OwnerReference {
	yes := true
	return []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: kind, Name: name, UID: uid, Controller: &yes}}
}

func TestWorkloadRefs(t *testing.T) {
	t.Parallel()
	rs := func(name string, uid types.UID, owners []metav1.OwnerReference) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{Namespace: "shop", Name: name, UID: uid, OwnerReferences: owners}
	}
	podRef := func(name string, owners []metav1.OwnerReference) RelatedRef {
		return RelatedRef{GVR: podGVR, Namespace: "shop", Name: name, UID: types.UID(name + "-uid"), Relation: "volumes[cfg]", ownerRefs: owners}
	}
	objs := map[schema.GroupVersionResource][]runtime.Object{
		replicaSetGVRForTest(): {
			rs("web-1", "rs1", appsControllerRef("Deployment", "web", "d1")),
			rs("web-2", "rs2", appsControllerRef("Deployment", "web", "d1")),
			rs("recreated", "rs-new", nil),
			rs("loop", "loop-rs", appsControllerRef("Deployment", "loop", "loop-d")),
			rs("deep-1", "deep-1", appsControllerRef("ReplicaSet", "deep-2", "deep-2")),
			rs("deep-2", "deep-2", appsControllerRef("ReplicaSet", "deep-3", "deep-3")),
			rs("deep-3", "deep-3", appsControllerRef("ReplicaSet", "deep-4", "deep-4")),
			rs("deep-4", "deep-4", appsControllerRef("ReplicaSet", "deep-5", "deep-5")),
			rs("deep-5", "deep-5", nil),
		},
		deploymentGVR: {
			&appsv1.Deployment{Namespace: "shop", Name: "web", UID: "d1"},
			&appsv1.Deployment{Namespace: "shop", Name: "loop", UID: "loop-d", OwnerReferences: appsControllerRef("ReplicaSet", "loop", "loop-rs")},
		},
	}
	widget := []metav1.OwnerReference{{APIVersion: "example.com/v1", Kind: "Widget", Name: "w", UID: "w1", Controller: new(true)}}
	cases := []struct {
		name   string
		pods   []RelatedRef
		denied map[schema.GroupVersionResource]bool
		want   []string // id, relation, problem
	}{
		{
			name: "pods of two ReplicaSets of one Deployment: one Deployment row",
			pods: []RelatedRef{
				podRef("web-1-a", appsControllerRef("ReplicaSet", "web-1", "rs1")),
				podRef("web-1-b", appsControllerRef("ReplicaSet", "web-1", "rs1")),
				podRef("web-2-a", appsControllerRef("ReplicaSet", "web-2", "rs2")),
			},
			want: []string{
				"apps/v1/replicasets/shop/web-1 | controller of pods/web-1-a, pods/web-1-b | ",
				"apps/v1/deployments/shop/web | controller of replicasets.apps/web-1, replicasets.apps/web-2 | ",
				"apps/v1/replicasets/shop/web-2 | controller of pods/web-2-a | ",
			},
		},
		{
			name: "a pod without a controller adds nothing",
			pods: []RelatedRef{podRef("bare", nil), {GVR: serviceGVR, Namespace: "shop", Name: "svc"}},
			want: nil,
		},
		{
			name: "owner gone, replaced and of an unknown kind",
			pods: []RelatedRef{
				podRef("orphan", appsControllerRef("ReplicaSet", "gone", "rs-gone")),
				podRef("stale", appsControllerRef("ReplicaSet", "recreated", "rs-old")),
				podRef("custom", widget),
			},
			want: []string{
				"apps/v1/replicasets/shop/gone | controller of pods/orphan | not found",
				"apps/v1/replicasets/shop/recreated | controller of pods/stale | not found (an object with another UID has the name)",
				"unresolved/example.com/v1/Widget//w | controller of pods/custom | unknown kind example.com/v1 Widget",
			},
		},
		{
			name:   "a controller the user may not read ends the chain",
			pods:   []RelatedRef{podRef("web-1-a", appsControllerRef("ReplicaSet", "web-1", "rs1"))},
			denied: map[schema.GroupVersionResource]bool{replicaSetGVRForTest(): true},
			want:   []string{"apps/v1/replicasets/shop/web-1 | controller of pods/web-1-a | forbidden"},
		},
		{
			name: "a cycle of owners ends when an object comes back",
			pods: []RelatedRef{podRef("looped", appsControllerRef("ReplicaSet", "loop", "loop-rs"))},
			want: []string{
				"apps/v1/replicasets/shop/loop | controller of pods/looped | ",
				"apps/v1/deployments/shop/loop | controller of replicasets.apps/loop | ",
			},
		},
		{
			name: "the walk stops after maxOwnerHops controllers",
			pods: []RelatedRef{podRef("deep", appsControllerRef("ReplicaSet", "deep-1", "deep-1"))},
			want: []string{
				"apps/v1/replicasets/shop/deep-1 | controller of pods/deep | ",
				"apps/v1/replicasets/shop/deep-2 | controller of replicasets.apps/deep-1 | ",
				"apps/v1/replicasets/shop/deep-3 | controller of replicasets.apps/deep-2 | ",
				"apps/v1/replicasets/shop/deep-4 | controller of replicasets.apps/deep-3 | ",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &denyingReader{fakeReader: newFakeReader(t, objs), denied: tc.denied}
			var got []string
			for _, ref := range workloadRefs(t.Context(), r, testMapper(), tc.pods) {
				got = append(got, RelatedRowID(ref)+" | "+ref.Relation+" | "+ref.Problem)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("workloads:\n got %q\nwant %q", got, tc.want)
			}
		})
	}

	// Each controller is read once, whatever the number of its pods.
	r := newFakeReader(t, objs)
	pods := []RelatedRef{
		podRef("a", appsControllerRef("ReplicaSet", "web-1", "rs1")),
		podRef("b", appsControllerRef("ReplicaSet", "web-1", "rs1")),
	}
	workloadRefs(t.Context(), r, testMapper(), pods)
	if len(r.reads) != 2 {
		t.Errorf("reads = %v, want the ReplicaSet and the Deployment once each", r.reads)
	}
}

func TestDependencyWatches(t *testing.T) {
	t.Parallel()
	got := dependencyWatches([]Dependency{
		{GVR: configMapGVR, Namespace: "shop", Name: "a", Via: "volumes[a]"},
		{GVR: secretGVR, Namespace: "shop", Name: "s", Via: "imagePullSecrets"},
		{GVR: configMapGVR, Namespace: "shop", Name: "b", Via: "volumes[b]"},
		{GVR: secretGVR, Namespace: "shop", Name: "s", Via: "volumes[s]"},
		{GVR: relNodesGVR, Name: "node-1", Via: "spec.nodeName"},
	})
	want := []relatedWatch{
		{gvr: configMapGVR, namespace: "shop"},
		{gvr: secretGVR, namespace: "shop", name: "s"},
		{gvr: relNodesGVR, name: "node-1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("watches:\n got %+v\nwant %+v", got, want)
	}
}

func TestJoinLimited(t *testing.T) {
	t.Parallel()
	if got := joinLimited([]string{"a", "b", "c"}, 3); got != "a, b, c" {
		t.Errorf("three items: %q", got)
	}
	if got := joinLimited([]string{"a", "b", "c", "d", "e"}, 3); got != "a, b, c, +2 more" {
		t.Errorf("five items: %q", got)
	}
}

// scopeRecorder is a relatedResolver with fixed decisions that counts its
// calls and releases.
type scopeRecorder struct {
	mu       sync.Mutex
	decide   map[schema.GroupVersionResource]listScope
	readers  map[listScope]RelatedReader
	calls    int
	released int
}

func (s *scopeRecorder) resolve(gvr schema.GroupVersionResource, _ string) (relatedScope, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	scope := s.decide[gvr]
	return relatedScope{reader: s.readers[scope], scope: scope}, func() {
		s.mu.Lock()
		s.released++
		s.mu.Unlock()
	}
}

func TestScopedReader(t *testing.T) {
	t.Parallel()
	shared := newFakeReader(t, map[schema.GroupVersionResource][]runtime.Object{
		configMapGVR: {&corev1.ConfigMap{Namespace: "shop", Name: "cfg"}},
	})
	namespaced := newFakeReader(t, map[schema.GroupVersionResource][]runtime.Object{
		podGVR: {&corev1.Pod{Namespace: "shop", Name: "web"}},
	})
	rec := &scopeRecorder{
		decide:  map[schema.GroupVersionResource]listScope{configMapGVR: scopeShared, podGVR: scopeNamespace, secretGVR: scopeDenied},
		readers: map[listScope]RelatedReader{scopeShared: shared, scopeNamespace: namespaced},
	}
	r := newScopedReader(rec.resolve)
	ctx := t.Context()

	if _, err := r.GetByGVR(ctx, configMapGVR, "shop", "cfg"); err != nil {
		t.Fatalf("shared get: %v", err)
	}
	if _, err := r.GetByGVR(ctx, podGVR, "shop", "web"); err != nil {
		t.Fatalf("namespace get: %v", err)
	}
	if len(shared.reads) != 1 || len(namespaced.reads) != 1 {
		t.Fatalf("reads went to the wrong cache: shared %v, namespace %v", shared.reads, namespaced.reads)
	}
	_, err := r.ListByGVR(ctx, secretGVR, "shop")
	if !apierrors.IsForbidden(err) {
		t.Fatalf("denied list: err = %v, want Forbidden", err)
	}
	if _, err := r.GetByGVR(ctx, secretGVR, "shop", "s"); !apierrors.IsForbidden(err) {
		t.Fatalf("denied get: err = %v, want Forbidden", err)
	}
	// A denied type sends no request to any cache.
	if len(shared.reads)+len(namespaced.reads) != 2 {
		t.Fatalf("a denied read reached a cache: %v %v", shared.reads, namespaced.reads)
	}
	// Each pair is decided once.
	r.GetByGVR(ctx, podGVR, "shop", "web")
	if rec.calls != 3 {
		t.Fatalf("resolve calls = %d, want 3 (one per type)", rec.calls)
	}
	if sc, ok := r.decided(podGVR, "shop"); !ok || sc.scope != scopeNamespace {
		t.Fatalf("decided(pods) = %v, %v", sc, ok)
	}
	if _, ok := r.decided(podGVR, "other"); ok {
		t.Fatalf("an undecided pair reports a decision")
	}

	r.Close()
	if rec.released != 3 {
		t.Fatalf("released %d, want 3", rec.released)
	}
	if _, err := r.GetByGVR(ctx, configMapGVR, "shop", "cfg"); !apierrors.IsForbidden(err) {
		t.Fatalf("read after Close: err = %v, want Forbidden", err)
	}
	if rec.calls != 3 {
		t.Fatalf("a read after Close decided again")
	}
}

func TestFindUsers(t *testing.T) {
	t.Parallel()
	pods := map[schema.GroupVersionResource][]runtime.Object{
		podGVR: {&corev1.Pod{Namespace: "shop", Name: "web-1-a", OwnerReferences: appsControllerRef("ReplicaSet", "web-1", "rs1"),
			Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "c", ConfigMap: &corev1.ConfigMapVolumeSource{Name: "cfg"}}}}}},
		replicaSetGVRForTest(): {&appsv1.ReplicaSet{Namespace: "shop", Name: "web-1", UID: "rs1"}},
	}
	finder, _ := UsedByFor(configMapGVR)
	ids := func(refs []RelatedRef) []string {
		var out []string
		for _, r := range refs {
			out = append(out, RelatedRowID(r)+" | "+r.Relation+" | "+r.Problem)
		}
		return out
	}

	refs, err := findUsers(t.Context(), newFakeReader(t, pods), testMapper(), finder, "shop", "cfg")
	if err != nil {
		t.Fatalf("findUsers: %v", err)
	}
	want := []string{
		"v1/pods/shop/web-1-a | volumes[c] | ",
		"apps/v1/replicasets/shop/web-1 | controller of pods/web-1-a | ",
	}
	if got := ids(refs); !reflect.DeepEqual(got, want) {
		t.Fatalf("users:\n got %q\nwant %q", got, want)
	}

	// The user may not list pods: the folder says the result is incomplete
	// instead of looking empty.
	denied := &denyingReader{fakeReader: newFakeReader(t, pods), denied: map[schema.GroupVersionResource]bool{podGVR: true}}
	refs, err = findUsers(t.Context(), denied, testMapper(), finder, "shop", "cfg")
	if err != nil {
		t.Fatalf("findUsers denied: %v", err)
	}
	want = []string{"unresolved//pods/shop/(not listed) | source | forbidden; the result is incomplete"}
	if got := ids(refs); !reflect.DeepEqual(got, want) {
		t.Fatalf("denied users:\n got %q\nwant %q", got, want)
	}

	// Any other error still reaches the caller.
	if _, err := findUsers(t.Context(), &fakeReader{listErr: errors.New("boom")}, testMapper(), finder, "shop", "cfg"); err == nil {
		t.Fatalf("list error swallowed")
	}
}
