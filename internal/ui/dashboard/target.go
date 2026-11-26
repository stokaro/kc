// Package dashboard is the full-screen dashboard of kc: CPU and memory
// charts of a scope, node, workload or pod, from Prometheus when the
// context has a backend and from the Metrics API session history
// otherwise, with the pods that use the most. It owns no cluster
// connection; the App gives it Sources.
package dashboard

import (
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/promhistory"
)

// Kind is what a dashboard is about.
type Kind int

const (
	// KindScope is the namespace scope: one namespace, or the cluster.
	KindScope Kind = iota
	// KindNode is one node.
	KindNode
	// KindWorkload is one workload with its pods.
	KindWorkload
	// KindPod is one pod.
	KindPod
)

var (
	nodesGVR = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
	podsGVR  = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
)

// Target is the object of one dashboard view.
type Target struct {
	Kind Kind
	// Namespace is the namespace scope (models.AllNamespaces for the
	// cluster). It is the scope of the overview engine behind every view
	// of one dashboard, and the namespace of a workload or pod.
	Namespace string
	// Name is the node, workload or pod; Workload's GVR is set for a
	// workload.
	Name     string
	Workload schema.GroupVersionResource
}

// Key identifies the target with its full GVR; the session history and
// a restored dashboard are keyed by it.
func (t Target) Key() string {
	switch t.Kind {
	case KindNode:
		return "node|" + t.Name
	case KindWorkload:
		return "workload|" + t.Workload.String() + "|" + t.Namespace + "/" + t.Name
	case KindPod:
		return "pod|" + t.Namespace + "/" + t.Name
	default:
		return "scope|" + t.Namespace
	}
}

// Title names the target for the title bar.
func (t Target) Title() string {
	switch t.Kind {
	case KindNode:
		return "node " + t.Name
	case KindWorkload:
		return workloadKind(t.Workload) + " " + t.Namespace + "/" + t.Name
	case KindPod:
		return "pod " + t.Namespace + "/" + t.Name
	default:
		if t.Namespace == models.AllNamespaces {
			return "all namespaces"
		}
		return "namespace " + t.Namespace
	}
}

// workloadKind names the kind of a workload: Deployment for a built-in
// one, the resource as kubectl names it (rollouts.argoproj.io) for any
// other, so a custom StatefulSet is not taken for the apps one.
func workloadKind(gvr schema.GroupVersionResource) string {
	if k, ok := promhistory.KindFor(gvr.Group, gvr.Resource); ok {
		return k
	}
	if gvr.Group == "" {
		return gvr.Resource
	}
	return gvr.Resource + "." + gvr.Group
}

// Focus is the overview focus of the target.
func (t Target) Focus() models.UsageFocus {
	switch t.Kind {
	case KindNode:
		return models.UsageFocus{Node: t.Name}
	case KindWorkload:
		return models.UsageFocus{Workload: &models.InfoWorkload{GVR: t.Workload, Namespace: t.Namespace, Name: t.Name}}
	case KindPod:
		return models.UsageFocus{PodNamespace: t.Namespace, Pod: t.Name}
	default:
		return models.UsageFocus{}
	}
}

// HistoryFunc names the Prometheus target of a workload, or says why
// Prometheus cannot read its pods.
type HistoryFunc func(models.InfoWorkload) (promhistory.Target, error)

// Scope is the Prometheus scope of the target, with the identity of the
// live object; the error says why Prometheus cannot read it. history names
// the target of a workload; nil knows the built-in kinds only.
func (t Target) Scope(uid types.UID, created time.Time, history HistoryFunc) (promhistory.Scope, error) {
	s := promhistory.Scope{Namespace: t.Namespace, Name: t.Name, UID: string(uid), Since: created}
	switch t.Kind {
	case KindNode:
		s.Kind, s.Namespace = promhistory.ScopeNode, ""
	case KindWorkload:
		w := models.InfoWorkload{GVR: t.Workload, Namespace: t.Namespace, Name: t.Name}
		target := promhistory.Target{Namespace: t.Namespace, Name: t.Name}
		var err error
		switch kind, ok := promhistory.KindFor(t.Workload.Group, t.Workload.Resource); {
		case history != nil:
			target, err = history(w)
		case ok:
			target.Kind = kind
		default:
			err = errors.New("no owner series for " + workloadKind(t.Workload))
		}
		if err != nil {
			return s, err
		}
		s.Kind, s.Workload, s.Name = promhistory.ScopeWorkload, target, ""
	case KindPod:
		s.Kind = promhistory.ScopePod
	default:
		s.Kind, s.Name = promhistory.ScopeNamespace, ""
		if t.Namespace == models.AllNamespaces {
			s.Namespace = ""
		}
	}
	return s, nil
}

// Object is the Kubernetes object of the target, to open in Commander;
// ok is false for a namespace scope of all namespaces.
func (t Target) Object(uid types.UID) (models.ObjectRef, bool) {
	switch t.Kind {
	case KindNode:
		return models.ObjectRef{GVR: nodesGVR, Name: t.Name}, true
	case KindWorkload:
		return models.ObjectRef{GVR: t.Workload, Namespaced: true, Namespace: t.Namespace, Name: t.Name, UID: uid}, true
	case KindPod:
		return models.ObjectRef{GVR: podsGVR, Namespaced: true, Namespace: t.Namespace, Name: t.Name, UID: uid}, true
	default:
		if t.Namespace == models.AllNamespaces {
			return models.ObjectRef{}, false
		}
		return models.ObjectRef{GVR: schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, Name: t.Namespace}, true
	}
}

// PodTarget is the dashboard of a pod in the scope of t.
func (t Target) PodTarget(namespace, name string) Target {
	return Target{Kind: KindPod, Namespace: namespace, Name: name}
}
