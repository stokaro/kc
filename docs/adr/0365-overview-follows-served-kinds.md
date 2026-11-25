# 0365. Overview follows kinds that are installed or removed while kc runs

- Status: Accepted
- Date: 2026-11-25
- Issue: #365

## Context

The overview engine, which also feeds the Info panel, got the informers
of its kinds once, when its connection started. A kind the cluster did
not serve was recorded as "not served by this cluster" and never tried
again. The connection lives as long as something reads the engine, and
the Info panel reads it all the time, so in practice an add-on such as
cert-manager installed while kc ran stayed "not installed" until kc
restarted or the context changed. `C-r` in `/overview` only recomputed.

Two questions were open.

**Does the cache's RESTMapper keep a miss?** `crcluster.New` sets no
`MapperProvider`, so the shared cache uses controller-runtime's
`apiutil.NewDynamicRESTMapper`, not the mapper that
`Cluster.RefreshDiscovery` resets. In controller-runtime v0.25.3 that
mapper reloads on every no-match: the first lookup reads aggregated
discovery once, and every later miss for a group version reads
`/apis/<group>/<version>` again (`addKnownGroupAndReload`,
`fetchGroupVersionResourcesLocked`). A 404 leaves the group out. It does
not keep a miss, so a `GetInformer` retried after an install finds the
kind. The envtests of this change show it with the mapper as it is. It
does keep a hit: a group it has loaded stays mapped after the group is
removed, so it cannot tell that a kind is gone.

**What happens when the add-on is removed?** The issue assumed that the
informer keeps its last objects, so the row keeps counting them. An
envtest against v1.0.0 showed otherwise: deleting the CRD makes the API
server delete every Certificate first, and the informer's watch delivers
those deletions, so its store empties. The row was still wrong. The
session lifecycle (ADR 0172) listed the deleted Certificate as recovered,
which keeps a row applicable, so the row read `0`, ok, instead of
"cert-manager is not installed". The informer also stays in the cache,
and its reflector retries a list that fails with 404 every 30 to 60
seconds for as long as kc runs.

## Decision

1. **Discovery decides what is served.** `Cluster.Serves(ctx, gvr)`
   reads the cluster's cached discovery, which every discovery refresh
   invalidates. An error (the server did not answer, or the group
   version failed its own discovery, as an aggregated API that is down
   does) means "cannot tell" and is never taken as "not served".

2. **The connection follows its cluster's discovery refreshes.** While
   it watches, each cache connection of the overview (one per pool
   entry, ADR 0182) listens to its cluster's discovery refreshes: the
   periodic one, every `kubernetes.discovery.refresh`, and the one `C-r`
   asks for. After a refresh it reads discovery once, on a goroutine of
   its own, and compares it with its kinds:
   - a kind recorded as not served that discovery lists now gets its
     informer and handler; its row says "loading" until the informer
     syncs, and the sync grace counts from that start;
   - a watched kind that discovery no longer lists is dropped: its
     handler is removed, it is recorded as not served, and its informer
     is removed from the cache;
   - a kind that failed for another reason (forbidden, or denied by the
     access reviews of ADR 0182 when the overview started) and every
     kind when discovery cannot tell are left as they are.

   Each change is logged once at INFO; for a retry the line comes
   before the informer request. The discovery read is logged at V(1),
   next to the lines
   `RefreshDiscovery` writes for the same refresh. A change marks the
   engine dirty, so it recomputes once. The connection reads its list
   of kinds on every round, so a kind added to it later is followed the
   same way.

3. **`C-r` reads discovery again.** `C-r` in `/overview`, in its
   subfolders and in the Info panel asks the engine's connection to
   refresh the discovery of every cache it reads. The listeners of
   those clusters run at once: the overview's own connection retries
   and drops, and any other overview engine on the same caches does
   too.

4. **A removed add-on has no recovered objects.** A finding that is not
   applicable because the cluster does not serve its add-on's kind
   forgets the targets the session saw. The objects went with the
   add-on; they did not recover. After a new install the row starts
   from what the new install holds.

5. **The informer of a removed kind is removed from the cache.**
   Otherwise its list keeps failing until kc stops, and after a new
   install it serves its old store until its next retry. The cache is
   the overview's own pool entry (`kc-overview`, ADR 0182); only
   overview engines read it, they read the same discovery, and each
   drops the kind in the same refresh. Without a pool (tests and
   examples) the connection reads `Deps.Cl`'s cache instead.

## Alternatives considered

- **Give the cache a mapper that discovery refreshes reset.** Not needed
  for an install, since the dynamic mapper retries a miss. For a removal
  the mapping is not the problem: the informer is. It would also change
  the mapping of every cache kc runs.
- **Retry `GetInformer` without reading discovery.** It costs a request
  per missing kind per refresh through the mapper, and it never sees a
  removal, because the mapper keeps the hit and the cache returns the
  informer it has.
- **Retry on every tick.** One request a second per missing kind for a
  change that is rare. The issue asks for retries on discovery
  refreshes only.
- **Keep the informer after a removal and only stop reading it.** The
  list keeps failing, a new install shows an empty store for up to a
  minute, and an engine started in the meantime trusts the old,
  synced informer.
- **Watch CustomResourceDefinitions.** Covers CRDs only, not aggregated
  APIs, and needs list and watch on CRDs, which many users lack.

## Consequences

- An add-on installed while kc runs shows in `/overview` and in the
  Info panel within one discovery refresh (30s by default), and at once
  after `C-r`.
- After a removal the row shows the deleted objects as recovered until
  the next refresh, then says the add-on is not installed.
- Each overview cache now reads discovery after every refresh: with
  aggregated discovery, two requests per cache every 30 seconds.
- Access decisions are still made once per engine start. A kind denied
  then is not retried after a grant; a new engine (namespace or context
  change, or the engine stopping after `kubernetes.clusters.ttl`) is
  needed.
- At worst, one informer per removal can stay behind: when another
  engine on the same cache starts a list of the kind in the instant
  between the removal and its own drop, the cache creates the informer
  again. It fails its list until the pool entry stops, as before this
  change.
