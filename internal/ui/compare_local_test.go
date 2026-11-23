package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/manifestdiff"
	"github.com/stokaro/kc/internal/models"
	kctesting "github.com/stokaro/kc/internal/testing"
	"github.com/stokaro/kc/internal/testing/kcenvtest"
	"github.com/stokaro/kc/pkg/appconfig"
)

const renderedManifests = `apiVersion: v1
kind: Service
metadata:
  name: web
  namespace: shop
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: shop
spec:
  replicas: 3
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: web
  namespace: other
`

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rendered.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadFileManifest(t *testing.T) {
	t.Parallel()
	deploys := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	configMaps := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	services := schema.GroupVersionResource{Version: "v1", Resource: "services"}
	path := writeFile(t, renderedManifests)
	cases := []struct {
		name      string
		body      string
		gvr       schema.GroupVersionResource
		namespace string
		object    string
		wantKind  string
		wantErr   string
	}{
		{name: "the Deployment, not the Service of the same name", gvr: deploys, namespace: "shop", object: "web", wantKind: "Deployment"},
		{name: "the Service", gvr: services, namespace: "shop", object: "web", wantKind: "Service"},
		{name: "a document without namespace matches any", gvr: deploys, namespace: "shop", object: "api", wantKind: "Deployment"},
		{name: "another namespace does not match", gvr: configMaps, namespace: "shop", object: "web", wantErr: "has no document for configmaps web"},
		{name: "two documents are ambiguous", body: renderedManifests + "---\n" + strings.SplitN(renderedManifests, "---\n", 3)[1], gvr: deploys, namespace: "shop", object: "web", wantErr: "has 2 documents"},
		{name: "invalid YAML", body: "kind: [", gvr: deploys, namespace: "shop", object: "web", wantErr: "document 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := path
			if tc.body != "" {
				p = writeFile(t, tc.body)
			}
			obj, rev, err := readFileManifest(p, tc.gvr, tc.namespace, tc.object)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if obj.GetKind() != tc.wantKind || obj.GetName() != tc.object {
				t.Errorf("got %s %s", obj.GetKind(), obj.GetName())
			}
			if len(rev.sum) != 64 || !strings.HasPrefix(rev.String(), "sha256 "+rev.sum[:12]+", modified ") {
				t.Errorf("revision = %q", rev)
			}
		})
	}
}

// C-x C-d with a rendered file on one side and the live object on the
// other compares the file's document for that object and names both
// origins and revisions.
func TestCompareLocalFileWithLiveObject(t *testing.T) {
	kcenvtest.Require(t, testCfg)
	cl, err := kccluster.New(testCfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	kctesting.RunInBackground(t, "cluster", cl.Start)
	ns := kctesting.UniqueName("cmp-file")
	createConfigMaps(t, testCfg, ns, map[string]string{"web": "live"})
	deps := models.Deps{
		Cl: cl, Ctx: ctx, CtxName: "envtest",
		KubeConfig: clientcmdapi.Config{CurrentContext: "envtest", Contexts: map[string]*clientcmdapi.Context{"envtest": {Namespace: ns}}},
		AppConfig:  appconfig.Default(),
	}
	folder := models.NewNamespacedObjectsFolder(deps, configMapsGVR, ns, []string{"namespaces", ns, "configmaps"}, nil)
	t.Cleanup(folder.Close)
	var live models.ManifestSource
	kctesting.Eventually(t, 10*time.Second, 50*time.Millisecond, func() bool {
		for _, r := range folder.Lines(ctx, 0, folder.Len(ctx)) {
			if src, ok := r.(models.ManifestSource); ok && src.Name() == "web" {
				live = src
			}
		}
		return live != nil
	})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "web.yaml"), []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: web\ndata:\n  k: desired\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	local := models.NewLocalDirFolder(deps, dir)
	var file models.Item
	for _, r := range local.Lines(ctx, 0, local.Len(ctx)) {
		if it, ok := r.(*models.LocalFileItem); ok {
			file = it
		}
	}
	if file == nil {
		t.Fatal("no file row in the local folder")
	}
	if _, _, ok := compareSides(file, file); ok {
		t.Error("two local files must not pair: neither is a cluster object")
	}
	left, right, ok := compareSides(file, live)
	if !ok {
		t.Fatal("a local file and a live object must pair")
	}
	a := &App{ctx: ctx}
	msg, ok := a.readComparison(nil, 1, left, right, "envtest", manifestdiff.Options{})().(compareResultMsg)
	if !ok || msg.err != nil {
		t.Fatalf("compare = %#v", msg)
	}
	header, _, _ := strings.Cut(msg.result.Text, "\n# view:")
	for _, want := range []string{"# left:    context local file " + filepath.Join(dir, "web.yaml"), ", sha256 ", "# right:   context envtest", ", resourceVersion "} {
		if !strings.Contains(header, want) {
			t.Errorf("header misses %q:\n%s", want, header)
		}
	}
	if !strings.Contains(msg.result.Text, "-  k: desired") || !strings.Contains(msg.result.Text, "+  k: live") {
		t.Errorf("diff misses the data change:\n%s", msg.result.Text)
	}
}
