package models

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"charm.land/lipgloss/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	toolscache "k8s.io/client-go/tools/cache"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	crlog "sigs.k8s.io/controller-runtime/pkg/log"
)

// ResourceGroupItem opens the object list for a specific resource and exposes aggregated counts.
type ResourceGroupItem struct {
	*RowItem
	enter     func() (Folder, error)
	deps      Deps
	gvr       schema.GroupVersionResource
	namespace string
	watchable bool
	gvk       schema.GroupVersionKind
	gvkKnown  bool

	mu         sync.Mutex
	count      int
	countKnown bool
	empty      bool
	emptyKnown bool
	// decided and decidedEmpty keep the last known emptiness across recounts.
	decided      bool
	decidedEmpty bool
	countOnce    sync.Once
	lastPeek     time.Time
	lastError    time.Time
	onChange     func()

	publishedCount      int
	publishedCountKnown bool
	publishedEmpty      bool
	publishedEmptyKnown bool
	recounting          bool // true while an async count recomputation is in flight
	recountPending      bool // true when an event arrives during recounting; triggers another recount afterwards
	watchOnce           sync.Once
	nextPeekScheduled   bool
	peekBackoff         wait.Backoff
	peekBlocked         bool
	peekForbiddenLogged bool
	hasAny              func(context.Context, schema.GroupVersionResource, string) (bool, error)
	peeking             bool // true while a background emptiness peek runs
	// synced is true once the count comes from a synced informer. The
	// informer then keeps count and emptiness current, so no remote peeks
	// are needed.
	synced bool

	// Lifecycle: a closed item stops its informer handler and peek timers.
	// Folders close their items when they leave navigation; without that,
	// every discarded folder kept polling the apiserver for the session.
	closed     bool
	stop       chan struct{}
	informer   crcache.Informer
	handlerReg toolscache.ResourceEventHandlerRegistration
}

// Close stops background work for the item. It is safe to call twice.
func (r *ResourceGroupItem) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	close(r.stop)
	informer, reg := r.informer, r.handlerReg
	r.informer, r.handlerReg = nil, nil
	r.mu.Unlock()
	r.deps.Activity.forget(r)
	if informer != nil && reg != nil {
		_ = informer.RemoveEventHandler(reg)
	}
}

func (r *ResourceGroupItem) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

func NewResourceGroupItem(deps Deps, gvr schema.GroupVersionResource, gvk schema.GroupVersionKind, namespace, id string, cells []string, path []string, detail string, style *lipgloss.Style, watchable bool, enter func() (Folder, error)) *ResourceGroupItem {
	row := NewRowItem(id, cells, path, style)
	row.details = detail
	gvkKnown := gvk.Kind != ""
	return &ResourceGroupItem{
		RowItem:   row,
		enter:     enter,
		deps:      deps,
		gvr:       gvr,
		gvk:       gvk,
		gvkKnown:  gvkKnown,
		namespace: namespace,
		watchable: watchable,
		stop:      make(chan struct{}),
	}
}

func (r *ResourceGroupItem) Enter() (Folder, error) {
	if r.enter == nil {
		return nil, nil
	}
	return r.enter()
}

// ComputeCountAsync triggers Count() on a background goroutine and invokes the
// provided callback once the count is known.
func (r *ResourceGroupItem) ComputeCountAsync(onUpdate func()) {
	if !r.isWatchable() {
		return
	}
	r.countOnce.Do(func() {
		go func() {
			_ = r.Count()
			r.notifyIfChanged(onUpdate)
		}()
	})
}

// informerSyncTimeout bounds how long a count waits for an informer to sync.
// An informer that cannot list (no permission, broken API) never syncs, so
// waiting on the long-lived context would block forever.
const informerSyncTimeout = 30 * time.Second

func (r *ResourceGroupItem) Count() int {
	if !r.isWatchable() {
		return 0
	}
	r.mu.Lock()
	if r.peekBlocked {
		r.mu.Unlock()
		return 0
	}
	if r.countKnown {
		count := r.count
		r.mu.Unlock()
		return count
	}
	r.mu.Unlock()

	logger := crlog.FromContext(r.deps.Ctx)
	logger.Info("ensuring informer for resource count", "gvr", r.gvr.String(), "namespace", r.namespace)
	// Wait for the informer without holding r.mu: the UI thread takes r.mu
	// to read cached state and must never wait for the network.
	informer, status := r.syncedInformer()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.countKnown {
		return r.count
	}
	var (
		count int
		ok    bool
	)
	switch status {
	case informerUnsupported:
		r.watchable = false
		count, ok = 0, true
	case informerSynced:
		count, ok = r.countFromInformerLocked(informer)
	}
	if ok {
		r.count = count
		r.countKnown = true
		r.empty = count == 0
		r.emptyKnown = true
		r.lastPeek = time.Now()
		r.synced = status == informerSynced
		return r.count
	}
	return 0
}

func (r *ResourceGroupItem) Empty() bool {
	cfg := r.deps.AppConfig
	return r.emptyWithin(cfg.Resources.PeekInterval.Duration)
}

func (r *ResourceGroupItem) emptyWithin(interval time.Duration) bool {
	if !r.isWatchable() {
		return true
	}
	r.mu.Lock()
	if r.synced && r.emptyKnown {
		val := r.empty
		r.mu.Unlock()
		return val
	}
	if r.emptyKnown && !r.lastPeek.IsZero() && time.Since(r.lastPeek) < interval {
		val := r.empty
		r.mu.Unlock()
		return val
	}
	if time.Since(r.lastError) < interval {
		// Back off when the last peek errored; skip hitting the API until interval elapses.
		r.mu.Unlock()
		return r.empty
	}
	hasAny := r.hasAny
	r.mu.Unlock()
	res := r.peekRemote(hasAny)
	r.mu.Lock()
	empty, ok := r.applyPeekLocked(res)
	prevEmpty := r.emptyKnown && r.empty
	r.lastPeek = time.Now()
	if ok {
		r.empty = empty
		r.emptyKnown = true
		needRecount := false
		if empty {
			r.count = 0
			r.countKnown = true
		} else if prevEmpty {
			r.countKnown = false
			r.empty = false
			r.emptyKnown = true
			needRecount = true
		}
		changed := r.recordPublishedLocked()
		onChange := r.onChange
		val := r.empty
		r.mu.Unlock()
		if changed && onChange != nil {
			onChange()
		}
		if needRecount {
			go r.scheduleRecount()
		}
		return val
	}
	val := r.empty
	r.mu.Unlock()
	return val
}

func (r *ResourceGroupItem) TryCount() (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.countKnown {
		return 0, false
	}
	return r.count, true
}

type informerStatus int

const (
	informerFailed informerStatus = iota
	informerSynced
	informerUnsupported
)

// syncedInformer returns the informer for the resource once it has synced,
// waiting at most informerSyncTimeout. It must be called without r.mu held.
func (r *ResourceGroupItem) syncedInformer() (crcache.Informer, informerStatus) {
	r.mu.Lock()
	if !r.gvkKnown {
		gvk, err := r.deps.Cl.RESTMapper().KindFor(r.gvr)
		if err != nil {
			r.mu.Unlock()
			crlog.FromContext(r.deps.Ctx).Info("mapper KindFor failed", "gvr", r.gvr.String(), "namespace", r.namespace, "error", err)
			return nil, informerFailed
		}
		r.gvk = gvk
		r.gvkKnown = true
	}
	gvk := r.gvk
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(r.deps.Ctx, informerSyncTimeout)
	defer cancel()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	informer, err := r.deps.Cl.GetCache().GetInformer(ctx, obj, crcache.BlockUntilSynced(false))
	if err != nil {
		if apierrors.IsMethodNotSupported(err) {
			crlog.FromContext(r.deps.Ctx).Info("resource watch not supported; skipping informer", "gvr", r.gvr.String(), "namespace", r.namespace)
			return nil, informerUnsupported
		}
		r.deps.reportReadError(r.what(), err)
		return nil, informerFailed
	}
	r.ensureInformerHandler(informer)
	if !informer.HasSynced() && !toolscache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		// The informer keeps retrying; its first sync fires an event that
		// triggers a recount.
		r.deps.reportReadError(r.what(), ctx.Err())
		return nil, informerFailed
	}
	return informer, informerSynced
}

func (r *ResourceGroupItem) what() string {
	if r.namespace == "" {
		return r.gvr.Resource
	}
	return r.gvr.Resource + " in " + r.namespace
}

func (r *ResourceGroupItem) countFromInformerLocked(informer crcache.Informer) (int, bool) {
	type storeInformer interface{ GetStore() toolscache.Store }
	type indexerInformer interface{ GetIndexer() toolscache.Indexer }
	switch inf := informer.(type) {
	case storeInformer:
		return r.countObjects(inf.GetStore().List()), true
	case indexerInformer:
		return r.countObjects(inf.GetIndexer().List()), true
	default:
		// Fallback to a lightweight client peek if informer/store not available.
		ctx, cancel := context.WithTimeout(r.deps.Ctx, r.peekTimeout())
		defer cancel()
		hasAny := true
		if ok, err := r.deps.Cl.HasAnyByGVR(ctx, r.gvr, r.namespace); err == nil {
			hasAny = ok
		} else {
			r.recordPeekErrorLocked(err)
			r.lastPeek = time.Now()
			r.scheduleNextPeekLocked()
			return 0, false
		}
		if !hasAny {
			r.scheduleNextPeekLocked()
			return 0, true
		}
		return r.countViaClient(ctx)
	}
}

func (r *ResourceGroupItem) countObjects(items []interface{}) int {
	if items == nil {
		return 0
	}
	if r.namespace == "" {
		return len(items)
	}
	count := 0
	for _, raw := range items {
		switch o := raw.(type) {
		case crclient.Object:
			if o.GetNamespace() == r.namespace {
				count++
			}
		case *unstructured.Unstructured:
			if o.GetNamespace() == r.namespace {
				count++
			}
		}
	}
	return count
}

func (r *ResourceGroupItem) countViaClient(ctx context.Context) (int, bool) {
	list, err := r.deps.Cl.ListByGVR(ctx, r.gvr, r.namespace)
	if err != nil || list == nil {
		if err != nil {
			crlog.FromContext(r.deps.Ctx).Error(err, "list fallback failed", "gvr", r.gvr.String(), "namespace", r.namespace)
		}
		return 0, false
	}
	return len(list.Items), true
}

type peekResult struct {
	metricsZero bool // apiserver metrics report no objects at all
	noPeek      bool // no way to peek
	has         bool
	err         error
}

// peekRemote asks the apiserver whether any object exists. It does network
// calls with a timeout and must be called without r.mu held.
func (r *ResourceGroupItem) peekRemote(hasAny func(context.Context, schema.GroupVersionResource, string) (bool, error)) peekResult {
	ctx, cancel := context.WithTimeout(r.deps.Ctx, r.peekTimeout())
	defer cancel()
	// Prefer apiserver metrics when they definitively report zero.
	// Metrics are keyed by resource name only (no group/version). A zero count means
	// no objects of this resource name exist anywhere; any non-zero still requires
	// peeks/informers to localize existence.
	if r.deps.Cl != nil {
		if cnt, ok := r.deps.Cl.StorageCount(ctx, r.gvr, r.peekInterval()); ok && cnt == 0 {
			return peekResult{metricsZero: true}
		}
	}
	crlog.FromContext(r.deps.Ctx).Info("peeking resource emptiness", "gvr", r.gvr.String(), "namespace", r.namespace)
	if hasAny == nil && r.deps.Cl != nil {
		hasAny = r.deps.Cl.HasAnyByGVR
	}
	if hasAny == nil {
		return peekResult{noPeek: true}
	}
	has, err := hasAny(ctx, r.gvr, r.namespace)
	if err != nil && !apierrors.IsForbidden(err) {
		r.deps.reportReadError(r.what(), err)
	}
	return peekResult{has: has, err: err}
}

// applyPeekLocked records a peek result and reports (empty, known).
func (r *ResourceGroupItem) applyPeekLocked(res peekResult) (bool, bool) {
	switch {
	case res.metricsZero:
		r.lastPeek = time.Now()
		r.recordPeekSuccessLocked()
		r.emptyKnown = true
		r.empty = true
		r.count = 0
		r.countKnown = true
		return true, true
	case res.noPeek:
		return false, false
	case res.err != nil:
		if apierrors.IsForbidden(res.err) {
			r.blockPeeksLocked(res.err)
			return true, true
		}
		r.lastPeek = time.Now()
		r.recordPeekErrorLocked(res.err)
		r.scheduleNextPeekLocked()
		crlog.FromContext(r.deps.Ctx).Error(res.err, "peek failed", "gvr", r.gvr.String(), "namespace", r.namespace)
		return false, false
	}
	r.recordPeekSuccessLocked()
	return !res.has, true
}

// EmptyCached reports the last known emptiness without touching the network.
// When the value is stale it starts a peek in the background; the item's
// onChange callback refreshes the folder when the answer changes. Unknown
// counts as not empty, so the row stays visible until a peek says otherwise.
func (r *ResourceGroupItem) EmptyCached() bool {
	empty, _ := r.EmptyState()
	return empty
}

// EmptyState is EmptyCached plus whether the answer is still pending: the
// emptiness has never been known and no peek has failed yet. A recount
// forgets the emptiness for a moment; the last known answer covers that gap
// so a filtered row does not flash in and out.
func (r *ResourceGroupItem) EmptyState() (empty, pending bool) {
	if !r.isWatchable() {
		return true, false
	}
	interval := r.peekBaseInterval()
	r.mu.Lock()
	fresh := r.emptyKnown && !r.lastPeek.IsZero() && time.Since(r.lastPeek) < interval
	backoff := time.Since(r.lastError) < interval
	r.rememberEmptyLocked()
	val := r.decided && r.decidedEmpty
	pending = !r.decided && r.lastError.IsZero()
	start := !fresh && !backoff && !r.peeking && !r.closed && !r.synced
	if start {
		r.peeking = true
	}
	r.mu.Unlock()
	if start && r.deps.Activity.Paused() {
		// Peek once the dialog closes; the folder asks again on its next rebuild.
		r.mu.Lock()
		r.peeking = false
		r.mu.Unlock()
		start = false
	}
	if start {
		go func() {
			defer func() {
				r.mu.Lock()
				r.peeking = false
				r.mu.Unlock()
			}()
			r.emptyWithin(interval)
		}()
	}
	return val, pending
}

func (r *ResourceGroupItem) String() string {
	return fmt.Sprintf("%s/%s", r.gvr.Resource, r.namespace)
}

func (r *ResourceGroupItem) ID() string {
	if r == nil || r.RowItem == nil {
		return ""
	}
	return r.RowItem.ID()
}

func (r *ResourceGroupItem) CopyFrom(other *ResourceGroupItem) {
	if r == nil || other == nil {
		return
	}
	if r.RowItem == nil && other.RowItem != nil {
		r.RowItem = NewRowItem(other.RowItem.ID(), nil, nil, nil)
	}
	if r.RowItem != nil && other.RowItem != nil {
		r.RowItem.copyFrom(other.RowItem)
	}
	r.enter = other.enter
	r.deps = other.deps
	r.gvr = other.gvr
	r.gvk = other.gvk
	r.gvkKnown = other.gvkKnown
	r.namespace = other.namespace
	r.watchable = other.watchable
}

func (r *ResourceGroupItem) applySpec(spec resourceGroupSpec, deps Deps, created bool) {
	if r == nil {
		return
	}
	if r.RowItem == nil {
		r.RowItem = NewRowItem(spec.id, spec.cells, spec.path, spec.style)
	} else {
		r.RowItem.reset(spec.id, spec.cells, spec.path, spec.style)
	}
	r.RowItem.details = spec.detail
	r.enter = spec.enter
	// Background recounts and peeks read deps, gvr and namespace without the
	// lock. The folder re-applies the same spec on every rebuild, so write
	// these only when they actually change.
	if created || r.deps.Cl != deps.Cl || r.deps.AppConfig != deps.AppConfig {
		r.deps = deps
	}
	if r.gvr != spec.gvr {
		r.gvr = spec.gvr
	}
	if spec.gvk.Kind != "" && (!r.gvkKnown || r.gvk != spec.gvk) {
		r.mu.Lock()
		r.gvk = spec.gvk
		r.gvkKnown = true
		r.mu.Unlock()
	}
	if r.namespace != spec.namespace {
		r.namespace = spec.namespace
	}
	r.mu.Lock()
	switch {
	case created:
		r.watchable = spec.watchable
	case !r.watchable:
		// preserve previous disabled state
	case !spec.watchable:
		r.watchable = false
	default:
		r.watchable = spec.watchable
	}
	r.mu.Unlock()
}

// isWatchable reads watchable, which Count may clear from a background
// goroutine and applySpec may set on a folder rebuild.
func (r *ResourceGroupItem) isWatchable() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.watchable
}

func (r *ResourceGroupItem) setCountCell(value string) {
	if r == nil || r.RowItem == nil {
		return
	}
	r.RowItem.SimpleRow.SetColumn(2, value, nil)
}

func (r *ResourceGroupItem) SetOnChange(fn func()) {
	r.mu.Lock()
	r.onChange = fn
	r.mu.Unlock()
}

// SetCount records a known count for non-watchable items and triggers change notifications.
func (r *ResourceGroupItem) SetCount(value int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.count = value
	r.countKnown = true
	r.empty = value == 0
	r.emptyKnown = true
	changed := r.recordPublishedLocked()
	onChange := r.onChange
	r.mu.Unlock()
	if changed && onChange != nil {
		onChange()
	}
}

func (r *ResourceGroupItem) notifyIfChanged(onUpdate func()) {
	r.mu.Lock()
	changed := r.recordPublishedLocked()
	onChange := r.onChange
	r.mu.Unlock()
	if changed {
		if onChange != nil {
			onChange()
		}
		if onUpdate != nil {
			onUpdate()
		}
	}
}

// rememberEmptyLocked keeps the emptiness before it is forgotten. A recount
// can start before the folder has read the first answer, and when it
// finishes with the same answer nothing notifies the folder again.
func (r *ResourceGroupItem) rememberEmptyLocked() {
	if r.emptyKnown {
		r.decided, r.decidedEmpty = true, r.empty
	}
}

func (r *ResourceGroupItem) recordPublishedLocked() bool {
	r.rememberEmptyLocked()
	changed := false
	if r.countKnown {
		if !r.publishedCountKnown || r.count != r.publishedCount {
			r.publishedCountKnown = true
			r.publishedCount = r.count
			changed = true
		}
	}
	if r.emptyKnown {
		if !r.publishedEmptyKnown || r.empty != r.publishedEmpty {
			r.publishedEmptyKnown = true
			r.publishedEmpty = r.empty
			changed = true
		}
	}
	return changed
}

func (r *ResourceGroupItem) ensureInformerHandler(informer crcache.Informer) {
	if informer == nil {
		return
	}
	r.watchOnce.Do(func() {
		crlog.FromContext(r.deps.Ctx).Info("resource group registering informer handler", "gvr", r.gvr.String(), "namespace", r.namespace)
		reg, err := informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				r.onInformerEvent(obj)
			},
			UpdateFunc: func(_, newObj interface{}) {
				r.onInformerEvent(newObj)
			},
			DeleteFunc: func(obj interface{}) {
				r.onInformerEvent(obj)
			},
		})
		if err != nil {
			crlog.FromContext(r.deps.Ctx).Error(err, "register informer handler", "gvr", r.gvr.String(), "namespace", r.namespace)
			return
		}
		r.mu.Lock()
		closed := r.closed
		if !closed {
			r.informer, r.handlerReg = informer, reg
		}
		r.mu.Unlock()
		if closed {
			_ = informer.RemoveEventHandler(reg)
		}
	})
}

func (r *ResourceGroupItem) onInformerEvent(obj interface{}) {
	if r.namespace != "" {
		if acc, ok := accessorForEvent(obj); ok && acc != nil && acc.GetNamespace() != r.namespace {
			return
		}
	}
	r.scheduleRecount()
}

func (r *ResourceGroupItem) scheduleRecount() {
	if r.isClosed() || r.deps.Activity.deferIfPaused(r) {
		return
	}
	r.mu.Lock()
	if r.recounting {
		r.recountPending = true
		r.mu.Unlock()
		return
	}
	r.recounting = true
	r.recountPending = false
	r.nextPeekScheduled = false
	r.rememberEmptyLocked()
	r.countKnown = false
	r.emptyKnown = false
	r.lastPeek = time.Time{}
	r.mu.Unlock()

	go func() {
		defer func() {
			r.mu.Lock()
			pending := r.recountPending
			r.recounting = false
			r.recountPending = false
			r.mu.Unlock()
			if pending {
				// Run another recount to process events that arrived while the previous one was in flight.
				r.scheduleRecount()
			}
		}()
		ctx := r.deps.Ctx
		log := crlog.FromContext(ctx)
		// Coalesce bursts of informer events (a rollout, a busy namespace)
		// into one recount.
		select {
		case <-time.After(recountDelay):
		case <-r.stop:
			return
		case <-ctx.Done():
			return
		}
		log.V(1).Info("resource group recount started", "gvr", r.gvr.String(), "namespace", r.namespace)
		_ = r.Count()
		r.mu.Lock()
		synced := r.synced
		r.mu.Unlock()
		if !synced {
			// Without a synced informer the store says nothing; ask the
			// apiserver whether anything exists.
			peekCtx, cancel := context.WithTimeout(ctx, r.peekTimeout())
			crlog.FromContext(ctx).Info("peeking resource emptiness", "gvr", r.gvr.String(), "namespace", r.namespace)
			hasAny, err := r.deps.Cl.HasAnyByGVR(peekCtx, r.gvr, r.namespace)
			cancel()
			r.mu.Lock()
			if err == nil {
				r.empty = !hasAny
				r.emptyKnown = true
				r.lastPeek = time.Now()
			}
			r.mu.Unlock()
			if err != nil {
				log.Error(err, "hasAny peek failed", "gvr", r.gvr.String(), "namespace", r.namespace)
			}
		}
		// Rebuild the folder only when the visible count or emptiness
		// changed; the rebuild writes the count cell on the UI thread.
		r.notifyIfChanged(nil)
	}()
}

// recountDelay coalesces informer events into one recount.
var recountDelay = 300 * time.Millisecond

func (r *ResourceGroupItem) scheduleNextPeek() {
	r.mu.Lock()
	ctx, delay, ok := r.nextPeekScheduleLocked()
	r.mu.Unlock()
	if !ok {
		return
	}
	go r.runPeekTimer(ctx, delay)
}

func (r *ResourceGroupItem) scheduleNextPeekLocked() {
	ctx, delay, ok := r.nextPeekScheduleLocked()
	if !ok {
		return
	}
	go r.runPeekTimer(ctx, delay)
}

func (r *ResourceGroupItem) nextPeekScheduleLocked() (context.Context, time.Duration, bool) {
	if r.nextPeekScheduled || r.peekBlocked {
		return nil, 0, false
	}
	r.nextPeekScheduled = true
	interval := r.nextPeekDelayLocked()
	ctx := r.deps.Ctx
	return ctx, interval, true
}

func (r *ResourceGroupItem) runPeekTimer(ctx context.Context, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		r.scheduleRecount()
	case <-ctx.Done():
	case <-r.stop:
	}
}

func (r *ResourceGroupItem) peekInterval() time.Duration {
	base := r.peekBaseInterval()
	interval := base
	if jitterBase := interval / 5; jitterBase > 0 {
		interval += time.Duration(rand.Int63n(int64(jitterBase)))
	}
	return interval
}

func (r *ResourceGroupItem) peekBaseInterval() time.Duration {
	cfg := r.deps.AppConfig
	interval := 10 * time.Second
	if cfg != nil && cfg.Resources.PeekInterval.Duration > 0 {
		interval = cfg.Resources.PeekInterval.Duration
	}
	if interval <= 0 {
		return 10 * time.Second
	}
	return interval
}

func (r *ResourceGroupItem) peekTimeout() time.Duration {
	// Timeout requests before they stall; cap the timeout so throttling does not block the UI.
	base := r.peekBaseInterval()
	if base > 10*time.Second {
		base = 10 * time.Second
	}
	timeout := base
	if timeout > 15*time.Second {
		timeout = 15 * time.Second
	}
	if timeout < 2*time.Second {
		timeout = 2 * time.Second
	}
	return timeout
}

func (r *ResourceGroupItem) resetPeekBackoffLocked() {
	base := r.peekBaseInterval()
	cap := base * 16
	if cap < 30*time.Second {
		cap = 30 * time.Second
	}
	r.peekBackoff = wait.Backoff{
		Duration: base,
		Factor:   2.0,
		Jitter:   0.2,
		Steps:    8,
		Cap:      cap,
	}
}

func (r *ResourceGroupItem) ensurePeekBackoffLocked() {
	if r.peekBackoff.Duration == 0 {
		r.resetPeekBackoffLocked()
	}
}

func (r *ResourceGroupItem) nextPeekDelayLocked() time.Duration {
	r.ensurePeekBackoffLocked()
	return r.peekBackoff.Step()
}

func (r *ResourceGroupItem) recordPeekErrorLocked(err error) {
	r.lastError = time.Now()
	crlog.FromContext(r.deps.Ctx).Info("peek backoff increased", "gvr", r.gvr.String(), "namespace", r.namespace, "delay", r.peekBackoff.Duration, "error", err)
}

func (r *ResourceGroupItem) recordPeekSuccessLocked() {
	r.lastError = time.Time{}
	r.resetPeekBackoffLocked()
}

func (r *ResourceGroupItem) blockPeeksLocked(err error) {
	if r.peekBlocked {
		return
	}
	r.peekBlocked = true
	r.nextPeekScheduled = true
	r.empty = true
	r.emptyKnown = true
	r.count = 0
	r.countKnown = true
	r.lastError = time.Time{}
	r.lastPeek = time.Now()
	r.logPeekForbiddenLocked(err)
}

func (r *ResourceGroupItem) logPeekForbiddenLocked(err error) {
	if r.peekForbiddenLogged {
		return
	}
	log := crlog.FromContext(r.deps.Ctx).WithValues("gvr", r.gvr.String(), "namespace", r.namespace)
	if err != nil {
		log.Info("suppressing peeks after forbidden", "error", err.Error())
	} else {
		log.Info("suppressing peeks after forbidden")
	}
	r.peekForbiddenLogged = true
}

// Forbidden reports whether peeks are suppressed due to a forbidden response.
func (r *ResourceGroupItem) Forbidden() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peekBlocked
}
