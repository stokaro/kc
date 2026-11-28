# 0405. Object folders load off the loop; one discovery call per read

- Status: Accepted
- Date: 2026-11-28
- PR: #405 (follow-up to #400 and #401, issue #378)
- Amends: decisions 1, 2 and 6 of
  [0378](0378-loop-never-waits-on-discovery.md), decisions 2 and 5 of
  [0378](0378-startup-draws-first.md)

## Context

A review of #400 and #401 after they merged found paths the two 0378
records did not cover.

- The folders that show one object, the keys of a ConfigMap or Secret
  and a pod's sections and containers, read the object with a GET on the
  UI goroutine. They never switched to background loads, so against a
  stalled server every frame waited for its deadline, and while the GET
  failed every read sent another one.
- A discovery read asked the server twice: `ServerGroupsAndResources` for
  the RESTMapper, then `ServerPreferredResources` for the type list.
  With legacy discovery, client-go's memory cache asks again for a group
  version that failed with a transient error, so the second call could
  find a type the first one missed: the type list then showed a type the
  mapper did not know. A read in which some groups failed reset the
  retry delay, and the missing groups came back only after the full
  refresh interval.
- The mapper's wait for the cluster's first read took no context, and
  the read it serves embedded a nil `discovery.DiscoveryInterface`, so a
  client-go version that called another method would panic.
- After `Enter` on the startup error, kc showed "Discovery resources
  failed" even when the cluster's own retry had succeeded meanwhile. The
  error box was wider than a 40-column terminal and taller than a short
  one, and the key hint was cut off.

## Decision

1. **Folders that show one object load off the UI goroutine**, as object
   lists do (0378 decision 6): ConfigMap keys, Secret keys, a pod's
   sections, a pod's init, regular and ephemeral containers, and a
   container's detail folder. The GET and the start of the pod's watch run
   on a goroutine, reads of the rows return what the last load returned,
   and a failing load is retried no more than once a second. The folders
   close their rows when the navigator drops them, which stops the watch
   and pending loads.

2. **A discovery read makes one `ServerGroupsAndResources` call**, and
   the mapper, the shortcut expansion and the type list are built from
   its answer; the preferred version of each resource is picked from it
   the way client-go's `ServerPreferredResources` does. They always list
   the same types. A read in which some groups failed still counts, its
   groups are logged, and the refresh loop reads again with the retry
   delay of a failed read (1 s, doubling up to the refresh interval)
   until a read misses no group.

3. **The read the mapper serves implements the whole discovery
   interface.** What it can answer from the read it answers from memory;
   `ServerVersion`, the OpenAPI calls and its REST client return an error
   and send nothing. A method added to the interface fails the build.

4. **The mapper also implements `meta.RESTMapperWithContext`.** Its
   `...WithContext` methods stop waiting for the first read when their
   context ends and return `ErrDiscoveryPending`. The cluster's
   context-carrying reads (`GetByGVR`, `ListRowsByGVR`, `GetRowByGVR`) use
   them. The plain `meta.RESTMapper` methods take no context and still
   wait for the first read, which the 30 s read timeout bounds; panels
   show a cluster only after that read, so the UI does not wait.

5. **The startup error reports the cluster's current state.** When the
   panels open after a discovery error, kc asks the cluster for its last
   read. If a retry has succeeded, the panels get its types and no error
   message; otherwise the message shows the last read's error.

6. **The connecting screen fits a 40 by 5 terminal.** The box takes the
   width it gets, up to 76 columns. Text wraps, a URL after a slash, and
   lines that do not fit are cut, the last one kept ending with "…". The
   margin above and below goes first, then the blank line before the key
   hint; the hint is always the last line. On a wide terminal the
   connecting line keeps `· F10 quits` on one line.

## Alternatives considered

- **Bound the folders' reads with the panel deadline and keep the GET on
  the loop.** Every frame would still wait 250 ms while the server stalls,
  and every read would send a GET.
- **Keep two discovery calls and drop types the mapper does not know.**
  The second call would still ask the server again for the failed group
  versions, and the type list would differ from what the server's first
  answer said.
- **Add a context to the mapper's wait by changing the interface.**
  `meta.RESTMapper` is client-go's; controller-runtime and the describe
  renderer take it as it is. The optional `WithContext` interface is
  client-go's own way to pass one.
- **Keep the startup error message as it was.** It would report a
  failure the cluster had already recovered from.

## Consequences

- Against a cluster with an aggregated API that stays down, kc reads
  discovery four more times in the first half minute (after 1, 2, 4 and
  8 s), then every refresh interval as before. Each of those reads runs
  the discovery listeners, which repaint the type lists.
- While an object folder's load is in flight and an event marks it dirty,
  each read still waits up to its deadline for the load, as object lists
  do.
- Tests: the stall proxy holds requests while each object folder is read
  six times; every read returns within its deadline and one GET is sent.
  A legacy discovery server that fails one group version shows the type
  list and the mapper agree, and that a partial read is retried before
  the refresh interval. The startup screen is checked at 40x10, 40x7 and
  40x5.
