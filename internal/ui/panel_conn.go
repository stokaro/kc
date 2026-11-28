package ui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	kccluster "github.com/stokaro/kc/internal/cluster"
	"github.com/stokaro/kc/internal/models"
	"github.com/stokaro/kc/internal/podfs"
	"github.com/stokaro/kc/pkg/appconfig"
	"github.com/stokaro/kc/pkg/kubeconfig"
)

// Each panel browses its own kubeconfig context through a panelConn; see
// docs/adr/0362-independent-panel-contexts.md.

// panelConn is one panel's connection to a kubeconfig context: the
// cluster its folders read, held in the pool by a lease. A connection
// never changes; a context switch makes a new one with a new id, so work
// started for an old connection can tell it no longer belongs to the
// panel. Two panels in one context have two connections that lease the
// same pool entry.
type panelConn struct {
	// id names this connection instance, never reused. Selections carry
	// it (panelcontent.Selection.Source).
	id    string
	kctx  *kubeconfig.Context
	key   kccluster.Key
	cl    *kccluster.Cluster
	lease *kccluster.Lease
	podfs podfs.Factory

	// mu guards refs and stopDiscovery: holders may release from a
	// stream's goroutine.
	mu            sync.Mutex
	refs          int
	stopDiscovery func()
}

// name is the kubeconfig context name, "" for none.
func (c *panelConn) name() string {
	if c == nil || c.kctx == nil {
		return ""
	}
	return c.kctx.Name
}

// kctxOrNil is the connection's kubeconfig context, nil for none.
func (c *panelConn) kctxOrNil() *kubeconfig.Context {
	if c == nil {
		return nil
	}
	return c.kctx
}

// cluster is the connection's cluster, nil for none.
func (c *panelConn) cluster() *kccluster.Cluster {
	if c == nil {
		return nil
	}
	return c.cl
}

// connID is the connection's identity, "" for none.
func (c *panelConn) connID() string {
	if c == nil {
		return ""
	}
	return c.id
}

// clusterKey is the pool key of the connection, the zero Key for none.
func (c *panelConn) clusterKey() kccluster.Key {
	if c == nil {
		return kccluster.Key{}
	}
	return c.key
}

// hold adds a holder. The returned release gives the hold up; it may be
// called more than once and from any goroutine. When the last holder
// releases, the lease and the discovery listener go; the pool then keeps
// the cluster for its TTL.
func (c *panelConn) hold() func() {
	if c == nil {
		return func() {}
	}
	c.mu.Lock()
	c.refs++
	c.mu.Unlock()
	var once sync.Once
	return func() { once.Do(c.drop) }
}

func (c *panelConn) drop() {
	c.mu.Lock()
	c.refs--
	last := c.refs <= 0
	stop := c.stopDiscovery
	if last {
		c.stopDiscovery = nil
	}
	c.mu.Unlock()
	if !last {
		return
	}
	if stop != nil {
		stop()
	}
	c.lease.Release()
}

// holders is the number of holders, for tests.
func (c *panelConn) holders() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refs
}

// heldConn is a connection and the release of one hold on it.
type heldConn struct {
	conn    *panelConn
	release func()
}

func (h heldConn) drop() {
	if h.release != nil {
		h.release()
	}
}

// clusterKeyFor is the pool key of kctx with kc's impersonation.
func (a *App) clusterKeyFor(kctx *kubeconfig.Context) kccluster.Key {
	if kctx == nil || kctx.Kubeconfig == nil {
		return kccluster.Key{}
	}
	return kccluster.Key{
		KubeconfigPath:       kctx.LoadPath(),
		ContextName:          kctx.Name,
		ImpersonateUser:      strings.TrimSpace(a.impersonateUser),
		ImpersonateUID:       strings.TrimSpace(a.impersonateUID),
		ImpersonateGroupsKey: a.impersonationGroupsKey(),
	}
}

// newPanelConn wraps a lease acquired for kctx. It runs on the UI
// goroutine; the connection has no holder until a panel or pin holds it.
func (a *App) newPanelConn(kctx *kubeconfig.Context, key kccluster.Key, lease *kccluster.Lease) *panelConn {
	return a.connFor(kctx, key, lease.Cluster(), lease)
}

// connFor builds a connection for cl. lease may be nil (tests drive
// clusters they start themselves).
func (a *App) connFor(kctx *kubeconfig.Context, key kccluster.Key, cl *kccluster.Cluster, lease *kccluster.Lease) *panelConn {
	a.connSeq++
	c := &panelConn{id: fmt.Sprintf("conn-%d", a.connSeq), kctx: kctx, key: key, cl: cl, lease: lease}
	if cl == nil {
		return c
	}
	if factory, err := podfs.NewFactory(cl.GetConfig()); err != nil {
		ctrllog.FromContext(a.ctx).WithName("conn").Error(err, "pod filesystem factory", "context", c.name())
	} else {
		c.podfs = factory
	}
	c.stopDiscovery = cl.AddDiscoveryListener(a.signalDiscovery)
	return c
}

// signalDiscovery wakes the discovery wait loop; a pending signal covers
// another one.
func (a *App) signalDiscovery() {
	if a.discoveryCh == nil {
		return
	}
	select {
	case a.discoveryCh <- struct{}{}:
	default:
	}
}

// ownConn is the connection panel idx browses in List mode.
func (a *App) ownConn(idx int) *panelConn {
	if idx < 0 || idx > 1 {
		return nil
	}
	return a.conns[idx].conn
}

// connOfPanel is ownConn for a panel pointer.
func (a *App) connOfPanel(p *Panel) *panelConn {
	idx, ok := a.panelIndexFor(p)
	if !ok {
		return nil
	}
	return a.ownConn(idx)
}

// setPanelConn makes c the connection of panel idx. The panel takes its
// hold on c before it gives up the old one, so a switch to a connection
// that shares the old one's pool entry never leaves the entry unheld.
func (a *App) setPanelConn(idx int, c *panelConn) {
	if idx < 0 || idx > 1 {
		return
	}
	old := a.conns[idx]
	a.conns[idx] = heldConn{conn: c, release: c.hold()}
	old.drop()
}

// releasePanelConns gives up both panels' connections and the ones
// their followers hold for the objects they show, when kc exits.
func (a *App) releasePanelConns() {
	for idx := range a.conns {
		a.conns[idx].drop()
		a.conns[idx] = heldConn{}
		if p := a.panelByIndex(idx); p != nil {
			p.follow(nil, "", followOrigin{})
		}
	}
}

// targetConn is the connection of what panel idx shows: its own in List
// and Command mode. While it follows the other panel: the pin's, else the
// connection the object it shows was read from, which Logs and Info keep
// after the other panel has moved on (followOrigin), else the other
// panel's.
func (a *App) targetConn(idx int) *panelConn {
	p := a.panelByIndex(idx)
	if p == nil || !followerMode(p.Mode()) {
		return a.ownConn(idx)
	}
	if pin := p.pinned(); pin != nil && pin.conn != nil {
		return pin.conn
	}
	if c := p.origin.conn; c != nil {
		return c
	}
	return a.ownConn(1 - idx)
}

// targetScope is the namespace scope of what panel idx shows, as
// targetConn.
func (a *App) targetScope(idx int) string {
	p := a.panelByIndex(idx)
	if p == nil || !followerMode(p.Mode()) {
		return a.panelScope[idx]
	}
	if pin := p.pinned(); pin != nil && pin.conn != nil {
		return pin.scope
	}
	if p.origin.conn != nil {
		return p.origin.scope
	}
	return a.panelScope[1-idx]
}

// activeConn is the connection of what the active panel shows.
func (a *App) activeConn() *panelConn {
	return a.targetConn(a.activePanel)
}

// connByID finds a connection a panel, a pin or a follower holds. A
// connection that nobody on screen holds any more returns nil: its panel
// switched.
func (a *App) connByID(id string) *panelConn {
	if id == "" {
		return nil
	}
	for idx := range a.conns {
		if c := a.conns[idx].conn; c != nil && c.id == id {
			return c
		}
		p := a.panelByIndex(idx)
		if p == nil {
			continue
		}
		if pin := p.pinned(); pin != nil && pin.conn != nil && pin.conn.id == id {
			return pin.conn
		}
		if c := p.origin.conn; c != nil && c.id == id {
			return c
		}
	}
	return nil
}

// connDeps are the Deps of folders that read c with cfg.
func (a *App) connDeps(c *panelConn, cfg *appconfig.Config) models.Deps {
	deps := a.makeDeps(c.cluster(), cfg, c.clusterKey())
	if c != nil {
		deps.PodFSFactory = c.podfs
	}
	return deps
}

// connNamed is the connection of a panel that shows the context called
// name, the active panel's first; nil when neither does.
func (a *App) connNamed(name string) *panelConn {
	for _, idx := range []int{a.activePanel, 1 - a.activePanel} {
		if c := a.targetConn(idx); c != nil && c.name() == name {
			return c
		}
		if c := a.ownConn(idx); c != nil && c.name() == name {
			return c
		}
	}
	return nil
}

// selectedRow is the row under panel's cursor and the connection it was
// read from: the panel's own list, or in Describe, Manifest, Logs and Info
// the other panel's selection or the pin. A row read without a connection
// (no context yet) gets the panel's effective target. A row whose
// connection nobody holds any more is no row: its panel switched.
func (a *App) selectedRow(ctx context.Context, panel *Panel) (models.Item, *panelConn) {
	idx, ok := a.panelIndexFor(panel)
	if !ok {
		return nil, nil
	}
	item, source := panel.selectedItemSource(ctx)
	if source == "" {
		return item, a.targetConn(idx)
	}
	if c := a.connByID(source); c != nil {
		return item, c
	}
	return nil, nil
}

// connNamespace is the namespace kubeconfig names for c's context, or "".
func connNamespace(c *panelConn) string {
	if c == nil || c.kctx == nil {
		return ""
	}
	return strings.TrimSpace(c.kctx.Namespace)
}
