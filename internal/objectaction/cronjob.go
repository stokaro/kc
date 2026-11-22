package objectaction

import (
	"context"
	"fmt"
	"maps"

	"github.com/sttts/kc/internal/models"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/utils/ptr"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

var (
	// CronJobsGVR is the resource the CronJob actions are registered for.
	CronJobsGVR = batchv1.SchemeGroupVersion.WithResource("cronjobs")
	// JobsGVR is the resource of the Jobs that "Run now" creates.
	JobsGVR = batchv1.SchemeGroupVersion.WithResource("jobs")
)

const (
	// InstantiateAnnotation marks a Job created by hand from a CronJob, as
	// kubectl create job --from does.
	InstantiateAnnotation = "cronjob.kubernetes.io/instantiate"

	manualInfix = "-manual-"
	// maxJobName keeps the Job name usable as the job-name label value.
	maxJobName = 63
)

func registerCronJobActions(r *Registry) {
	RegisterCronJobActions(r, func() string { return utilrand.String(5) })
}

// RegisterCronJobActions adds "Run now" and "Suspend/Resume" for CronJobs.
// suffix returns the random part of a manual Job name.
func RegisterCronJobActions(r *Registry, suffix func() string) {
	r.Register(CronJobsGVR,
		Action{
			ID:    "cronjob.trigger",
			Label: "Run now...",
			Verb:  "get",
			Prepare: func(ctx context.Context, env models.ActionEnv, obj models.ObjectRef) (models.ClusterAction, error) {
				cj, err := getCronJob(ctx, env, obj)
				if err != nil {
					return nil, err
				}
				return &triggerAction{env: env, cronJob: cj, job: JobFromCronJob(cj, ManualJobName(cj.Name, suffix()))}, nil
			},
		},
		Action{
			ID:    "cronjob.suspend",
			Label: "Suspend/Resume...",
			Verb:  "patch",
			Prepare: func(ctx context.Context, env models.ActionEnv, obj models.ObjectRef) (models.ClusterAction, error) {
				cj, err := getCronJob(ctx, env, obj)
				if err != nil {
					return nil, err
				}
				return &suspendAction{env: env, cronJob: cj, suspend: !ptr.Deref(cj.Spec.Suspend, false)}, nil
			},
		},
	)
}

// getCronJob reads the CronJob from the shared cache. It reads it as
// unstructured so it shares the informer that viewing the object uses.
func getCronJob(ctx context.Context, env models.ActionEnv, obj models.ObjectRef) (*batchv1.CronJob, error) {
	ctrllog.FromContext(ctx).WithName("objectaction").Info("client get",
		"gvr", CronJobsGVR.String(), "namespace", obj.Namespace, "name", obj.Name)
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(batchv1.SchemeGroupVersion.WithKind("CronJob"))
	if err := env.Client.Get(ctx, crclient.ObjectKey{Namespace: obj.Namespace, Name: obj.Name}, u); err != nil {
		return nil, fmt.Errorf("reading cronjob %s/%s: %w", obj.Namespace, obj.Name, err)
	}
	cj := &batchv1.CronJob{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, cj); err != nil {
		return nil, fmt.Errorf("decoding cronjob %s/%s: %w", obj.Namespace, obj.Name, err)
	}
	return cj, nil
}

// ManualJobName names a Job started by hand: <cronjob>-manual-<suffix>.
// The CronJob part is cut so that the name fits in 63 characters.
func ManualJobName(cronJob, suffix string) string {
	if room := maxJobName - len(manualInfix) - len(suffix); len(cronJob) > room {
		cronJob = cronJob[:max(room, 0)]
	}
	return cronJob + manualInfix + suffix
}

// JobFromCronJob builds the Job that kubectl create job --from=cronjob/<name>
// builds: the CronJob's job template, the template's labels and
// annotations, the manual annotation, and the CronJob as controller owner.
func JobFromCronJob(cj *batchv1.CronJob, name string) *batchv1.Job {
	annotations := maps.Clone(cj.Spec.JobTemplate.Annotations)
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[InstantiateAnnotation] = "manual"
	return &batchv1.Job{
		TypeMeta: metav1.TypeMeta{APIVersion: batchv1.SchemeGroupVersion.String(), Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   cj.Namespace,
			Labels:      maps.Clone(cj.Spec.JobTemplate.Labels),
			Annotations: annotations,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: batchv1.SchemeGroupVersion.String(),
				Kind:       "CronJob",
				Name:       cj.Name,
				UID:        cj.UID,
				Controller: ptr.To(true),
			}},
		},
		Spec: *cj.Spec.JobTemplate.Spec.DeepCopy(),
	}
}

// triggerAction creates one Job from the CronJob's template as it was when
// the user confirmed.
type triggerAction struct {
	env     models.ActionEnv
	cronJob *batchv1.CronJob
	job     *batchv1.Job
}

func (a *triggerAction) Confirmation() models.Confirmation {
	notes := []string{
		"Creates a Job from the CronJob's job template.",
		"The CronJob owns the Job, so deleting the",
		"CronJob deletes the Job too.",
	}
	if ptr.Deref(a.cronJob.Spec.Suspend, false) {
		notes = append(notes, "The CronJob is suspended; the Job runs anyway.")
	}
	return models.Confirmation{
		Title: "Run CronJob now?",
		Details: [][2]string{
			{"Context", a.env.Context},
			{"Namespace", a.cronJob.Namespace},
			{"CronJob", a.cronJob.Name},
			{"New Job", a.job.Name},
		},
		Notes:   notes,
		Command: a.env.Kubectl("create", "job", "--from=cronjob/"+a.cronJob.Name, a.job.Name, "-n", a.cronJob.Namespace),
		Action:  "Run",
	}
}

func (a *triggerAction) Run(ctx context.Context) (models.ActionResult, error) {
	job := a.job.DeepCopy()
	ctrllog.FromContext(ctx).WithName("objectaction").Info("client create",
		"gvr", JobsGVR.String(), "namespace", job.Namespace, "name", job.Name, "cronjob", a.cronJob.Name)
	if err := a.env.Client.Create(ctx, job); err != nil {
		return models.ActionResult{}, fmt.Errorf("creating job %s/%s: %w", job.Namespace, job.Name, err)
	}
	return models.ActionResult{
		Message: fmt.Sprintf("Created job %s from cronjob %s", job.Name, a.cronJob.Name),
		Created: &models.ObjectRef{GVR: JobsGVR, Namespace: job.Namespace, Name: job.Name},
	}, nil
}

// suspendAction sets spec.suspend to the opposite of what the user saw.
// The patch names the value rather than toggling it, so sending it twice
// changes nothing.
type suspendAction struct {
	env     models.ActionEnv
	cronJob *batchv1.CronJob
	suspend bool
}

func (a *suspendAction) patch() string {
	return fmt.Sprintf(`{"spec":{"suspend":%t}}`, a.suspend)
}

func (a *suspendAction) Confirmation() models.Confirmation {
	c := models.Confirmation{
		Details: [][2]string{
			{"Context", a.env.Context},
			{"Namespace", a.cronJob.Namespace},
			{"CronJob", a.cronJob.Name},
		},
		Command: a.env.Kubectl("patch", "cronjob", a.cronJob.Name, "-n", a.cronJob.Namespace, "--type=merge", "-p", a.patch()),
	}
	if a.suspend {
		c.Title = "Suspend CronJob?"
		c.Notes = []string{
			"The CronJob starts no new Jobs until it is",
			"resumed. Jobs that already run go on.",
		}
		c.Action = "Suspend"
	} else {
		c.Title = "Resume CronJob?"
		c.Notes = []string{
			"The CronJob starts Jobs on its schedule again.",
			"The most recent missed run may start at once.",
		}
		c.Action = "Resume"
	}
	return c
}

func (a *suspendAction) Run(ctx context.Context) (models.ActionResult, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(batchv1.SchemeGroupVersion.WithKind("CronJob"))
	u.SetNamespace(a.cronJob.Namespace)
	u.SetName(a.cronJob.Name)
	ctrllog.FromContext(ctx).WithName("objectaction").Info("client patch",
		"gvr", CronJobsGVR.String(), "namespace", u.GetNamespace(), "name", u.GetName(), "suspend", a.suspend)
	if err := a.env.Client.Patch(ctx, u, crclient.RawPatch(types.MergePatchType, []byte(a.patch()))); err != nil {
		return models.ActionResult{}, fmt.Errorf("patching cronjob %s/%s: %w", u.GetNamespace(), u.GetName(), err)
	}
	verb := "Resumed"
	if a.suspend {
		verb = "Suspended"
	}
	return models.ActionResult{Message: fmt.Sprintf("%s cronjob %s", verb, a.cronJob.Name)}, nil
}
