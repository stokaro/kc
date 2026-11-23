package models

import (
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stokaro/kc/pkg/appconfig"
)

func cfgVolume(name string) corev1.PodSpec {
	return corev1.PodSpec{Volumes: []corev1.Volume{{Name: "c", ConfigMap: &corev1.ConfigMapVolumeSource{Name: name}}}}
}

func usersOf(t *testing.T, r RelatedReader) []string {
	t.Helper()
	finder, _ := UsedByFor(configMapGVR)
	refs, err := findUsers(t.Context(), r, testMapper(), finder, "shop", "cfg")
	if err != nil {
		t.Fatalf("findUsers: %v", err)
	}
	var out []string
	for _, ref := range refs {
		out = append(out, RelatedRowID(ref)+" | "+ref.Relation+" | "+ref.Problem)
	}
	return out
}

func TestUsedByPodTemplates(t *testing.T) {
	t.Parallel()
	zero := int32(0)
	objs := map[schema.GroupVersionResource][]runtime.Object{
		relDeploymentsGVR: {
			// Scaled to zero: no pod, still a consumer.
			&appsv1.Deployment{Namespace: "shop", Name: "idle", UID: "idle-uid", Spec: appsv1.DeploymentSpec{Replicas: &zero, Template: corev1.PodTemplateSpec{Spec: cfgVolume("cfg")}}},
			&appsv1.Deployment{Namespace: "shop", Name: "web", UID: "web-uid", Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: cfgVolume("cfg")}}},
			&appsv1.Deployment{Namespace: "shop", Name: "other", UID: "other-uid", Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: cfgVolume("not-cfg")}}},
		},
		relCronJobsGVR: {&batchv1.CronJob{Namespace: "shop", Name: "nightly", UID: "cj-uid", Spec: batchv1.CronJobSpec{
			JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: cfgVolume("cfg")}}}}}},
		replicaSetGVRForTest(): {&appsv1.ReplicaSet{Namespace: "shop", Name: "web-1", UID: "rs1", OwnerReferences: appsControllerRef("Deployment", "web", "web-uid")}},
		podGVR:                 {&corev1.Pod{Namespace: "shop", Name: "web-1-a", OwnerReferences: appsControllerRef("ReplicaSet", "web-1", "rs1"), Spec: cfgVolume("cfg")}},
	}
	got := usersOf(t, newFakeReader(t, objs))
	want := []string{
		"v1/pods/shop/web-1-a | volumes[c] | ",
		"apps/v1/deployments/shop/idle | pod template: template.volumes[c] | ",
		"apps/v1/deployments/shop/web | pod template: template.volumes[c]; controller of replicasets.apps/web-1 | ",
		"apps/v1/replicasets/shop/web-1 | controller of pods/web-1-a | ",
		"batch/v1/cronjobs/shop/nightly | pod template: template.volumes[c] | ",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("users:\n got %q\nwant %q", got, want)
	}

	// Pods denied: the templates still answer, and the missing source is
	// said.
	denied := usersOf(t, &denyingReader{fakeReader: newFakeReader(t, objs), denied: map[schema.GroupVersionResource]bool{podGVR: true, relCronJobsGVR: true}})
	for _, w := range []string{
		"apps/v1/deployments/shop/idle | pod template",
		"pods/shop/(not listed) | source | forbidden",
		"workloads/shop/(not listed) | pod templates of cronjobs.batch | forbidden; the result is incomplete",
	} {
		if !slices.ContainsFunc(denied, func(s string) bool { return strings.Contains(s, w) }) {
			t.Errorf("denied users miss %q: %q", w, denied)
		}
	}
	if slices.ContainsFunc(denied, func(s string) bool { return strings.Contains(s, "nightly") }) {
		t.Errorf("a denied CronJob list is listed: %q", denied)
	}
}

// A Deployment recreated under the same name is a different consumer: the
// running pods of the old instance do not merge into its template row.
func TestMergeRelationsKeepsInstancesApart(t *testing.T) {
	t.Parallel()
	refs := mergeRelations([]RelatedRef{
		{GVR: relDeploymentsGVR, Namespace: "shop", Name: "web", UID: "new", Relation: "pod template: template.volumes[c]"},
		{GVR: relDeploymentsGVR, Namespace: "shop", Name: "web", UID: "old", Relation: "controller of replicasets/web-1"},
		{GVR: relDeploymentsGVR, Namespace: "shop", Name: "web", UID: "new", Relation: "controller of replicasets/web-2"},
	})
	if len(refs) != 2 || refs[0].Relation != "pod template: template.volumes[c]; controller of replicasets/web-2" || refs[1].UID != "old" {
		t.Fatalf("merged = %+v", refs)
	}
}

func chainObj(gvr schema.GroupVersionResource, kind, name string, uid types.UID, ownerKind, owner string, ownerUID types.UID) unstructured.Unstructured {
	u := unstructured.Unstructured{}
	u.SetAPIVersion(gvr.GroupVersion().String())
	u.SetKind(kind)
	u.SetNamespace("shop")
	u.SetName(name)
	u.SetUID(uid)
	if owner != "" {
		yes := true
		u.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "x/v1", Kind: ownerKind, Name: owner, UID: ownerUID, Controller: &yes}})
	}
	return u
}

func TestWalkChain(t *testing.T) {
	t.Parallel()
	in := chainInput{Objects: map[schema.GroupVersionResource][]unstructured.Unstructured{
		relDeploymentsGVR: {
			chainObj(relDeploymentsGVR, "Deployment", "db", "dep", "Database", "db", "cr"),
			// Same name as the CR's Deployment in another life: other owner UID.
			chainObj(relDeploymentsGVR, "Deployment", "db-old", "dep-old", "Database", "db", "cr-old"),
			// A cycle: two objects that name each other as controller.
			chainObj(relDeploymentsGVR, "Deployment", "loop-a", "la", "Deployment", "loop-b", "lb"),
			chainObj(relDeploymentsGVR, "Deployment", "loop-b", "lb", "Deployment", "loop-a", "la"),
		},
		relReplicaSetsGVR: {chainObj(relReplicaSetsGVR, "ReplicaSet", "db-1", "rs", "Deployment", "db", "dep")},
	}, Errs: make(map[schema.GroupVersionResource]error)}
	owners, edges := walkChain("cr", in)
	for _, uid := range []types.UID{"cr", "dep", "rs"} {
		if !owners[uid] {
			t.Errorf("%s not reached", uid)
		}
	}
	if owners["dep-old"] || owners["la"] {
		t.Errorf("owners = %v", owners)
	}
	if len(edges) != 2 || edges[0].Name != "db" || edges[1].Name != "db-1" || edges[1].Depth != 2 {
		t.Errorf("edges = %+v", edges)
	}
	// A cycle not reachable from the root terminates; one reachable too.
	owners, _ = walkChain("la", in)
	if !owners["lb"] || len(owners) != 2 {
		t.Errorf("cycle owners = %v", owners)
	}
}

func TestChainFindingsPartial(t *testing.T) {
	t.Parallel()
	in := chainInput{Errs: map[schema.GroupVersionResource]error{relReplicaSetsGVR: deniedError(relReplicaSetsGVR, "shop", "")}}
	fs := chainFindings(in, nil, "shop")
	if len(fs) != 1 || fs[0].Code != "owner-chain-partial" || fs[0].Severity != diagIncomplete || !strings.Contains(fs[0].Title, "replicasets.apps (forbidden)") {
		t.Fatalf("findings = %+v", fs)
	}
}

// Not parallel: it installs the package-wide health config. A custom
// resource that runs its pods through a Deployment counts them through
// the ReplicaSet, and a denied type is said.
func TestHealthDiagnosisOwnerChain(t *testing.T) {
	installHealth(t, map[string][]appconfig.HealthRule{"example.com/v1/widgets": fluxRules})
	obj := widget(1, conds(cond("Ready", "True", "Done")))
	chain := chainInput{Objects: map[schema.GroupVersionResource][]unstructured.Unstructured{
		relDeploymentsGVR: {chainObj(relDeploymentsGVR, "Deployment", "w", "dep", "Widget", obj.GetName(), obj.GetUID())},
		relReplicaSetsGVR: {chainObj(relReplicaSetsGVR, "ReplicaSet", "w-1", "rs", "Deployment", "w", "dep")},
	}, Errs: map[schema.GroupVersionResource]error{relJobsGVR: deniedError(relJobsGVR, "shop", "")}}
	in := workloadInput{GVR: widgetsGVR, Object: obj, Now: diagNow, Chain: &chain,
		Pods: []corev1.Pod{dgPod("w-1-a", "p-0", "rs", nil), dgPod("other", "p-1", "elsewhere", nil)}}
	d := diagWorkload(t, in)
	if summary := strings.Join(d.Summary, "\n"); !strings.Contains(summary, "pods: 1 owned, 1 ready") {
		t.Errorf("summary:\n%s", summary)
	}
	codes := dgCodes(d)
	if !slices.Contains(codes, "owner-chain") || !slices.Contains(codes, "owner-chain-partial") {
		t.Fatalf("codes = %q, want owner-chain and owner-chain-partial", codes)
	}
}
