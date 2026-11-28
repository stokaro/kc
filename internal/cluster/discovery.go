package cluster

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	openapiv2 "github.com/google/gnostic-models/openapiv2"
	metamapper "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/openapi"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// discoveryReadTimeout bounds one read of the API server's discovery.
const discoveryReadTimeout = 30 * time.Second

// discoveryRetryMin is the wait after a failed discovery read. It doubles
// with each further failure, up to the refresh interval.
const discoveryRetryMin = time.Second

// ErrDiscoveryPending says that the cluster has no complete discovery read
// yet. DiscoveredResources returns it when its context ends before the
// first read does, and the RESTMapper when the first read failed.
var ErrDiscoveryPending = errors.New("API discovery is still being read")

// discoveryState is one read of the API server's discovery: a memory cache
// and a RESTMapper built from what the read returned. Each read fills a new
// state and then swaps it in, so the state readers use is never emptied
// while the server is asked again. Until the first read succeeded readers
// get an empty state, which New stores.
type discoveryState struct {
	disco discovery.CachedDiscoveryInterface
	// mapper is set by fill before filled; it reads only the groups and
	// resources of the read and never asks the server. client-go's memory
	// cache asks again for a group version that failed with a transient
	// error, so a mapper over the cache could.
	mapper metamapper.RESTMapper
	filled atomic.Bool
}

func newDiscoveryState(dc discovery.DiscoveryInterface) *discoveryState {
	return &discoveryState{disco: memory.NewMemCacheClient(dc)}
}

// fill reads discovery into s, builds the mapper from it and returns the
// resource list of the read. A read in which only some groups failed (an
// aggregated API that is down) counts; their types are missing.
func (s *discoveryState) fill(ctx context.Context) (*resourceSnapshot, error) {
	d := discovery.ToCachedDiscoveryInterfaceWithContext(s.disco)
	groups, lists, err := d.ServerGroupsAndResourcesWithContext(ctx)
	if err != nil && (len(groups) == 0 || !discovery.IsGroupDiscoveryFailedError(err)) {
		return nil, err
	}
	infos, failed, err := resourceInfosFrom(func() ([]*metav1.APIResourceList, error) {
		return d.ServerPreferredResourcesWithContext(ctx)
	})
	if err != nil {
		return nil, err
	}
	read := readResources{groups: groups, lists: lists}
	groupResources, err := restmapper.GetAPIGroupResources(read)
	if err != nil {
		return nil, err
	}
	s.mapper = restmapper.NewShortcutExpander(restmapper.NewDiscoveryRESTMapper(groupResources), read, func(string) {})
	s.filled.Store(true)
	return &resourceSnapshot{infos: infos, failed: failed}, nil
}

// readResources serves the groups and resources of one discovery read to
// the RESTMapper and its shortcut expansion, which call only
// ServerGroupsAndResources (client-go restmapper, discovery.go and
// shortcut.go). The embedded interface is nil: any other call panics
// rather than reach a server.
type readResources struct {
	discovery.DiscoveryInterface
	groups []*metav1.APIGroup
	lists  []*metav1.APIResourceList
}

func (r readResources) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	return r.groups, r.lists, nil
}

// resourceSnapshot is the resource list of one discovery read. It is never
// changed after it is stored.
type resourceSnapshot struct {
	infos  []ResourceInfo
	failed []string
}

// discoveryState returns the state readers use now, or nil for a Cluster
// that New did not build.
func (c *Cluster) discoveryState() *discoveryState {
	return c.disco.Load()
}

// DiscoveredResources returns the resource types of the last discovery
// read without asking the server. It waits only for the cluster's first
// read, until that read ends or ctx is done; ErrDiscoveryPending says ctx
// ended first. When the first read failed it returns that read's error at
// once, while the cluster retries in the background; discovery listeners
// run when a read succeeds.
//
// The UI calls it with a short deadline. A slow or stalled server then
// costs a render at most that deadline once, not a wait for the server.
func (c *Cluster) DiscoveredResources(ctx context.Context) (infos []ResourceInfo, failedGroups []string, err error) {
	if snap := c.resources.Load(); snap != nil {
		return slices.Clone(snap.infos), slices.Clone(snap.failed), nil
	}
	if c.firstRead == nil {
		return nil, nil, errors.New("cluster has no discovery")
	}
	select {
	case <-c.firstRead:
	case <-ctx.Done():
		return nil, nil, ErrDiscoveryPending
	}
	if snap := c.resources.Load(); snap != nil {
		return slices.Clone(snap.infos), slices.Clone(snap.failed), nil
	}
	if err := c.DiscoveryError(); err != nil {
		return nil, nil, err
	}
	return nil, nil, ErrDiscoveryPending
}

// DiscoveryError is the error of the last discovery read, or nil when it
// succeeded or none has ended yet.
func (c *Cluster) DiscoveryError() error {
	c.readErrMu.Lock()
	defer c.readErrMu.Unlock()
	return c.readErr
}

func (c *Cluster) setDiscoveryError(err error) {
	c.readErrMu.Lock()
	c.readErr = err
	c.readErrMu.Unlock()
}

// readDiscovery reads the API server's discovery once and makes it the
// state readers use. A failure leaves the current state as it is. Reads are
// serialized; listeners run, inside that order, after a successful read.
func (c *Cluster) readDiscovery(ctx context.Context) error {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	log := ctrllog.FromContext(ctx).WithName("discovery")
	if c.discoveryState() == nil {
		return errors.New("cluster has no discovery")
	}
	target := newDiscoveryState(c.dc)
	rctx, cancel := context.WithTimeout(ctx, discoveryReadTimeout)
	defer cancel()
	log.V(1).Info("reading API discovery", "host", c.GetConfig().Host)
	snap, err := target.fill(rctx)
	if err != nil {
		c.setDiscoveryError(err)
		c.endFirstRead()
		log.Info("API discovery read failed", "host", c.GetConfig().Host, "error", err.Error())
		return err
	}
	c.disco.Store(target)
	c.resources.Store(snap)
	c.setDiscoveryError(nil)
	c.endFirstRead()
	c.notifyDiscoveryListeners()
	return nil
}

func (c *Cluster) endFirstRead() {
	c.firstReadOnce.Do(func() { close(c.firstRead) })
}

// refreshLoop reads discovery at once, then every refresh interval, and
// when RequestDiscoveryRefresh asks. After a failure it retries sooner,
// with a delay that doubles up to the interval.
func (c *Cluster) refreshLoop(ctx context.Context) {
	// A cluster stopped before its first read ends the wait for it.
	defer c.endFirstRead()
	timer := time.NewTimer(0)
	defer timer.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-c.refreshReq:
		}
		if err := c.readDiscovery(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			timer.Reset(discoveryRetryDelay(failures, c.refresh))
			continue
		}
		failures = 0
		timer.Reset(c.refresh)
	}
}

// discoveryRetryDelay is the wait after the given number of failed reads
// in a row: discoveryRetryMin, doubled per further failure, at most limit.
func discoveryRetryDelay(failures int, limit time.Duration) time.Duration {
	d := discoveryRetryMin
	for range failures - 1 {
		if d >= limit {
			break
		}
		d *= 2
	}
	return min(d, max(limit, discoveryRetryMin))
}

// RequestDiscoveryRefresh asks the cluster to read discovery again soon,
// in the background, and returns at once. Requests made while a read runs
// are served by one more read. Listeners run once the read succeeds.
func (c *Cluster) RequestDiscoveryRefresh() {
	if c.refreshReq == nil {
		return
	}
	select {
	case c.refreshReq <- struct{}{}:
	default:
	}
}

// RefreshDiscovery reads discovery again now and returns when the read
// ended; listeners run when it succeeded. Readers keep the previous read
// until the new one is complete, so none of them waits for this one. It
// sends requests and may take up to discoveryReadTimeout: call it off the
// UI goroutine, which uses RequestDiscoveryRefresh.
func (c *Cluster) RefreshDiscovery() {
	if c.loopCtx == nil {
		return
	}
	_ = c.readDiscovery(c.loopCtx)
}

// resourceInfosFrom builds the resource list from a preferred-resources
// read. A read in which only some groups failed (an aggregated API that is
// down) still gives the other groups; failed names the groups missing.
func resourceInfosFrom(read func() ([]*metav1.APIResourceList, error)) (infos []ResourceInfo, failed []string, err error) {
	lists, err := read()
	if err != nil {
		if len(lists) == 0 {
			return nil, nil, err
		}
		partial, ok := errors.AsType[*discovery.ErrGroupDiscoveryFailed](err)
		if !ok {
			return nil, nil, err
		}
		for gv := range partial.Groups {
			if !slices.Contains(failed, gv.Group) {
				failed = append(failed, gv.Group)
			}
		}
		slices.Sort(failed)
	}
	for _, l := range lists {
		gv, err := schema.ParseGroupVersion(l.GroupVersion)
		if err != nil {
			continue
		}
		for _, ar := range l.APIResources {
			if ar.Name == "" || ar.Kind == "" || strings.Contains(ar.Name, "/") {
				continue
			}
			infos = append(infos, ResourceInfo{
				GVK:        schema.GroupVersionKind{Group: gv.Group, Version: gv.Version, Kind: ar.Kind},
				Resource:   ar.Name,
				Namespaced: ar.Namespaced,
				Verbs:      append([]string(nil), ar.Verbs...),
				Categories: append([]string(nil), ar.Categories...),
				ShortNames: append([]string(nil), ar.ShortNames...),
			})
		}
	}
	return infos, failed, nil
}

// liveMapper is the RESTMapper Cluster.RESTMapper hands out. It answers from
// the discovery state of the moment, so a caller that keeps it (a describe
// renderer, a cache) sees each refresh. It answers only from a complete
// read and never sends a request. Until the cluster's first read ended a
// call waits for that read; after a failed first read it returns the
// read's error at once, until a retry succeeds.
type liveMapper struct{ c *Cluster }

var _ metamapper.RESTMapper = liveMapper{}

func (m liveMapper) delegate() (metamapper.RESTMapper, error) {
	st := m.c.discoveryState()
	if st == nil {
		return nil, errors.New("cluster has no discovery")
	}
	if st.filled.Load() {
		return st.mapper, nil
	}
	<-m.c.firstRead
	if st = m.c.discoveryState(); st.filled.Load() {
		return st.mapper, nil
	}
	if err := m.c.DiscoveryError(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDiscoveryPending, err)
	}
	return nil, ErrDiscoveryPending
}

func (m liveMapper) KindFor(resource schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	d, err := m.delegate()
	if err != nil {
		return schema.GroupVersionKind{}, err
	}
	return d.KindFor(resource)
}

func (m liveMapper) KindsFor(resource schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	d, err := m.delegate()
	if err != nil {
		return nil, err
	}
	return d.KindsFor(resource)
}

func (m liveMapper) ResourceFor(input schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	d, err := m.delegate()
	if err != nil {
		return schema.GroupVersionResource{}, err
	}
	return d.ResourceFor(input)
}

func (m liveMapper) ResourcesFor(input schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	d, err := m.delegate()
	if err != nil {
		return nil, err
	}
	return d.ResourcesFor(input)
}

func (m liveMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*metamapper.RESTMapping, error) {
	d, err := m.delegate()
	if err != nil {
		return nil, err
	}
	return d.RESTMapping(gk, versions...)
}

func (m liveMapper) RESTMappings(gk schema.GroupKind, versions ...string) ([]*metamapper.RESTMapping, error) {
	d, err := m.delegate()
	if err != nil {
		return nil, err
	}
	return d.RESTMappings(gk, versions...)
}

func (m liveMapper) ResourceSingularizer(resource string) (string, error) {
	d, err := m.delegate()
	if err != nil {
		return "", err
	}
	return d.ResourceSingularizer(resource)
}

// liveDiscovery is the cached discovery client Cluster.DiscoveryClient hands
// out. Like liveMapper it reads the discovery state of the moment.
// Invalidate asks for a background refresh instead of emptying the cache
// other readers use.
type liveDiscovery struct{ c *Cluster }

var _ discovery.CachedDiscoveryInterface = liveDiscovery{}

func (l liveDiscovery) state() discovery.CachedDiscoveryInterface {
	return l.c.discoveryState().disco
}

func (l liveDiscovery) RESTClient() restclient.Interface { return l.state().RESTClient() }

func (l liveDiscovery) ServerGroups() (*metav1.APIGroupList, error) {
	return l.state().ServerGroups()
}

func (l liveDiscovery) ServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error) {
	return l.state().ServerResourcesForGroupVersion(groupVersion)
}

func (l liveDiscovery) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	return l.state().ServerGroupsAndResources()
}

func (l liveDiscovery) ServerPreferredResources() ([]*metav1.APIResourceList, error) {
	return l.state().ServerPreferredResources()
}

func (l liveDiscovery) ServerPreferredNamespacedResources() ([]*metav1.APIResourceList, error) {
	return l.state().ServerPreferredNamespacedResources()
}

func (l liveDiscovery) ServerVersion() (*version.Info, error) { return l.state().ServerVersion() }

func (l liveDiscovery) OpenAPISchema() (*openapiv2.Document, error) {
	return l.state().OpenAPISchema()
}

func (l liveDiscovery) OpenAPIV3() openapi.Client { return l.state().OpenAPIV3() }

func (l liveDiscovery) WithLegacy() discovery.DiscoveryInterface { return l.state().WithLegacy() }

func (l liveDiscovery) Fresh() bool { return l.state().Fresh() }

func (l liveDiscovery) Invalidate() { l.c.RequestDiscoveryRefresh() }

// discoveryFields is the discovery part of Cluster, kept in this file.
type discoveryFields struct {
	// dc is the uncached discovery client every state reads through.
	dc        discovery.DiscoveryInterface
	disco     atomic.Pointer[discoveryState]
	resources atomic.Pointer[resourceSnapshot]

	// readMu serializes discovery reads; readErr is the last one's error.
	readMu    sync.Mutex
	readErrMu sync.Mutex
	readErr   error

	// firstRead is closed when the first read ended, with or without
	// success.
	firstRead     chan struct{}
	firstReadOnce sync.Once
	refreshReq    chan struct{}
	// loopCtx is the context of the refresh loop; Stop cancels it.
	loopCtx context.Context
}

// initDiscovery builds the discovery client and the first, empty state.
// The refresh loop fills it.
func (c *Cluster) initDiscovery(ctx context.Context, cfg *restclient.Config) error {
	dc, err := discovery.NewDiscoveryClientForConfig(restclient.CopyConfig(cfg))
	if err != nil {
		return fmt.Errorf("discovery client: %w", err)
	}
	c.dc = dc
	c.disco.Store(newDiscoveryState(dc))
	c.firstRead = make(chan struct{})
	c.refreshReq = make(chan struct{}, 1)
	c.loopCtx = ctx
	return nil
}
