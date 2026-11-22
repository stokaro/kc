package workloadaction_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/sttts/kc/internal/models"
	"github.com/sttts/kc/internal/workloadaction"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const ns = "shop"

// calls records the writes and subresource reads the actions send.
type calls struct {
	mu      sync.Mutex
	entries []string
}

func (c *calls) add(s string) {
	c.mu.Lock()
	c.entries = append(c.entries, s)
	c.mu.Unlock()
}

func (c *calls) list() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.entries...)
}

func newEnv(t *testing.T, objs ...crclient.Object) (workloadaction.Env, crclient.Client, *calls) {
	t.Helper()
	rec := &calls{}
	cl := fake.NewClientBuilder().
		WithScheme(clientgoscheme.Scheme).
		WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, c crclient.WithWatch, obj crclient.Object, patch crclient.Patch, opts ...crclient.PatchOption) error {
				rec.add("patch " + string(patch.Type()))
				return c.Patch(ctx, obj, patch, opts...)
			},
			SubResourceGet: func(ctx context.Context, c crclient.Client, sub string, obj, subObj crclient.Object, opts ...crclient.SubResourceGetOption) error {
				rec.add("get " + sub)
				return c.SubResource(sub).Get(ctx, obj, subObj, opts...)
			},
			SubResourcePatch: func(ctx context.Context, c crclient.Client, sub string, obj crclient.Object, patch crclient.Patch, opts ...crclient.SubResourcePatchOption) error {
				rec.add("patch " + sub + " " + string(patch.Type()))
				return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
			},
		}).
		Build()
	env := workloadaction.Env{
		Client:  cl,
		Context: "kind-dev",
		Kubectl: func(args ...string) string { return "kubectl " + strings.Join(args, " ") },
	}
	return env, cl, rec
}

func ref(gvr schema.GroupVersionResource, name string) workloadaction.Ref {
	return workloadaction.Ref{GVR: gvr, Namespace: ns, Name: name}
}

func notes(c models.Confirmation) string {
	return strings.Join(strings.Fields(strings.Join(c.Notes, " ")), " ")
}

func detail(c models.Confirmation, label string) string {
	for _, d := range c.Details {
		if d[0] == label {
			return d[1]
		}
	}
	return ""
}

func podTemplate(image string, annotations map[string]string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}, Annotations: annotations},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: image}}},
	}
}

func deployment(replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns, UID: "uid-web"},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			Template: podTemplate("web:3", nil),
		},
	}
}
