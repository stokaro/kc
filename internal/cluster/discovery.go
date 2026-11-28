package cluster

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
// resource list of the read. The mapper and the resource list come from
// one ServerGroupsAndResources call, so they list the same types. A second
// call could ask again for a group version that failed (client-go's memory
// cache does after a transient error with legacy discovery) and list a
// type the mapper does not know. A read in which only some group versions
// failed (an aggregated API that is down) counts; the snapshot names their
// groups, and the refresh loop reads again sooner.
func (s *discoveryState) fill(ctx context.Context) (*resourceSnapshot, error) {
	d := discovery.ToCachedDiscoveryInterfaceWithContext(s.disco)
	groups, lists, err := d.ServerGroupsAndResourcesWithContext(ctx)
	var failed []string
	if err != nil {
		partial, ok := errors.AsType[*discovery.ErrGroupDiscoveryFailed](err)
		if !ok || len(groups) == 0 {
			return nil, err
		}
		failed = failedGroups(partial)
	}
	read := readResources{groups: groups, lists: lists}
	groupResources, err := restmapper.GetAPIGroupResources(read)
	if err != nil {
		return nil, err
	}
	preferred, err := read.ServerPreferredResources()
	if err != nil {
		return nil, err
	}
	s.mapper = restmapper.NewShortcutExpander(restmapper.NewDiscoveryRESTMapper(groupResources), read, func(string) {})
	s.filled.Store(true)
	return &resourceSnapshot{infos: resourceInfos(preferred), failed: failed}, nil
}

// failedGroups names the groups of the group versions a read missed,
// sorted.
func failedGroups(partial *discovery.ErrGroupDiscoveryFailed) []string {
	var failed []string
	for gv := range partial.Groups {
		if !slices.Contains(failed, gv.Group) {
			failed = append(failed, gv.Group)
		}
	}
	slices.Sort(failed)
	return failed
}

// errReadOnlyDiscovery is what readResources returns for a call it cannot
// answer from the read.
var errReadOnlyDiscovery = errors.New("a discovery read answers from memory and sends no requests")

// readResources is one discovery read: the groups and the resource lists
// the server returned. The RESTMapper and its shortcut expansion read it
// through ServerGroupsAndResources (client-go restmapper, discovery.go and
// shortcut.go). It implements every method of the interface itself rather
// than embed it: a call it can answer from the read is answered from
// memory, any other returns errReadOnlyDiscovery. A client-go version that
// calls another method gets an error instead of a panic, and a method
// added to the interface fails the build.
type readResources struct {
	groups []*metav1.APIGroup
	lists  []*metav1.APIResourceList
}

var _ discovery.DiscoveryInterface = readResources{}

func (r readResources) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	return r.groups, r.lists, nil
}

func (r readResources) ServerGroups() (*metav1.APIGroupList, error) {
	list := &metav1.APIGroupList{Groups: make([]metav1.APIGroup, 0, len(r.groups))}
	for _, g := range r.groups {
		list.Groups = append(list.Groups, *g)
	}
	return list, nil
}

func (r readResources) ServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error) {
	for _, l := range r.lists {
		if l.GroupVersion == groupVersion {
			return l, nil
		}
	}
	return nil, fmt.Errorf("group version %q is not in the discovery read", groupVersion)
}

// ServerPreferredResources picks for each resource of a group the group's
// preferred version, or else the first version that lists it, as
// client-go's ServerPreferredResources does (discovery_client.go), from the
// read instead of the server. Subresources are left out.
func (r readResources) ServerPreferredResources() ([]*metav1.APIResourceList, error) {
	byGV := make(map[string]*metav1.APIResourceList, len(r.lists))
	for _, l := range r.lists {
		byGV[l.GroupVersion] = l
	}
	chosen := make(map[schema.GroupResource]string)
	for _, g := range r.groups {
		for _, v := range g.Versions {
			for _, res := range resourcesOf(byGV[v.GroupVersion]) {
				gr := schema.GroupResource{Group: g.Name, Resource: res.Name}
				if _, ok := chosen[gr]; ok && v.Version != g.PreferredVersion.Version {
					continue
				}
				chosen[gr] = v.Version
			}
		}
	}
	var out []*metav1.APIResourceList
	for _, g := range r.groups {
		for _, v := range g.Versions {
			if byGV[v.GroupVersion] == nil {
				continue
			}
			picked := &metav1.APIResourceList{GroupVersion: v.GroupVersion}
			for _, res := range resourcesOf(byGV[v.GroupVersion]) {
				if chosen[schema.GroupResource{Group: g.Name, Resource: res.Name}] == v.Version {
					picked.APIResources = append(picked.APIResources, res)
				}
			}
			out = append(out, picked)
		}
	}
	return out, nil
}

// resourcesOf is the resources of l without subresources; l may be nil.
func resourcesOf(l *metav1.APIResourceList) []metav1.APIResource {
	if l == nil {
		return nil
	}
	out := make([]metav1.APIResource, 0, len(l.APIResources))
	for _, res := range l.APIResources {
		if !strings.Contains(res.Name, "/") {
			out = append(out, res)
		}
	}
	return out
}

func (r readResources) ServerPreferredNamespacedResources() ([]*metav1.APIResourceList, error) {
	all, err := r.ServerPreferredResources()
	if err != nil {
		return nil, err
	}
	return discovery.FilteredBy(discovery.ResourcePredicateFunc(func(_ string, res *metav1.APIResource) bool {
		return res.Namespaced
	}), all), nil
}

func (readResources) ServerVersion() (*version.Info, error) { return nil, errReadOnlyDiscovery }

func (readResources) OpenAPISchema() (*openapiv2.Document, error) { return nil, errReadOnlyDiscovery }

func (readResources) OpenAPIV3() openapi.Client { return refusingOpenAPIV3{} }

func (r readResources) WithLegacy() discovery.DiscoveryInterface { return r }

// RESTClient returns a client whose requests fail with
// errReadOnlyDiscovery before they leave the process.
func (readResources) RESTClient() restclient.Interface {
	// NewRESTClient never returns an error (client-go rest/client.go).
	c, _ := restclient.NewRESTClient(&url.URL{}, "", restclient.ClientContentConfig{}, nil,
		&http.Client{Transport: refusingTransport{}})
	return c
}

type refusingOpenAPIV3 struct{}

func (refusingOpenAPIV3) Paths() (map[string]openapi.GroupVersion, error) {
	return nil, errReadOnlyDiscovery
}

type refusingTransport struct{}

func (refusingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errReadOnlyDiscovery
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
// It returns the snapshot of a successful read, which names the groups the
// read missed.
func (c *Cluster) readDiscovery(ctx context.Context) (*resourceSnapshot, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	log := ctrllog.FromContext(ctx).WithName("discovery")
	if c.discoveryState() == nil {
		return nil, errors.New("cluster has no discovery")
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
		return nil, err
	}
	if len(snap.failed) > 0 {
		log.Info("API discovery read missed groups", "host", c.GetConfig().Host, "groups", snap.failed)
	}
	c.disco.Store(target)
	c.resources.Store(snap)
	c.setDiscoveryError(nil)
	c.endFirstRead()
	c.notifyDiscoveryListeners()
	return snap, nil
}

func (c *Cluster) endFirstRead() {
	c.firstReadOnce.Do(func() { close(c.firstRead) })
}

// refreshLoop reads discovery at once, then every refresh interval, and
// when RequestDiscoveryRefresh asks. After a failure, or a read that missed
// some groups, it reads again sooner, with a delay that doubles up to the
// interval.
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
		snap, err := c.readDiscovery(ctx)
		if err != nil && ctx.Err() != nil {
			return
		}
		if err != nil || len(snap.failed) > 0 {
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
	_, _ = c.readDiscovery(c.loopCtx)
}

// resourceInfos lists the resource types of preferred-resources lists.
func resourceInfos(lists []*metav1.APIResourceList) []ResourceInfo {
	var infos []ResourceInfo
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
	return infos
}

// liveMapper is the RESTMapper Cluster.RESTMapper hands out. It answers from
// the discovery state of the moment, so a caller that keeps it (a describe
// renderer, a cache) sees each refresh. It answers only from a complete
// read and never sends a request. Until the cluster's first read ended a
// call waits for that read; after a failed first read it returns the
// read's error at once, until a retry succeeds.
//
// The meta.RESTMapper methods take no context, so they wait for the first
// read until it ends, which discoveryReadTimeout bounds. The WithContext
// methods of meta.RESTMapperWithContext stop waiting when their context
// ends; code that has a context, such as a background load, uses them
// (meta.ToRESTMapperWithContext).
type liveMapper struct{ c *Cluster }

var (
	_ metamapper.RESTMapper            = liveMapper{}
	_ metamapper.RESTMapperWithContext = liveMapper{}
)

// delegate returns the mapper of the current read. Before the first read
// ended it waits for that read or until cancel is closed; a nil cancel
// waits for the read alone.
func (m liveMapper) delegate(cancel <-chan struct{}) (metamapper.RESTMapper, error) {
	st := m.c.discoveryState()
	if st == nil {
		return nil, errors.New("cluster has no discovery")
	}
	if st.filled.Load() {
		return st.mapper, nil
	}
	select {
	case <-m.c.firstRead:
	case <-cancel:
		return nil, ErrDiscoveryPending
	}
	if st = m.c.discoveryState(); st.filled.Load() {
		return st.mapper, nil
	}
	if err := m.c.DiscoveryError(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDiscoveryPending, err)
	}
	return nil, ErrDiscoveryPending
}

func (m liveMapper) KindFor(resource schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	return m.kindFor(nil, resource)
}

func (m liveMapper) KindForWithContext(ctx context.Context, resource schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	return m.kindFor(ctx.Done(), resource)
}

func (m liveMapper) kindFor(cancel <-chan struct{}, resource schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	d, err := m.delegate(cancel)
	if err != nil {
		return schema.GroupVersionKind{}, err
	}
	return d.KindFor(resource)
}

func (m liveMapper) KindsFor(resource schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	return m.kindsFor(nil, resource)
}

func (m liveMapper) KindsForWithContext(ctx context.Context, resource schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	return m.kindsFor(ctx.Done(), resource)
}

func (m liveMapper) kindsFor(cancel <-chan struct{}, resource schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	d, err := m.delegate(cancel)
	if err != nil {
		return nil, err
	}
	return d.KindsFor(resource)
}

func (m liveMapper) ResourceFor(input schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	return m.resourceFor(nil, input)
}

func (m liveMapper) ResourceForWithContext(ctx context.Context, input schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	return m.resourceFor(ctx.Done(), input)
}

func (m liveMapper) resourceFor(cancel <-chan struct{}, input schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	d, err := m.delegate(cancel)
	if err != nil {
		return schema.GroupVersionResource{}, err
	}
	return d.ResourceFor(input)
}

func (m liveMapper) ResourcesFor(input schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	return m.resourcesFor(nil, input)
}

func (m liveMapper) ResourcesForWithContext(ctx context.Context, input schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	return m.resourcesFor(ctx.Done(), input)
}

func (m liveMapper) resourcesFor(cancel <-chan struct{}, input schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	d, err := m.delegate(cancel)
	if err != nil {
		return nil, err
	}
	return d.ResourcesFor(input)
}

func (m liveMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*metamapper.RESTMapping, error) {
	return m.restMapping(nil, gk, versions...)
}

func (m liveMapper) RESTMappingWithContext(ctx context.Context, gk schema.GroupKind, versions ...string) (*metamapper.RESTMapping, error) {
	return m.restMapping(ctx.Done(), gk, versions...)
}

func (m liveMapper) restMapping(cancel <-chan struct{}, gk schema.GroupKind, versions ...string) (*metamapper.RESTMapping, error) {
	d, err := m.delegate(cancel)
	if err != nil {
		return nil, err
	}
	return d.RESTMapping(gk, versions...)
}

func (m liveMapper) RESTMappings(gk schema.GroupKind, versions ...string) ([]*metamapper.RESTMapping, error) {
	return m.restMappings(nil, gk, versions...)
}

func (m liveMapper) RESTMappingsWithContext(ctx context.Context, gk schema.GroupKind, versions ...string) ([]*metamapper.RESTMapping, error) {
	return m.restMappings(ctx.Done(), gk, versions...)
}

func (m liveMapper) restMappings(cancel <-chan struct{}, gk schema.GroupKind, versions ...string) ([]*metamapper.RESTMapping, error) {
	d, err := m.delegate(cancel)
	if err != nil {
		return nil, err
	}
	return d.RESTMappings(gk, versions...)
}

func (m liveMapper) ResourceSingularizer(resource string) (string, error) {
	return m.resourceSingularizer(nil, resource)
}

func (m liveMapper) ResourceSingularizerWithContext(ctx context.Context, resource string) (string, error) {
	return m.resourceSingularizer(ctx.Done(), resource)
}

func (m liveMapper) resourceSingularizer(cancel <-chan struct{}, resource string) (string, error) {
	d, err := m.delegate(cancel)
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
