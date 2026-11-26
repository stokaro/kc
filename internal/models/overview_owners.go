package models

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	toolscache "k8s.io/client-go/tools/cache"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// The overview follows the controller chain of a pod through any namespaced
// kind, not only the built-in controllers of overviewKinds (ADR 0364). An
// owner kind outside them gets an informer in the overview's own caches
// once a walk meets it: metadata only, or the whole object when the kind's
// CRD declares where its desired replicas are. A cluster with hundreds of
// CRDs gets informers only for the kinds that own pods in scope.

// scaleSpecMaxAge is how long a learned scale path serves before a
// discovery refresh has it read again.
const scaleSpecMaxAge = 5 * time.Minute

var crdGVR = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}

// builtinScalePaths are the built-in kinds outside overviewKinds with a
// scale subresource: they have no CRD that says where it reads.
var builtinScalePaths = map[schema.GroupResource][]string{
	{Resource: "replicationcontrollers"}: {"spec", "replicas"},
}

// ownerKind is an owner kind outside overviewKinds as the engine resolved it.
type ownerKind struct {
	gk  schema.GroupKind
	gvr schema.GroupVersionResource
	// namespaced is false for a cluster-scoped kind (a Node is the
	// controller of its mirror pods), and for a kind that does not resolve:
	// such an owner ends the walk and is no workload.
	namespaced bool
	// hasScale is true when discovery lists the kind's scale subresource.
	// scalePath is where its objects keep the desired replicas (the CRD's
	// specReplicasPath, split at the dots); empty when kc could not learn
	// it, and scaleNote then says why.
	hasScale  bool
	scalePath []string
	scaleNote string
}

// ownerObject is one object of an ownerKind as one recompute read it.
type ownerObject struct {
	meta *metav1.ObjectMeta
	// desired is the value at the kind's scale path; desiredNote says why
	// there is none when the kind has a scale path.
	desired     int64
	hasDesired  bool
	desiredNote string
}

// ownerInput is what one recompute read of the owner kinds outside
// overviewKinds. A kind whose objects cannot be read has its reason in
// overviewInput.missing instead of objects.
type ownerInput struct {
	kinds   map[schema.GroupKind]ownerKind
	objects map[schema.GroupVersionResource][]ownerObject
}

// kindOf returns the resolved kind of gvr.
func (o ownerInput) kindOf(gvr schema.GroupVersionResource) (ownerKind, bool) {
	for _, k := range o.kinds {
		if k.namespaced && k.gvr == gvr {
			return k, true
		}
	}
	return ownerKind{}, false
}

// ownerCacheConn is an overviewConn that can serve owner kinds outside
// overviewKinds: it returns its cache of scope; ok is false when it has
// none.
type ownerCacheConn interface {
	ownerCache(scope listScope) (cache crcache.Cache, ok bool)
}

// ownerCache returns the one cache a connection without access reviews
// reads every kind from.
func (c *clusterOverviewConn) ownerCache(listScope) (crcache.Cache, bool) {
	return c.cl.GetCache(), true
}

// ownerCache returns the cache of scope that the connection acquired. The
// chain of a pod stays in its namespace, so an overview of one namespace
// may read an owner kind in the cache limited to it.
func (c *scopedOverviewConn) ownerCache(scope listScope) (crcache.Cache, bool) {
	var shared, local *clusterOverviewConn
	for _, p := range c.parts {
		if len(p.kinds) == 0 {
			continue
		}
		switch c.scopes[p.kinds[0].gvr] {
		case scopeShared:
			shared = p
		case scopeNamespace:
			local = p
		}
	}
	switch {
	case scope == scopeShared && shared != nil:
		return shared.cl.GetCache(), true
	case scope != scopeDenied && local != nil && c.namespace != AllNamespaces:
		return local.cl.GetCache(), true
	}
	return nil, false
}

// ownerWatch resolves the owner kinds the walks of one engine meet, starts
// their informers and reads their objects for each recompute. The engine's
// goroutine calls start, read and learn; want comes from the UI goroutine.
type ownerWatch struct {
	deps      Deps
	namespace string
	now       func() time.Time

	mu    sync.Mutex
	kinds map[schema.GroupKind]*watchedOwner
	// wanted are the GVRs Info or the dashboard focused, with the
	// discovery generation they were looked at in (-1: not yet). A kind
	// with a scale subresource is read for them although no walk met it,
	// so an object scaled to 0 shows its desired count.
	wanted map[schema.GroupVersionResource]int
	// gen counts discovery refreshes. A kind that did not resolve is tried
	// again, and old scale paths are read again, after one.
	gen       int
	learntGen int

	conn    overviewConn
	onEvent func()
}

// watchedOwner is one kind of an ownerWatch.
type watchedOwner struct {
	ownerKind
	tried  int    // gen of the last attempt to resolve it
	reason string // why its objects are not read
	inf    crcache.Informer
	reader crclient.Reader
	full   bool // the informer holds whole objects, not metadata
	at     time.Time
	stop   func() // removes the event handler
}

func newOwnerWatch(deps Deps, namespace string) *ownerWatch {
	if deps.Cl == nil {
		return nil
	}
	return &ownerWatch{
		deps: deps, namespace: namespace, now: time.Now,
		kinds:     make(map[schema.GroupKind]*watchedOwner),
		wanted:    make(map[schema.GroupVersionResource]int),
		learntGen: -1,
	}
}

// start records the connection and the event callback of a running engine
// and follows discovery refreshes. stop removes the event handlers; the
// informers stay in the caches until the pool stops them.
func (w *ownerWatch) start(conn overviewConn, onEvent func()) (stop func()) {
	w.mu.Lock()
	w.conn, w.onEvent = conn, onEvent
	w.mu.Unlock()
	stopDisco := w.deps.Cl.AddDiscoveryListener(func() {
		w.mu.Lock()
		w.gen++
		w.mu.Unlock()
		onEvent()
	})
	return func() {
		stopDisco()
		w.mu.Lock()
		defer w.mu.Unlock()
		for gk, k := range w.kinds {
			if k.stop != nil {
				k.stop()
			}
			// A new run resolves and watches again; the informers of the
			// cache are still there.
			delete(w.kinds, gk)
		}
		w.conn, w.onEvent = nil, nil
	}
}

// want asks for the objects of gvr: Info or the dashboard focus an object
// of it. Only a kind with a scale subresource is read for that. It reports
// whether gvr is new, so the engine computes again.
func (w *ownerWatch) want(gvr schema.GroupVersionResource) bool {
	if w == nil || IsInfoWorkload(gvr) {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.wanted[gvr]; ok {
		return false
	}
	w.wanted[gvr] = -1
	return true
}

// watched returns the GVRs that have an informer; tests count them.
func (w *ownerWatch) watched() []schema.GroupVersionResource {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []schema.GroupVersionResource
	for _, k := range w.kinds {
		if k.inf != nil {
			out = append(out, k.gvr)
		}
	}
	return out
}

// read lists the objects of every watched kind into in. It reports whether
// a kind is still syncing.
func (w *ownerWatch) read(ctx context.Context, in *overviewInput) bool {
	w.mu.Lock()
	kinds := make([]watchedOwner, 0, len(w.kinds))
	for _, k := range w.kinds {
		kinds = append(kinds, *k)
	}
	w.mu.Unlock()
	in.owners = ownerInput{kinds: make(map[schema.GroupKind]ownerKind, len(kinds)), objects: make(map[schema.GroupVersionResource][]ownerObject)}
	pending := false
	for i := range kinds {
		k := &kinds[i]
		in.owners.kinds[k.gk] = k.ownerKind
		if !k.namespaced {
			continue
		}
		switch {
		case k.reason != "":
			in.missing[k.gvr] = k.reason
			continue
		case k.inf == nil:
			in.missing[k.gvr] = "not watched"
			continue
		case !k.inf.HasSynced() && w.now().Sub(k.at) < overviewSyncGrace:
			in.missing[k.gvr], pending = "loading", true
			continue
		case !k.inf.HasSynced():
			in.missing[k.gvr] = "the cache did not sync (no list or watch permission?)"
			continue
		}
		objs, err := k.list(ctx, w.namespace)
		if err != nil {
			in.missing[k.gvr] = err.Error()
			continue
		}
		in.owners.objects[k.gvr] = objs
	}
	return pending
}

// list reads the objects of k in namespace from its cache.
func (k *watchedOwner) list(ctx context.Context, namespace string) ([]ownerObject, error) {
	gvk := k.gvr.GroupVersion().WithKind(k.gk.Kind)
	opts := []crclient.ListOption{crclient.UnsafeDisableDeepCopy}
	if namespace != AllNamespaces {
		opts = append(opts, crclient.InNamespace(namespace))
	}
	if !k.full {
		l := &metav1.PartialObjectMetadataList{}
		l.SetGroupVersionKind(listGVK(gvk))
		if err := k.reader.List(ctx, l, opts...); err != nil {
			return nil, err
		}
		out := make([]ownerObject, len(l.Items))
		for i := range l.Items {
			out[i] = ownerObject{meta: &l.Items[i].ObjectMeta}
		}
		return out, nil
	}
	l := &unstructured.UnstructuredList{}
	l.SetGroupVersionKind(listGVK(gvk))
	if err := k.reader.List(ctx, l, opts...); err != nil {
		return nil, err
	}
	out := make([]ownerObject, len(l.Items))
	for i := range l.Items {
		u := &l.Items[i]
		o := ownerObject{meta: objectMetaOf(u)}
		if len(k.scalePath) > 0 {
			o.desired, o.hasDesired, o.desiredNote = desiredAt(u.Object, k.scalePath)
		}
		out[i] = o
	}
	return out, nil
}

// objectMetaOf is the metadata of u the walk and the summaries need.
func objectMetaOf(u *unstructured.Unstructured) *metav1.ObjectMeta {
	return &metav1.ObjectMeta{
		Namespace: u.GetNamespace(), Name: u.GetName(), UID: u.GetUID(),
		CreationTimestamp: u.GetCreationTimestamp(), OwnerReferences: u.GetOwnerReferences(),
	}
}

// desiredAt reads the desired replicas at path of obj.
func desiredAt(obj map[string]any, path []string) (int64, bool, string) {
	field := "." + strings.Join(path, ".")
	v, found, err := unstructured.NestedFieldNoCopy(obj, path...)
	if err != nil || !found || v == nil {
		return 0, false, field + " is not set"
	}
	switch n := v.(type) {
	case int64:
		return n, true, ""
	case int32:
		return int64(n), true, ""
	case int:
		return int64(n), true, ""
	case float64:
		if n == float64(int64(n)) {
			return int64(n), true, ""
		}
	}
	return 0, false, fmt.Sprintf("%s is not a whole number (%v)", field, v)
}

// learn resolves the owner kinds the walks met that are not known yet,
// the ones that did not resolve before a discovery refresh, and the wanted
// kinds, and starts their informers. It reports whether it started one, so
// the next tick computes again.
func (w *ownerWatch) learn(ctx context.Context, met map[schema.GroupKind]bool) bool {
	log := ctrllog.FromContext(ctx).WithName("overview").WithValues("namespace", w.namespace)
	w.mu.Lock()
	gen, fresh := w.gen, w.learntGen != w.gen
	w.learntGen = gen
	var todo []schema.GroupKind
	for gk := range met {
		if _, known := w.kinds[gk]; !known {
			todo = append(todo, gk)
		}
	}
	var retry []schema.GroupKind
	for gk, k := range w.kinds {
		if k.reason != "" && k.inf == nil && k.tried < gen {
			retry = append(retry, gk)
		}
	}
	var wanted []schema.GroupVersionResource
	for gvr, tried := range w.wanted {
		if tried < gen {
			wanted = append(wanted, gvr)
			w.wanted[gvr] = gen
		}
	}
	w.mu.Unlock()

	if fresh {
		w.learnScalable(ctx)
	}
	changed := false
	for _, gk := range append(todo, retry...) {
		w.resolve(ctx, gk, gen, false)
		changed = true
	}
	for _, gvr := range wanted {
		if w.resolveWanted(ctx, gvr, gen) {
			changed = true
		}
	}
	if fresh && gen > 0 && w.refreshScale(ctx) {
		changed = true
	}
	if changed {
		log.V(1).Info("owner kinds", "watched", len(w.watched()))
	}
	return changed
}

// resolveWanted resolves the kind of a wanted GVR, once per discovery
// generation: a kind with a scale subresource is read, any other kind is
// not kept (a walk that meets it adds it). It reports whether it resolved
// the kind.
func (w *ownerWatch) resolveWanted(ctx context.Context, gvr schema.GroupVersionResource, gen int) bool {
	w.mu.Lock()
	for _, k := range w.kinds {
		if k.gvr == gvr {
			w.mu.Unlock()
			return false
		}
	}
	w.mu.Unlock()
	ctrllog.FromContext(ctx).WithName("overview").Info("resolving the kind of a focused workload", "gvr", gvr.String())
	gvk, err := w.deps.Cl.RESTMapper().KindFor(gvr)
	if err != nil {
		return false
	}
	w.mu.Lock()
	_, known := w.kinds[gvk.GroupKind()]
	w.mu.Unlock()
	if known {
		return false
	}
	w.resolve(ctx, gvk.GroupKind(), gen, true)
	return true
}

// resolve maps gk to its preferred GVR, learns its scale path and starts
// its informer. With scaleOnly a kind without a scale subresource is not
// kept.
func (w *ownerWatch) resolve(ctx context.Context, gk schema.GroupKind, gen int, scaleOnly bool) {
	log := ctrllog.FromContext(ctx).WithName("overview").WithValues("namespace", w.namespace, "groupKind", gk.String())
	k := &watchedOwner{gk: gk, tried: gen}
	log.Info("resolving owner kind")
	m, err := w.deps.Cl.RESTMapper().RESTMapping(gk)
	switch {
	case meta.IsNoMatchError(err):
		k.reason = notServedReason
	case err != nil:
		k.reason = err.Error()
	default:
		k.gvr = m.Resource
		k.namespaced = m.Scope.Name() == meta.RESTScopeNameNamespace
	}
	if k.namespaced {
		w.learnScale(ctx, &k.ownerKind)
		if scaleOnly && !k.hasScale {
			return
		}
		w.startInformer(ctx, k)
	}
	if k.reason != "" {
		log.Info("owner kind not read", "reason", k.reason)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if old := w.kinds[gk]; old != nil && old.stop != nil {
		old.stop()
	}
	w.kinds[gk] = k
}

// startInformer starts the informer of k in the cache that may read it:
// whole objects when k has a scale path, metadata otherwise.
func (w *ownerWatch) startInformer(ctx context.Context, k *watchedOwner) {
	w.mu.Lock()
	conn, onEvent := w.conn, w.onEvent
	w.mu.Unlock()
	oc, ok := conn.(ownerCacheConn)
	if !ok || onEvent == nil {
		k.reason = "not watched"
		return
	}
	scope := scopeShared
	if w.deps.NamespaceFactory != nil {
		scope = ownerScope(ctx, w.deps.Cl, k.gvr, w.namespace)
	}
	cache, ok := oc.ownerCache(scope)
	switch {
	case scope == scopeDenied:
		k.reason = overviewDeniedReason(overviewKind{gvr: k.gvr, namespaced: true}, w.namespace)
		return
	case !ok:
		// Readable in the namespace only, while the overview reads every
		// kind of its own in all namespaces: it has no cache for one.
		k.reason = overviewOnlyIn + w.namespace + "; the overview has no cache limited to it"
		return
	}
	gvk := k.gvr.GroupVersion().WithKind(k.gk.Kind)
	var obj crclient.Object
	if len(k.scalePath) > 0 {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		obj, k.full = u, true
	} else {
		m := &metav1.PartialObjectMetadata{}
		m.SetGroupVersionKind(gvk)
		obj = m
	}
	getCtx, cancel := context.WithTimeout(ctx, informerStartTimeout)
	inf, err := cache.GetInformer(getCtx, obj, crcache.BlockUntilSynced(false))
	cancel()
	if err != nil {
		k.reason = err.Error()
		if meta.IsNoMatchError(err) {
			k.reason = notServedReason
		}
		return
	}
	reg, err := inf.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { onEvent() },
		UpdateFunc: func(any, any) { onEvent() },
		DeleteFunc: func(any) { onEvent() },
	})
	if err != nil {
		k.reason = err.Error()
		return
	}
	ctrllog.FromContext(ctx).WithName("overview").Info("watching owner kind", "gvr", k.gvr.String(), "objects", map[bool]string{true: "whole", false: "metadata"}[k.full], "scope", scope.String())
	k.inf, k.reader, k.at = inf, cache, w.now()
	k.stop = func() { _ = inf.RemoveEventHandler(reg) }
}

// ownerScope decides the cache of an owner kind as overviewScopes decides
// it for the overview's own kinds.
func ownerScope(ctx context.Context, a overviewAccess, gvr schema.GroupVersionResource, namespace string) listScope {
	rctx, cancel := context.WithTimeout(ctx, listScopeTimeout)
	defer cancel()
	ok, err := a.CanListWatchAllNamespaces(rctx, gvr)
	all, inNamespace := accessOf(ok, err), accessUnknown
	if all == accessDenied && namespace != AllNamespaces {
		ok, err := a.CanListWatchInNamespace(rctx, gvr, namespace)
		inNamespace = accessOf(ok, err)
	}
	return chooseOverviewScope(namespace, true, all, inNamespace)
}

// refreshScale reads the scale paths of the watched kinds again once they
// are older than scaleSpecMaxAge, and swaps a metadata informer for a whole
// one (or back) when the kind gained or lost its scale path. It reports
// whether it changed a kind.
func (w *ownerWatch) refreshScale(ctx context.Context) bool {
	w.mu.Lock()
	var due []*watchedOwner
	for _, k := range w.kinds {
		if k.namespaced && k.inf != nil {
			cp := *k
			due = append(due, &cp)
		}
	}
	w.mu.Unlock()
	changed := false
	for _, k := range due {
		before := k.ownerKind
		w.learnScale(ctx, &k.ownerKind)
		if (len(before.scalePath) > 0) == (len(k.scalePath) > 0) {
			w.mu.Lock()
			if cur := w.kinds[k.gk]; cur != nil {
				cur.ownerKind = k.ownerKind
			}
			w.mu.Unlock()
			continue
		}
		next := &watchedOwner{ownerKind: k.ownerKind, tried: k.tried}
		w.startInformer(ctx, next)
		w.mu.Lock()
		if cur := w.kinds[k.gk]; cur != nil && cur.stop != nil {
			cur.stop()
		}
		w.kinds[k.gk] = next
		w.mu.Unlock()
		changed = true
	}
	return changed
}

// learnScale sets whether k has a scale subresource and where it reads the
// desired replicas, from the cluster's cache of scale specs when that is
// not older than scaleSpecMaxAge.
func (w *ownerWatch) learnScale(ctx context.Context, k *ownerKind) {
	key := w.deps.ClusterKey
	if s, ok := learnedWorkloads.scaleSpec(key, k.gvr, w.now(), scaleSpecMaxAge); ok {
		k.hasScale, k.scalePath, k.scaleNote = s.hasScale, s.path, s.note
		return
	}
	s := w.readScaleSpec(ctx, k.gvr)
	s.at = w.now()
	learnedWorkloads.recordScaleSpec(key, k.gvr, s)
	k.hasScale, k.scalePath, k.scaleNote = s.hasScale, s.path, s.note
}

// readScaleSpec asks discovery whether gvr has a scale subresource and,
// when it has, reads where from the CRD.
func (w *ownerWatch) readScaleSpec(ctx context.Context, gvr schema.GroupVersionResource) scaleSpec {
	log := ctrllog.FromContext(ctx).WithName("overview")
	gv := gvr.GroupVersion().String()
	log.Info("discovery get", "groupVersion", gv, "for", "scale subresource of "+gvr.Resource)
	list, err := w.deps.Cl.DiscoveryClient().ServerResourcesForGroupVersion(gv)
	if err != nil {
		return scaleSpec{note: "discovery of " + gv + ": " + err.Error()}
	}
	has := false
	for _, r := range list.APIResources {
		has = has || r.Name == gvr.Resource+"/scale"
	}
	if !has {
		return scaleSpec{note: kubectlResource(gvr) + " has no scale subresource"}
	}
	if p, ok := builtinScalePaths[gvr.GroupResource()]; ok {
		return scaleSpec{hasScale: true, path: p}
	}
	name := gvr.Resource + "." + gvr.Group
	log.Info("client get", "gvr", crdGVR.String(), "name", name, "for", "specReplicasPath")
	crd, err := w.deps.Cl.Dynamic().Resource(crdGVR).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return scaleSpec{hasScale: true, note: "no CustomResourceDefinition " + name + " says where its scale reads"}
	case err != nil:
		return scaleSpec{hasScale: true, note: kubectlResource(crdGVR) + " " + name + ": " + readProblem(err)}
	}
	return scaleSpecOf(crd, gvr)
}

// scaleSpecOf reads the specReplicasPath of gvr's version from crd.
func scaleSpecOf(crd *unstructured.Unstructured, gvr schema.GroupVersionResource) scaleSpec {
	versions, _, _ := unstructured.NestedSlice(crd.Object, "spec", "versions")
	for _, v := range versions {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if name, _, _ := unstructured.NestedString(m, "name"); name != gvr.Version {
			continue
		}
		p, found, _ := unstructured.NestedString(m, "subresources", "scale", "specReplicasPath")
		if !found || strings.TrimPrefix(p, ".") == "" {
			return scaleSpec{note: "version " + gvr.Version + " of " + crd.GetName() + " declares no scale subresource"}
		}
		return scaleSpec{hasScale: true, path: strings.Split(strings.TrimPrefix(p, "."), ".")}
	}
	return scaleSpec{hasScale: true, note: crd.GetName() + " has no version " + gvr.Version}
}

// learnScalable records the namespaced kinds whose scale subresource
// discovery lists, in every served version, so Info and the dashboard
// treat an object of such a kind as a workload before a walk met one of
// its pods: a Rollout scaled to 0. The preferred resources leave the
// subresources out, so it reads them all.
func (w *ownerWatch) learnScalable(ctx context.Context) {
	ctrllog.FromContext(ctx).WithName("overview").Info("discovery get", "resources", "all", "for", "scale subresources")
	_, lists, err := w.deps.Cl.DiscoveryClient().ServerGroupsAndResources()
	if err != nil && len(lists) == 0 {
		return
	}
	out := make(map[schema.GroupVersionResource]string)
	for _, l := range lists {
		gv, err := schema.ParseGroupVersion(l.GroupVersion)
		if err != nil {
			continue
		}
		kinds := make(map[string]string, len(l.APIResources))
		for _, r := range l.APIResources {
			if r.Namespaced && !strings.Contains(r.Name, "/") {
				kinds[r.Name] = r.Kind
			}
		}
		for _, r := range l.APIResources {
			parent, ok := strings.CutSuffix(r.Name, "/scale")
			if kind, namespaced := kinds[parent]; ok && namespaced {
				out[gv.WithResource(parent)] = kind
			}
		}
	}
	learnedWorkloads.recordScalable(w.deps.ClusterKey, out)
}
