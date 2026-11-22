package models

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	table "github.com/sttts/kc/internal/table"
	"github.com/sttts/kc/internal/tablecache"
	"github.com/sttts/kc/pkg/appconfig"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	toolscache "k8s.io/client-go/tools/cache"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// ObjectsFolder provides shared scaffolding for object list folders.
type ObjectsFolder struct {
	*BaseFolder
	gvr                schema.GroupVersionResource
	namespace          string
	rows               *liveObjectRowSource
	objectOrder        string
	hasObjOrder        bool
	verbs              []string
	ageMu              sync.Mutex
	ageHooks           []*ageCellHook
	ageTimer           *time.Timer
	Filter             func(*tablecache.Row) bool
	FilterUnstructured func(*unstructured.Unstructured) bool

	// kubectlArgs and kubectlNote refine KubectlCommand for filtered folders.
	kubectlArgs []string
	kubectlNote string
}

// NewObjectsFolder constructs an object-list folder with the provided metadata.
func NewObjectsFolder(deps Deps, gvr schema.GroupVersionResource, namespace string, path []string, verbs []string) *ObjectsFolder {
	base := NewBaseFolder(deps, nil, path)
	base.SetColumns([]table.Column{{Title: " Name"}})
	folder := &ObjectsFolder{
		BaseFolder: base,
		gvr:        gvr,
		namespace:  namespace,
		verbs:      append([]string(nil), verbs...),
	}
	rows := newLiveObjectRowSource(folder)
	folder.rows = rows
	base.SetRowSource(rows)
	return folder
}

func (o *ObjectsFolder) populateRows(ctx context.Context) ([]table.Row, error) {
	cfg := o.Deps.AppConfig
	columnsMode := cfg.Objects.Columns
	order := cfg.Objects.Order
	if o.hasObjOrder && o.objectOrder != "" {
		order = o.objectOrder
	}
	if rl, err := o.Deps.Cl.ListRowsByGVR(ctx, o.gvr, o.namespace); err == nil && rl != nil && len(rl.Items) > 0 {
		return o.rowsFromRowList(rl, columnsMode, order), nil
	} else if err != nil && apierrors.IsForbidden(err) {
		return nil, err
	}
	list, err := o.Deps.Cl.ListByGVR(ctx, o.gvr, o.namespace)
	if err != nil {
		return nil, err
	}
	return o.rowsFromList(list, order), nil
}

// Close stops the folder's watch; navigators call it when they drop the folder.
func (o *ObjectsFolder) Close() {
	if o.rows != nil {
		o.rows.Close()
	}
}

// GVR exposes the folder's group-version-resource identifier.
func (o *ObjectsFolder) GVR() schema.GroupVersionResource { return o.gvr }

// Namespace returns the namespace when the folder is namespaced, or an empty string when cluster scoped.
func (o *ObjectsFolder) Namespace() string { return o.namespace }

func (o *ObjectsFolder) ObjectListMeta() (schema.GroupVersionResource, string, bool) {
	return o.gvr, o.namespace, true
}

func (o *ObjectsFolder) rowsFromRowList(rl *tablecache.RowList, columnsMode, order string) []table.Row {
	namespaced := o.namespace == "" && o.resourceNamespaced()
	vis := visibleColumns(rl.Columns, columnsMode)
	cols := make([]table.Column, len(vis))
	for i := range vis {
		c := rl.Columns[vis[i]]
		cols[i] = table.Column{Title: c.Name}
	}
	if namespaced {
		cols = withNamespaceColumn(cols, table.Column{Title: "Namespace"})
	}
	o.SetColumns(cols)
	ageCols := ageColumnIndices(cols)

	idxs := orderRowIndices(rl.Items, order, namespaced)
	rows := make([]table.Row, 0, len(idxs))
	var hooks []*ageCellHook
	gvStr := o.gvr.GroupVersion().String()
	kind := o.kindString()
	ctor, hasChild := o.childConstructor()

	for _, ii := range idxs {
		rr := &rl.Items[ii]
		if o.Filter != nil && !o.Filter(rr) {
			continue
		}
		name := rowName(rr)
		ns, id := o.objectNamespaceAndID(namespaced, rr.ObjectMeta.Namespace, name)
		cells := buildCells(rr.Cells, vis, hasChild)
		if namespaced {
			cells = withNamespaceColumn(cells, ns)
		}
		basePath := append(append([]string{}, o.Path()...), name)
		info := RowStyleInfo{
			GVR:        o.gvr,
			GVK:        rr.TableTarget(),
			ObjectMeta: rr.ObjectMeta,
			BaseStyle:  WhiteStyle(),
		}
		info.Unstructured = unstructuredFromRow(rr)
		style := applyRowStylers(info)
		obj := NewObjectRow(id, cells, basePath, o.gvr, ns, name, style)
		obj.setRowFacts(rowFactsFor(info, id))
		obj.SetResourceVerbs(o.verbs)
		obj.WithViewContent(objectViewContent(o.Deps, o.gvr, ns, name))
		obj.SetViewCommand(o.objectViewCommand(ns, name))
		obj.RowItem.details = objectDetails(ns, name, kind, gvStr)
		if len(ageCols) > 0 {
			if hook := newAgeCellHook(ageCols, rr.ObjectMeta.CreationTimestamp.Time); hook != nil {
				obj.RowItem.SetCellsHook(hook.apply)
				hooks = append(hooks, hook)
			}
		}
		if hasChild && ctor != nil {
			nm := name
			rows = append(rows, NewObjectWithChildItem(obj, func() (Folder, error) {
				return ctor(o.Deps, ns, nm, basePath), nil
			}))
		} else {
			rows = append(rows, obj)
		}
	}
	o.installAgeHooks(hooks)
	return rows
}

func (o *ObjectsFolder) rowsFromList(list *unstructured.UnstructuredList, order string) []table.Row {
	o.installAgeHooks(nil)
	namespaced := o.namespace == "" && o.resourceNamespaced()
	if namespaced {
		o.SetColumns([]table.Column{{Title: " Name"}, {Title: "Namespace"}})
	}
	ids := make([]string, 0, len(list.Items))
	byID := make(map[string]*unstructured.Unstructured, len(list.Items))
	for i := range list.Items {
		if o.FilterUnstructured != nil && !o.FilterUnstructured(&list.Items[i]) {
			continue
		}
		_, id := o.objectNamespaceAndID(namespaced, list.Items[i].GetNamespace(), list.Items[i].GetName())
		ids = append(ids, id)
		byID[id] = &list.Items[i]
	}
	sort.Strings(ids)
	rows := make([]table.Row, 0, len(ids))
	gvStr := o.gvr.GroupVersion().String()
	kind := o.kindString()
	ctor, hasChild := o.childConstructor()
	for _, id := range ids {
		item := byID[id]
		name := item.GetName()
		ns, _ := o.objectNamespaceAndID(namespaced, item.GetNamespace(), name)
		basePath := append(append([]string{}, o.Path()...), name)
		title := name
		if hasChild {
			title = "/" + strings.TrimPrefix(name, "/")
		}
		meta := metav1.ObjectMeta{
			Name:              name,
			Namespace:         ns,
			UID:               item.GetUID(),
			Labels:            item.GetLabels(),
			Annotations:       item.GetAnnotations(),
			CreationTimestamp: item.GetCreationTimestamp(),
		}
		meta.DeletionTimestamp = item.GetDeletionTimestamp()
		info := RowStyleInfo{
			GVR:          o.gvr,
			GVK:          item.GroupVersionKind(),
			ObjectMeta:   meta,
			Unstructured: item,
			BaseStyle:    WhiteStyle(),
		}
		style := applyRowStylers(info)
		cells := []string{title}
		if namespaced {
			cells = append(cells, ns)
		}
		obj := NewObjectRow(id, cells, basePath, o.gvr, ns, name, style)
		obj.setRowFacts(rowFactsFor(info, id))
		obj.SetResourceVerbs(o.verbs)
		obj.WithViewContent(objectViewContent(o.Deps, o.gvr, ns, name))
		obj.SetViewCommand(o.objectViewCommand(ns, name))
		obj.RowItem.details = objectDetails(ns, name, kind, gvStr)
		if hasChild && ctor != nil {
			nm := name
			rows = append(rows, NewObjectWithChildItem(obj, func() (Folder, error) {
				return ctor(o.Deps, ns, nm, basePath), nil
			}))
		} else {
			rows = append(rows, obj)
		}
	}
	return rows
}

// objectNamespaceAndID returns the namespace to address an object with and its
// row ID. Folders listing across all namespaces have no namespace of their own,
// so the object's namespace is used, and the ID includes it because names are
// only unique within a namespace.
//
// namespaced is the result of resourceNamespaced, computed once per listing.
func (o *ObjectsFolder) objectNamespaceAndID(namespaced bool, objectNamespace, name string) (string, string) {
	if o.namespace != "" {
		return o.namespace, name
	}
	// Table rows may carry a guessed namespace for cluster-scoped objects
	// (tablecache falls back to the second cell), so only trust it for
	// namespaced resources.
	if objectNamespace == "" || !namespaced {
		return "", name
	}
	return objectNamespace, objectNamespace + "/" + name
}

// resourceNamespaced reports whether the folder's resource is namespaced. When
// the scope cannot be determined it assumes namespaced, so objects keep the
// namespace they report.
func (o *ObjectsFolder) resourceNamespaced() bool {
	if o.Deps.Cl == nil {
		return true
	}
	mapper := o.Deps.Cl.RESTMapper()
	if mapper == nil {
		return true
	}
	gvk, err := mapper.KindFor(o.gvr)
	if err != nil {
		return true
	}
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return true
	}
	return mapping.Scope.Name() == meta.RESTScopeNameNamespace
}

func (o *ObjectsFolder) kindString() string {
	if o.Deps.Cl == nil {
		return ""
	}
	if mapper := o.Deps.Cl.RESTMapper(); mapper != nil {
		if k, err := mapper.KindFor(o.gvr); err == nil {
			return k.Kind
		}
	}
	return ""
}

func (o *ObjectsFolder) childConstructor() (ChildConstructor, bool) {
	return ChildFor(o.gvr)
}

// ApplyObjectOrder overrides the object ordering strategy until changed again.
func (o *ObjectsFolder) ApplyObjectOrder(order string) {
	order = normalizeObjectOrder(order)
	if order == "" {
		return
	}
	o.objectOrder = order
	o.hasObjOrder = true
	o.Refresh()
}

func visibleColumns(cols []metav1.TableColumnDefinition, mode string) []int {
	vis := make([]int, 0, len(cols))
	for i, c := range cols {
		if mode == appconfig.ColumnsModeWide || c.Priority == 0 {
			vis = append(vis, i)
		}
	}
	return vis
}

// orderRowIndices sorts the rows by order. Across namespaces
// (byNamespace) the rows are grouped by namespace first, as kubectl get -A
// prints them, and order applies inside each namespace.
func orderRowIndices(items []tablecache.Row, order string, byNamespace bool) []int {
	idxs := make([]int, len(items))
	for i := range items {
		idxs[i] = i
	}
	nameOf := func(rr *tablecache.Row) string {
		if rr == nil {
			return ""
		}
		n := rr.Name
		if n == "" && len(rr.Cells) > 0 {
			if s, ok := rr.Cells[0].(string); ok {
				n = strings.TrimPrefix(s, "/")
			}
		}
		return strings.ToLower(n)
	}
	switch order {
	case appconfig.ObjectsOrderNameDesc:
		sort.Slice(idxs, func(i, j int) bool { return nameOf(&items[idxs[i]]) > nameOf(&items[idxs[j]]) })
	case appconfig.ObjectsOrderCreation:
		sort.Slice(idxs, func(i, j int) bool {
			return items[idxs[i]].ObjectMeta.CreationTimestamp.Time.Before(items[idxs[j]].ObjectMeta.CreationTimestamp.Time)
		})
	case appconfig.ObjectsOrderCreationDesc:
		sort.Slice(idxs, func(i, j int) bool {
			return items[idxs[i]].ObjectMeta.CreationTimestamp.Time.After(items[idxs[j]].ObjectMeta.CreationTimestamp.Time)
		})
	default:
		sort.Slice(idxs, func(i, j int) bool { return nameOf(&items[idxs[i]]) < nameOf(&items[idxs[j]]) })
	}
	if byNamespace {
		sort.SliceStable(idxs, func(i, j int) bool {
			return items[idxs[i]].ObjectMeta.Namespace < items[idxs[j]].ObjectMeta.Namespace
		})
	}
	return idxs
}

func normalizeObjectOrder(order string) string {
	switch strings.ToLower(strings.TrimSpace(order)) {
	case "", appconfig.ObjectsOrderName:
		return appconfig.ObjectsOrderName
	case appconfig.ObjectsOrderNameDesc:
		return appconfig.ObjectsOrderNameDesc
	case appconfig.ObjectsOrderCreation:
		return appconfig.ObjectsOrderCreation
	case appconfig.ObjectsOrderCreationDesc:
		return appconfig.ObjectsOrderCreationDesc
	}
	return appconfig.ObjectsOrderName
}

// NormalizeObjectOrder exposes the canonical form of object ordering for other packages.
func NormalizeObjectOrder(order string) string {
	return normalizeObjectOrder(order)
}

// withNamespaceColumn inserts v after the first (Name) column. Lists across
// all namespaces need it: the server table has no namespace column, and
// kubectl adds one on the client side too.
func withNamespaceColumn[T any](row []T, v T) []T {
	if len(row) == 0 {
		return []T{v}
	}
	out := make([]T, 0, len(row)+1)
	out = append(out, row[0], v)
	return append(out, row[1:]...)
}

func buildCells(cells []interface{}, vis []int, hasChild bool) []string {
	out := make([]string, len(vis))
	for i := range vis {
		idx := vis[i]
		if idx < len(cells) {
			out[i] = fmt.Sprint(cells[idx])
		}
	}
	if len(out) > 0 && hasChild {
		name := strings.TrimPrefix(out[0], "/")
		out[0] = "/" + name
	}
	return out
}

// rowName extracts the name from a row item, falling back to metadata/name when missing.
func rowName(rr *tablecache.Row) string {
	if rr == nil {
		return ""
	}
	if rr.Name != "" {
		return rr.Name
	}
	if rr.Cells != nil && len(rr.Cells) > 0 {
		if s, ok := rr.Cells[0].(string); ok {
			return strings.TrimPrefix(s, "/")
		}
	}
	return ""
}

func objectDetails(namespace, name, kind, gv string) string {
	if namespace != "" {
		return fmt.Sprintf("%s/%s (%s)", namespace, name, gv)
	}
	return fmt.Sprintf("%s (%s)", name, gv)
}

func ageColumnIndices(cols []table.Column) []int {
	idx := make([]int, 0, len(cols))
	for i := range cols {
		if strings.EqualFold(strings.TrimSpace(cols[i].Title), "age") {
			idx = append(idx, i)
		}
	}
	return idx
}

type liveSourceTarget struct {
	gvr       schema.GroupVersionResource
	namespace string
	name      string
}

// liveObjectRowSource adapts an ObjectsFolder to the rowSource interface while
// keeping rows synced with informer events for the target GVR.
type liveObjectRowSource struct {
	populate        func(context.Context) ([]table.Row, error)
	reportErr       func(error) // shows a read failure to the user; may be nil
	failingSince    time.Time
	onFolderDirty   func()
	mu              sync.Mutex
	rows            []table.Row // the rows that pass filter
	all             []table.Row // every loaded row
	filter          RowFilter
	index           map[string]int
	items           map[string]Item
	dirty           bool
	once            sync.Once
	watchFactory    func(func(), func()) (func(), error)
	watchCancel     func()
	watchTimer      *time.Timer
	watchTTL        time.Duration
	listBlocked     bool
	watchBlocked    bool
	forbiddenLogged bool
	target          liveSourceTarget

	// Background loading. With deps set, lists load off the UI thread at
	// most once per listRefreshInterval; the UI waits for a load only until
	// its own deadline and otherwise keeps the rows it has. Without deps
	// (unit tests) populate runs synchronously.
	deps        *Deps
	loading     bool
	loaded      bool
	loadDone    chan struct{}
	lastLoad    time.Time
	notifyTimer *time.Timer
	closed      bool
}

// listRefreshInterval spaces list reloads of an open folder. Informer events
// mark it dirty; a busy namespace would otherwise re-list on every event.
var listRefreshInterval = time.Second

// listLoadTimeout bounds one background list load.
const listLoadTimeout = 30 * time.Second

func newLiveObjectRowSource(owner *ObjectsFolder) *liveObjectRowSource {
	rows := newLiveObjectRowSourceWithHooks(
		func(ctx context.Context) ([]table.Row, error) { return owner.populateRows(ctx) },
		owner.BaseFolder.markDirtyFromSource,
		func(onEvent func(), onStop func()) (func(), error) {
			return startInformerForObjectsFolder(owner, onEvent, onStop)
		},
	)
	rows.setTarget(liveSourceTarget{gvr: owner.gvr, namespace: owner.namespace})
	rows.useBackground(owner.Deps)
	rows.watchTTL = watchDuration(owner.Deps)
	rows.reportErr = listErrorReporter(owner.Deps, owner.gvr, owner.namespace)
	return rows
}

func newLiveObjectRowSourceWithHooks(
	populate func(context.Context) ([]table.Row, error),
	onDirty func(),
	startInformer func(func(), func()) (func(), error),
) *liveObjectRowSource {
	src := &liveObjectRowSource{
		populate:      populate,
		onFolderDirty: onDirty,
		dirty:         true,
		watchFactory:  startInformer,
	}
	return src
}

func (s *liveObjectRowSource) setTarget(target liveSourceTarget) {
	s.mu.Lock()
	s.target = target
	s.mu.Unlock()
}

func startInformerForObjectsFolder(owner *ObjectsFolder, onEvent func(), onStop func()) (func(), error) {
	if owner == nil {
		return nil, nil
	}
	return startInformerForResource(owner.Deps, owner.gvr, owner.namespace, "", onEvent, onStop)
}

func (s *liveObjectRowSource) logForbiddenLocked(ctx context.Context, stage string, err error) {
	if s.forbiddenLogged {
		return
	}
	log := ctrllog.FromContext(ctx).WithName("liveObjectRowSource")
	if !s.target.gvr.Empty() {
		log = log.WithValues(
			"gvr", s.target.gvr.String(),
			"namespace", s.target.namespace,
			"name", s.target.name,
		)
	}
	if err != nil {
		log.Info("suppressing list/watch retries after forbidden", "stage", stage, "error", err.Error())
	} else {
		log.Info("suppressing list/watch retries after forbidden", "stage", stage)
	}
	s.forbiddenLogged = true
}

func (s *liveObjectRowSource) blockWatchLocked(ctx context.Context, stage string, err error) {
	if s.watchBlocked {
		return
	}
	if s.watchTimer != nil {
		s.watchTimer.Stop()
		s.watchTimer = nil
	}
	s.watchCancel = nil
	s.watchFactory = nil
	s.watchBlocked = true
	s.logForbiddenLocked(ctx, stage, err)
}

func (s *liveObjectRowSource) blockListLocked(ctx context.Context, err error) {
	if s.listBlocked {
		return
	}
	s.listBlocked = true
	s.dirty = false
	s.logForbiddenLocked(ctx, "list", err)
	s.blockWatchLocked(ctx, "list", nil)
}

func (s *liveObjectRowSource) ensureWatcherLocked(ctx context.Context) {
	if s.watchFactory == nil || s.watchCancel != nil || s.watchBlocked {
		return
	}
	factory := s.watchFactory
	s.mu.Unlock()
	cancel, err := factory(s.MarkDirty, s.watchStopped)
	s.mu.Lock()
	if err != nil {
		if apierrors.IsForbidden(err) {
			s.blockWatchLocked(ctx, "watch", err)
			return
		}
		ctrllog.FromContext(ctx).WithName("liveObjectRowSource").Error(err, "failed to start resource watch")
		return
	}
	if cancel != nil {
		s.watchCancel = cancel
		s.scheduleWatchTimeoutLocked()
	}
}

func (s *liveObjectRowSource) touchWatchLocked() {
	if s.watchTTL <= 0 || s.watchCancel == nil || s.watchBlocked {
		return
	}
	s.scheduleWatchTimeoutLocked()
}

func (s *liveObjectRowSource) scheduleWatchTimeoutLocked() {
	if s.watchTTL <= 0 || s.watchCancel == nil {
		if s.watchTimer != nil {
			s.watchTimer.Stop()
			s.watchTimer = nil
		}
		return
	}
	cancel := s.watchCancel
	if s.watchTimer != nil {
		s.watchTimer.Stop()
	}
	s.watchTimer = time.AfterFunc(s.watchTTL, func() {
		cancel()
	})
}

func (s *liveObjectRowSource) watchStopped() {
	s.mu.Lock()
	if s.watchTimer != nil {
		s.watchTimer.Stop()
		s.watchTimer = nil
	}
	s.watchCancel = nil
	s.mu.Unlock()
}

func (s *liveObjectRowSource) ensureLocked(ctx context.Context) {
	s.once.Do(func() { s.dirty = true })
	if s.listBlocked {
		return
	}
	s.ensureWatcherLocked(ctx)
	s.touchWatchLocked()
	if !s.dirty {
		return
	}
	if s.deps == nil {
		rows, err := s.populate(ctx)
		s.applyLoadLocked(ctx, rows, err)
		return
	}
	if s.loaded && s.deps.Activity.Paused() {
		// A dialog covers the panel; reload once it closes.
		return
	}
	if !s.loading {
		if wait := listRefreshInterval - time.Since(s.lastLoad); s.loaded && wait > 0 {
			s.notifyLaterLocked(wait)
			return
		}
		s.startLoadLocked()
	}
	done := s.loadDone
	s.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
	}
	s.mu.Lock()
}

// useBackground switches the source to background loads tied to deps.Ctx.
func (s *liveObjectRowSource) useBackground(deps Deps) {
	s.mu.Lock()
	s.deps = &deps
	s.mu.Unlock()
}

func (s *liveObjectRowSource) startLoadLocked() {
	s.loading = true
	s.dirty = false // events during the load mark it dirty again
	s.lastLoad = time.Now()
	done := make(chan struct{})
	s.loadDone = done
	root := s.deps.Ctx
	go func() {
		ctx, cancel := context.WithTimeout(root, listLoadTimeout)
		rows, err := s.populate(ctx)
		cancel()
		s.mu.Lock()
		if !s.closed {
			s.applyLoadLocked(root, rows, err)
		}
		s.loading = false
		s.loaded = true
		close(done)
		s.mu.Unlock()
		s.notifyFolder()
	}()
}

// notifyLaterLocked asks the folder to repaint after d, which retries a
// dirty list once the refresh interval has passed.
func (s *liveObjectRowSource) notifyLaterLocked(d time.Duration) {
	if s.notifyTimer != nil || s.closed {
		return
	}
	s.notifyTimer = time.AfterFunc(d, func() {
		s.mu.Lock()
		s.notifyTimer = nil
		s.mu.Unlock()
		s.notifyFolder()
	})
}

func (s *liveObjectRowSource) notifyFolder() {
	if s.onFolderDirty != nil {
		s.onFolderDirty()
	}
}

// Close stops the watch and pending refreshes; the source serves its last
// rows afterwards.
func (s *liveObjectRowSource) Close() {
	s.mu.Lock()
	s.closed = true
	if s.notifyTimer != nil {
		s.notifyTimer.Stop()
		s.notifyTimer = nil
	}
	if s.watchTimer != nil {
		s.watchTimer.Stop()
		s.watchTimer = nil
	}
	cancel := s.watchCancel
	s.watchCancel = nil
	s.watchFactory = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// applyLoadLocked stores the result of a list load.
func (s *liveObjectRowSource) applyLoadLocked(ctx context.Context, rows []table.Row, err error) {
	if err != nil {
		if apierrors.IsForbidden(err) {
			s.blockListLocked(ctx, err)
			return
		}
		// keep dirty so we retry next time
		s.dirty = true
		if s.failingSince.IsZero() {
			s.failingSince = time.Now()
		} else if s.reportErr != nil && time.Since(s.failingSince) > listFailureGrace {
			s.reportErr(err)
		}
		return
	}
	s.failingSince = time.Time{}
	s.all = rows
	s.applyFilterLocked()
	s.dirty = false
}

func (s *liveObjectRowSource) rebuildIndexLocked() {
	s.index = make(map[string]int, len(s.rows))
	s.items = make(map[string]Item, len(s.rows))
	for i, row := range s.rows {
		if row == nil {
			continue
		}
		id, _, _, ok := row.Columns()
		if !ok {
			continue
		}
		s.index[id] = i
		if item, ok := row.(Item); ok {
			s.items[id] = item
		}
	}
}

func (s *liveObjectRowSource) Lines(ctx context.Context, top, num int) []table.Row {
	if num <= 0 {
		return nil
	}
	s.mu.Lock()
	s.ensureLocked(ctx)
	rows := s.rows
	s.mu.Unlock()
	if len(rows) == 0 || top >= len(rows) {
		return nil
	}
	if top < 0 {
		top = 0
	}
	end := top + num
	if end > len(rows) {
		end = len(rows)
	}
	return rows[top:end]
}

func (s *liveObjectRowSource) Above(ctx context.Context, id string, n int) []table.Row {
	if n <= 0 {
		return nil
	}
	s.mu.Lock()
	s.ensureLocked(ctx)
	idx, ok := s.index[id]
	if !ok {
		s.mu.Unlock()
		return nil
	}
	start := idx - n
	if start < 0 {
		start = 0
	}
	rows := append([]table.Row(nil), s.rows[start:idx]...)
	s.mu.Unlock()
	if len(rows) == 0 {
		return nil
	}
	return rows
}

func (s *liveObjectRowSource) Below(ctx context.Context, id string, n int) []table.Row {
	if n <= 0 {
		return nil
	}
	s.mu.Lock()
	s.ensureLocked(ctx)
	idx, ok := s.index[id]
	if !ok {
		s.mu.Unlock()
		return nil
	}
	start := idx + 1
	if start >= len(s.rows) {
		s.mu.Unlock()
		return nil
	}
	end := start + n
	if end > len(s.rows) {
		end = len(s.rows)
	}
	rows := append([]table.Row(nil), s.rows[start:end]...)
	s.mu.Unlock()
	return rows
}

func (s *liveObjectRowSource) Len(ctx context.Context) int {
	s.mu.Lock()
	s.ensureLocked(ctx)
	ln := len(s.rows)
	s.mu.Unlock()
	return ln
}

func (s *liveObjectRowSource) Find(ctx context.Context, id string) (int, table.Row, bool) {
	s.mu.Lock()
	s.ensureLocked(ctx)
	idx, ok := s.index[id]
	if !ok || idx < 0 || idx >= len(s.rows) {
		s.mu.Unlock()
		return -1, nil, false
	}
	row := s.rows[idx]
	s.mu.Unlock()
	return idx, row, true
}

func (s *liveObjectRowSource) ItemByID(ctx context.Context, id string) (Item, bool) {
	s.mu.Lock()
	s.ensureLocked(ctx)
	it, ok := s.items[id]
	s.mu.Unlock()
	return it, ok
}

func (s *liveObjectRowSource) MarkDirty() {
	s.mu.Lock()
	if s.listBlocked {
		s.mu.Unlock()
		return
	}
	s.dirty = true
	s.mu.Unlock()
	if s.onFolderDirty != nil {
		s.onFolderDirty()
	}
}

func newLiveKeyRowSource(deps Deps, gvr schema.GroupVersionResource, namespace, name string, populate func(context.Context) ([]table.Row, error), onDirty func()) *liveObjectRowSource {
	rows := newLiveObjectRowSourceWithHooks(populate, onDirty, func(onEvent func(), onStop func()) (func(), error) {
		return startInformerForResource(deps, gvr, namespace, name, onEvent, onStop)
	})
	rows.setTarget(liveSourceTarget{gvr: gvr, namespace: namespace, name: name})
	rows.watchTTL = watchDuration(deps)
	rows.reportErr = listErrorReporter(deps, gvr, namespace)
	return rows
}

// informerStartTimeout bounds getting (not syncing) an informer from the cache.
const informerStartTimeout = 5 * time.Second

// listFailureGrace is how long a list may keep failing (for example while the
// informer syncs) before the user is told.
const listFailureGrace = 10 * time.Second

func startInformerForResource(deps Deps, gvr schema.GroupVersionResource, namespace, name string, onEvent func(), onStop func()) (func(), error) {
	if deps.Cl == nil {
		return nil, nil
	}
	// This runs on the UI thread (from Lines). Do not wait for the informer
	// to sync: an informer that cannot list never syncs and the UI would hang
	// forever. The handler below sees the initial objects as add events.
	ctx, cancelStart := context.WithTimeout(deps.Ctx, informerStartTimeout)
	defer cancelStart()
	log := ctrllog.FromContext(ctx).WithName("resourceWatch").WithValues("gvr", gvr.String(), "namespace", namespace, "name", name)
	mapper := deps.Cl.RESTMapper()
	if mapper == nil {
		return nil, fmt.Errorf("rest mapper unavailable")
	}
	gvk, err := mapper.KindFor(gvr)
	if err != nil {
		return nil, err
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	informer, err := deps.Cl.GetCache().GetInformer(ctx, obj, crcache.BlockUntilSynced(false))
	if err != nil || informer == nil {
		if err != nil && !apierrors.IsForbidden(err) {
			what := gvr.Resource
			if namespace != "" {
				what += " in " + namespace
			}
			deps.reportReadError(what, err)
		}
		return nil, err
	}
	type handlerReg interface {
		Remove() error
	}
	reg, err := informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if !matchesTarget(obj, namespace, name) {
				return
			}
			if onEvent != nil {
				onEvent()
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			if !matchesTarget(newObj, namespace, name) {
				return
			}
			if onEvent != nil {
				onEvent()
			}
		},
		DeleteFunc: func(obj interface{}) {
			if !matchesTarget(obj, namespace, name) {
				return
			}
			if onEvent != nil {
				onEvent()
			}
		},
	})
	if err != nil {
		log.Error(err, "failed to add informer handler")
		return nil, err
	}
	// Signal once the informer has synced so the folder re-lists from the
	// cache. This waits in the background; an informer that never syncs
	// (no permission) only leaves this goroutine waiting until cancel.
	stop := make(chan struct{})
	go func() {
		select {
		case <-deps.Ctx.Done():
			return
		case <-stop:
			return
		default:
		}
		synced := informer.HasSynced()
		if !synced {
			done := make(chan struct{})
			go func() {
				defer close(done)
				select {
				case <-deps.Ctx.Done():
				case <-stop:
				}
			}()
			synced = toolscache.WaitForCacheSync(done, informer.HasSynced)
		}
		if synced && onEvent != nil {
			onEvent()
		}
	}()
	var stopOnce sync.Once
	cancel := func() {
		stopOnce.Do(func() { close(stop) })
		if h, ok := reg.(handlerReg); ok {
			_ = h.Remove()
		}
		if onStop != nil {
			onStop()
		}
	}
	return cancel, nil
}

func watchDuration(deps Deps) time.Duration {
	if deps.AppConfig != nil {
		if dur := deps.AppConfig.Kubernetes.Clusters.TTL.Duration; dur > 0 {
			return dur
		}
	}
	return 2 * time.Minute
}

func accessorForEvent(obj interface{}) (metav1.Object, bool) {
	switch o := obj.(type) {
	case toolscache.DeletedFinalStateUnknown:
		return accessorForEvent(o.Obj)
	default:
		accessor, err := meta.Accessor(o)
		if err != nil {
			return nil, false
		}
		return accessor, true
	}
}

func matchesTarget(obj interface{}, namespace, name string) bool {
	accessor, ok := accessorForEvent(obj)
	if !ok || accessor == nil {
		return false
	}
	if namespace != "" && accessor.GetNamespace() != namespace {
		return false
	}
	if name != "" && accessor.GetName() != name {
		return false
	}
	return true
}

func (o *ObjectsFolder) installAgeHooks(hooks []*ageCellHook) {
	o.ageMu.Lock()
	defer o.ageMu.Unlock()
	if len(hooks) == 0 {
		o.ageHooks = nil
		if o.ageTimer != nil {
			o.ageTimer.Stop()
			o.ageTimer = nil
		}
		return
	}
	o.ageHooks = hooks
	interval := o.nextAgeIntervalLocked(time.Now())
	o.scheduleAgeTimerLocked(interval)
}

func (o *ObjectsFolder) nextAgeIntervalLocked(now time.Time) time.Duration {
	if len(o.ageHooks) == 0 {
		return 0
	}
	var interval time.Duration
	for _, hook := range o.ageHooks {
		if hook == nil {
			continue
		}
		if d := hook.nextInterval(now); d > 0 {
			if interval == 0 || d < interval {
				interval = d
			}
		}
	}
	return interval
}

func (o *ObjectsFolder) scheduleAgeTimerLocked(interval time.Duration) {
	if o.ageTimer != nil {
		o.ageTimer.Stop()
		o.ageTimer = nil
	}
	if interval <= 0 {
		return
	}
	o.ageTimer = time.AfterFunc(interval, o.ageTick)
}

func (o *ObjectsFolder) ageTick() {
	select {
	case <-o.Deps.Ctx.Done():
		return
	default:
	}
	o.BaseFolder.markDirtyFromSource()
	o.ageMu.Lock()
	defer o.ageMu.Unlock()
	o.ageTimer = nil
	interval := o.nextAgeIntervalLocked(time.Now())
	o.scheduleAgeTimerLocked(interval)
}

func listErrorReporter(deps Deps, gvr schema.GroupVersionResource, namespace string) func(error) {
	what := gvr.Resource
	if namespace != "" {
		what += " in " + namespace
	}
	return func(err error) { deps.reportReadError(what, err) }
}

// objectViewCommand prints one object as YAML, like the viewer.
func (o *ObjectsFolder) objectViewCommand(ns, name string) string {
	args := []string{"get", kubectlResource(o.gvr), name}
	if ns != "" {
		args = append(args, "-n", ns)
	}
	return kubectlCommand(o.Deps, append(args, "-o", "yaml")...)
}
