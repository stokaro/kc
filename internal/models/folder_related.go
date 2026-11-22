package models

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	table "github.com/sttts/kc/internal/table"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// RelatedKind selects what a related-objects folder lists.
type RelatedKind int

const (
	// RelatedOwners lists the owner references of an object.
	RelatedOwners RelatedKind = iota
	// RelatedUsedBy lists the objects that use an object.
	RelatedUsedBy
)

// Path segments of the related-objects folders under an object.
const (
	OwnersSegment = "owners"
	UsedBySegment = "used-by"
)

var (
	// ErrNoOwners means the object has no owner references.
	ErrNoOwners = errors.New("the object has no owner")
	// ErrNoUsedBy means kc does not know what uses objects of this type.
	ErrNoUsedBy = errors.New("kc does not know what uses objects of this type")
)

// RelatedSource is a folder whose object rows can open their owners and
// used-by folders.
type RelatedSource interface {
	RelatedFolder(ctx context.Context, kind RelatedKind, item ObjectItem) (Folder, error)
}

var _ RelatedSource = (*BaseFolder)(nil)

// RelatedFolder builds the owners or used-by folder of an object row of this
// folder. It reads the cache and may block until an informer syncs, so call
// it off the UI thread.
func (b *BaseFolder) RelatedFolder(ctx context.Context, kind RelatedKind, item ObjectItem) (Folder, error) {
	return NewRelatedFolder(ctx, b.Deps, kind, item.GVR(), item.Namespace(), item.Name(), item.Path())
}

// RelatedObjectsFolder lists objects related to one object: its owners or
// the objects that use it. Rows come from the shared cache only.
type RelatedObjectsFolder struct {
	*BaseFolder
	kind      RelatedKind
	gvr       schema.GroupVersionResource
	namespace string
	name      string
	finder    UsedByFinder
	rows      *liveObjectRowSource
}

// NewRelatedFolder builds the folder at objectPath/owners or
// objectPath/used-by. It returns ErrNoOwners when the object has no owner
// references and ErrNoUsedBy when no used-by finder is registered for gvr.
func NewRelatedFolder(ctx context.Context, deps Deps, kind RelatedKind, gvr schema.GroupVersionResource, namespace, name string, objectPath []string) (*RelatedObjectsFolder, error) {
	deps = deps.unscoped()
	f := &RelatedObjectsFolder{kind: kind, gvr: gvr, namespace: namespace, name: name}
	segment := OwnersSegment
	switch kind {
	case RelatedOwners:
		obj, err := deps.Cl.GetByGVR(ctx, gvr, namespace, name)
		if err != nil {
			return nil, err
		}
		if len(obj.GetOwnerReferences()) == 0 {
			return nil, ErrNoOwners
		}
	case RelatedUsedBy:
		finder, ok := UsedByFor(gvr)
		if !ok {
			return nil, ErrNoUsedBy
		}
		f.finder = finder
		segment = UsedBySegment
	default:
		return nil, fmt.Errorf("unknown related kind %d", kind)
	}
	path := append(append([]string(nil), objectPath...), segment)
	f.BaseFolder = NewBaseFolder(deps, []table.Column{{Title: " Name"}, {Title: "Resource"}, {Title: "Namespace"}, {Title: "Relation"}}, path)
	rows := newLiveObjectRowSourceWithHooks(f.populate, f.BaseFolder.markDirtyFromSource, f.startWatch)
	rows.setTarget(liveSourceTarget{gvr: gvr, namespace: namespace, name: name})
	rows.useBackground(deps)
	rows.watchTTL = watchDuration(deps)
	rows.reportErr = listErrorReporter(deps, gvr, namespace)
	f.rows = rows
	f.BaseFolder.SetRowSource(rows)
	return f, nil
}

// Kind reports whether the folder lists owners or users.
func (f *RelatedObjectsFolder) Kind() RelatedKind { return f.kind }

// Close stops the folder's watches.
func (f *RelatedObjectsFolder) Close() {
	if f.rows != nil {
		f.rows.Close()
	}
}

// KubectlCommand prints what the folder is built from.
func (f *RelatedObjectsFolder) KubectlCommand() string {
	if f.kind == RelatedOwners {
		args := []string{"get", kubectlResource(f.gvr), f.name}
		if f.namespace != "" {
			args = append(args, "-n", f.namespace)
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

// startWatch watches what the rows depend on: the object itself for
// owners, the finder's source types for used-by, in the object's namespace.
func (f *RelatedObjectsFolder) startWatch(onEvent func(), onStop func()) (func(), error) {
	type watch struct {
		gvr  schema.GroupVersionResource
		name string
	}
	var watches []watch
	if f.kind == RelatedOwners {
		watches = append(watches, watch{gvr: f.gvr, name: f.name})
	} else {
		for _, s := range f.finder.Sources {
			watches = append(watches, watch{gvr: s})
		}
	}
	var cancels []func()
	cancelAll := func() {
		for _, c := range cancels {
			c()
		}
	}
	for _, w := range watches {
		cancel, err := startInformerForResource(f.Deps, w.gvr, f.namespace, w.name, onEvent, nil)
		if err != nil {
			cancelAll()
			return nil, err
		}
		if cancel != nil {
			cancels = append(cancels, cancel)
		}
	}
	return func() {
		cancelAll()
		if onStop != nil {
			onStop()
		}
	}, nil
}

func (f *RelatedObjectsFolder) populate(ctx context.Context) ([]table.Row, error) {
	log := ctrllog.FromContext(ctx).WithName("related")
	var refs []RelatedRef
	switch f.kind {
	case RelatedOwners:
		obj, err := f.Deps.Cl.GetByGVR(ctx, f.gvr, f.namespace, f.name)
		if err != nil {
			return nil, err
		}
		refs = ResolveOwners(f.Deps.Cl.RESTMapper(), f.namespace, obj.GetOwnerReferences())
		refs = checkOwners(ctx, f.Deps.Cl, refs)
	case RelatedUsedBy:
		found, err := f.finder.Find(ctx, f.Deps.Cl, f.namespace, f.name)
		if err != nil {
			return nil, err
		}
		refs = found
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
	}
	log.V(1).Info("related objects", "kind", f.kind, "gvr", f.gvr.String(), "namespace", f.namespace, "name", f.name, "count", len(refs))
	return relatedRows(f.Deps, f.Path(), refs), nil
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
// cannot be opened is a dim plain row that says why.
func relatedRows(deps Deps, folderPath []string, refs []RelatedRef) []table.Row {
	rows := make([]table.Row, 0, len(refs))
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		id := RelatedRowID(ref)
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
		ctor, hasChild := ChildFor(ref.GVR)
		title := ref.Name
		if hasChild {
			title = "/" + ref.Name
		}
		obj := NewObjectRow(id, []string{title, resource, ref.Namespace, relation}, basePath, ref.GVR, ref.Namespace, ref.Name, WhiteStyle())
		obj.WithViewContent(objectViewContent(deps, ref.GVR, ref.Namespace, ref.Name))
		args := []string{"get", kubectlResource(ref.GVR), ref.Name}
		if ref.Namespace != "" {
			args = append(args, "-n", ref.Namespace)
		}
		obj.SetViewCommand(kubectlCommand(deps, append(args, "-o", "yaml")...))
		obj.RowItem.details = objectDetails(ref.Namespace, ref.Name, ref.GVR.Resource, ref.GVR.GroupVersion().String())
		if hasChild && ctor != nil {
			r := ref
			rows = append(rows, NewObjectWithChildItem(obj, func() (Folder, error) {
				return ctor(deps, r.Namespace, r.Name, basePath), nil
			}))
			continue
		}
		rows = append(rows, obj)
	}
	return rows
}
