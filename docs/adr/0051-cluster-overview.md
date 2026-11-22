# 0051. Cluster overview folder

- Status: Accepted
- Date: 2026-11-22
- Issue: #51

## Context

kc shows objects well, but it cannot answer "is this cluster healthy, how
loaded is it, and what is about to break?" without walking many folders.
Issue #51 proposes an `/overview` folder whose rows are findings, with Enter
opening the objects behind each finding, and a five-step plan.

This change covers step 1 (Health and Activity), step 3 (Risks and the
`overview:` thresholds) and the requests/limits half of step 2 (Capacity).
Measured usage (metrics.k8s.io) is left out because PR #98 (issue #44) adds a
metrics poller that is not merged yet. The Info panel mode (step 4) and the
kubelet stats (step 5) are follow-ups.

Constraints: object state comes from the shared controller-runtime cache
only, never ad-hoc lists (AGENTS.md); aggregates are recomputed in the
background at most once a second; cluster-wide informers run only while the
overview is visible. PR #85 (cache leases) and PR #93 (shared row health
classification) are open, so this change uses the existing pool and keeps a
small local severity-to-style mapping.

The issue leaves four questions open. Each one is answered below.

## Decision

### 1. A root row for every context, not only on request

The first row of every panel root is `/overview`. The row is a plain
enterable item: it starts nothing and costs nothing until it is entered.
A key binding and `kc overview` are not added here; every free Meta key is
contested by open PRs (see the taken-key list), and the root row already
makes the overview one Enter away. A startup intent (`kc overview`) is a
small follow-up through `internal/ui/startup_intent.go`.

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
  window: 1h              # events and restarts count inside this window
  restartThreshold: 3     # restarts (last one inside window) = restarting
  requestsWarnPercent: 80 # node requests vs allocatable: "high"
  requestsFailPercent: 95 # node requests vs allocatable: "full"
  limitsWarnPercent: 100  # node limits above this: "overcommitted"
  podsWarnPercent: 90     # pods per node vs allocatable pods
  quotaWarnPercent: 80    # ResourceQuota used vs hard
```

`OverviewConfig.Normalized` replaces zero or negative values with the
default, so a partial `overview:` section works. Per-context overrides are
not added: no user has asked for them, and the shape leaves room for an
`overview.contexts.<name>:` map later without breaking these keys.

### 4. Large clusters: aggregates in the overview, details on Enter

The overview never has one row per node. Capacity rows show the sum over
all nodes, and their state is the worst node's flag, so one overcommitted
node shows even when the sum looks fine. Enter lists every node with its
own value, sorted by pressure (worst first). The same holds for every row:
targets are sorted by severity, then by a rule-specific rank (pressure,
restart count, newest event), then by namespace and name.

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
- While read, the engine renews the pool entry every TTL/3. Once no folder
  has read it for `kubernetes.clusters.ttl`, it removes its informer
  handlers and stops; the pool stops the cache one TTL later. This is the
  "stop through the pool's TTL" rule of the issue, done with the existing
  pool. When PR #85 lands, the engine should hold a lease instead of
  renewing the entry.
- A kind that cannot be read (not served, no permission, not synced after
  30 s) makes the rows that need it dim, with the reason in F3.

The cache is cluster-wide even for a namespace scope: Capacity needs every
pod on a node, nodes and PVs are cluster-scoped, and a namespace-scoped kc
cache cannot read cluster-scoped kinds. Namespace scope is a filter on the
rows.

### 6. Rows, Enter and F3

Each row has a value, a state word (`ok`, `warning`, `failed`, `n/a`, or a
capacity flag `high`, `full`, `overcommitted`) and a style from its
severity. Enter opens a folder that lists exactly the objects behind the
row. Its rows are object rows with full GVR identity
(`<group>/<version>/<resource>/<namespace>/<name>`), so F3, F4, F8 and Enter
into child folders work as in any object list, and a Value column says why
each object is listed. F3 on an overview row shows how the value is
computed, the scope, the reason a source is missing, and the kubectl
commands that show the same inputs.

### 7. Capacity "use" plugs in later

Capacity rows read measured usage through `overviewUsage`, a function that
returns usage per node. It is nil here, so the rows say `use n/a`. The
follow-up wires it to the metrics poller of PR #98 (`Deps.Usage`, node
snapshot) and adds the "under-requested" flag and the "pods near memory
limit" risk, which need usage.

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

## Consequences

- One Enter from any root shows load, health and risks for the scope.
- The overview adds one cluster-wide cache while it is open, released one
  TTL after it is left.
- Follow-ups on #51: metrics.k8s.io usage for Capacity and the near-limit
  risks (after #98), the Info panel mode (step 4), kubelet stats for node
  disk and volumes (step 5, plus optional cert-manager), `kc overview` and a
  key to open the overview for a list's namespace, leases from #85, and the
  shared row health styles from #93.
