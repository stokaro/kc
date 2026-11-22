package models_test

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/models"
)

type obj = map[string]any

func conditions(pairs ...string) []any {
	out := make([]any, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, obj{"type": pairs[i], "status": pairs[i+1]})
	}
	return out
}

func waiting(reason string) []any {
	return []any{obj{"name": "app", "state": obj{"waiting": obj{"reason": reason}}}}
}

func TestClassifyRowHealth(t *testing.T) {
	var (
		pods         = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
		nodes        = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
		pvcs         = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}
		pvs          = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}
		deployments  = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
		statefulsets = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
		replicasets  = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
		daemonsets   = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}
		jobs         = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
		configmaps   = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	)
	tests := []struct {
		name string
		gvr  schema.GroupVersionResource
		obj  obj
		want models.RowHealth
	}{
		{name: "pod running ready", gvr: pods, obj: obj{"status": obj{"phase": "Running", "conditions": conditions("Ready", "True")}}, want: models.RowHealthOK},
		{name: "pod running not ready", gvr: pods, obj: obj{"status": obj{"phase": "Running", "conditions": conditions("Ready", "False")}}, want: models.RowHealthNotReady},
		{name: "pod running no conditions", gvr: pods, obj: obj{"status": obj{"phase": "Running"}}, want: models.RowHealthOK},
		{name: "pod pending", gvr: pods, obj: obj{"status": obj{"phase": "Pending"}}, want: models.RowHealthNotReady},
		{name: "pod unknown phase", gvr: pods, obj: obj{"status": obj{"phase": "Unknown"}}, want: models.RowHealthNotReady},
		{name: "pod failed", gvr: pods, obj: obj{"status": obj{"phase": "Failed"}}, want: models.RowHealthFailed},
		{name: "pod succeeded is fine", gvr: pods, obj: obj{"status": obj{"phase": "Succeeded", "conditions": conditions("Ready", "False")}}, want: models.RowHealthOK},
		{name: "pod crash loop", gvr: pods, obj: obj{"status": obj{"phase": "Running", "conditions": conditions("Ready", "True"), "containerStatuses": waiting("CrashLoopBackOff")}}, want: models.RowHealthNotReady},
		{name: "pod init image pull", gvr: pods, obj: obj{"status": obj{"phase": "Running", "initContainerStatuses": waiting("ImagePullBackOff")}}, want: models.RowHealthNotReady},
		{name: "pod container creating", gvr: pods, obj: obj{"status": obj{"conditions": conditions("Ready", "True"), "containerStatuses": waiting("ContainerCreating")}}, want: models.RowHealthOK},
		{name: "pod no status", gvr: pods, obj: obj{}, want: models.RowHealthUnknown},

		{name: "node ready", gvr: nodes, obj: obj{"status": obj{"conditions": conditions("MemoryPressure", "False", "Ready", "True")}}, want: models.RowHealthOK},
		{name: "node not ready", gvr: nodes, obj: obj{"status": obj{"conditions": conditions("Ready", "False")}}, want: models.RowHealthNotReady},
		{name: "node ready unknown", gvr: nodes, obj: obj{"status": obj{"conditions": conditions("Ready", "Unknown")}}, want: models.RowHealthNotReady},
		{name: "node cordoned but ready", gvr: nodes, obj: obj{"spec": obj{"unschedulable": true}, "status": obj{"conditions": conditions("Ready", "True")}}, want: models.RowHealthOK},
		{name: "node no conditions", gvr: nodes, obj: obj{}, want: models.RowHealthUnknown},

		{name: "pvc bound", gvr: pvcs, obj: obj{"status": obj{"phase": "Bound"}}, want: models.RowHealthOK},
		{name: "pvc pending", gvr: pvcs, obj: obj{"status": obj{"phase": "Pending"}}, want: models.RowHealthNotReady},
		{name: "pvc lost", gvr: pvcs, obj: obj{"status": obj{"phase": "Lost"}}, want: models.RowHealthFailed},
		{name: "pv released", gvr: pvs, obj: obj{"status": obj{"phase": "Released"}}, want: models.RowHealthOK},
		{name: "pv failed", gvr: pvs, obj: obj{"status": obj{"phase": "Failed"}}, want: models.RowHealthFailed},

		{name: "deployment available", gvr: deployments, obj: obj{"spec": obj{"replicas": int64(2)}, "status": obj{"readyReplicas": int64(2), "availableReplicas": int64(2), "conditions": conditions("Available", "True")}}, want: models.RowHealthOK},
		{name: "deployment short", gvr: deployments, obj: obj{"spec": obj{"replicas": int64(3)}, "status": obj{"readyReplicas": int64(1), "availableReplicas": int64(1)}}, want: models.RowHealthNotReady},
		{name: "deployment unavailable condition", gvr: deployments, obj: obj{"spec": obj{"replicas": int64(1)}, "status": obj{"readyReplicas": int64(1), "availableReplicas": int64(1), "conditions": conditions("Available", "False")}}, want: models.RowHealthNotReady},
		{name: "deployment scaled to zero", gvr: deployments, obj: obj{"spec": obj{"replicas": int64(0)}}, want: models.RowHealthOK},
		{name: "statefulset ready", gvr: statefulsets, obj: obj{"spec": obj{"replicas": int64(3)}, "status": obj{"readyReplicas": int64(3)}}, want: models.RowHealthOK},
		{name: "statefulset short", gvr: statefulsets, obj: obj{"spec": obj{"replicas": int64(3)}, "status": obj{"readyReplicas": int64(2)}}, want: models.RowHealthNotReady},
		{name: "statefulset default replicas", gvr: statefulsets, obj: obj{"status": obj{}}, want: models.RowHealthNotReady},
		{name: "replicaset old and empty", gvr: replicasets, obj: obj{"spec": obj{"replicas": int64(0)}, "status": obj{"replicas": int64(0)}}, want: models.RowHealthOK},
		{name: "replicaset unavailable", gvr: replicasets, obj: obj{"spec": obj{"replicas": int64(2)}, "status": obj{"readyReplicas": int64(2), "availableReplicas": int64(1)}}, want: models.RowHealthNotReady},
		{name: "daemonset ready", gvr: daemonsets, obj: obj{"status": obj{"desiredNumberScheduled": int64(3), "numberReady": int64(3), "numberAvailable": int64(3)}}, want: models.RowHealthOK},
		{name: "daemonset short", gvr: daemonsets, obj: obj{"status": obj{"desiredNumberScheduled": int64(3), "numberReady": int64(3), "numberAvailable": int64(2)}}, want: models.RowHealthNotReady},

		{name: "job running", gvr: jobs, obj: obj{"spec": obj{"backoffLimit": int64(6)}, "status": obj{"active": int64(1)}}, want: models.RowHealthOK},
		{name: "job succeeded", gvr: jobs, obj: obj{"status": obj{"succeeded": int64(1)}}, want: models.RowHealthSucceeded},
		{name: "job backoff exhausted", gvr: jobs, obj: obj{"spec": obj{"backoffLimit": int64(2)}, "status": obj{"failed": int64(2)}}, want: models.RowHealthFailed},
		{name: "job failed condition", gvr: jobs, obj: obj{"status": obj{"conditions": conditions("Failed", "True")}}, want: models.RowHealthFailed},
		{name: "job retrying", gvr: jobs, obj: obj{"spec": obj{"backoffLimit": int64(6)}, "status": obj{"failed": int64(1)}}, want: models.RowHealthOK},

		{name: "kind without rule", gvr: configmaps, obj: obj{"data": obj{"a": "b"}}, want: models.RowHealthUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := models.ClassifyRowHealth(tc.gvr, &unstructured.Unstructured{Object: tc.obj})
			if got != tc.want {
				t.Errorf("ClassifyRowHealth() = %v, want %v", got, tc.want)
			}
		})
	}
	if got := models.ClassifyRowHealth(pods, nil); got != models.RowHealthUnknown {
		t.Errorf("ClassifyRowHealth(nil) = %v, want unknown", got)
	}
}

func TestRowHealthFaulty(t *testing.T) {
	tests := []struct {
		health models.RowHealth
		want   bool
	}{
		{models.RowHealthUnknown, false},
		{models.RowHealthOK, false},
		{models.RowHealthSucceeded, false},
		{models.RowHealthNotReady, true},
		{models.RowHealthFailed, true},
	}
	for _, tc := range tests {
		if got := tc.health.Faulty(); got != tc.want {
			t.Errorf("RowHealth(%d).Faulty() = %v, want %v", tc.health, got, tc.want)
		}
	}
}
