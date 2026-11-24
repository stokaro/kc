# 0051. Cluster overview folder

- Status: Accepted; the pause while a dialog is open amended for Info by 0342
- Date: 2026-11-22
- Issue: #51

## Context

kc shows objects well, but it cannot answer "is this cluster healthy, how
loaded is it, and what is about to break?" without walking many folders.
Issue #51 proposes an `/overview` folder whose rows are findings, with Enter
opening the objects behind each finding, and a five-step plan.

The first change (PR #104) covered step 1 (Health and Activity), step 3
(Risks and the `overview:` thresholds) and the requests/limits half of
step 2 (Capacity). The second change covers the rest: measured usage from
metrics.k8s.io through the poller of #44 (`docs/adr/0044-usage-columns.md`),
the flags and risks that need it, the Info panel mode (step 4), the kubelet
stats for disk and volumes (step 5), the navigation of step 4 of the issue
text (`kc overview`, a key for a list's namespace, Enter on an event), and
the shared row health styles from #93 (`docs/adr/0038-faults-only.md`).

Constraints: object state comes from the shared controller-runtime cache
only, never ad-hoc lists (AGENTS.md); aggregates are recomputed in the
background at most once a second; cluster-wide informers and polls run only
while the overview is visible; every real request logs an INFO line first.

The issue leaves four questions open. Sections 1 to 4 answer them; the
other sections record the choices made while building it.

## Decision

### 1. A root row for every context, a key and a startup verb

The first row of every panel root is `/overview`. The row is a plain
enterable item: it starts nothing and costs nothing until it is entered.

`M-i` (with the `Esc i` fallback) opens `/overview` in the active panel for
the namespace of the list the panel shows: an object list's own namespace
(`ObjectListMeta`), so `/namespaces/shop/pods` in an all-namespaces panel
gives the overview of `shop` and a list across namespaces gives the whole
cluster; otherwise the panel's namespace scope. It walks with
`NavigatePanel` to the `__overview__` row of that scope's root, so `/..`
leads back to the root of that scope. `M-i` was free on main and in the
open PRs (taken-key list, `git grep '"alt+'`, the diff of #122); it is in
`builtinHotkeys` as "open the overview", so custom command hotkeys cannot
take it. The issue suggested Alt+I.

`kc overview` starts in `/overview` of the left panel. It takes `-n` and
`-A` like `kc get`; without them it uses the context's namespace, else
`default`, like every other kc command. A bare `kc overview` across all
namespaces was considered, but `-A` is one flag away and the same rule for
every verb is easier to remember.

### 2. No frame coloring from health (for now)

The panel frame is not colored by health. Coloring it would need the health
counts computed all the time, for every context shown, which means
cluster-wide pod and event informers for the whole session. That is exactly
the cost the issue's data rules forbid. PR #89 colors the frame per context;
mixing a health color into it would also conflict with that meaning. If a
cheap signal is wanted later, it can come from node conditions only (a
small, cluster-scoped informer), not from pods or events.

### 3. Thresholds: fixed defaults with config overrides, flat keys

Thresholds live under `overview:` with defaults in `appconfig.Default()`,
`config-default.yaml` and the README, compared by the defaults test:

```yaml
overview:
  window: 1h                  # events and restarts count inside this window
  restartThreshold: 3         # restarts (last one inside window) = restarting
  requestsWarnPercent: 80     # node requests vs allocatable: "high"
  requestsFailPercent: 95     # node requests vs allocatable: "full"
  limitsWarnPercent: 100      # node limits above this: "overcommitted"
  podsWarnPercent: 90         # pods per node vs allocatable pods
  quotaWarnPercent: 80        # ResourceQuota used vs hard
  usageWarnPercent: 80        # measured node usage vs allocatable: "high"
  usageFailPercent: 90        # measured node usage vs allocatable: "full"
  underRequestedFactor: 2     # usage >= factor x requests: "under-req" ...
  underRequestedMinPercent: 25 # ... when usage reaches this of allocatable
  memoryLimitWarnPercent: 90  # container memory usage vs its limit
  cpuLimitWarnPercent: 90     # container CPU usage vs its limit
  diskWarnPercent: 80         # node and image filesystem: "high"
  diskFailPercent: 90         # node and image filesystem: "full"
  volumeWarnPercent: 80       # mounted PVC used vs capacity
  statsMaxNodes: 50           # no kubelet stats above this many nodes
```

`OverviewConfig.Normalized` replaces zero or negative values with the
default, so a partial `overview:` section works; a fail value below its warn
value is raised to it, and an `underRequestedFactor` of 1 or less (which
would flag every node that uses what it requests) takes the default. Per
context overrides are not added: no user has asked for them, and the shape
leaves room for an `overview.contexts.<name>:` map later without breaking
these keys.

The under-requested flag needs a floor (`underRequestedMinPercent`): an idle
node that uses 2% while its pods request 0.5% is "four times its requests",
which says nothing. The mba example of the issue (use 55%, requests 10%) is
flagged with the defaults.

### 4. Large clusters: aggregates in the overview, details on Enter

The overview never has one row per node. Capacity rows show the sum over
all nodes, and their state is the worst node's flag, so one overcommitted
node shows even when the sum looks fine. Enter lists every node with its
own value, sorted by pressure (worst first). The same holds for every row:
targets are sorted by severity, then by a rule-specific rank (pressure,
restart count, newest event), then by namespace and name. The kubelet stats
(one request per node) are not read above `statsMaxNodes` nodes; the disk
and volume rows then say so.

### 5. Data source and lifetime: an own pool entry, typed informers

The overview reads from its own pool entry (the context's key with selector
key `kc-overview`), not from the panel's shared cache:

- It starts when the overview is first read and lists typed objects
  (`corev1.Pod`, `appsv1.Deployment`, ...) from that cache with
  `UnsafeDisableDeepCopy`. Typed objects make the rules plain Go; the
  panel's shared cache holds unstructured objects and table rows.
- An engine recomputes from informer events at most once a second, plus
  every 30 seconds so windows and ages move. It pauses while a dialog is
  open (`models.Activity`). Folders only read its latest report.
- The engine holds a lease on its pool entry (`NamespaceClusterFactory`'s
  release, `docs/adr/0062-cache-ownership.md`). Once no folder has read it
  for `kubernetes.clusters.ttl`, it removes its informer handlers and gives
  the lease up; the pool stops the cache one TTL later.
- A kind that cannot be read (not served, no permission, not synced after
  30 s) makes the rows that need it dim, with the reason in F3.

The cache is cluster-wide even for a namespace scope: Capacity needs every
pod on a node, nodes and PVs are cluster-scoped, and a namespace-scoped kc
cache cannot read cluster-scoped kinds. Namespace scope is a filter on the
rows.

### 6. Rows, Enter and F3

Each row has a value, a state word (`ok`, `warning`, `failed`, `n/a`, or a
capacity flag `high`, `full`, `overcommitted`, `under-req`) and a style from
its severity. Enter opens a folder that lists exactly the objects behind the
row. Its rows are object rows with full GVR identity
(`<group>/<version>/<resource>/<namespace>/<name>`), so F3, F4, F8 and Enter
into child folders work as in any object list, and a Value column says why
each object is listed. F3 on an overview row shows how the value is
computed, the scope, the reason a source is missing, and the kubectl
commands that show the same inputs.

Enter on a warning event opens the object the event is about in the other
panel. The event row is a `models.ObjectLink` (as the objects of a Helm
release are), so the existing `OpenObjectRequest` path moves the other
panel to the object's namespace and type list and selects it, and focus
stays on the events list for the next one. The involved object's kind is
mapped with the RESTMapper (cached discovery, no request); an object
without a namespace of its own is in the event's namespace when its kind is
namespaced. A kind the cluster does not serve gives a message instead.

### 7. Measured usage: metrics-only pollers from #44

Capacity "use" and the container risks read the usage poller of #44
(`Deps.Usage`). The overview asks for its own pollers, keyed with
`metricsOnly`: they list NodeMetrics (cluster-wide) and PodMetrics (in the
overview's namespace scope, all namespaces for the whole cluster) and do
not join with objects from the panel's shared cache. A join would start
cluster-wide unstructured pod and node informers in the shared cache, which
live for the session; the overview has the specs in its own cache already.
PodMetrics are kept per container (`usageSnapshot.containers`), because a
pod sum hides one container at its limit next to an idle one.

The engine reads the snapshots on every tick (once a second), which keeps
the pollers alive while it runs, and a poll marks it dirty, so a new value
is shown within a second. When the engine stops, the pollers stop two
intervals later. The poll interval stays 15 seconds; no config key.

A missing source makes the dependent part n/a, with the reason in F3: no
registry ("usage metrics are off"), API not served, a failed list (the
error, for example Forbidden or 503), or "loading" before the first poll or
when the snapshot is older than two intervals. Capacity rows keep their
requests and limits when usage is missing; the two container risks are dim.

The flags, in this order (the first that applies wins): `full` (requests
reach `requestsFailPercent` or usage reaches `usageFailPercent`, failed),
`overcommitted` (limits above `limitsWarnPercent`), `under-req` (see
section 3), `high` (usage reaches `usageWarnPercent` or requests reach
`requestsWarnPercent`). `/pods near memory limit` and `/pods at cpu limit`
compare each app container and sidecar with its own limit and show the
container closest to it.

### 8. Kubelet stats: node disk and claim volumes

`/nodes disk` (Capacity) and `/pvcs near full` (Risks) read the kubelet
stats summary through the API server's node proxy
(`GET /api/v1/nodes/<node>/proxy/stats/summary`): once a minute while the
engine runs, for the nodes of the last recompute, at most five requests at
a time, ten seconds each, skipped while a dialog is open. The node
filesystem and, when the runtime keeps images elsewhere (another capacity),
the image filesystem count; a node is `high` or `full` by the worse of the
two. Volumes count when they have a `pvcRef` and a capacity; a claim mounted
on two nodes counts once.

Forbidden on every node means the identity has no get on `nodes/proxy`;
the rows are dim with "forbidden: nodes/proxy". One failing node is listed
as "no stats: <error>" while the others count. The summary is decoded into
local structs instead of importing `k8s.io/kubelet`: two filesystems and
the volume list are a few lines, and the module is not a dependency of kc.
F3 of the volume row warns that local-path and hostPath report the node
filesystem as the volume's capacity.

The optional cert-manager check of step 5 is not done (see Consequences).

### 9. Row health styles from #93

Overview rows and target rows use `RowHealthStyle`, the mapping object
lists use for `RowHealth` (now a function shared by the list stylers): ok
is the normal style, a warning the not-ready style, a failure the failed
style, and an unknown source dim. A warning in the overview therefore has
the color of a not-ready pod in a list, not the yellow of part 1. The Info
mode colors its bars the same way.

### 10. Info panel mode

`PanelModeInfo` ("Info") is a new panel mode between Logs and Command,
reached with `C-1` / `C-2`, the mode chooser and the F9 panel menu ("Info",
as in mc's Left/Right menu). It shows, for the other panel:

- the scope: the namespace that `M-i` would use for the other panel, or one
  node when the other panel's cursor is on a node row;
- bars for CPU and memory (measured usage of allocatable, or requests when
  usage is not known, with requests and limits as text), pods of allocatable
  pods and disk (node and separate image filesystem), each with the
  overview's flag and color;
- a sparkline of the CPU and memory usage, one sample per metrics poll, the
  last 60 (15 minutes), kept in memory for each scope and node while kc
  runs;
- the five pods with the most memory usage in the scope (on the node).

The widget (`internal/ui/panelcontent/info`) holds a `models.OverviewInfo`,
an overview engine of its own for the scope, and reads it once a second
with a `BackgroundMsg` tick, so it follows a dialog-free UI without I/O on
the update goroutine. A new scope closes the old engine; leaving the mode
(`SetFocus(false)`) or closing the panel closes it. It is read-only like
Describe and Manifest.

A workload or a namespace row as the Info target (bars for one Deployment)
is not done: the rows that would feed it are per pod, and it needs a
grouping by owner that the overview does not keep.

## Alternatives considered

- Reading from the panel's shared cache. No second set of informers, but
  the shared cache is pinned for the session: cluster-wide pod and event
  informers started by the overview would never stop. The cost is a second
  copy of pods and events while the overview is open and the user also
  lists all pods.
- Computing on every Lines call. Simple, but a busy cluster would recompute
  on every redraw; the issue asks for at most once a second.
- Reusing a filtered `ObjectsFolder` for Enter. It lists one GVR; several
  rows mix kinds (workloads, capacity targets) and need a reason column.
- A LimitRange check for "containers without memory limit". LimitRange
  defaults are applied when a pod is created, so a container that still has
  no limit in its spec was not covered by one; the check needs no
  LimitRange informer.
- Coloring the frame from health (question 2) and per-context thresholds
  (question 3): see above.
- Joining usage with the shared cache like the usage columns do. It works,
  but starts session-long cluster-wide informers in the panel cache (section
  7).
- Pod-level near-limit checks from `usageValues`. A pod is unlimited when
  one container has no limit, and the sum hides one container at its limit.
- Polling the kubelet stats with the metrics (every 15 s). The kubelet
  refreshes filesystem stats about once a minute; faster polls cost one
  request per node for the same numbers.
- `k8s.io/kubelet` for the summary types. A new module for two structs.
- Sharing one engine between `/overview` and the Info mode of the other
  panel. It would halve the work when both show the same scope, but they
  often do not, and a shared engine needs reference counting across two
  owners with different lifetimes. Both read the same pool entry, so the
  informers are shared; only the recompute runs twice.
- A sparkline from Prometheus. The issue keeps history in memory; a
  Prometheus source can come later.
- `C-x i` for Info, as in mc. kc has no `C-x` chords for panel modes, and
  `C-1` / `C-2` and the menu already reach every mode.

## Consequences

- One Enter from any root, `M-i` from any list, or `kc overview` shows load,
  health and risks for the scope.
- The overview adds one cluster-wide cache while it is open, released one
  TTL after it is left, plus two metrics lists every 15 seconds and one
  stats request per node a minute. An Info panel on a different scope adds
  its own engine and pollers.
- Overview warnings changed color from yellow to the not-ready color of
  lists.
- Without get on `nodes/proxy` (common for non-admin users) the disk and
  volume rows stay dim; without metrics-server the usage parts say n/a.
- Left open on #51: the optional cert-manager Certificates check (not Ready,
  or expiring within 14 days) and Info for a single workload.
