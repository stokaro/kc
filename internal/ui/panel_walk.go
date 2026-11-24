package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stokaro/kc/internal/models"
	navui "github.com/stokaro/kc/internal/navigation"
)

const (
	// pathWaitTimeout bounds how long a walk waits for one path element
	// to appear. Lists fill from informers and the root shows a type only
	// once its emptiness is known, so a fresh element can take a moment.
	pathWaitTimeout = 10 * time.Second
	// pathWaitInterval is how often a waiting walk looks again.
	pathWaitInterval = 250 * time.Millisecond
)

// typeResolver maps a typed resource name to its group and resource.
type typeResolver func(name string) (schema.GroupResource, error)

// typeFolder is a folder that lists resource types (the root, a
// namespace's resources, the cluster resources).
type typeFolder interface {
	TypeItem(schema.GroupResource) (models.Item, string, bool)
}

// errWalkMoved reports that the navigator left the folder the walk put it
// on, so the user navigated meanwhile; the walk stops without a message.
var errWalkMoved = errors.New("panel moved during navigation")

// pathWalk moves a navigator along path elements, one attempt per advance
// call. An element that is not there yet is retried on the next call until
// timeout has passed since the walk started waiting for it. advance runs
// on the Bubble Tea update goroutine; the waiting happens between calls.
type pathWalk struct {
	nav        *navui.Navigator
	elems      []PathElem
	selectLast bool
	resolve    typeResolver
	timeout    time.Duration

	idx      int
	since    time.Time // when waiting for elems[idx] started
	cur      models.Folder
	resolved map[int]schema.GroupResource
	// selected is the row ID the walk left the cursor on, "" if none.
	selected string
}

func newPathWalk(nav *navui.Navigator, elems []PathElem, selectLast bool, resolve typeResolver, now time.Time) *pathWalk {
	return &pathWalk{
		nav:        nav,
		elems:      elems,
		selectLast: selectLast,
		resolve:    resolve,
		timeout:    pathWaitTimeout,
		since:      now,
		cur:        nav.Current(),
		resolved:   make(map[int]schema.GroupResource),
	}
}

// advance takes as many steps as it can. It returns done when the walk
// finished, and an error when it failed or the navigator moved
// (errWalkMoved). With neither, the walk waits for an element and advance
// must be called again later.
func (w *pathWalk) advance(ctx context.Context, now time.Time) (done bool, err error) {
	if w.nav.Current() != w.cur {
		return false, errWalkMoved
	}
	for w.idx < len(w.elems) {
		elem := w.elems[w.idx]
		last := w.idx == len(w.elems)-1
		if elem.isParent() {
			w.nav.Back()
			w.selected = ""
			w.step(now)
			continue
		}
		folder := w.nav.Current()
		if folder == nil {
			return false, errors.New("no folder")
		}
		item, id, err := w.find(ctx, folder, elem)
		if err != nil {
			return false, err
		}
		if item == nil {
			if now.Sub(w.since) >= w.timeout {
				return false, fmt.Errorf("%s not found in %s after %s", elem, folderLocation(folder), w.timeout)
			}
			return false, nil
		}
		w.nav.SetSelectionID(id)
		w.selected = id
		enterable, canEnter := item.(models.Enterable)
		if last && (w.selectLast || !canEnter) {
			w.step(now)
			continue
		}
		if !canEnter {
			return false, fmt.Errorf("%s in %s is not a folder", elem, folderLocation(folder))
		}
		next, err := enterable.Enter()
		if err != nil {
			return false, fmt.Errorf("entering %s: %w", elem, err)
		}
		if next == nil {
			return false, fmt.Errorf("%s cannot be entered", elem)
		}
		w.nav.Push(next)
		w.selected = ""
		w.step(now)
	}
	return true, nil
}

// step moves to the next element and restarts its wait.
func (w *pathWalk) step(now time.Time) {
	w.idx++
	w.since = now
	w.cur = w.nav.Current()
}

// find looks elem up in folder. It returns a nil item while the element is
// not there (yet) and an error when it can never be there.
func (w *pathWalk) find(ctx context.Context, folder models.Folder, elem PathElem) (models.Item, string, error) {
	// Len makes lazy folders populate before the lookup.
	folder.Len(ctx)
	if elem.ID != "" {
		if item, ok := folder.ItemByID(ctx, elem.ID); ok {
			return item, elem.ID, nil
		}
		if ef, ok := folder.(models.ExplicitFinder); ok {
			if item, ok := ef.ExplicitItemByID(ctx, elem.ID); ok {
				return item, elem.ID, nil
			}
		}
		return nil, "", nil
	}
	if elem.Resource.Resource != "" {
		tf, ok := folder.(typeFolder)
		if !ok {
			return nil, "", fmt.Errorf("%s does not list resource types", folderLocation(folder))
		}
		item, id, _ := tf.TypeItem(elem.Resource)
		return item, id, nil
	}
	if tf, ok := folder.(typeFolder); ok {
		gr, err := w.resolveType(elem.Name)
		if err == nil {
			item, id, _ := tf.TypeItem(gr)
			return item, id, nil
		}
		// Not a type: a row such as /namespaces may still match by name.
		if item, id, ok := findRowByName(ctx, folder, elem.Name); ok {
			return item, id, nil
		}
		return nil, "", fmt.Errorf("unknown resource type %q", elem.Name)
	}
	if item, id, ok := findRowByName(ctx, folder, elem.Name); ok {
		return item, id, nil
	}
	return nil, "", nil
}

// resolveType resolves the name of the current element once per walk.
func (w *pathWalk) resolveType(name string) (schema.GroupResource, error) {
	if gr, ok := w.resolved[w.idx]; ok {
		return gr, nil
	}
	if w.resolve == nil {
		return schema.GroupResource{}, errors.New("no resource types known")
	}
	gr, err := w.resolve(name)
	if err != nil {
		return schema.GroupResource{}, err
	}
	w.resolved[w.idx] = gr
	return gr, nil
}

// findRowByName matches a row ID or the text of the first column, which
// shows "/" in front of folders.
func findRowByName(ctx context.Context, folder models.Folder, name string) (models.Item, string, bool) {
	if item, ok := folder.ItemByID(ctx, name); ok {
		if _, isBack := item.(models.Back); !isBack {
			return item, name, true
		}
	}
	n := folder.Len(ctx)
	for _, row := range folder.Lines(ctx, 0, n) {
		id, cells, _, ok := row.Columns()
		if !ok || len(cells) == 0 {
			continue
		}
		if _, isBack := row.(models.Back); isBack {
			continue
		}
		if strings.TrimPrefix(cells[0], "/") != name {
			continue
		}
		if item, ok := folder.ItemByID(ctx, id); ok {
			return item, id, true
		}
		if item, ok := row.(models.Item); ok {
			return item, id, true
		}
	}
	return nil, "", false
}

func folderLocation(f models.Folder) string {
	return "/" + strings.Join(f.Path(), "/")
}
