package models

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	kccluster "github.com/stokaro/kc/internal/cluster"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/pkg/appconfig"
)

// closingFolder is a folder that navigators close when they drop it.
type closingFolder interface {
	Folder
	Close()
}

// The keys of a ConfigMap or Secret and the sections and containers of a
// pod come from a live GET of the object; controller-runtime's client does
// not cache unstructured objects. Those folders loaded on the reader's
// goroutine, which is the UI's, and a failed load left them dirty: against
// a server that stops answering every read waited for its own GET until
// its deadline, and sent another. Now they load in the background like
// object lists, and reads during a load wait at most until their deadline
// without asking the server again.
func TestObjectFoldersReadOffTheLoopWhileTheServerStalls(t *testing.T) {
	kcenvtest.Require(t, testCfg)
	t.Parallel()
	admin, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	t.Cleanup(admin.Stop)
	ns := kctesting.UniqueName("stalled-object")
	createNamespace(t, admin, ns)
	createConfigMap(t, admin, ns, "settings")
	createSecret(t, admin, ns, "token")
	createPod(t, admin, ns, "web")

	proxy := kctesting.NewStallProxy(t, testCfg)
	cl, err := kccluster.New(proxy.Config())
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	t.Cleanup(cl.Stop)
	kctesting.RunInBackground(t, "cluster", cl.Start)
	if _, err := cl.GetResourceInfos(); err != nil {
		t.Fatalf("discovery: %v", err)
	}
	deps := Deps{Cl: cl, Ctx: t.Context(), AppConfig: appconfig.Default()}
	podPath := []string{"namespaces", ns, "pods", "web"}

	tests := []struct {
		name   string
		object string // the API path of the object the folder reads
		folder func() closingFolder
	}{
		{
			name:   "ConfigMap keys",
			object: "/api/v1/namespaces/" + ns + "/configmaps/settings",
			folder: func() closingFolder {
				return NewConfigMapKeysFolder(deps, []string{"namespaces", ns, "configmaps", "settings"}, ns, "settings")
			},
		},
		{
			name:   "Secret keys",
			object: "/api/v1/namespaces/" + ns + "/secrets/token",
			folder: func() closingFolder {
				return NewSecretKeysFolder(deps, []string{"namespaces", ns, "secrets", "token"}, ns, "token")
			},
		},
		{
			name:   "pod sections",
			object: "/api/v1/namespaces/" + ns + "/pods/web",
			folder: func() closingFolder {
				return NewPodContainersFolder(deps, podPath, ns, "web")
			},
		},
		{
			name:   "pod containers",
			object: "/api/v1/namespaces/" + ns + "/pods/web",
			folder: func() closingFolder {
				return NewPodContainerListFolder(deps, append(podPath, "containers"), ns, "web", containerKindPrimary)
			},
		},
	}
	const (
		reads    = 6
		deadline = 250 * time.Millisecond
	)
	for _, tt := range tests {
		// Sequential: the cases share the proxy, and each counts the GETs
		// of its object.
		t.Run(tt.name, func(t *testing.T) {
			gets := func() int {
				return proxy.Requests(func(method, uri string) bool {
					path, _, _ := strings.Cut(uri, "?")
					return method == http.MethodGet && path == tt.object
				})
			}
			// Everything but discovery stalls: the object, the watch's
			// list and the watch.
			proxy.Stall(func(r *http.Request) bool { return !kctesting.DiscoveryRequest(r) })
			t.Cleanup(proxy.Resume)
			folder := tt.folder()
			t.Cleanup(folder.Close)
			before := gets()

			for i := range reads {
				ctx, cancel := context.WithTimeout(t.Context(), deadline)
				done := make(chan time.Duration, 1)
				go func() {
					start := time.Now()
					folder.Len(ctx)
					done <- time.Since(start)
				}()
				select {
				case took := <-done:
					if took > deadline+time.Second {
						t.Errorf("read %d took %v with a %v deadline", i+1, took, deadline)
					}
				case <-time.After(10 * time.Second):
					t.Fatalf("read %d with a %v deadline did not return while the server stalls", i+1, deadline)
				}
				cancel()
			}
			sent := gets() - before
			t.Logf("%d reads while the server stalls sent %d GETs of %s", reads, sent, tt.object)
			// One: the first read started the load, and the others did not
			// start another while it was held.
			if sent != 1 {
				t.Errorf("%d reads while the server stalls sent %d GETs of %s; want one load for all of them", reads, sent, tt.object)
			}

			// The held load completes once the server answers.
			proxy.Resume()
			kctesting.Eventually(t, 30*time.Second, 50*time.Millisecond, func() bool {
				ctx, cancel := context.WithTimeout(t.Context(), deadline)
				defer cancel()
				return folder.Len(ctx) > 0
			}, "the folder shows no rows after the server answered")
		})
	}
}
