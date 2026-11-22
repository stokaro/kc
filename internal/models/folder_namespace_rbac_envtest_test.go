package models

import (
	"context"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	kccluster "github.com/stokaro/kc/internal/cluster"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/pkg/appconfig"
)

// namespaceUserEnv is a user whose only grants are Roles on pods in some
// namespaces, reached through a real cluster pool as kc builds it.
type namespaceUserEnv struct {
	admin  *kccluster.Cluster
	shared *kccluster.Cluster // the user's shared cluster (no namespace)
	deps   Deps

	mu       sync.Mutex
	reported []error
	holds    map[kccluster.Key]int // open holds per pool key
}

func (e *namespaceUserEnv) errors() []error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]error(nil), e.reported...)
}

func (e *namespaceUserEnv) openHolds() map[kccluster.Key]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[kccluster.Key]int{}
	for k, n := range e.holds {
		if n != 0 {
			out[k] = n
		}
	}
	return out
}

func newNamespaceUserEnv(t *testing.T, user string, grantNamespaces ...string) *namespaceUserEnv {
	t.Helper()
	if testCfg == nil {
		t.Skip("envtest not available")
	}
	ctx := t.Context()
	admin, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("admin cluster: %v", err)
	}
	go func() { _ = admin.Start(ctx) }()
	t.Cleanup(admin.Stop)
	for _, ns := range grantNamespaces {
		createNamespace(t, admin, ns)
		grantPodsInNamespace(t, admin, ns, user)
	}

	pool := kccluster.NewPool(time.Minute, 0)
	pool.Start()
	t.Cleanup(pool.Stop)
	key := kccluster.Key{
		KubeconfigPath:  writeEnvtestKubeconfig(t, testCfg),
		ContextName:     "envtest",
		ImpersonateUser: user,
	}
	sharedLease, err := pool.Acquire(ctx, key, nil)
	if err != nil {
		t.Fatalf("user cluster: %v", err)
	}
	t.Cleanup(sharedLease.Release)
	shared := sharedLease.Cluster()
	e := &namespaceUserEnv{admin: admin, shared: shared, holds: map[kccluster.Key]int{}}
	e.deps = Deps{
		Cl:         shared,
		Ctx:        ctx,
		CtxName:    "envtest",
		AppConfig:  appconfig.Default(),
		ClusterKey: key,
		// The factory counts the leases it hands out, so the tests can
		// check that every folder releases its namespace cache on Close.
		NamespaceFactory: func(ctx context.Context, key kccluster.Key, selectors map[schema.GroupVersionResource]crcache.ByObject) (*kccluster.Cluster, func(), error) {
			lease, err := pool.Acquire(ctx, key, selectors)
			if err != nil {
				return nil, nil, err
			}
			e.mu.Lock()
			e.holds[key]++
			e.mu.Unlock()
			return lease.Cluster(), sync.OnceFunc(func() {
				e.mu.Lock()
				e.holds[key]--
				e.mu.Unlock()
				lease.Release()
			}), nil
		},
		ReportError: func(err error) {
			e.mu.Lock()
			e.reported = append(e.reported, err)
			e.mu.Unlock()
		},
	}
	// The premise of every test here: pods are readable in the granted
	// namespaces only, so a cluster-wide informer could never sync.
	if ok, err := shared.CanListWatchAllNamespaces(ctx, podGVR); err != nil || ok {
		t.Fatalf("user %s may list pods cluster-wide (%v, %v); the test needs namespace-only access", user, ok, err)
	}
	for _, ns := range grantNamespaces {
		if ok, err := shared.CanListWatchInNamespace(ctx, podGVR, ns); err != nil || !ok {
			t.Fatalf("user %s may not list pods in %s (%v, %v)", user, ns, ok, err)
		}
	}
	return e
}

// writeEnvtestKubeconfig writes the envtest admin credentials as a
// kubeconfig, because the pool builds clusters from kubeconfig files.
func writeEnvtestKubeconfig(t *testing.T, cfg *rest.Config) string {
	t.Helper()
	kc := clientcmdapi.NewConfig()
	kc.Clusters["envtest"] = &clientcmdapi.Cluster{Server: cfg.Host, CertificateAuthorityData: cfg.CAData}
	kc.AuthInfos["envtest"] = &clientcmdapi.AuthInfo{ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData, Token: cfg.BearerToken}
	kc.Contexts["envtest"] = &clientcmdapi.Context{Cluster: "envtest", AuthInfo: "envtest"}
	kc.CurrentContext = "envtest"
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*kc, path); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}

func grantPodsInNamespace(t *testing.T, cl *kccluster.Cluster, namespace, user string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	role := &rbacv1.Role{
		Name: "pod-reader", Namespace: namespace,
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch"}},
		},
	}
	if err := cl.GetClient().Create(ctx, role); err != nil {
		t.Fatalf("create role in %s: %v", namespace, err)
	}
	binding := &rbacv1.RoleBinding{
		Name: "pod-reader", Namespace: namespace,
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: user}},
	}
	if err := cl.GetClient().Create(ctx, binding); err != nil {
		t.Fatalf("create rolebinding in %s: %v", namespace, err)
	}
}

func deletePod(t *testing.T, cl *kccluster.Cluster, namespace, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	p := &corev1.Pod{Name: name, Namespace: namespace}
	if err := cl.GetClient().Delete(ctx, p, crclient.GracePeriodSeconds(0)); err != nil {
		t.Fatalf("delete pod %s/%s: %v", namespace, name, err)
	}
}

// objectNames lists the object rows of a folder, without the back row.
func objectNames(t *testing.T, f *NamespacedObjectsFolder) []string {
	t.Helper()
	var names []string
	for id := range rowsByID(t, f, t.Context()) {
		if id != "" && id != "__back__" {
			names = append(names, id)
		}
	}
	sort.Strings(names)
	return names
}

func waitObjectNames(t *testing.T, f *NamespacedObjectsFolder, want ...string) {
	t.Helper()
	sort.Strings(want)
	var got []string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if got = objectNames(t, f); slices.Equal(got, want) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	list, err := f.Deps.Cl.ListByGVR(t.Context(), podGVR, f.Namespace())
	t.Fatalf("pods in %s: folder rows %v, want %v (cache list: %v, err %v)", f.Namespace(), got, want, list, err)
}

func TestNamespaceOnlyUserListsAndWatchesPods(t *testing.T) {
	const user = "team-user"
	e := newNamespaceUserEnv(t, user, "team-a", "team-b")
	createNamespace(t, e.admin, "team-c") // not granted
	createPod(t, e.admin, "team-a", "pod-a1")
	createPod(t, e.admin, "team-b", "pod-b1")
	createPod(t, e.admin, "team-c", "pod-c1")

	// Two panels, two namespaces.
	fa := NewNamespacedObjectsFolder(e.deps, podGVR, "team-a", []string{"pods"}, nil)
	fb := NewNamespacedObjectsFolder(e.deps, podGVR, "team-b", []string{"pods"}, nil)
	for _, f := range []*NamespacedObjectsFolder{fa, fb} {
		if f.Deps.Cl == e.shared {
			t.Fatalf("folder in %s uses the shared cluster, whose pod informer can never sync", f.Namespace())
		}
		if got := f.Deps.Cl.Namespace(); got != f.Namespace() {
			t.Fatalf("folder in %s uses a cache scoped to %q", f.Namespace(), got)
		}
	}
	if fa.Deps.Cl == fb.Deps.Cl {
		t.Fatal("two namespaces share one namespace-scoped cluster")
	}
	if got := e.openHolds(); len(got) != 2 {
		t.Fatalf("open holds = %v, want one per namespace", got)
	}

	waitObjectNames(t, fa, "pod-a1")
	waitObjectNames(t, fb, "pod-b1")

	// Live updates arrive through the namespace watch.
	createPod(t, e.admin, "team-a", "pod-a2")
	waitObjectNames(t, fa, "pod-a1", "pod-a2")
	deletePod(t, e.admin, "team-a", "pod-a1")
	waitObjectNames(t, fa, "pod-a2")
	if got := objectNames(t, fb); !slices.Equal(got, []string{"pod-b1"}) {
		t.Fatalf("team-b saw team-a's changes: %v", got)
	}

	if errs := e.errors(); len(errs) > 0 {
		t.Fatalf("reported read errors: %v", errs)
	}
	fa.Close()
	fb.Close()
	fa.Close() // twice is safe
	if got := e.openHolds(); len(got) != 0 {
		t.Fatalf("holds left after Close: %v", got)
	}
}

func TestNamespaceOnlyUserRootCountsPermittedTypes(t *testing.T) {
	const user = "root-user"
	e := newNamespaceUserEnv(t, user, "root-a")
	createPod(t, e.admin, "root-a", "pod-r1")

	root := NewRootFolder(e.deps, "root-a")
	t.Cleanup(root.Close)
	podsID := "root-a//v1/pods"
	countOf := func() string {
		rows := rowsByID(t, root, t.Context())
		if cells, ok := rows[podsID]; ok && len(cells) > 2 {
			return cells[2]
		}
		return ""
	}
	kctesting.Eventually(t, 15*time.Second, 100*time.Millisecond, func() bool { return countOf() == "1" },
		"root never counted 1 pod in root-a")
	createPod(t, e.admin, "root-a", "pod-r2")
	kctesting.Eventually(t, 15*time.Second, 100*time.Millisecond, func() bool { return countOf() == "2" },
		"root count did not follow a new pod in root-a")

	// Cluster-scoped types the user may not list are hidden, not errors.
	nsItem := root.items["namespaces"]
	if nsItem == nil {
		t.Fatal("root has no namespaces item")
	}
	kctesting.Eventually(t, 15*time.Second, 100*time.Millisecond, nsItem.Forbidden,
		"namespaces item never became forbidden")
	if rows := rowsByID(t, root, t.Context()); rows["namespaces"] != nil {
		t.Fatalf("forbidden namespaces row is shown: %v", rows["namespaces"])
	}
	if errs := e.errors(); len(errs) > 0 {
		t.Fatalf("reported read errors: %v", errs)
	}
	if got := e.openHolds(); len(got) != 1 {
		t.Fatalf("open holds = %v, want the root-a cache held by the counts", got)
	}
	root.Close()
	kctesting.Eventually(t, 5*time.Second, 50*time.Millisecond, func() bool { return len(e.openHolds()) == 0 },
		"closing the root left namespace caches held")
}
