package explain

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/openapi"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// ErrNotPublished means the server's /openapi/v3 index does not list the
// group-version, for example an aggregated API that publishes no schema.
var ErrNotPublished = errors.New("group-version not published under /openapi/v3")

// IsAbsent reports whether err means the server answered but publishes no
// documentation for the type, as opposed to a failed request.
func IsAbsent(err error) bool {
	return errors.Is(err, ErrNotPublished) || errors.Is(err, ErrKindNotDocumented)
}

// DefaultCacheSize bounds how many parsed group-version documents a Loader
// keeps. A built-in document is a few hundred KiB to a few MiB parsed.
const DefaultCacheSize = 16

// Loader fetches OpenAPI v3 group-version documents and keeps the most
// recently used ones. Each load reads the small /openapi/v3 index first; it
// names every document with a hash that changes when the schema changes, so
// a CRD update or a different cluster never serves a stale document.
type Loader struct {
	mu      sync.Mutex
	max     int
	order   *list.List // front is most recently used
	entries map[cacheKey]*list.Element
}

type cacheKey struct {
	cluster string
	gv      schema.GroupVersion
}

type cacheEntry struct {
	key cacheKey
	url string // ServerRelativeURL, including the schema hash
	doc *Document
}

// NewLoader returns a loader that keeps at most size documents.
func NewLoader(size int) *Loader {
	if size <= 0 {
		size = DefaultCacheSize
	}
	return &Loader{max: size, order: list.New(), entries: make(map[cacheKey]*list.Element)}
}

// openapiPath is the key of a group-version in the /openapi/v3 index.
func openapiPath(gv schema.GroupVersion) string {
	if gv.Group == "" {
		return "api/" + gv.Version
	}
	return "apis/" + gv.Group + "/" + gv.Version
}

// Load returns the document for gv on the cluster identified by cluster.
// It issues real requests and must not run on the UI goroutine.
func (l *Loader) Load(ctx context.Context, cluster string, client openapi.ClientWithContext, gv schema.GroupVersion) (*Document, error) {
	log := ctrllog.FromContext(ctx).WithName("explain")
	log.Info("requesting OpenAPI v3 index", "path", "/openapi/v3", "cluster", cluster, "groupVersion", gv.String())
	paths, err := client.PathsWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading /openapi/v3: %w", err)
	}
	entry, ok := paths[openapiPath(gv)]
	if !ok {
		return nil, fmt.Errorf("%s: %w", gv.String(), ErrNotPublished)
	}
	key := cacheKey{cluster: cluster, gv: gv}
	url := entry.ServerRelativeURL()
	if doc, ok := l.get(key, url); ok {
		return doc, nil
	}
	log.Info("requesting OpenAPI v3 schema", "url", url, "cluster", cluster, "groupVersion", gv.String())
	data, err := entry.SchemaWithContext(ctx, runtime.ContentTypeJSON)
	if err != nil {
		return nil, fmt.Errorf("reading the %s schema: %w", gv.String(), err)
	}
	doc, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", gv.String(), err)
	}
	l.put(key, url, doc)
	return doc, nil
}

func (l *Loader) get(key cacheKey, url string) (*Document, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	el, ok := l.entries[key]
	if !ok {
		return nil, false
	}
	e, _ := el.Value.(*cacheEntry) // the list holds only *cacheEntry
	if e.url != url {
		return nil, false
	}
	l.order.MoveToFront(el)
	return e.doc, true
}

func (l *Loader) put(key cacheKey, url string, doc *Document) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.entries[key]; ok {
		e, _ := el.Value.(*cacheEntry) // the list holds only *cacheEntry
		e.url, e.doc = url, doc
		l.order.MoveToFront(el)
		return
	}
	l.entries[key] = l.order.PushFront(&cacheEntry{key: key, url: url, doc: doc})
	for l.order.Len() > l.max {
		oldest := l.order.Back()
		l.order.Remove(oldest)
		delete(l.entries, oldest.Value.(*cacheEntry).key)
	}
}

// Len reports how many documents are cached.
func (l *Loader) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.order.Len()
}
