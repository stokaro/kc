package models

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/helm"
	table "github.com/stokaro/kc/internal/table"
)

// HelmReleaseFolder shows one stored revision of a release: values,
// manifest, notes, hooks, its objects and, for the newest revision, the
// revision history. Revision 0 means the newest revision.
type HelmReleaseFolder struct {
	*BaseFolder
	reader    *helmReader
	namespace string
	release   string
	revision  int
	src       *liveObjectRowSource
}

var helmReleaseFolderColumns = []table.Column{{Title: " Name"}, {Title: "Stored content"}}

func newHelmReleaseFolder(deps Deps, reader *helmReader, namespace, release string, revision int, path []string) *HelmReleaseFolder {
	base := NewBaseFolder(deps, helmReleaseFolderColumns, path)
	f := &HelmReleaseFolder{BaseFolder: base, reader: reader, namespace: namespace, release: release, revision: revision}
	f.src = newHelmSource(base, reader, namespace, release, f.populate)
	base.SetRowSource(f.src)
	return f
}

// Namespace returns the release namespace.
func (f *HelmReleaseFolder) Namespace() string { return f.namespace }

// Close stops the watch.
func (f *HelmReleaseFolder) Close() { f.src.Close() }

func (f *HelmReleaseFolder) populate(ctx context.Context) ([]table.Row, error) {
	h, ok, err := f.reader.history(ctx, f.namespace, f.release)
	if ae, isAccess := isHelmAccessError(err); isAccess {
		return []table.Row{helmAccessNotice(f.Deps, ae, f.namespace, f.Path(), len(helmReleaseFolderColumns))}, nil
	}
	if err != nil {
		return nil, err
	}
	return helmReleaseFolderRows(f.Deps, f.reader, h, ok, f.revision, f.Path()), nil
}

// helmReleaseFolderRows builds the rows of a release or revision folder.
func helmReleaseFolderRows(deps Deps, reader *helmReader, h helm.History, found bool, revision int, path []string) []table.Row {
	cols := len(helmReleaseFolderColumns)
	if !found {
		detail := fmt.Sprintf("No revision of release %s is stored in %s any more; it was probably uninstalled.", h.Release, h.Namespace)
		return []table.Row{helmNotice("__helm_gone__", "! release is no longer stored", detail, path, cols)}
	}
	var rows []table.Row
	rec := h.Latest()
	if revision > 0 {
		r, ok := h.Revision(revision)
		if !ok {
			detail := fmt.Sprintf("Revision %d of release %s is no longer stored. Helm keeps only the last revisions (helm upgrade --history-max).", revision, h.Release)
			return []table.Row{helmNotice("__helm_gone__", "! revision is no longer stored", detail, path, cols)}
		}
		rec = r
	}
	rel, err := reader.decode(rec)
	if err != nil {
		detail := fmt.Sprintf("Revision %d of release %s cannot be decoded.\n\n%v\n\nThe record is in Secret %s/%s.", rec.Revision, h.Release, err, rec.Namespace, rec.Secret)
		rows = append(rows, helmNotice("__helm_corrupt__", fmt.Sprintf("! revision %d cannot be decoded", rec.Revision), detail, path, cols))
	} else {
		rows = append(rows, helmContentRows(deps, rel, revision, path)...)
		rows = append(rows, newHelmResourcesEntry(deps, rel, revision, path))
	}
	if revision == 0 {
		histPath := append(slices.Clone(path), "history")
		item := NewChildResourceItem("history", []string{"/history", fmt.Sprintf("%d stored %s", len(h.Revisions), plural(len(h.Revisions), "revision", "revisions"))}, histPath, func() (Folder, error) {
			return newHelmHistoryFolder(deps, reader, h.Namespace, h.Release, histPath), nil
		})
		rows = append(rows, item)
	}
	return rows
}

// helmContentRows are the viewable parts of a stored revision.
func helmContentRows(deps Deps, rel *helm.Release, revision int, path []string) []table.Row {
	stored := fmt.Sprintf("stored in revision %d", rel.Version)
	notesDesc := stored
	if strings.TrimSpace(rel.Info.Notes) == "" {
		notesDesc = "none"
	}
	hooksDesc := "none"
	if n := len(rel.Hooks); n > 0 {
		hooksDesc = fmt.Sprintf("%d hooks, %s", n, stored)
	}
	parts := []struct {
		id, desc, lang string
		body           func() (string, error)
	}{
		{"values", "user-supplied values, " + stored, "yaml", rel.ValuesYAML},
		{"manifest", "rendered manifest, " + stored, "yaml", func() (string, error) { return rel.Manifest, nil }},
		{"notes", notesDesc, "", func() (string, error) { return rel.Info.Notes, nil }},
		{"hooks", hooksDesc, "yaml", func() (string, error) { return rel.HooksManifest(), nil }},
	}
	rows := make([]table.Row, 0, len(parts))
	for _, p := range parts {
		item := NewSimpleItem(p.id, []string{p.id, p.desc}, append(slices.Clone(path), p.id), WhiteStyle())
		item.SetDetails(fmt.Sprintf("%s of %s, %s", p.id, rel.Name, stored))
		title := fmt.Sprintf("%s %s (revision %d, stored)", rel.Name, p.id, rel.Version)
		ext, mime := ".txt", ""
		if p.lang == "yaml" {
			ext, mime = ".yaml", "application/yaml"
		}
		filename := fmt.Sprintf("%s-%d-%s%s", rel.Name, rel.Version, p.id, ext)
		body, lang := p.body, p.lang
		item.WithViewContent(func() (string, string, string, string, string, error) {
			text, err := body()
			if err != nil {
				return "", "", "", "", "", err
			}
			return title, text, lang, mime, filename, nil
		})
		args := append([]string{"get", p.id, rel.Name, "-n", rel.Namespace}, revisionArgs(revision)...)
		item.SetViewCommand(helmCommand(deps, args...))
		rows = append(rows, item)
	}
	return rows
}

func revisionArgs(revision int) []string {
	if revision <= 0 {
		return nil
	}
	return []string{"--revision", strconv.Itoa(revision)}
}

// HelmHistoryFolder lists every stored revision of a release.
type HelmHistoryFolder struct {
	*BaseFolder
	reader    *helmReader
	namespace string
	release   string
	src       *liveObjectRowSource
}

var helmHistoryColumns = []table.Column{
	{Title: " Revision"}, {Title: "Updated"}, {Title: "Status"}, {Title: "Chart"}, {Title: "App version"}, {Title: "Description"},
}

func newHelmHistoryFolder(deps Deps, reader *helmReader, namespace, release string, path []string) *HelmHistoryFolder {
	base := NewBaseFolder(deps, helmHistoryColumns, path)
	f := &HelmHistoryFolder{BaseFolder: base, reader: reader, namespace: namespace, release: release}
	f.src = newHelmSource(base, reader, namespace, release, f.populate)
	base.SetRowSource(f.src)
	return f
}

// Namespace returns the release namespace.
func (f *HelmHistoryFolder) Namespace() string { return f.namespace }

// Close stops the watch.
func (f *HelmHistoryFolder) Close() { f.src.Close() }

func (f *HelmHistoryFolder) populate(ctx context.Context) ([]table.Row, error) {
	h, ok, err := f.reader.history(ctx, f.namespace, f.release)
	if ae, isAccess := isHelmAccessError(err); isAccess {
		return []table.Row{helmAccessNotice(f.Deps, ae, f.namespace, f.Path(), len(helmHistoryColumns))}, nil
	}
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return helmHistoryRows(f.Deps, f.reader, h, f.Path()), nil
}

// helmHistoryRows has one row per stored revision, newest first, like
// "helm history" in reverse.
func helmHistoryRows(deps Deps, reader *helmReader, h helm.History, path []string) []table.Row {
	rows := make([]table.Row, 0, len(h.Revisions))
	for _, rec := range h.Revisions {
		rev := rec.Revision
		status, chart, appVersion, updated, desc := rec.Status, "?", "", "", ""
		rel, err := reader.decode(rec)
		if err != nil {
			desc = "cannot decode: " + err.Error()
		} else {
			if rel.Info.Status != "" {
				status = rel.Info.Status
			}
			chart = rel.ChartName()
			appVersion = rel.AppVersion()
			updated = formatHelmTime(rel.Info.LastDeployed)
			desc = rel.Info.Description
		}
		revPath := append(slices.Clone(path), strconv.Itoa(rev))
		item := NewChildResourceItem(strconv.Itoa(rev), []string{"/" + strconv.Itoa(rev), updated, status, chart, appVersion, desc}, revPath, func() (Folder, error) {
			return newHelmReleaseFolder(deps, reader, h.Namespace, h.Release, rev, revPath), nil
		})
		item.SetDetails(fmt.Sprintf("revision %d of %s: %s", rev, h.Release, status))
		rows = append(rows, item)
	}
	return rows
}

// newHelmResourcesEntry opens the objects of a stored manifest.
func newHelmResourcesEntry(deps Deps, rel *helm.Release, revision int, parent []string) *ChildResourceItem {
	path := append(slices.Clone(parent), "resources")
	objs, _ := helm.SplitManifest(rel.Manifest)
	desc := fmt.Sprintf("%d %s of the manifest, linked to the live objects", len(objs), plural(len(objs), "object", "objects"))
	item := NewChildResourceItem("resources", []string{"/resources", desc}, path, func() (Folder, error) {
		return newHelmResourcesFolder(deps, rel, revision, path), nil
	})
	item.SetDetails("objects of " + rel.Name + " revision " + strconv.Itoa(rel.Version))
	return item
}

// HelmResourcesFolder lists the objects of a stored manifest and whether
// each exists in the cluster now. Enter on a row opens the live object in
// the other panel.
type HelmResourcesFolder struct {
	*BaseFolder
	rel      *helm.Release
	revision int
	src      *liveObjectRowSource
}

var helmResourceColumns = []table.Column{{Title: " Object"}, {Title: "Namespace"}, {Title: "API version"}, {Title: "Live"}}

func newHelmResourcesFolder(deps Deps, rel *helm.Release, revision int, path []string) *HelmResourcesFolder {
	base := NewBaseFolder(deps, helmResourceColumns, path)
	f := &HelmResourcesFolder{BaseFolder: base, rel: rel, revision: revision}
	// The manifest is a stored snapshot; there is nothing to watch. The
	// live column is read when the folder loads.
	f.src = newLiveObjectRowSourceWithHooks(f.populate, base.markDirtyFromSource, nil)
	f.src.useBackground(deps)
	f.src.reportErr = listErrorReporter(deps, secretsGVR, rel.Namespace)
	base.SetRowSource(f.src)
	return f
}

// Namespace returns the release namespace.
func (f *HelmResourcesFolder) Namespace() string { return f.rel.Namespace }

// Close stops pending refreshes.
func (f *HelmResourcesFolder) Close() { f.src.Close() }

func (f *HelmResourcesFolder) populate(ctx context.Context) ([]table.Row, error) {
	var mapper meta.RESTMapper
	var get liveGetter
	if f.Deps.Cl != nil {
		mapper = f.Deps.Cl.RESTMapper()
		get = func(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
			_, err := f.Deps.Cl.GetByGVR(ctx, gvr, namespace, name)
			return err
		}
	}
	return helmResourceRows(ctx, mapper, get, f.rel, f.Path()), nil
}

// liveGetter reads one live object; only the error matters.
type liveGetter func(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error

// liveGetTimeout bounds the read of one live object.
const liveGetTimeout = 5 * time.Second

// Live column values.
const (
	liveYes       = "yes"
	liveMissing   = "missing"
	liveDenied    = "denied"
	liveUnknown   = "?"
	liveNotServed = "type not served"
	liveNoName    = "no name"
)

// helmResourceRows resolves each manifest object to its GVR with the REST
// mapper and reads whether it exists. Reads are direct GETs, not watches:
// a release can name many types, and starting an informer per type would
// watch the whole cluster for a one-off answer. After a type is denied or
// times out, its other objects are not read.
func helmResourceRows(ctx context.Context, mapper meta.RESTMapper, get liveGetter, rel *helm.Release, path []string) []table.Row {
	objs, errs := helm.SplitManifest(rel.Manifest)
	rows := make([]table.Row, 0, len(objs)+len(errs))
	skip := make(map[schema.GroupVersionResource]string)
	seen := make(map[string]int)
	for _, obj := range objs {
		item := &HelmResourceItem{obj: obj, release: rel.Name, revision: rel.Version}
		ns := obj.Namespace
		gv, err := schema.ParseGroupVersion(obj.APIVersion)
		var mapping *meta.RESTMapping
		if err == nil && mapper != nil {
			mapping, err = mapper.RESTMapping(schema.GroupKind{Group: gv.Group, Kind: obj.Kind}, gv.Version)
		}
		var live string
		switch {
		case mapper == nil:
			live = liveUnknown
			item.missing = "the cluster's API types are not known yet; try again"
		case err != nil:
			live = liveNotServed
			item.missing = fmt.Sprintf("%s %s is not served by the cluster, so %s cannot exist there", obj.APIVersion, obj.Kind, obj.Name)
		default:
			namespaced := mapping.Scope.Name() == meta.RESTScopeNameNamespace
			if namespaced && ns == "" {
				ns = rel.Namespace
			}
			if !namespaced {
				ns = ""
			}
			item.ref = ObjectRef{GVR: mapping.Resource, Namespaced: namespaced, Namespace: ns, Name: obj.Name}
			live, item.missing = liveState(ctx, get, skip, item.ref, obj.Kind)
		}
		item.live = live
		key := obj.APIVersion + "/" + obj.Kind + "/" + ns + "/" + obj.Name
		seen[key]++
		id := key
		if n := seen[key]; n > 1 {
			id = fmt.Sprintf("%s#%d", key, n)
		}
		cells := []string{"@" + obj.Kind + "/" + obj.Name, ns, obj.APIVersion, live}
		item.RowItem = NewRowItem(id, cells, append(slices.Clone(path), obj.Kind+"/"+obj.Name), WhiteStyle())
		if live == liveMissing || live == liveNotServed {
			item.RowItem = NewRowItem(id, cells, append(slices.Clone(path), obj.Kind+"/"+obj.Name), &helmNoticeStyle)
		}
		if obj.Source != "" {
			item.SetDetails(obj.Kind + "/" + obj.Name + " from " + obj.Source)
		}
		rows = append(rows, item)
	}
	for i, err := range errs {
		id := fmt.Sprintf("__helm_manifest_error_%d__", i)
		rows = append(rows, helmNotice(id, "! manifest document skipped", err.Error(), path, len(helmResourceColumns)))
	}
	return rows
}

func liveState(ctx context.Context, get liveGetter, skip map[schema.GroupVersionResource]string, ref ObjectRef, kind string) (state, missing string) {
	if ref.Name == "" {
		return liveNoName, "the manifest object has no name"
	}
	if get == nil {
		return liveUnknown, ""
	}
	if state, ok := skip[ref.GVR]; ok {
		return state, ""
	}
	gctx, cancel := context.WithTimeout(ctx, liveGetTimeout)
	err := get(gctx, ref.GVR, ref.Namespace, ref.Name)
	cancel()
	switch {
	case err == nil:
		return liveYes, ""
	case apierrors.IsNotFound(err):
		where := ""
		if ref.Namespace != "" {
			where = " in " + ref.Namespace
		}
		return liveMissing, fmt.Sprintf("%s %s is in the release manifest but not in the cluster%s: it was deleted, or the release did not create it", kind, ref.Name, where)
	case apierrors.IsForbidden(err):
		skip[ref.GVR] = liveDenied
		return liveDenied, ""
	case errors.Is(err, context.DeadlineExceeded):
		skip[ref.GVR] = liveUnknown
		return liveUnknown, ""
	default:
		return liveUnknown, ""
	}
}

// HelmResourceItem is one object of a stored release manifest.
type HelmResourceItem struct {
	*RowItem
	obj      helm.ManifestObject
	release  string
	revision int
	ref      ObjectRef
	live     string
	missing  string
}

var _ ObjectLink = (*HelmResourceItem)(nil)

// LinkTarget returns the live object. ref.GVR is empty when the type is
// not served.
func (i *HelmResourceItem) LinkTarget() (ObjectRef, string) { return i.ref, i.missing }

// ViewContent shows the object as the release stored it, not as it is now.
//
//revive:disable-next-line:function-result-limit the Viewable interface fixes these six results; internal/ui calls it
func (i *HelmResourceItem) ViewContent() (title, body, lang, mime, filename string, err error) {
	title = fmt.Sprintf("%s/%s (stored in %s revision %d)", i.obj.Kind, i.obj.Name, i.release, i.revision)
	filename = fmt.Sprintf("%s-%s.yaml", strings.ToLower(i.obj.Kind), i.obj.Name)
	return title, i.obj.Doc, "yaml", "application/yaml", filename, nil
}
