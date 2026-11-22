# 0062. Cache ownership in the cluster pool

- Status: Accepted
- Date: 2026-11-22
- Issue: #62

## Context

`cluster.Pool` keeps one controller-runtime `Cluster` (informer cache, table
cache, discovery) per `cluster.Key`. An idle loop stops entries whose last use
is older than `kubernetes.clusters.ttl` (default 2m).

"Use" meant only the moment of `Pool.Get`. A node's `/pods` folder gets a
separate selector-scoped entry once, when it is entered, and then reads it for
as long as it is open. After two minutes the pool stopped that entry while the
folder still showed it, so pod changes stopped arriving. The app's main cluster
was protected by an explicit `Pin`; nothing else was. `Pool.Touch` existed but
had no callers.

Open questions the issue left:

1. How a folder keeps its cache alive: renew on access, or hold it explicitly.
2. Who releases, and when.
3. What happens on a context switch and on a namespace change.
4. How the namespace-scoped caches of #56 fit in.

## Decision

### 1. Reference counting with leases

`Pool.Acquire(ctx, key, selectors)` returns a `*cluster.Lease`. Each lease
counts as one owner of the entry. `Lease.Release` gives the hold up; it is
idempotent and nil-safe, and a lease is bound to the entry it was taken on, so
releasing a stale lease never touches an entry that replaced it.

An entry with at least one unreleased lease is never evicted by the TTL and
never pruned. The TTL counts from the release of the last lease (or from
creation, if nobody ever held it). Idle eviction itself is unchanged: the pool
checks every 30 s and stops unheld entries past the TTL.

`Pool.Get`, `Pool.GetWithSelectors`, `Pin`, `Unpin` and `Touch` are removed, so
there is one way to hold an entry.

### 2. The folder that acquires a scope releases it

`models.NamespaceClusterFactory` now returns `(cluster, release, error)`.
`Deps.ForSelectorScope` and `Deps.ForNamespace` return the derived `Deps` and a
release function that is never nil. The folder built from those `Deps` stores
the release and calls it from `Close`.

Navigators already call `Close` on every folder they drop: `Back` closes the
popped folder, and `Navigator.Close` (called by `replaceNavigator` when a panel
is rescoped, a namespace or context changes) closes the rest. So ownership
ends exactly when navigation discards the folder. Two panels that open the same
node each hold their own lease on the shared entry; leaving one does not stop
the other's cache.

The node pods folder is the only scoped cache in use today. `ForNamespace` has
no callers yet but follows the same contract.

### 3. The app holds the current context's cluster

The app keeps a lease (`App.clLease`) on the cluster of the current context
instead of pinning it. On a context switch the new cluster is acquired in the
background command. `applyContextSwitch` takes the new lease over first, then
releases the old one, so switching to the same context never leaves its
cluster unheld even for a moment. If the switch fails, the new lease is
released. The old context's folders are closed when `goToNamespace` replaces
both navigators; after that nothing holds the old cluster and it is evicted
after the TTL, which keeps a quick switch back cheap.

### 4. Pruning respects leases

`PruneNamespaces` keeps entries that a lease holds; they fall under the TTL
once released. `goToNamespace` now prunes after it has replaced the navigators,
so caches held by the folders it just closed are already released and can go at
once.

## Alternatives considered

- **Renew on access (call `Touch` on read or repaint).** Simple, but a folder
  that is open and quiet is not read: rows come from the watch, and with no
  changes nothing repaints. The cache would still expire under an idle open
  folder, the case the issue describes. Touching from a timer per folder fixes
  that, but only by reimplementing a lease with extra goroutines, and the
  lifetime would still depend on the timer period being shorter than the TTL.
- **Pin every scoped entry.** `Pin` was keyed by `Key`, not by owner, so two
  panels on one node would share one flag: the first to leave would unpin the
  other's cache. It is a reference count of one.
- **Never evict scoped entries.** Each node a user browses would keep a watch
  open for the whole session.
- **Return a release function from the pool instead of a `Lease` type.** The
  pool returns a `Lease` so callers can read the key and cluster from one value;
  the models layer still takes a plain release function, which keeps
  `models` tests free of a real pool.

## Consequences

- Visible data stays live for long sessions: an open folder's cache runs as
  long as the folder is open, whatever the TTL.
- Every acquired scope must reach a `Close`. All current entry paths
  (`Enter` from the list widget, `navigation.GoTo`) push the new folder onto a
  navigator at once, so this holds today. A future path that builds a scoped
  folder and drops it without closing it would keep that cache running until
  the app exits. That failure is a leak, not stale data.
- Viewers that read `App.cl` (describe, logs) are not lease owners. They follow
  the current context as before.
- The pool still builds a `Cluster` (including a discovery call) while holding
  its lock, as `Get` did. Moving construction out of the lock is a separate
  change.
- #56 (browse permitted namespaces without cluster-wide list) will add
  namespace-scoped entries. It should obtain them through `Deps.ForNamespace`
  (or `Pool.Acquire` directly), store the release on the folder or panel scope
  that reads the cache, and release it when that scope is discarded. With that,
  pruning and TTL eviction need no further changes.
- Tests drive the pool with an injected clock and a fake cluster starter, and
  one envtest case runs a real selector-scoped cache past ten TTLs.
