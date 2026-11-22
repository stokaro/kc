package workloadaction_test

import (
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stokaro/kc/internal/workloadaction"
)

// replicaSet is a ReplicaSet of revision rev, controlled by the owner with
// ownerUID, with the hash label the Deployment controller adds.
func replicaSet(name, rev, image string, ownerUID types.UID, annotations map[string]string) *appsv1.ReplicaSet {
	a := map[string]string{}
	for k, v := range annotations {
		a[k] = v
	}
	if rev != "" {
		a["deployment.kubernetes.io/revision"] = rev
	}
	tmpl := podTemplate(image, map[string]string{"note": "rev " + rev})
	tmpl.Labels["pod-template-hash"] = "h" + rev
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns, Annotations: a,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: ownerUID, Controller: ptr.To(true)}},
		},
		Spec: appsv1.ReplicaSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			Template: tmpl,
		},
	}
}

// rollbackFixture is a Deployment at revision 3 with ReplicaSets for
// revisions 1 to 3, plus ReplicaSets that must not be listed: one of
// another Deployment and one without a revision.
func rollbackFixture() []crclient.Object {
	d := deployment(2)
	d.Annotations = map[string]string{
		"deployment.kubernetes.io/revision":                "3",
		"kubectl.kubernetes.io/last-applied-configuration": "{}",
		"owner": "team-shop",
	}
	current := replicaSet("web-3", "3", "web:3", "uid-web", nil)
	d.Spec.Template = *current.Spec.Template.DeepCopy()
	delete(d.Spec.Template.Labels, "pod-template-hash")
	return []crclient.Object{
		d,
		replicaSet("web-1", "1", "web:1", "uid-web", map[string]string{"custom": "from-rs-1", "deployment.kubernetes.io/desired-replicas": "9"}),
		replicaSet("web-2", "2", "web:2", "uid-web", nil),
		current,
		replicaSet("other-9", "9", "other:9", "uid-other", nil),
		replicaSet("web-norev", "", "web:x", "uid-web", nil),
	}
}

func TestRevisionsListsOwnedReplicaSetsNewestFirst(t *testing.T) {
	objs := rollbackFixture()
	d := objs[0].(*appsv1.Deployment)
	var rss []appsv1.ReplicaSet
	for _, o := range objs[1:] {
		rss = append(rss, *o.(*appsv1.ReplicaSet))
	}
	revs := workloadaction.Revisions(d, rss)
	var got []string
	for _, r := range revs {
		s := r.ReplicaSet + "=" + strings.Join(r.Images, ",")
		if r.Current {
			s += "*"
		}
		got = append(got, s)
	}
	// other-9 (another owner) and web-norev (no revision) are refused.
	want := []string{"web-3=web:3*", "web-2=web:2", "web-1=web:1"}
	if !slices.Equal(got, want) {
		t.Fatalf("revisions = %q, want %q", got, want)
	}
}

func TestRollbackDefaultsToThePreviousRevisionAndListsAll(t *testing.T) {
	env, _, _ := newEnv(t, rollbackFixture()...)
	prepared, err := workloadaction.PrepareRollback(t.Context(), env, ref(workloadaction.DeploymentsGVR, "web"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	chooser, ok := prepared.(workloadaction.Chooser)
	if !ok {
		t.Fatalf("prepared %T offers no revision list", prepared)
	}
	_, choices, selected := chooser.Choices()
	if selected != "2" {
		t.Errorf("preselected revision = %q, want 2 (what kubectl rollout undo picks)", selected)
	}
	var got []string
	for _, c := range choices {
		got = append(got, c.Value+":"+c.Note)
	}
	if want := []string{"3:current", "2:web:2", "1:web:1"}; !slices.Equal(got, want) {
		t.Errorf("choices = %q, want %q", got, want)
	}
	if _, err := chooser.Choose("3"); err == nil {
		t.Error("choosing the current revision was accepted")
	}
	if _, err := chooser.Choose("9"); err == nil {
		t.Error("choosing another Deployment's revision was accepted")
	}

	c := prepared.Confirmation()
	if detail(c, "From revision") != "3" || detail(c, "To revision") != "2" {
		t.Errorf("details = %v, want from 3 to 2", c.Details)
	}
	if want := "kubectl rollout undo deployments.apps/web --to-revision=2 -n shop"; !strings.Contains(notes(c), want) {
		t.Errorf("notes lack %q:\n%s", want, strings.Join(c.Notes, "\n"))
	}
}

func TestRollbackCopiesTheReplicaSetTemplate(t *testing.T) {
	objs := rollbackFixture()
	env, cl, rec := newEnv(t, objs...)
	prepared, err := workloadaction.PrepareRollback(t.Context(), env, ref(workloadaction.DeploymentsGVR, "web"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	change, err := prepared.(workloadaction.Chooser).Choose("1")
	if err != nil {
		t.Fatalf("choose 1: %v", err)
	}
	for range 2 { // a repeat writes the same template again
		if _, err := change.Run(t.Context()); err != nil {
			t.Fatalf("run: %v", err)
		}
	}

	got := &appsv1.Deployment{}
	if err := cl.Get(t.Context(), crclient.ObjectKey{Namespace: ns, Name: "web"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	want := *objs[1].(*appsv1.ReplicaSet).Spec.Template.DeepCopy()
	delete(want.Labels, "pod-template-hash")
	if !apiequality.Semantic.DeepEqual(got.Spec.Template, want) {
		t.Errorf("template = %+v\nwant the template of web-1 without the hash label: %+v", got.Spec.Template, want)
	}
	wantAnnotations := map[string]string{
		// kept from the Deployment
		"deployment.kubernetes.io/revision":                "3",
		"kubectl.kubernetes.io/last-applied-configuration": "{}",
		// copied from the ReplicaSet; desired-replicas is not
		"custom": "from-rs-1",
	}
	if !apiequality.Semantic.DeepEqual(got.Annotations, wantAnnotations) {
		t.Errorf("annotations = %v, want %v", got.Annotations, wantAnnotations)
	}
	if got, want := rec.list(), []string{"patch application/json-patch+json", "patch application/json-patch+json"}; !slices.Equal(got, want) {
		t.Errorf("requests = %q, want %q", got, want)
	}
}

func TestRollbackRefusals(t *testing.T) {
	tests := []struct {
		name string
		objs func() []crclient.Object
		want string
	}{
		{
			name: "paused",
			objs: func() []crclient.Object {
				objs := rollbackFixture()
				objs[0].(*appsv1.Deployment).Spec.Paused = true
				return objs
			},
			want: "paused",
		},
		{
			name: "only the current revision",
			objs: func() []crclient.Object {
				objs := rollbackFixture()
				return []crclient.Object{objs[0], objs[3], objs[4]}
			},
			want: "no previous revision",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env, _, rec := newEnv(t, tt.objs()...)
			_, err := workloadaction.PrepareRollback(t.Context(), env, ref(workloadaction.DeploymentsGVR, "web"))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("prepare = %v, want an error with %q", err, tt.want)
			}
			if got := rec.list(); len(got) != 0 {
				t.Errorf("a refused rollback sent %q", got)
			}
		})
	}
}
