# 0378. The UI loop never waits for API discovery

- Status: Accepted
- Date: 2026-11-28
- Issue: #378
- Supersedes: in [0236](0236-panel-reread.md), the synchronous discovery
  invalidation of `C-r` on a list of resource types

## Context

With an API server that accepts connections and stops answering (a
stopped process, a hung load balancer), kc froze for up to half a minute
at a time, and a server that failed discovery made every frame slow. The
issue traced it to code on the UI goroutine that reached API discovery:

- `Cluster.RefreshDiscovery` emptied the memory cache and reset the
  deferred RESTMapper. The next reader sent the discovery requests and
  waited for them. The periodic refresh (`kubernetes.discovery.refresh`,
  30 s) did this every interval, and the readers included `View`: the
  command line of an object list asked the mapper whether its type is
  namespaced, and the refresh handler read the resource list in `Update`.
- `sliceRowSource` kept a folder dirty after a failed populate, so every
  `Len` and `Lines`, several per frame, ran populate again. The root's
  populate reads discovery: one discovery round per frame against a
  server that fails it.
- A live list started its watch on the first read of its rows, on the UI
  goroutine. Starting asks the RESTMapper and the shared cache for an
  informer, and the cache's own mapper may ask the server.

Startup, which read discovery before the UI was drawn, is a separate
change under the same issue.

## Decision

1. **A refresh never empties what readers read.** A discovery read fills
   a new memory cache and builds a new RESTMapper from what it returned;
   only when the read is complete does the cluster swap them in. Readers
   always see a complete read, the previous one while a new one runs. One
   goroutine per cluster reads: when the cluster is built, every refresh
   interval, and when `RequestDiscoveryRefresh` asks. A failed read keeps
   the previous state, records its error, logs it and is retried after
   1 s, doubling up to the refresh interval. Discovery listeners run after
   each successful read, the first one included, on that goroutine.

2. **What each reader gets.**
   - `DiscoveredResources(ctx)` returns the resource types of the last
     read. It waits only for the cluster's first read and only until
     `ctx` is done (`ErrDiscoveryPending`). After a failed first read it
     returns that read's error at once. Code on the UI goroutine calls it
     with the panel deadline (250 ms).
   - `GetResourceInfos` returns the same list and waits for the first
     read to end, at most the 30 s read timeout. It is for background
     code (a context switch loads in a command).
   - The RESTMapper is built from the groups and resources of the read,
     shortcuts included, and never sends a request. client-go's memory
     cache asks again for a group version that failed with a transient
     error, so a mapper over that cache could. Until the cluster's first
     read ended a call waits for it; panels show a cluster only after
     that, so the UI never sees the wait. After a failed first read the
     mapper returns `ErrDiscoveryPending` at once. Holders of the mapper
     (the describe renderer) see each refresh.
   - `DiscoveryClient` and `Serves` read the memory cache of the current
     read. They are used off the UI goroutine. `Invalidate` on that client
     asks for a background read instead of emptying the cache.
   - `RefreshDiscovery` reads now and returns when done, for background
     code (the overview's `C-r`). Reads are serialized. `RequestDiscoveryRefresh` returns at
     once; requests during a read are served by one more read.

3. **Facts the UI shows are kept on the folder.** An object list keeps
   its type's scope and kind. The constructor sets what it knows: a
   cluster-scoped list is cluster-scoped, a namespaced list is namespaced,
   a node's pods and an object's Events are namespaced Pods and Events,
   and a type list sets the kind from discovery. A load, off the UI
   goroutine, asks the mapper for what is still unknown and keeps the
   answer. `KubectlCommand` reads only the kept scope; an unknown scope
   counts as namespaced, as a mapper error did before. The kubectl
   resource name comes from the GVR.

4. **Type lists read the last discovery read.** The root, the cluster and
   namespace type lists and the context root populate from
   `DiscoveredResources`. So do the discovery refresh handler, the
   visibility editor, object links and the overview's targets list.

5. **A failed populate is recorded and retried with backoff.** A
   populated folder records the error and the number of failures in a
   row. Its reads keep the previous rows and do not run populate again
   until a delay has passed: 250 ms, doubling to 30 s. Then a timer marks
   the folder dirty, and the repaint that follows runs populate once. A
   change of the data (`MarkDirty`: an event, a discovery read that
   succeeded, `C-r`) retries at the next read. Populate still runs on the
   UI goroutine, as before; for type lists it now reads memory.

6. **Watches start off the UI goroutine.** A live list starts its watch on
   a goroutine of its own, and reads of its rows do not wait for it. A
   list closed meanwhile stops the late watch, and reads during the start
   start no second one.

7. **`C-r` on a type list reads discovery in the background.** The list
   repaints at once from the current read and again when the new read
   is in; a new CRD shows when that read ends, normally within a frame
   or two.

8. **Keys typed during a block are not dropped.** Bubble Tea gives a key
   message no timestamp, so kc cannot tell keys typed during a block from
   keys typed after it, and a rule based on arrival time would drop keys
   the user meant. With the loop no longer waiting for discovery, a block
   long enough to queue keys is a bug to fix where it is.

### What still runs on the UI goroutine

Not discovery or the mapper, but reported here because they can wait for
the server:

- Entering a namespaced type in a namespace asks SelfSubjectAccessReviews
  to decide the list's cache (`resolveListScope`, at most 10 s), once per
  type and cluster; the answers are cached.
- Startup, until the separate startup change: kc reads the kubeconfig,
  builds the cluster and waits for its first discovery read before the
  UI is drawn.

The Update paths that call the mapper (delete, `cd`, explain, workload
logs, compare, startup intents) now read memory.

## Alternatives considered

- **Keep the invalidation and move every mapper caller off the loop.**
  There are many callers, and the first one after each refresh would still
  pay for the server. Swapping fixes all of them in one place.
- **Invalidate in the background.** client-go's memory cache empties in
  place, and its readers wait on its lock while it reads again.
- **Bound the UI's reads with contexts.** RESTMapper methods take no
  context.
- **Run every populate in the background.** Populate functions of type
  lists assume the UI goroutine. With discovery in memory they no longer
  wait, so the change is not needed for this issue.
- **Drop queued keys after a block.** See decision 8.

## Consequences

- During a read two discovery caches exist for a moment.
- A type installed in the cluster shows after the next read, periodic or
  `C-r`, and no longer on the first frame after an invalidation.
- A cluster whose first read failed shows no types until a retry
  succeeds; its listeners then repaint the type lists.
- Tests: a proxy in front of envtest (`kctesting.StallProxy`) holds or
  refuses requests. The cluster tests show readers answer during a
  stalled refresh and the mapper sends no requests; the UI test draws a
  panel and the command line with discovery stalled; the model tests
  count discovery requests of a failing root and the watch start.
