package models

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	meta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	table "github.com/stokaro/kc/internal/table"
)

// RelatedKind selects what a related-objects folder lists.
type RelatedKind int

const (
	// RelatedOwners lists the owner references of an object.
	RelatedOwners RelatedKind = iota
	// RelatedUsedBy lists the objects that use an object.
	RelatedUsedBy
	// RelatedDependsOn lists the objects an object names in its spec.
	RelatedDependsOn
	// RelatedDiagnosis checks a Service against its pods and EndpointSlices
	// (service_diagnosis.go), or explains the problems of a workload or pod
	// (workload_diagnosis.go). It is entered from the object's folder, not
	// opened by a key.
	RelatedDiagnosis
)

// Path segments of the related-objects folders under an object.
const (
	OwnersSegment    = "owners"
	UsedBySegment    = "used-by"
	DependsOnSegment = "requires"
	DiagnosisSegment = "diagnosis"
)

var (
	// ErrNoOwners means the object has no owner references.
	ErrNoOwners = errors.New("the object has no owner")
	// ErrNoUsedBy means kc does not know what uses objects of this type.
	ErrNoUsedBy = errors.New("kc does not know what uses objects of this type")
	// ErrNoDependsOn means kc does not know what objects of this type
	// depend on.
	ErrNoDependsOn = errors.New("kc does not know what objects of this type depend on")
)

// RelatedSource is a folder whose object rows can open their owners,
// used-by and depends-on folders.
type RelatedSource interface {
	RelatedFolder(ctx context.Context, kind RelatedKind, item ObjectItem) (Folder, error)
}

var _ RelatedSource = (*BaseFolder)(nil)

// RelatedFolder builds a related-objects folder of an object row of this
// folder. It reads the cache and may block until an informer syncs, so call
// it off the UI thread.
func (b *BaseFolder) RelatedFolder(ctx context.Context, kind RelatedKind, item ObjectItem) (Folder, error) {
	return NewRelatedFolder(ctx, b.Deps, kind, item.GVR(), item.Namespace(), item.Name(), item.Path())
}

// RelatedObjectsFolder lists objects related to one object: its owners, the
// objects that use it, or the objects it depends on. Rows come from the
// shared cache only (or a namespace cache when the user may read a type only
// there).
type RelatedObjectsFolder struct {
	*BaseFolder
	kind      RelatedKind
	gvr       schema.GroupVersionResource
	namespace string
	name      string
	finder    UsedByFinder
	dependsOn DependsOnFinder
	reader    *scopedReader
	watches   []relatedWatch // what the rows depend on
	rows      *liveObjectRowSource
	release   func() // gives up the unscoped cluster
}

// NewRelatedFolder builds the folder at objectPath/owners,
// objectPath/used-by or objectPath/requires. It returns ErrNoOwners when
// the object has no owner references, ErrNoUsedBy when no used-by finder is
// registered for gvr, and ErrNoDependsOn when no depends-on finder is.
func NewRelatedFolder(ctx context.Context, deps Deps, kind RelatedKind, gvr schema.GroupVersionResource, namespace, name string, objectPath []string) (*RelatedObjectsFolder, error) {
	deps, release := deps.unscoped()
	return newRelatedFolder(ctx, deps, newScopedReader(depsResolver(deps)), release, kind, gvr, namespace, name, objectPath)
}

func newRelatedFolder(ctx context.Context, deps Deps, reader *scopedReader, release func(), kind RelatedKind, gvr schema.GroupVersionResource, namespace, name string, objectPath []string) (*RelatedObjectsFolder, error) {
	f := &RelatedObjectsFolder{kind: kind, gvr: gvr, namespace: namespace, name: name, reader: reader, release: release}
	fail := func(err error) (*RelatedObjectsFolder, error) {
		reader.Close()
		release()
		return nil, err
	}
	self := relatedWatch{gvr: gvr, namespace: namespace, name: name}
	var segment string
	switch kind {
	case RelatedOwners:
		obj, err := reader.GetByGVR(ctx, gvr, namespace, name)
		if err != nil {
			return fail(err)
		}
		if len(obj.GetOwnerReferences()) == 0 {
			return fail(ErrNoOwners)
		}
		segment = OwnersSegment
		f.watches = []relatedWatch{self}
	case RelatedUsedBy:
		finder, ok := UsedByFor(gvr)
		if !ok {
			return fail(ErrNoUsedBy)
		}
		f.finder = finder
		segment = UsedBySegment
		for _, s := range finder.Sources {
			f.watches = append(f.watches, relatedWatch{gvr: s, namespace: namespace})
		}
	case RelatedDependsOn:
		finder, ok := DependsOnFor(gvr)
		if !ok {
			return fail(ErrNoDependsOn)
		}
		f.dependsOn = finder
		segment = DependsOnSegment
		obj, err := reader.GetByGVR(ctx, gvr, namespace, name)
		if err != nil {
			return fail(err)
		}
		dependencies, err := finder(obj)
		if err != nil {
			return fail(err)
		}
		// Watch the object and the types it names, so a target that
		// appears or goes away shows at once. The types come from the
		// object as it is now; a new type in a later spec is read but not
		// watched until the folder is opened again.
		f.watches = append([]relatedWatch{self}, dependencyWatches(dependencies)...)
	case RelatedDiagnosis:
		watches, ok := diagnosisWatches(gvr, namespace, self)
		if !ok {
			return fail(fmt.Errorf("no diagnosis for %s", gvr))
		}
		// Built on Enter, on the UI thread: no reads here. The first
		// populate and startWatch decide the scopes in the background.
		segment = DiagnosisSegment
		f.watches = watches
	default:
		return fail(fmt.Errorf("unknown related kind %d", kind))
	}
	// Decide the scope of every watched type here, off the UI thread, so
	// startWatch (on it) finds the decisions made.
	if kind != RelatedDiagnosis {
		for _, w := range f.watches {
			reader.scopeFor(w.gvr, w.namespace)
		}
	}
	path := append(append([]string(nil), objectPath...), segment)
	last := "Relation"
	if kind == RelatedDiagnosis {
		last = "Evidence"
	}
	f.BaseFolder = NewBaseFolder(deps, relatedColumns(last), path)
	rows := newLiveObjectRowSourceWithHooks(f.populate, f.BaseFolder.markDirtyFromSource, f.startWatch)
	rows.setTarget(liveSourceTarget{gvr: gvr, namespace: namespace, name: name})
	rows.useBackground(deps)
	rows.watchTTL = watchDuration(deps)
	rows.reportErr = listErrorReporter(deps, gvr, namespace)
	f.rows = rows
	f.BaseFolder.SetRowSource(rows)
	return f, nil
}

// relatedColumns are the columns of a related folder. The last one says
// why the row is listed and never shrinks; the name, resource and
// namespace give up width first in a narrow pane, so the reason gets the
// rest of the line. The bottom status line shows it in full.
func relatedColumns(last string) []table.Column {
	return []table.Column{{Title: " Name", MinWidth: 12}, {Title: "Resource", MinWidth: 7}, {Title: "Namespace", MinWidth: 7}, {Title: last}}
}

// relatedWatch is one watch of a related folder: a type in a namespace,
// filtered to one name when name is set.
type relatedWatch struct {
	relatedScopeKey
	name string
}

// dependencyWatches returns one watch per target type and namespace, in
// order. A type with one target is filtered to its name, so events of other
// objects of the type (every node, every ConfigMap of the namespace) do not
// refresh the folder.
func dependencyWatches(deps []Dependency) []relatedWatch {
	var out []relatedWatch
	index := make(map[relatedScopeKey]int)
	for _, d := range deps {
		k := relatedScopeKey{gvr: d.GVR, namespace: d.Namespace}
		i, ok := index[k]
		if !ok {
			index[k] = len(out)
			out = append(out, relatedWatch{relatedScopeKey: k, name: d.Name})
			continue
		}
		if out[i].name != d.Name {
			out[i].name = ""
		}
	}
	return out
}

// Kind reports what the folder lists.
func (f *RelatedObjectsFolder) Kind() RelatedKind { return f.kind }

// Close stops the folder's watches and gives up the clusters it holds.
func (f *RelatedObjectsFolder) Close() {
	if f.rows != nil {
		f.rows.Close()
	}
	if f.reader != nil {
		f.reader.Close()
	}
	if f.release != nil {
		f.release()
		f.release = nil
	}
}

// KubectlCommand prints what the folder is built from.
func (f *RelatedObjectsFolder) KubectlCommand() string {
	switch f.kind {
	case RelatedDiagnosis:
		if hasRouteDiagnosis(f.gvr) {
			return kubectlCommand(f.Deps, "describe", kubectlResource(f.gvr), f.name, "-n", f.namespace) + "  # and its backend services"
		}
		if f.gvr != serviceGVR {
			return kubectlCommand(f.Deps, "describe", kubectlResource(f.gvr), f.name, "-n", f.namespace) + "  # and its pods, events and claims"
		}
		return kubectlCommand(f.Deps, "get", "service,pods,endpointslices", "-n", f.namespace) + "  # behind service/" + f.name
	case RelatedOwners, RelatedDependsOn:
		args := []string{"get", kubectlResource(f.gvr), f.name}
		if f.namespace != "" {
			args = append(args, "-n", f.namespace)
		}
		if f.kind == RelatedDependsOn {
			return kubectlCommand(f.Deps, append(args, "-o", "yaml")...) + "  # the objects its spec names"
		}
		return kubectlCommand(f.Deps, append(args, "-o", "jsonpath={.metadata.ownerReferences}")...)
	}
	sources := make([]string, 0, len(f.finder.Sources))
	for _, s := range f.finder.Sources {
		sources = append(sources, kubectlResource(s))
	}
	args := []string{"get", strings.Join(sources, ",")}
	if f.namespace != "" {
		args = append(args, "-n", f.namespace)
	} else {
		args = append(args, "-A")
	}
	return kubectlCommand(f.Deps, args...) + "  # that use " + kubectlResource(f.gvr) + "/" + f.name
}

// startWatch watches what the rows depend on in the scope decided for each
// type. A type the user may not list and watch is not watched: its informer
// would never sync. It runs on the UI thread: pairs not decided yet are
// decided and watched in the background.
func (f *RelatedObjectsFolder) startWatch(onEvent, onStop func()) (func(), error) {
	var (
		mu      sync.Mutex
		cancels []func()
		stopped bool
	)
	cancelAll := func() {
		mu.Lock()
		stopped = true
		cs := cancels
		cancels = nil
		mu.Unlock()
		for _, c := range cs {
			c()
		}
	}
	start := func(w relatedWatch, sc relatedScope) error {
		if sc.scope == scopeDenied || sc.deps.Cl == nil {
			return nil
		}
		cancel, err := startInformerForResource(sc.deps, w.gvr, w.namespace, w.name, onEvent, nil)
		if err != nil || cancel == nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		if stopped {
			cancel()
			return nil
		}
		cancels = append(cancels, cancel)
		return nil
	}
	var later []relatedWatch
	for _, w := range f.watches {
		sc, ok := f.reader.decided(w.gvr, w.namespace)
		if !ok {
			later = append(later, w)
			continue
		}
		if err := start(w, sc); err != nil {
			cancelAll()
			return nil, err
		}
	}
	if len(later) > 0 {
		go func() {
			for _, w := range later {
				mu.Lock()
				done := stopped
				mu.Unlock()
				if done {
					return
				}
				// An error is reported by the informer start itself; the
				// rows still refresh on the other watches.
				_ = start(w, f.reader.scopeFor(w.gvr, w.namespace))
			}
			// The rows may have been read before these watches existed.
			onEvent()
		}()
	}
	return func() {
		cancelAll()
		if onStop != nil {
			onStop()
		}
	}, nil
}

func (f *RelatedObjectsFolder) populate(ctx context.Context) ([]table.Row, error) {
	if f.kind == RelatedDiagnosis {
		return f.diagnosisRows(ctx)
	}
	log := ctrllog.FromContext(ctx).WithName("related")
	refs, err := f.findRefs(ctx)
	if err != nil {
		return nil, err
	}
	log.V(1).Info("related objects", "kind", f.kind, "gvr", f.gvr.String(), "namespace", f.namespace, "name", f.name, "count", len(refs))
	return relatedRows(f.Deps, f.rowDeps, f.Path(), refs), nil
}

func (f *RelatedObjectsFolder) findRefs(ctx context.Context) ([]RelatedRef, error) {
	switch f.kind {
	case RelatedOwners:
		obj, err := f.reader.GetByGVR(ctx, f.gvr, f.namespace, f.name)
		if err != nil {
			return nil, err
		}
		refs := ResolveOwners(f.Deps.Cl.RESTMapper(), f.namespace, obj.GetOwnerReferences())
		return checkOwners(ctx, f.reader, refs), nil
	case RelatedDependsOn:
		obj, err := f.reader.GetByGVR(ctx, f.gvr, f.namespace, f.name)
		if err != nil {
			return nil, err
		}
		return f.dependencyRefs(ctx, obj)
	default:
		return findUsers(ctx, f.reader, f.Deps.Cl.RESTMapper(), f.finder, f.namespace, f.name)
	}
}

func (f *RelatedObjectsFolder) dependencyRefs(ctx context.Context, obj *unstructured.Unstructured) ([]RelatedRef, error) {
	found, err := f.dependsOn(obj)
	if err != nil {
		return nil, err
	}
	return dependencyRefs(ctx, f.reader, found), nil
}

// rowDeps returns the Deps a row's viewer and child folder read through:
// the scope decided for its type and namespace, so a user who may read a
// type only in a namespace can open it from here.
func (f *RelatedObjectsFolder) rowDeps(ref RelatedRef) Deps {
	if sc, ok := f.reader.decided(ref.GVR, ref.Namespace); ok && sc.scope != scopeDenied && sc.deps.Cl != nil {
		return sc.deps
	}
	return f.Deps
}

// findUsers runs a used-by finder and adds the controllers of the pods it
// found. A source the user may not list gives a row that says the result is
// incomplete instead of an error or an empty folder.
func findUsers(ctx context.Context, r RelatedReader, mapper meta.RESTMapper, finder UsedByFinder, namespace, name string) ([]RelatedRef, error) {
	found, err := finder.Find(ctx, r, namespace, name)
	if err != nil {
		partial, ok := partialRef(namespace, err)
		if !ok {
			return nil, err
		}
		return []RelatedRef{partial}, nil
	}
	refs := mergeRelations(slices.Concat(found, workloadRefs(ctx, r, mapper, found)))
	sort.SliceStable(refs, func(i, j int) bool {
		a, b := refs[i], refs[j]
		if a.GVR.String() != b.GVR.String() {
			return a.GVR.String() < b.GVR.String()
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return refs, nil
}

// RelatedRowID is the row ID of a related object: the full GVR, the
// namespace and the name, because one folder mixes types and namespaces.
func RelatedRowID(ref RelatedRef) string {
	if ref.GVR.Empty() {
		return strings.Join([]string{"unresolved", ref.APIVersion, ref.Kind, ref.Namespace, ref.Name}, "/")
	}
	return strings.Join([]string{ref.GVR.GroupVersion().String(), ref.GVR.Resource, ref.Namespace, ref.Name}, "/")
}

// relatedRows renders refs as rows. An object that can be opened is an
// object row that behaves like the row in its own list: Enter opens its
// child folder when the type has one, F3 shows its YAML. A reference that
// cannot be opened is a dim plain row that says why. depsFor returns the
// Deps a row reads through; nil uses deps for every row.
func relatedRows(deps Deps, depsFor func(RelatedRef) Deps, folderPath []string, refs []RelatedRef) []table.Row {
	return relatedRowsPrefixed(deps, depsFor, folderPath, "", refs)
}

// relatedRowsPrefixed is relatedRows with idPrefix before every row ID, for
// a folder that lists one object in several sections.
func relatedRowsPrefixed(deps Deps, depsFor func(RelatedRef) Deps, folderPath []string, idPrefix string, refs []RelatedRef) []table.Row {
	rows := make([]table.Row, 0, len(refs))
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		id := idPrefix + RelatedRowID(ref)
		if seen[id] {
			continue
		}
		seen[id] = true
		resource := ref.Kind
		if !ref.GVR.Empty() {
			resource = kubectlResource(ref.GVR)
		}
		relation := ref.Relation
		basePath := append(append([]string(nil), folderPath...), ref.Name)
		if ref.Problem != "" {
			relation += ": " + ref.Problem
			row := NewRowItem(id, []string{ref.Name, resource, ref.Namespace, relation}, basePath, DimStyle())
			row.details = fmt.Sprintf("%s %s: %s", resource, ref.Name, ref.Problem)
			rows = append(rows, row)
			continue
		}
		rowDeps := deps
		if depsFor != nil {
			rowDeps = depsFor(ref)
		}
		ctor, hasChild := ChildFor(ref.GVR)
		title := ref.Name
		if hasChild {
			title = "/" + ref.Name
		}
		obj := NewObjectRow(id, []string{title, resource, ref.Namespace, relation}, basePath, ref.GVR, ref.Namespace, ref.Name, WhiteStyle())
		obj.WithViewContent(objectViewContent(rowDeps, ref.GVR, ref.Namespace, ref.Name))
		args := []string{"get", kubectlResource(ref.GVR), ref.Name}
		if ref.Namespace != "" {
			args = append(args, "-n", ref.Namespace)
		}
		obj.SetViewCommand(kubectlCommand(rowDeps, append(args, "-o", "yaml")...))
		obj.RowItem.details = objectDetails(ref.Namespace, ref.Name, ref.GVR.Resource, ref.GVR.GroupVersion().String())
		if relation != "" {
			obj.RowItem.details += ": " + relation
		}
		if hasChild && ctor != nil {
			r := ref
			rows = append(rows, NewObjectWithChildItem(obj, func() (Folder, error) {
				return ctor(rowDeps, r.Namespace, r.Name, basePath), nil
			}))
			continue
		}
		rows = append(rows, obj)
	}
	return rows
}
