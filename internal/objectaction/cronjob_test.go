package objectaction_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/objectaction"
)

func newCronJob(suspend *bool) *batchv1.CronJob {
	return &batchv1.CronJob{
		Name: "backup", Namespace: "ops", UID: "cj-uid",
		Spec: batchv1.CronJobSpec{
			Schedule: "0 * * * *",
			Suspend:  suspend,
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"app": "backup"},
					Annotations: map[string]string{"team": "ops"},
				},
				Spec: batchv1.JobSpec{
					BackoffLimit: ptr.To[int32](2),
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
						RestartPolicy: corev1.RestartPolicyNever,
						Containers:    []corev1.Container{{Name: "backup", Image: "busybox"}},
					}},
				},
			},
		},
	}
}

func newEnv(t *testing.T, objs ...crclient.Object) models.ActionEnv {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(objs...).Build()
	return models.ActionEnv{
		Client:  c,
		Context: "dev",
		Kubectl: func(args ...string) string { return "kubectl " + strings.Join(args, " ") },
	}
}

func cronJobActions(t *testing.T) map[string]objectaction.Action {
	t.Helper()
	r := objectaction.NewRegistry()
	objectaction.RegisterCronJobActions(r, func() string { return "abcde" })
	out := map[string]objectaction.Action{}
	for _, a := range r.For(objectaction.CronJobsGVR, nil) {
		out[a.ID] = a
	}
	if len(out) != 2 {
		t.Fatalf("want 2 cronjob actions, got %d", len(out))
	}
	return out
}

var backupRef = models.ObjectRef{GVR: objectaction.CronJobsGVR, Namespace: "ops", Name: "backup"}

func TestTriggerCreatesJobLikeKubectl(t *testing.T) {
	t.Parallel()
	env := newEnv(t, newCronJob(new(true)))
	ctx := t.Context()

	action, err := cronJobActions(t)["cronjob.trigger"].Prepare(ctx, env, backupRef)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	c := action.Confirmation()
	if want := "kubectl create job --from=cronjob/backup backup-manual-abcde -n ops"; c.Command != want {
		t.Errorf("command = %q, want %q", c.Command, want)
	}
	if !hasDetail(c, "New Job", "backup-manual-abcde") || !hasDetail(c, "Context", "dev") {
		t.Errorf("details lack the job name or context: %v", c.Details)
	}
	if !strings.Contains(strings.Join(c.Notes, " "), "suspended") {
		t.Errorf("a suspended CronJob must say that the Job runs anyway: %v", c.Notes)
	}

	res, err := action.Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	want := models.ObjectRef{GVR: objectaction.JobsGVR, Namespace: "ops", Name: "backup-manual-abcde"}
	if res.Created == nil || *res.Created != want {
		t.Fatalf("created = %v, want %v", res.Created, want)
	}

	job := &batchv1.Job{}
	if err := env.Client.Get(ctx, crclient.ObjectKey{Namespace: "ops", Name: "backup-manual-abcde"}, job); err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got := job.Annotations[objectaction.InstantiateAnnotation]; got != "manual" {
		t.Errorf("instantiate annotation = %q, want manual", got)
	}
	if job.Annotations["team"] != "ops" || job.Labels["app"] != "backup" {
		t.Errorf("template labels and annotations not copied: %v %v", job.Labels, job.Annotations)
	}
	if len(job.OwnerReferences) != 1 {
		t.Fatalf("owner references = %v, want one", job.OwnerReferences)
	}
	owner := job.OwnerReferences[0]
	if owner.Kind != "CronJob" || owner.Name != "backup" || owner.UID != "cj-uid" || !ptr.Deref(owner.Controller, false) {
		t.Errorf("owner reference = %+v, want controller CronJob backup/cj-uid", owner)
	}
	if ptr.Deref(job.Spec.BackoffLimit, 0) != 2 || job.Spec.Template.Spec.Containers[0].Image != "busybox" {
		t.Errorf("job spec is not the template spec: %+v", job.Spec)
	}

	// A second run with the same name must fail rather than reuse the Job.
	if _, err := action.Run(ctx); !apierrors.IsAlreadyExists(err) {
		t.Errorf("second run error = %v, want AlreadyExists", err)
	}
}

func TestSuspendAndResume(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		suspend     *bool
		wantTitle   string
		wantSuspend bool
	}{
		{name: "unset suspends", suspend: nil, wantTitle: "Suspend CronJob?", wantSuspend: true},
		{name: "false suspends", suspend: new(false), wantTitle: "Suspend CronJob?", wantSuspend: true},
		{name: "true resumes", suspend: new(true), wantTitle: "Resume CronJob?", wantSuspend: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newEnv(t, newCronJob(tt.suspend))
			ctx := t.Context()
			action, err := cronJobActions(t)["cronjob.suspend"].Prepare(ctx, env, backupRef)
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			c := action.Confirmation()
			if c.Title != tt.wantTitle {
				t.Errorf("title = %q, want %q", c.Title, tt.wantTitle)
			}
			wantCmd := "kubectl patch cronjob backup -n ops --type=merge -p {\"spec\":{\"suspend\":" + boolString(tt.wantSuspend) + "}}"
			if c.Command != wantCmd {
				t.Errorf("command = %q, want %q", c.Command, wantCmd)
			}
			// Running twice must give the same state: the patch sets a value.
			for i := range 2 {
				if _, err := action.Run(ctx); err != nil {
					t.Fatalf("run %d: %v", i, err)
				}
				if got := suspended(ctx, t, env); got != tt.wantSuspend {
					t.Fatalf("run %d: suspend = %v, want %v", i, got, tt.wantSuspend)
				}
			}
		})
	}
}

func TestPrepareReportsMissingCronJob(t *testing.T) {
	t.Parallel()
	env := newEnv(t)
	for id, a := range cronJobActions(t) {
		if _, err := a.Prepare(t.Context(), env, backupRef); !apierrors.IsNotFound(err) {
			t.Errorf("%s: error = %v, want NotFound", id, err)
		}
	}
}

func TestManualJobName(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 52) // the longest CronJob name the API allows
	tests := []struct {
		name, cronJob, want string
	}{
		{name: "short", cronJob: "backup", want: "backup-manual-abcde"},
		{name: "longest", cronJob: long, want: long[:50] + "-manual-abcde"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := objectaction.ManualJobName(tt.cronJob, "abcde")
			if got != tt.want {
				t.Errorf("ManualJobName = %q, want %q", got, tt.want)
			}
			if len(got) > 63 {
				t.Errorf("name %q is %d characters, more than 63", got, len(got))
			}
		})
	}
}

func TestRegistryFiltersByGVRAndVerb(t *testing.T) {
	t.Parallel()
	r := objectaction.Default()
	ids := func(actions []objectaction.Action) []string {
		var out []string
		for _, a := range actions {
			out = append(out, a.ID)
		}
		return out
	}
	only := func(verbs ...string) func(string) bool {
		return func(v string) bool {
			return slices.Contains(verbs, v)
		}
	}
	tests := []struct {
		name     string
		gvr      schema.GroupVersionResource
		supports func(string) bool
		want     string
	}{
		{name: "all verbs", gvr: objectaction.CronJobsGVR, supports: only("get", "patch"), want: "cronjob.trigger,cronjob.suspend"},
		{name: "no patch hides suspend", gvr: objectaction.CronJobsGVR, supports: only("get"), want: "cronjob.trigger"},
		{name: "no verbs hides all", gvr: objectaction.CronJobsGVR, supports: only(), want: ""},
		{name: "other version", gvr: schema.GroupVersionResource{Group: "batch", Version: "v1beta1", Resource: "cronjobs"}, want: ""},
		{name: "other group", gvr: schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "cronjobs"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := strings.Join(ids(r.For(tt.gvr, tt.supports)), ","); got != tt.want {
				t.Errorf("actions = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRegisterRejectsDuplicateID(t *testing.T) {
	t.Parallel()
	r := objectaction.NewRegistry()
	objectaction.RegisterCronJobActions(r, func() string { return "x" })
	defer func() {
		if recover() == nil {
			t.Fatalf("registering the same ID twice must panic")
		}
	}()
	objectaction.RegisterCronJobActions(r, func() string { return "x" })
}

func hasDetail(c models.Confirmation, label, value string) bool {
	for _, d := range c.Details {
		if d[0] == label && d[1] == value {
			return true
		}
	}
	return false
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func suspended(ctx context.Context, t *testing.T, env models.ActionEnv) bool {
	t.Helper()
	cj := &batchv1.CronJob{}
	if err := env.Client.Get(ctx, crclient.ObjectKey{Namespace: "ops", Name: "backup"}, cj); err != nil {
		t.Fatalf("get cronjob: %v", err)
	}
	return ptr.Deref(cj.Spec.Suspend, false)
}
