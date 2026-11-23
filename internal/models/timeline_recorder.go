package models

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	toolscache "k8s.io/client-go/tools/cache"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/timeline"
	"github.com/stokaro/kc/pkg/appconfig"
)

// A recording follows one object through the shared cache: it adds an
// event handler to the informer of the object's type that kc already
// shares, and holds a lease on the cluster so the informer keeps running
// while the user looks elsewhere. It starts when the user opens the
// object's /timeline and runs until stopped, until more recordings than
// timeline.maxObjects start, or until kc exits.

var controllerRevisionsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "controllerrevisions"}

// historyGVR is where the controller of gvr keeps old revisions of its
// template: ReplicaSets of a Deployment, ControllerRevisions of a
// StatefulSet or DaemonSet.
func historyGVR(gvr schema.GroupVersionResource) (schema.GroupVersionResource, bool) {
	switch gvr {
	case relDeploymentsGVR:
		return relReplicaSetsGVR, true
	case relStatefulSetsGVR, relDaemonSetsGVR:
		return controllerRevisionsGVR, true
	}
	return schema.GroupVersionResource{}, false
}

// hasTimeline reports whether objects of gvr offer /timeline. Secrets do
// not: a recording would keep every revision of their data in memory.
func hasTimeline(gvr schema.GroupVersionResource) bool {
	return gvr != secretGVR && gvr != ovEventsGVR
}

type recordingKey struct {
	cluster   kccluster.Key
	gvr       schema.GroupVersionResource
	namespace string
	name      string
}

// historyRef is one retained ReplicaSet or ControllerRevision of the
// recorded object.
type historyRef struct {
	name     string
	owner    types.UID
	revision string
	created  time.Time
}

type recording struct {
	key recordingKey
	tl  *timeline.Timeline

	mu      sync.Mutex
	subs    map[int]func()
	nextSub int
	history map[string]historyRef
	hgvr    schema.GroupVersionResource
	lastUID types.UID
	status  string // why the recording does not run, when it does not
	stop    func()
	stopped bool
}

var recordings = struct {
	sync.Mutex
	m     map[recordingKey]*recording
	order []recordingKey // oldest first
}{m: make(map[recordingKey]*recording)}

func timelineConfig(deps Deps) appconfig.TimelineConfig {
	if deps.AppConfig != nil {
		return deps.AppConfig.Timeline.Normalized()
	}
	return appconfig.DefaultTimeline()
}

// recordingFor returns the running recording of the object, starting one
// when there is none or the last one stopped. It returns at once; the
// recording attaches to the informer in the background.
func recordingFor(deps Deps, gvr schema.GroupVersionResource, namespace, name string) *recording {
	cfg := timelineConfig(deps)
	key := recordingKey{cluster: deps.ClusterKey, gvr: gvr, namespace: namespace, name: name}
	recordings.Lock()
	if r, ok := recordings.m[key]; ok && !r.isStopped() {
		recordings.Unlock()
		return r
	}
	now := time.Now()
	id := timeline.Identity{Context: deps.ClusterKey.ContextName, GVR: gvr, Namespace: namespace, Name: name}
	r := &recording{
		key: key, tl: timeline.New(id, timeline.Limits{MaxEntries: cfg.MaxEntries, MaxBytes: cfg.MaxBytes}, now),
		subs: make(map[int]func()), history: make(map[string]historyRef), status: "starting",
	}
	r.hgvr, _ = historyGVR(gvr)
	recordings.order = slices.DeleteFunc(recordings.order, func(k recordingKey) bool { return k == key })
	recordings.m[key] = r
	recordings.order = append(recordings.order, key)
	var evict []*recording
	for len(recordings.order) > cfg.MaxObjects {
		old := recordings.m[recordings.order[0]]
		delete(recordings.m, recordings.order[0])
		recordings.order = recordings.order[1:]
		evict = append(evict, old)
	}
	recordings.Unlock()
	for _, old := range evict {
		old.tl.Gap(fmt.Sprintf("recording stopped: more than %d objects (timeline.maxObjects) were being recorded", cfg.MaxObjects), now)
		old.Stop()
	}
	go r.run(deps)
	return r
}

// RecordedRevisions reports whether kc records, or recorded, the revisions
// of the object in this session: when the recording started, and whether
// it still runs. It starts nothing.
func RecordedRevisions(cluster kccluster.Key, gvr schema.GroupVersionResource, namespace, name string) (since time.Time, running, ok bool) {
	recordings.Lock()
	r, found := recordings.m[recordingKey{cluster: cluster, gvr: gvr, namespace: namespace, name: name}]
	recordings.Unlock()
	if !found {
		return time.Time{}, false, false
	}
	return r.tl.Snapshot().Started, !r.isStopped(), true
}

func (r *recording) isStopped() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}

// subscribe calls fn after every recorded change; the returned func stops
// that.
func (r *recording) subscribe(fn func()) func() {
	r.mu.Lock()
	id := r.nextSub
	r.nextSub++
	r.subs[id] = fn
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.subs, id)
		r.mu.Unlock()
	}
}

func (r *recording) notify() {
	r.mu.Lock()
	fns := make([]func(), 0, len(r.subs))
	for _, fn := range r.subs {
		fns = append(fns, fn)
	}
	r.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

func (r *recording) setStatus(s string) {
	r.mu.Lock()
	r.status = s
	r.mu.Unlock()
	r.notify()
}

// Stop ends the recording, removes its handlers and gives up its lease.
// The recorded entries stay readable.
func (r *recording) Stop() {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	stop := r.stop
	r.stop = nil
	r.mu.Unlock()
	r.tl.Stop(time.Now())
	if stop != nil {
		stop()
	}
	r.notify()
}

// run attaches the recording to the shared informers. It holds its own
// lease on the object's cluster, so leaving the folders that opened the
// cluster does not stop the informer.
func (r *recording) run(deps Deps) {
	log := ctrllog.FromContext(deps.Ctx).WithName("timeline").WithValues("gvr", r.key.gvr.String(), "namespace", r.key.namespace, "name", r.key.name)
	release := noRelease
	if deps.NamespaceFactory != nil {
		cl, rel, err := deps.NamespaceFactory(deps.Ctx, deps.ClusterKey, nil)
		if err != nil {
			log.Info("recording failed", "reason", "cannot hold the cluster: "+err.Error())
			r.fail("cannot hold the cluster: " + err.Error())
			return
		}
		deps.Cl, release = cl, rel
	}
	if deps.Cl == nil {
		release()
		log.Info("recording failed", "reason", "no cluster connection")
		r.fail("no cluster connection")
		return
	}
	var removes []func()
	done := make(chan struct{})
	var doneOnce sync.Once
	stopAll := func() {
		doneOnce.Do(func() { close(done) })
		for _, rm := range removes {
			rm()
		}
		release()
	}
	rm, informer, err := addObjectHandler(deps, r.key.gvr, r.handler())
	if err != nil {
		stopAll()
		log.Info("recording failed", "reason", "cannot watch "+r.key.gvr.Resource+": "+err.Error())
		r.fail("cannot watch " + r.key.gvr.Resource + ": " + err.Error())
		return
	}
	removes = append(removes, rm)
	if !r.hgvr.Empty() {
		// Retained template revisions are evidence from before the
		// recording; a failure to read them leaves the recording running.
		if rm, _, err := addObjectHandler(deps, r.hgvr, r.historyHandler()); err == nil {
			removes = append(removes, rm)
		} else {
			log.V(1).Info("history unavailable", "history", r.hgvr.String(), "error", err.Error())
		}
	}
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		stopAll()
		return
	}
	r.stop = stopAll
	r.status = ""
	r.mu.Unlock()
	log.V(1).Info("recording started")
	r.notify()
	go r.watchInformer(deps.Ctx, informer, done)
}

// informerCheckEvery is how often a recording checks that its informer
// still runs.
const informerCheckEvery = 5 * time.Second

// watchInformer ends the recording with a gap when the shared informer
// stops (its cluster was closed) or kc shuts down: from then on nothing
// would be observed, and a silent timeline would look like a quiet object.
func (r *recording) watchInformer(ctx context.Context, informer crcache.Informer, done <-chan struct{}) {
	stoppable, _ := informer.(interface{ IsStopped() bool })
	tick := time.NewTicker(informerCheckEvery)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			r.endWithGap("kc stopped watching (the session ended)")
			return
		case <-tick.C:
			if stoppable != nil && stoppable.IsStopped() {
				r.endWithGap("the shared cache stopped watching this type; later changes are not recorded")
				return
			}
		}
	}
}

// endWithGap records why observation ended and stops the recording.
func (r *recording) endWithGap(reason string) {
	if r.isStopped() {
		return
	}
	r.tl.Gap(reason, time.Now())
	r.Stop()
}

func (r *recording) fail(reason string) {
	r.tl.Gap("recording not running: "+reason, time.Now())
	r.mu.Lock()
	r.stopped = true
	r.mu.Unlock()
	r.tl.Stop(time.Now())
	r.setStatus(reason)
}

// addObjectHandler adds h to the shared informer of gvr without waiting for
// it to sync, and returns the func that removes it.
func addObjectHandler(deps Deps, gvr schema.GroupVersionResource, h toolscache.ResourceEventHandler) (func(), crcache.Informer, error) {
	ctx, cancel := context.WithTimeout(deps.Ctx, informerStartTimeout)
	defer cancel()
	mapper := deps.Cl.RESTMapper()
	if mapper == nil {
		return nil, nil, fmt.Errorf("rest mapper unavailable")
	}
	gvk, err := mapper.KindFor(gvr)
	if err != nil {
		return nil, nil, err
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	informer, err := deps.Cl.GetCache().GetInformer(ctx, obj, crcache.BlockUntilSynced(false))
	if err != nil {
		return nil, nil, err
	}
	reg, err := informer.AddEventHandler(h)
	if err != nil {
		return nil, nil, err
	}
	return func() { _ = informer.RemoveEventHandler(reg) }, informer, nil
}

func (r *recording) matches(obj any) (*unstructured.Unstructured, bool) {
	u, ok := obj.(*unstructured.Unstructured)
	return u, ok && u.GetNamespace() == r.key.namespace && u.GetName() == r.key.name
}

// handler records the object's notifications. The informer delivers the
// current object as an add when the handler is added, which becomes the
// initial revision.
func (r *recording) handler() toolscache.ResourceEventHandler {
	observe := func(obj any) {
		u, ok := r.matches(obj)
		if !ok {
			return
		}
		r.mu.Lock()
		r.lastUID = u.GetUID()
		r.mu.Unlock()
		if r.tl.Observe(u.Object, time.Now()) {
			r.notify()
		}
	}
	return toolscache.ResourceEventHandlerFuncs{
		AddFunc:    observe,
		UpdateFunc: func(_, obj any) { observe(obj) },
		DeleteFunc: func(obj any) {
			if tomb, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
				if _, ok := r.matches(tomb.Obj); ok {
					r.tl.Deleted(nil, time.Now())
					r.notify()
				}
				return
			}
			if u, ok := r.matches(obj); ok {
				r.tl.Deleted(u.Object, time.Now())
				r.notify()
			}
		},
	}
}

// historyHandler keeps the retained revisions the recorded object
// controls, by owner UID.
func (r *recording) historyHandler() toolscache.ResourceEventHandler {
	put := func(obj any) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok || u.GetNamespace() != r.key.namespace {
			return
		}
		var owner types.UID
		for _, ref := range u.GetOwnerReferences() {
			if ref.Controller != nil && *ref.Controller && ref.Name == r.key.name {
				owner = ref.UID
			}
		}
		if owner == "" {
			return
		}
		ref := historyRef{name: u.GetName(), owner: owner, created: u.GetCreationTimestamp().Time}
		ref.revision = u.GetAnnotations()["deployment.kubernetes.io/revision"]
		if n, ok, _ := unstructured.NestedInt64(u.Object, "revision"); ok {
			ref.revision = strconv.FormatInt(n, 10)
		}
		r.mu.Lock()
		r.history[ref.name] = ref
		r.mu.Unlock()
		r.notify()
	}
	return toolscache.ResourceEventHandlerFuncs{
		AddFunc:    put,
		UpdateFunc: func(_, obj any) { put(obj) },
		DeleteFunc: func(obj any) {
			if tomb, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
				obj = tomb.Obj
			}
			u, ok := obj.(*unstructured.Unstructured)
			if !ok || u.GetNamespace() != r.key.namespace {
				return
			}
			r.mu.Lock()
			delete(r.history, u.GetName())
			r.mu.Unlock()
			r.notify()
		},
	}
}

// historyRefs returns the retained revisions of the current lifetime,
// newest first.
func (r *recording) historyRefs() []historyRef {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]historyRef, 0, len(r.history))
	for _, h := range r.history {
		if h.owner == r.lastUID {
			out = append(out, h)
		}
	}
	slices.SortFunc(out, func(a, b historyRef) int {
		if c := b.created.Compare(a.created); c != 0 {
			return c
		}
		return cmp.Compare(a.name, b.name)
	})
	return out
}

func (r *recording) state() (status string, stopped bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status, r.stopped
}
