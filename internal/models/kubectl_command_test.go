package models

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestKubectlCommand(t *testing.T) {
	// KubeConfig.CurrentContext is the panel's own context in kc; only
	// KubectlContext decides whether --context is needed.
	current := Deps{CtxName: "dev", KubectlContext: "dev", KubeConfig: clientcmdapi.Config{CurrentContext: "dev"}}
	other := Deps{CtxName: "prod", KubectlContext: "dev", KubeConfig: clientcmdapi.Config{CurrentContext: "prod"}}
	deploy := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	objects := func(deps Deps, gvr schema.GroupVersionResource, ns string) *ObjectsFolder {
		return &ObjectsFolder{BaseFolder: &BaseFolder{Deps: deps}, gvr: gvr, namespace: ns}
	}
	nodePods := objects(current, podGVR, "")
	nodePods.kubectlArgs = []string{"--field-selector", "spec.nodeName=n1"}
	owned := objects(current, podGVR, "web")
	owned.kubectlNote = "owned by replicasets.apps/web-1"
	filtered := func(text string) *ObjectsFolder {
		o := objects(current, podGVR, "web")
		o.rows = newLiveObjectRowSourceWithHooks(nil, nil, nil)
		f, err := ParseRowFilter(text)
		if err != nil {
			t.Fatal(err)
		}
		f.FaultsOnly = true
		o.rows.setRowFilter(f)
		return o
	}

	tests := []struct {
		name   string
		folder KubectlCommander
		want   string
	}{
		{"namespaced core", objects(current, podGVR, "web"), "kubectl get pods -n web"},
		{"all namespaces with group", objects(current, deploy, ""), "kubectl get deployments.apps -A"},
		{"other context", objects(other, podGVR, "web"), "kubectl --context prod get pods -n web"},
		{"node pods", nodePods, "kubectl get pods -A --field-selector spec.nodeName=n1"},
		{"owned children", owned, "kubectl get pods -n web  # owned by replicasets.apps/web-1"},
		{"label filter", filtered("-l app=web"), "kubectl get pods -n web -l app=web"},
		{"label set filter", filtered("-l tier in (a, b)"), "kubectl get pods -n web -l 'tier in (a,b)'"},
		{"name filter is not a kubectl flag", filtered("!web"), "kubectl get pods -n web"},
		{"namespace resources", &NamespacedResourcesFolder{ResourcesFolder: &ResourcesFolder{BaseFolder: &BaseFolder{Deps: current}}}, "kubectl api-resources --namespaced=true"},
		{"configmap keys", &ConfigMapKeysFolder{BaseFolder: &BaseFolder{Deps: current}, Namespace: "web", Name: "cfg"}, `kubectl get configmap cfg -n web -o 'jsonpath={.data}{"\n"}{.binaryData}'`},
		{"containers", &PodContainerListFolder{BaseFolder: &BaseFolder{Deps: current}, Namespace: "web", Pod: "p", Kind: containerKindInit},
			`kubectl get pod p -n web -o 'jsonpath={range .spec.initContainers[*]}{.name}{"\n"}{end}'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.folder.KubectlCommand(); got != tt.want {
				t.Fatalf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{"pods": "pods", "a b": "'a b'", "it's": `'it'\''s'`, "": "''"} {
		if got := shellQuote(in); got != want {
			t.Fatalf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestViewCommands(t *testing.T) {
	deps := Deps{CtxName: "dev", KubectlContext: "dev"}
	if got := keyViewCommand(deps, "secret", "web", "tls", "tls.crt", true); got != `kubectl get secret tls -n web -o 'jsonpath={.data.tls\.crt}' | base64 -d` {
		t.Fatalf("secret key: %s", got)
	}
	for _, tt := range []struct {
		name   string
		key    string
		binary bool
		want   string
	}{
		{"text key", "app.conf", false, `kubectl get configmap cfg -n web -o 'jsonpath={.data.app\.conf}'`},
		{"binary key", "logo.png", true, `kubectl get configmap cfg -n web -o 'jsonpath={.binaryData.logo\.png}' | base64 -d`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := configMapKeyViewCommand(deps, "web", "cfg", tt.key, tt.binary); got != tt.want {
				t.Fatalf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
	spec := LogsSpec{Namespace: "web", Pod: "p", Container: "app", Follow: true, TailLines: 200}
	if got := logsCommand(deps, spec); got != "kubectl logs p -n web -c app --tail=200 -f" {
		t.Fatalf("logs: %s", got)
	}
	o := &ObjectsFolder{BaseFolder: &BaseFolder{Deps: deps}, gvr: podGVR}
	if got := o.objectViewCommand("web", "p"); got != "kubectl get pods p -n web -o yaml" {
		t.Fatalf("object: %s", got)
	}
}
