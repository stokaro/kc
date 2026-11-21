package models

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestKubectlCommand(t *testing.T) {
	current := Deps{CtxName: "dev", KubeConfig: clientcmdapi.Config{CurrentContext: "dev"}}
	other := Deps{CtxName: "prod", KubeConfig: clientcmdapi.Config{CurrentContext: "dev"}}
	deploy := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	objects := func(deps Deps, gvr schema.GroupVersionResource, ns string) *ObjectsFolder {
		return &ObjectsFolder{BaseFolder: &BaseFolder{Deps: deps}, gvr: gvr, namespace: ns}
	}
	nodePods := objects(current, podGVR, "")
	nodePods.kubectlArgs = []string{"--field-selector", "spec.nodeName=n1"}
	owned := objects(current, podGVR, "web")
	owned.kubectlNote = "owned by replicasets.apps/web-1"

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
		{"namespace resources", &NamespacedResourcesFolder{ResourcesFolder: &ResourcesFolder{BaseFolder: &BaseFolder{Deps: current}}}, "kubectl api-resources --namespaced=true"},
		{"configmap keys", &ConfigMapKeysFolder{BaseFolder: &BaseFolder{Deps: current}, Namespace: "web", Name: "cfg"}, "kubectl get configmap cfg -n web -o 'jsonpath={.data}'"},
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
