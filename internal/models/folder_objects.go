package models

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	toolscache "k8s.io/client-go/tools/cache"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	table "github.com/stokaro/kc/internal/table"
	"github.com/stokaro/kc/internal/tablecache"
	"github.com/stokaro/kc/pkg/appconfig"
)

// ObjectsFolder provides shared scaffolding for object list folders.
type ObjectsFolder struct {
	*BaseFolder
	gvr                schema.GroupVersionResource
	namespace          string
	rows               *liveObjectRowSource
	objectOrder        string
	hasObjOrder        bool
	columnsMode        string // set by ApplyColumnsMode; "" reads the configuration
	groupNamespaces    bool
	hasGroupNamespaces bool
	filterCols         *filterColumns
	sortMu             sync.Mutex
	sortState          folderSortState
	verbs              []string
	ageMu              sync.Mutex
	ageHooks           []*ageCellHook
	ageTimer           *time.Timer
	Filter             func(*tablecache.Row) bool
	FilterUnstructured func(*unstructured.Unstructured) bool
	// FilterObject keeps a row by its full object from the shared cache,
	// for a filter on a field the server table does not carry. A row
	// whose object is not in the cache is hidden.
	FilterObject func(*unstructured.Unstructured) bool

	// kubectlArgs and kubectlNote refine KubectlCommand for filtered folders.
	kubectlArgs []string
	kubectlNote string

	// release gives up the scoped cluster the folder reads, if it holds one.
	// Close calls it; until then the pool keeps that cluster running.
	release func()
}

// NewObjectsFolder constructs an object-list folder with the provided metadata.
func NewObjectsFolder(deps Deps, gvr schema.GroupVersionResource, namespace string, path, verbs []string) *ObjectsFolder {
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
	columnsMode := o.requestedColumnsMode()
	order := o.requestedOrder()
	cc := planCustomColumns(o.Deps, o.gvr, columnsMode)
	if rl, err := o.Deps.Cl.ListRowsByGVR(ctx, o.gvr, o.namespace); err == nil && rl != nil && len(rl.Items) > 0 {
		cc.loadObjects(ctx, o.Deps, o.namespace)
		objs := loadRowObjects(ctx, o.Deps, o.gvr, o.namespace)
		if o.FilterObject != nil && objs == nil {
			// Without the objects every row would be hidden, which reads
			// as "none".
			return nil, fmt.Errorf("%s: the objects to filter by could not be read", o.gvr.Resource)
		}
		return o.rowsFromRowListWithObjects(o.withUsageColumns(rl), columnsMode, order, cc, objs), nil
	} else if err != nil && apierrors.IsForbidden(err) {
		return nil, err
	}
	list, err := o.Deps.Cl.ListByGVR(ctx, o.gvr, o.namespace)
	if err != nil {
		return nil, err
	}
	cc.setObjects(list.Items)
	return o.rowsFromList(list, order, cc), nil
}

// origin names the cluster connection this folder lists from.
func (o *ObjectsFolder) origin() ObjectOrigin {
	ctxName := o.Deps.CtxName
	if ctxName == "" {
		ctxName = o.Deps.ClusterKey.ContextName
	}
	return ObjectOrigin{Cluster: o.Deps.Cl, Context: ctxName}
}

// Close stops the folder's watch and releases the scoped cluster it holds;
// navigators call it when they drop the folder. Calling it again is a no-op.
func (o *ObjectsFolder) Close() {
	if o.rows != nil {
		o.rows.Close()
	}
	if r := o.release; r != nil {
		o.release = nil
		r()
	}
}

// GVR exposes the folder's group-version-resource identifier.
func (o *ObjectsFolder) GVR() schema.GroupVersionResource { return o.gvr }

// Namespace returns the namespace when the folder is namespaced, or an empty string when cluster scoped.
func (o *ObjectsFolder) Namespace() string { return o.namespace }

func (o *ObjectsFolder) ObjectListMeta() (schema.GroupVersionResource, string, bool) {
	return o.gvr, o.namespace, true
}

func (o *ObjectsFolder) rowsFromRowList(rl *tablecache.RowList, columnsMode, order string, cc *customColumns) []table.Row {
	return o.rowsFromRowListWithObjects(rl, columnsMode, order, cc, nil)
}

// rowsFromRowListWithObjects builds the rows of a server table list. objs
// holds the full objects the health rules and row stylers read; a row
// without one falls back to what the table row embeds.
func (o *ObjectsFolder) rowsFromRowListWithObjects(rl *tablecache.RowList, columnsMode, order string, cc *customColumns, objs rowObjects) []table.Row {
	namespaced := o.namespace == "" && o.resourceNamespaced()
	vis := cc.serverColumns(rl.Columns, visibleColumns(rl.Columns, columnsMode))
	cols := make([]table.Column, len(vis))
	for i := range vis {
		c := rl.Columns[vis[i]]
		cols[i] = serverTableColumn(c)
	}
	// The name column sorts by the object name. It is found by its
	// format, not its place: the Event table starts with Last Seen.
	nameCol := nameColumn(rl.Columns)
	keys := make([]string, len(vis))
	for i := range vis {
		keys[i] = appconfig.ObjectsOrderKey(rl.Columns[vis[i]].Name)
		if vis[i] == nameCol {
			keys[i] = sortKeyName
		}
	}
	for _, c := range cc.tableColumns() {
		cols = append(cols, c)
		keys = append(keys, appconfig.ObjectsOrderKey(c.Title))
	}
	if namespaced {
		cols = withNamespaceColumn(cols, table.Column{Title: "Namespace"})
		keys = withNamespaceColumn(keys, sortKeyNamespace)
	}
	o.SetColumns(cols)
	ageCols := ageColumnIndices(cols)
	times := eventColumnTimes(o.gvr, rl.Items, rl.Columns, objs)
	timeCols, timeSrc := timeColumnIndices(cols, vis, rl.Columns, namespaced, times)

	idxs, sortBy := orderRowIndicesWithTimes(rl.Items, rl.Columns, order, layoutFor(namespaced, o.requestedGrouping()), times)
	o.setSortState(sortBy, keys, namespaced)
	rows := make([]table.Row, 0, len(idxs))
	var hooks []*ageCellHook
	gvStr := o.gvr.GroupVersion().String()
	kind := o.kindString()
	ctor, hasChild := o.childConstructor()

	// Column filter terms see every server column, wide ones included, and
	// the added ones, whatever the columns mode shows.
	customCells := make([][]string, len(rl.Items))
	filterCells := make([][]any, len(rl.Items))
	for i := range rl.Items {
		rr := &rl.Items[i]
		name := rowName(rr)
		ns, _ := o.objectNamespaceAndID(namespaced, rr.ObjectMeta.Namespace, name)
		customCells[i] = cc.cells(ns, name)
		filterCells[i] = withCustomCells(rr.Cells, len(rl.Columns), customCells[i])
	}
	fc := newFilterColumns(slices.Concat(rl.Columns, customColumnDefs(cc)), filterCells)
	o.setFilterColumns(fc)

	for _, ii := range idxs {
		rr := &rl.Items[ii]
		if o.Filter != nil && !o.Filter(rr) {
			continue
		}
		if o.FilterObject != nil {
			if u := objs.object(rr.ObjectMeta.Namespace, rowName(rr)); u == nil || !o.FilterObject(u) {
				continue
			}
		}
		name := rowName(rr)
		ns, id := o.objectNamespaceAndID(namespaced, rr.ObjectMeta.Namespace, name)
		cells := buildCells(rr.Cells, vis, hasChild)
		if columnsMode != appconfig.ColumnsModeWide {
			compactRestarts(cells, vis, rl.Columns)
		}
		cells = append(cells, customCells[ii]...)
		if namespaced {
			cells = withNamespaceColumn(cells, ns)
		}
		basePath := append(slices.Clone(o.Path()), name)
		info := RowStyleInfo{
			GVR:        o.gvr,
			GVK:        rr.TableTarget(),
			ObjectMeta: rr.ObjectMeta,
			BaseStyle:  WhiteStyle(),
		}
		info.Unstructured = objs.object(rr.ObjectMeta.Namespace, name)
		if info.Unstructured == nil {
			info.Unstructured = unstructuredFromRow(rr)
		}
		style := applyRowStylers(info)
		obj := NewObjectRow(id, cells, basePath, o.gvr, ns, name, style)
		obj.setRowFacts(rowFactsFor(info, id, ns, fc, ii))
		obj.SetResourceVerbs(o.verbs)
		obj.SetInstance(rr.ObjectMeta.UID, o.origin())
		obj.WithViewContent(objectViewContent(o.Deps, o.gvr, ns, name))
		obj.WithManifest(o.Deps.CtxName, objectManifest(o.Deps, o.gvr, ns, name))
		obj.SetViewCommand(o.objectViewCommand(ns, name))
		obj.RowItem.details = objectDetails(ns, name, kind, gvStr)
		obj.SetKind(kind)
		if hook := rowTimeHook(ageCols, rr.ObjectMeta.CreationTimestamp.Time, timeCols, timeSrc, times, ii); hook != nil {
			obj.RowItem.SetCellsHook(hook.apply)
			hooks = append(hooks, hook)
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

func (o *ObjectsFolder) rowsFromList(list *unstructured.UnstructuredList, order string, cc *customColumns) []table.Row {
	o.installAgeHooks(nil)
	namespaced := o.namespace == "" && o.resourceNamespaced()
	if cols := cc.tableColumns(); namespaced || len(cols) > 0 {
		head := []table.Column{{Title: " Name"}}
		if namespaced {
			head = append(head, table.Column{Title: "Namespace"})
		}
		o.SetColumns(append(head, cols...))
	}
	keys := []string{sortKeyName}
	if namespaced {
		keys = append(keys, sortKeyNamespace)
	}
	// Without a server table only the metadata can be sorted by.
	items := make([]*unstructured.Unstructured, 0, len(list.Items))
	metas := make([]tablecache.Row, 0, len(list.Items))
	for i := range list.Items {
		if o.FilterUnstructured != nil && !o.FilterUnstructured(&list.Items[i]) {
			continue
		}
		if o.FilterObject != nil && !o.FilterObject(&list.Items[i]) {
			continue
		}
		var r tablecache.Row
		r.Name = list.Items[i].GetName()
		r.CreationTimestamp = list.Items[i].GetCreationTimestamp()
		if namespaced {
			r.Namespace = list.Items[i].GetNamespace()
		}
		items = append(items, &list.Items[i])
		metas = append(metas, r)
	}
	idxs, sortBy := orderRowIndices(metas, nil, order, layoutFor(namespaced, o.requestedGrouping()))
	o.setSortState(sortBy, keys, namespaced)
	rows := make([]table.Row, 0, len(idxs))
	gvStr := o.gvr.GroupVersion().String()
	kind := o.kindString()
	ctor, hasChild := o.childConstructor()
	customCells := make([][]string, len(items))
	filterCells := make([][]any, len(items))
	for i, item := range items {
		customCells[i] = cc.cellsFor(item)
		filterCells[i] = withCustomCells([]any{item.GetName()}, 1, customCells[i])
	}
	fc := newFilterColumns(slices.Concat([]metav1.TableColumnDefinition{{Name: "Name"}}, customColumnDefs(cc)), filterCells)
	o.setFilterColumns(fc)
	for _, ii := range idxs {
		item := items[ii]
		name := item.GetName()
		_, id := o.objectNamespaceAndID(namespaced, item.GetNamespace(), name)
		ns, _ := o.objectNamespaceAndID(namespaced, item.GetNamespace(), name)
		basePath := append(slices.Clone(o.Path()), name)
		title := name
		if hasChild {
			title = "/" + strings.TrimPrefix(name, "/")
		}
		objMeta := metav1.ObjectMeta{
			Name:              name,
			Namespace:         ns,
			UID:               item.GetUID(),
			Labels:            item.GetLabels(),
			Annotations:       item.GetAnnotations(),
			CreationTimestamp: item.GetCreationTimestamp(),
		}
		objMeta.DeletionTimestamp = item.GetDeletionTimestamp()
		info := RowStyleInfo{
			GVR:          o.gvr,
			GVK:          item.GroupVersionKind(),
			ObjectMeta:   objMeta,
			Unstructured: item,
			BaseStyle:    WhiteStyle(),
		}
		style := applyRowStylers(info)
		cells := []string{title}
		if namespaced {
			cells = append(cells, ns)
		}
		cells = append(cells, customCells[ii]...)
		obj := NewObjectRow(id, cells, basePath, o.gvr, ns, name, style)
		obj.setRowFacts(rowFactsFor(info, id, ns, fc, ii))
		obj.SetResourceVerbs(o.verbs)
		obj.SetInstance(item.GetUID(), o.origin())
		obj.WithViewContent(objectViewContent(o.Deps, o.gvr, ns, name))
		obj.WithManifest(o.Deps.CtxName, objectManifest(o.Deps, o.gvr, ns, name))
		obj.SetViewCommand(o.objectViewCommand(ns, name))
		obj.RowItem.details = objectDetails(ns, name, kind, gvStr)
		obj.SetKind(kind)
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
func (o *ObjectsFolder) objectNamespaceAndID(namespaced bool, objectNamespace, name string) (namespace, id string) {
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

// ApplyColumnsMode shows the normal or the wide columns until changed again.
func (o *ObjectsFolder) ApplyColumnsMode(mode string) {
	mode = normalizeColumnsMode(mode)
	o.sortMu.Lock()
	changed := o.columnsMode != mode
	o.columnsMode = mode
	o.sortMu.Unlock()
	if changed {
		o.Refresh()
	}
}

// requestedColumnsMode returns the columns mode the panel asked for, else
// the configured one.
func (o *ObjectsFolder) requestedColumnsMode() string {
	o.sortMu.Lock()
	defer o.sortMu.Unlock()
	if o.columnsMode != "" {
		return o.columnsMode
	}
	if o.Deps.AppConfig != nil {
		return normalizeColumnsMode(o.Deps.AppConfig.Objects.Columns)
	}
	return appconfig.ColumnsModeNormal
}

func normalizeColumnsMode(mode string) string {
	if strings.EqualFold(mode, appconfig.ColumnsModeWide) {
		return appconfig.ColumnsModeWide
	}
	return appconfig.ColumnsModeNormal
}

// ApplyObjectOrder overrides the object ordering strategy until changed again.
func (o *ObjectsFolder) ApplyObjectOrder(order string) {
	order = normalizeObjectOrder(order)
	o.sortMu.Lock()
	changed := !o.hasObjOrder || o.objectOrder != order
	o.objectOrder = order
	o.hasObjOrder = true
	o.sortMu.Unlock()
	if changed {
		o.Refresh()
	}
}

// ApplyNamespaceGrouping turns the grouping of a list across namespaces on
// or off until changed again. Lists of one namespace ignore it.
func (o *ObjectsFolder) ApplyNamespaceGrouping(on bool) {
	o.sortMu.Lock()
	changed := !o.hasGroupNamespaces || o.groupNamespaces != on
	o.groupNamespaces = on
	o.hasGroupNamespaces = true
	o.sortMu.Unlock()
	if changed {
		o.Refresh()
	}
}

// NamespaceGrouping reports whether the rows are grouped by namespace, and
// whether the list spans namespaces at all, so grouping applies to it. The
// scope comes from the last listing, so the call does no lookup; before the
// first listing a list of all namespaces counts as spanning them.
func (o *ObjectsFolder) NamespaceGrouping() (on, applies bool) {
	on = o.requestedGrouping()
	o.sortMu.Lock()
	defer o.sortMu.Unlock()
	if !o.sortState.applied {
		return on, o.namespace == ""
	}
	return on, o.sortState.spansNamespaces
}

// requestedGrouping is the grouping set on the folder, or else the
// configured one.
func (o *ObjectsFolder) requestedGrouping() bool {
	o.sortMu.Lock()
	defer o.sortMu.Unlock()
	if o.hasGroupNamespaces {
		return o.groupNamespaces
	}
	if o.Deps.AppConfig != nil {
		return o.Deps.AppConfig.Objects.GroupByNamespace
	}
	return true
}

// folderSortState is the sort of the last listing.
type folderSortState struct {
	applied         bool
	sortBy          objectSort
	keys            []string
	spansNamespaces bool
}

// requestedOrder is the order set on the folder, or else the configured one.
func (o *ObjectsFolder) requestedOrder() string {
	o.sortMu.Lock()
	defer o.sortMu.Unlock()
	if o.hasObjOrder && o.objectOrder != "" {
		return o.objectOrder
	}
	if o.Deps.AppConfig != nil {
		return normalizeObjectOrder(o.Deps.AppConfig.Objects.Order)
	}
	return appconfig.ObjectsOrderName
}

func (o *ObjectsFolder) setSortState(sortBy objectSort, keys []string, spansNamespaces bool) {
	o.sortMu.Lock()
	o.sortState = folderSortState{applied: true, sortBy: sortBy, keys: keys, spansNamespaces: spansNamespaces}
	o.sortMu.Unlock()
}

// SortKeys returns the order key of each column, in column order.
func (o *ObjectsFolder) SortKeys() []string {
	o.sortMu.Lock()
	defer o.sortMu.Unlock()
	if !o.sortState.applied {
		return []string{sortKeyName}
	}
	return append([]string(nil), o.sortState.keys...)
}

// ObjectOrder returns the order the rows are sorted by. Before the first
// listing it is the requested order.
func (o *ObjectsFolder) ObjectOrder() string {
	o.sortMu.Lock()
	st := o.sortState
	o.sortMu.Unlock()
	if !st.applied {
		return o.requestedOrder()
	}
	return st.sortBy.order()
}

// SortMark returns the shown column the rows are sorted by.
func (o *ObjectsFolder) SortMark() (SortMark, bool) {
	o.sortMu.Lock()
	defer o.sortMu.Unlock()
	if !o.sortState.applied {
		return SortMark{}, false
	}
	for i, k := range o.sortState.keys {
		if k == o.sortState.sortBy.key {
			return SortMark{Column: i, Desc: o.sortState.sortBy.desc}, true
		}
	}
	return SortMark{}, false
}

// serverTableColumn returns the panel column of a server table column. A
// Message column (Events, many custom resources) is free text: it takes the
// rest of the line and gives up width before the columns that name things.
func serverTableColumn(c metav1.TableColumnDefinition) table.Column {
	if strings.EqualFold(c.Name, "Message") {
		return table.Column{Title: c.Name, Fill: true, MinWidth: reasonMinWidth}
	}
	return table.Column{Title: c.Name}
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

func normalizeObjectOrder(order string) string {
	return appconfig.NormalizeObjectsOrder(order)
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

func buildCells(cells []any, vis []int, hasChild bool) []string {
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

// compactRestarts cuts a pod's Restarts cell to the count in the normal
// columns mode: "8 (5m58s ago)" becomes "8". The time of the last restart
// made the column wide enough to push Age and the usage columns out of a
// half panel; the wide mode keeps it, and filters and sorting read the
// whole cell either way.
func compactRestarts(cells []string, vis []int, cols []metav1.TableColumnDefinition) {
	for i, ci := range vis {
		if i >= len(cells) || ci >= len(cols) || !strings.EqualFold(cols[ci].Name, "Restarts") {
			continue
		}
		if count, _, ok := strings.Cut(cells[i], " ("); ok {
			cells[i] = count
		}
	}
}

// rowName extracts the name from a row item, falling back to metadata/name when missing.
func rowName(rr *tablecache.Row) string {
	if rr == nil {
		return ""
	}
	if rr.Name != "" {
		return rr.Name
	}
	if len(rr.Cells) > 0 {
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

// timeColumnIndices returns the shown columns that have times, and for
// each the server column its times belong to. vis maps shown server
// columns to server columns; a list across namespaces shows a Namespace
// column after the first one.
func timeColumnIndices(cols []table.Column, vis []int, serverCols []metav1.TableColumnDefinition, namespaced bool, times columnTimes) (shown, server []int) {
	if len(times) == 0 {
		return nil, nil
	}
	for i, sc := range vis {
		if _, ok := times[sc]; !ok || sc >= len(serverCols) {
			continue
		}
		at := i
		if namespaced && i > 0 {
			at++ // after the inserted Namespace column
		}
		if at < len(cols) {
			shown = append(shown, at)
			server = append(server, sc)
		}
	}
	return shown, server
}

// rowTimeHook returns the hook that keeps the relative time cells of row
// item current: Age from the creation time, and the columns with times
// from their own.
func rowTimeHook(ageCols []int, created time.Time, timeCols, timeSrc []int, times columnTimes, item int) *ageCellHook {
	cols := slices.Clone(ageCols)
	at := make([]time.Time, 0, len(ageCols)+len(timeCols))
	for range ageCols {
		at = append(at, created)
	}
	for i, c := range timeCols {
		cols = append(cols, c)
		at = append(at, times[timeSrc[i]][item])
	}
	return newTimeCellHook(cols, at)
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
	startWatch      func(func(), func()) (func(), error) // watchFactory before a forbidden answer
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
		startWatch:    startInformer,
	}
	return src
}

func (s *liveObjectRowSource) setTarget(target liveSourceTarget) {
	s.mu.Lock()
	s.target = target
	s.mu.Unlock()
}

func startInformerForObjectsFolder(owner *ObjectsFolder, onEvent, onStop func()) (func(), error) {
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
	end := min(top+num, len(rows))
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
	start := max(idx-n, 0)
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
	end := min(start+n, len(s.rows))
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

// reread is the explicit reread (C-r): a list or watch given up after a
// forbidden answer is tried again, because the permission may have been
// granted since, and the next load skips the listRefreshInterval spacing.
// A running watch stays: the shared cache keeps the rows current.
func (s *liveObjectRowSource) reread() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if s.listBlocked || s.watchBlocked {
		s.listBlocked, s.watchBlocked, s.forbiddenLogged = false, false, false
		s.watchFactory = s.startWatch
	}
	s.lastLoad = time.Time{}
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

func startInformerForResource(deps Deps, gvr schema.GroupVersionResource, namespace, name string, onEvent, onStop func()) (func(), error) {
	return startFilteredInformer(deps, gvr, namespace, name, nil, onEvent, onStop)
}

// startFilteredInformer is startInformerForResource with an extra filter on
// the event's object; a nil filter accepts every object.
func startFilteredInformer(deps Deps, gvr schema.GroupVersionResource, namespace, name string, filter func(metav1.Object) bool, onEvent, onStop func()) (func(), error) {
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
	reg, err := informer.AddEventHandler(filteredEventHandler(namespace, name, filter, onEvent))
	if err != nil {
		log.Error(err, "failed to add informer handler")
		return nil, err
	}
	// Signal once the informer has synced so the folder re-lists from the
	// cache. This waits in the background; an informer that never syncs
	// (no permission) only leaves this goroutine waiting until cancel.
	stop := make(chan struct{})
	go notifyWhenSynced(deps.Ctx, informer, stop, onEvent)
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

// filteredEventHandler calls onEvent for every add, update and delete of an
// object that matches namespace, name and filter.
func filteredEventHandler(namespace, name string, filter func(metav1.Object) bool, onEvent func()) toolscache.ResourceEventHandlerFuncs {
	notify := func(obj any) {
		if matchesFiltered(obj, namespace, name, filter) && onEvent != nil {
			onEvent()
		}
	}
	return toolscache.ResourceEventHandlerFuncs{
		AddFunc:    notify,
		UpdateFunc: func(_, newObj any) { notify(newObj) },
		DeleteFunc: notify,
	}
}

// notifyWhenSynced calls onEvent once the informer has synced. It gives up
// when ctx is done or stop is closed.
func notifyWhenSynced(ctx context.Context, informer crcache.Informer, stop <-chan struct{}, onEvent func()) {
	select {
	case <-ctx.Done():
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
			case <-ctx.Done():
			case <-stop:
			}
		}()
		synced = toolscache.WaitForCacheSync(done, informer.HasSynced)
	}
	if synced && onEvent != nil {
		onEvent()
	}
}

func watchDuration(deps Deps) time.Duration {
	if deps.AppConfig != nil {
		if dur := deps.AppConfig.Kubernetes.Clusters.TTL.Duration; dur > 0 {
			return dur
		}
	}
	return 2 * time.Minute
}

func accessorForEvent(obj any) (metav1.Object, bool) {
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

func matchesTarget(obj any, namespace, name string) bool {
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

func matchesFiltered(obj any, namespace, name string, filter func(metav1.Object) bool) bool {
	if !matchesTarget(obj, namespace, name) {
		return false
	}
	if filter == nil {
		return true
	}
	accessor, ok := accessorForEvent(obj)
	return ok && filter(accessor)
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
