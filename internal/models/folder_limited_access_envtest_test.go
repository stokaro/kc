package models

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/client-go/rest"

	kccluster "github.com/stokaro/kc/internal/cluster"
	table "github.com/stokaro/kc/internal/table"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/pkg/appconfig"
)

func TestLimitedUserSeesAllowedResourcesOnly(t *testing.T) {
	kcenvtest.Require(t, testCfg)

	ctx := t.Context()
	adminCl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("admin cluster: %v", err)
	}
	kctesting.RunInBackground(t, "adminCl", adminCl.Start)
	t.Cleanup(adminCl.Stop)

	ns, user := kctesting.UniqueName("limited-access"), kctesting.UniqueName("limited-user")
	const (
		pod = "pod-visible"
		sec = "secret-hidden"
	)

	createNamespace(t, adminCl, ns)
	createPod(t, adminCl, ns, pod)
	createSecret(t, adminCl, ns, sec)
	grantLimitedUser(t, adminCl, user)

	limitedCfg := rest.CopyConfig(testCfg)
	limitedCfg.Impersonate.UserName = user

	limitedCl, err := kccluster.New(limitedCfg)
	if err != nil {
		t.Fatalf("limited cluster: %v", err)
	}
	kctesting.RunInBackground(t, "limitedCl", limitedCl.Start)
	t.Cleanup(limitedCl.Stop)

	deps := Deps{
		Cl:        limitedCl,
		Ctx:       ctx,
		CtxName:   "envtest",
		AppConfig: appconfig.Default(),
	}

	root := NewRootFolder(deps, "default")
	waitFolder(t, root)
	assertRowPresent(t, root, "namespaces")

	nsResources := NewNamespacedResourcesFolder(deps, ns, []string{"namespaces", ns})
	waitFolder(t, nsResources)

	kctesting.Eventually(t, 5*time.Second, 100*time.Millisecond, func() bool {
		rows := rowsByID(t, nsResources, ctx)
		_, podsOK := rows[ns+"//v1/pods"]
		_, secretsOK := rows[ns+"//v1/secrets"]
		return podsOK && !secretsOK
	})
}

func rowsByID(t *testing.T, folder interface {
	Len(context.Context) int
	Lines(context.Context, int, int) []table.Row
}, ctx context.Context) map[string][]string {
	t.Helper()
	count := folder.Len(ctx)
	rows := folder.Lines(ctx, 0, count)
	out := make(map[string][]string, len(rows))
	for _, r := range rows {
		id, cells, _, ok := r.Columns()
		if !ok {
			continue
		}
		out[id] = cells
	}
	return out
}

func assertRowPresent(t *testing.T, folder interface {
	Len(context.Context) int
	Lines(context.Context, int, int) []table.Row
}, id string) {
	t.Helper()
	ctx := t.Context()
	kctesting.Eventually(t, 5*time.Second, 100*time.Millisecond, func() bool {
		rows := rowsByID(t, folder, ctx)
		_, ok := rows[id]
		return ok
	})
}

func createPod(t *testing.T, cl *kccluster.Cluster, namespace, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	p := &corev1.Pod{
		Name:      name,
		Namespace: namespace,
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "pause",
				Image: "registry.k8s.io/pause:3.9",
			}},
		},
	}
	if err := cl.GetClient().Create(ctx, p); err != nil {
		t.Fatalf("create pod %s/%s: %v", namespace, name, err)
	}
	t.Cleanup(func() {
		delCtx, cancelDel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancelDel()
		_ = cl.GetClient().Delete(delCtx, p)
	})
}

func createSecret(t *testing.T, cl *kccluster.Cluster, namespace, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	s := &corev1.Secret{
		Name:       name,
		Namespace:  namespace,
		StringData: map[string]string{"k": "v"},
	}
	if err := cl.GetClient().Create(ctx, s); err != nil {
		t.Fatalf("create secret %s/%s: %v", namespace, name, err)
	}
	t.Cleanup(func() {
		delCtx, cancelDel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancelDel()
		_ = cl.GetClient().Delete(delCtx, s)
	})
}

func grantLimitedUser(t *testing.T, cl *kccluster.Cluster, user string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	// Cluster-scoped and shared by every test on the control plane, so the
	// names are unique and the grant goes away with the test.
	name := kctesting.UniqueName("limited-reader")
	role := &rbacv1.ClusterRole{
		Name: name,
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"namespaces"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch"}},
		},
	}
	if err := cl.GetClient().Create(ctx, role); err != nil {
		t.Fatalf("create clusterrole: %v", err)
	}
	binding := &rbacv1.ClusterRoleBinding{
		Name: name,
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     role.Name,
		},
		Subjects: []rbacv1.Subject{
			{Kind: rbacv1.UserKind, Name: user},
		},
	}
	if err := cl.GetClient().Create(ctx, binding); err != nil {
		t.Fatalf("create clusterrolebinding: %v", err)
	}
	t.Cleanup(func() {
		delCtx, cancelDel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancelDel()
		_ = cl.GetClient().Delete(delCtx, binding)
		_ = cl.GetClient().Delete(delCtx, role)
	})
	kctesting.WaitForAccess(t, cl.GetClient(), user, "", "", "namespaces", "list", "watch")
	kctesting.WaitForAccess(t, cl.GetClient(), user, "", "", "pods", "list", "watch")
}
