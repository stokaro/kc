package models

import (
	"cmp"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// maxUsageEvents bounds the events a report keeps for the dashboard; the
// newest are kept.
const maxUsageEvents = 2000

// UsageEvent is one Kubernetes Event about the focus of UsageAmounts.
type UsageEvent struct {
	// Event names the Event object itself.
	Event ObjectRef
	// Object is the involved object; its GVR is empty when kc does not
	// know the kind's resource.
	Object     ObjectRef
	Kind       string
	Type       string // Normal or Warning
	Reason     string
	Message    string
	Count      int32
	First      time.Time
	Last       time.Time
	FirstKnown bool // false when the API server kept no first time
}

// UsageEvents are the retained Events of a focus.
type UsageEvents struct {
	// Items are oldest first by their last time.
	Items []UsageEvent
	// Oldest is the earliest time of any Event the engine holds for the
	// scope: the API server keeps Events about an hour by default, so the
	// lane knows nothing before it.
	Oldest time.Time
	// Missing says why Events cannot be read, when they cannot.
	Missing string
	// Dropped counts Events of the scope beyond maxUsageEvents.
	Dropped int
}

// overviewEvent is what the dashboard needs of one Event.
type overviewEvent struct {
	ev UsageEvent
	// subjects are the workloads the involved object is or belongs to;
	// pod and node name the involved pod and its node, or the node.
	subjects  []InfoWorkload
	pod, node string
}

// knownKinds maps the involved objects the dashboard can open.
var knownKinds = map[schema.GroupKind]schema.GroupVersionResource{
	{Kind: "Pod"}:                   ovPodsGVR,
	{Kind: "Node"}:                  ovNodesGVR,
	{Kind: "PersistentVolumeClaim"}: {Version: "v1", Resource: "persistentvolumeclaims"},
	{Group: "autoscaling", Kind: "HorizontalPodAutoscaler"}: {Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"},
}

// involvedGVR maps the object an Event is about to its GVR: a workload the
// walks can reach (built in, or an owner kind the engine resolved) or one
// of knownKinds.
func involvedGVR(idx *workloadIndex, ref corev1.ObjectReference) (schema.GroupVersionResource, bool) {
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil {
		return schema.GroupVersionResource{}, false
	}
	gk := schema.GroupKind{Group: gv.Group, Kind: ref.Kind}
	if gvr, ok := workloadKinds[gk]; ok {
		return gvr, true
	}
	if k, ok := idx.ownerKinds[gk]; ok && k.namespaced {
		return k.gvr, true
	}
	gvr, ok := knownKinds[gk]
	return gvr, ok
}

// eventFirstSeen is when an Event first happened, if the API server kept it.
func eventFirstSeen(e *corev1.Event) (time.Time, bool) {
	switch {
	case !e.FirstTimestamp.IsZero():
		return e.FirstTimestamp.Time, true
	case !e.EventTime.IsZero():
		return e.EventTime.Time, true
	}
	return e.CreationTimestamp.Time, false
}

// summarizeEvents keeps the Events of the scope with what they are about.
func summarizeEvents(rc *ruleContext, idx *workloadIndex, byPod map[string][]InfoWorkload, pods []overviewPodAmount) ([]overviewEvent, UsageEvents) {
	var meta UsageEvents
	if r := rc.in.missingReason(ovEventsGVR); r != "" {
		meta.Missing = r
		return nil, meta
	}
	nodeOf := make(map[string]string, len(pods))
	for i := range pods {
		nodeOf[usageObjectKey(pods[i].namespace, pods[i].name)] = pods[i].node
	}
	out := make([]overviewEvent, 0, len(rc.in.events))
	for i := range rc.in.events {
		e := &rc.in.events[i]
		if !rc.in.inScope(e.Namespace) {
			continue
		}
		first, firstKnown := eventFirstSeen(e)
		last := eventLastSeen(e)
		if meta.Oldest.IsZero() || first.Before(meta.Oldest) {
			meta.Oldest = first
		}
		count := max(e.Count, 1)
		if e.Series != nil && e.Series.Count > count {
			count = e.Series.Count
		}
		io := e.InvolvedObject
		oe := overviewEvent{ev: UsageEvent{
			Event: ObjectRef{GVR: ovEventsGVR, Namespaced: true, Namespace: e.Namespace, Name: e.Name, UID: e.UID},
			Kind:  io.Kind, Type: e.Type, Reason: e.Reason, Message: e.Message, Count: count,
			First: first, Last: last, FirstKnown: firstKnown,
		}}
		gvr, known := involvedGVR(idx, io)
		ns := cmp.Or(io.Namespace, e.Namespace)
		oe.ev.Object = ObjectRef{Namespaced: io.Kind != "Node", Namespace: ns, Name: io.Name, UID: io.UID}
		if io.Kind == "Node" {
			oe.ev.Object.Namespace = ""
		}
		if known {
			oe.ev.Object.GVR = gvr
		}
		switch {
		case io.Kind == "Pod" && io.APIVersion == "v1":
			key := usageObjectKey(ns, io.Name)
			oe.pod, oe.node, oe.subjects = io.Name, nodeOf[key], byPod[key]
		case io.Kind == "Node":
			oe.node = io.Name
		case known && idx.isWorkload(gvr):
			oe.subjects = workloadChain(idx, InfoWorkload{GVR: gvr, Namespace: ns, Name: io.Name}, io.UID)
		}
		out = append(out, oe)
	}
	slices.SortStableFunc(out, func(a, b overviewEvent) int { return a.ev.Last.Compare(b.ev.Last) })
	if n := len(out) - maxUsageEvents; n > 0 {
		meta.Dropped = n
		out = slices.Delete(out, 0, n)
	}
	return out, meta
}

// workloadChain is w and the workloads that control it. A listed object
// with another UID is a recreated one: the Event was about the earlier
// object, which no longer belongs to the current owners.
func workloadChain(idx *workloadIndex, w InfoWorkload, uid types.UID) []InfoWorkload {
	out := []InfoWorkload{w}
	objs, listed := idx.objects[w.GVR]
	if !listed {
		return out
	}
	om, found := objs[objectKey(w.Namespace, w.Name)]
	if !found || (uid != "" && om.UID != uid) {
		return out
	}
	return append(out, idx.owners(w.Namespace, metav1.GetControllerOfNoCopy(om))...)
}

// focusEvents are the Events about what focus names: the pod; the
// workload, its pods and the workloads it controls; the node and the pods
// on it; or every Event of the scope.
func focusEvents(events []overviewEvent, focus UsageFocus) []UsageEvent {
	in := func(*overviewEvent) bool { return true }
	switch {
	case focus.Pod != "":
		in = func(e *overviewEvent) bool {
			return e.pod == focus.Pod && e.ev.Object.Namespace == focus.PodNamespace
		}
	case focus.Workload != nil:
		w := *focus.Workload
		in = func(e *overviewEvent) bool { return slices.Contains(e.subjects, w) }
	case focus.Node != "":
		in = func(e *overviewEvent) bool { return e.node == focus.Node }
	}
	var out []UsageEvent
	for i := range events {
		if in(&events[i]) {
			out = append(out, events[i].ev)
		}
	}
	return out
}
