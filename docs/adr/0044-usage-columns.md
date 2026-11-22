# 0044. CPU and memory usage columns in pod and node lists

- Status: Accepted
- Date: 2026-11-22
- Issue: #44

## Context

k9s shows CPU and memory usage from metrics-server in its pod and node views.
kc only had the `kubectl top` custom commands. The issue asks for usage
columns with percentages of requests and limits, polled at a low rate, and
hidden when `metrics.k8s.io` is not served. It leaves open which columns,
how often and when to poll, what to do when the API is missing or forbidden,
and how the columns join the server-side Table that kc renders.

Constraints: kc must stay cheap on big clusters (AGENTS.md: object state from
the shared cache only, an INFO log line before every real request, no work on
the Bubble Tea goroutine). Column sorting (#37, PR #82) is in review and
detects quantity columns from cell text. The cluster overview (#51) will reuse
the metrics client.

## Decision

### 1. Columns

Pods: `CPU`, `MEM`, `%CPU/R`, `%MEM/L` in normal mode; wide mode adds
`%CPU/L` and `%MEM/R`. The order is k9s's: `CPU MEM %CPU/R %CPU/L %MEM/R
%MEM/L`. `%CPU/R` says whether the pod uses what the scheduler reserved for
it; `%MEM/L` says how close it is to an OOM kill. Those two answer most
questions, and six extra columns do not fit a half-screen panel.

Nodes: `CPU`, `%CPU`, `MEM`, `%MEM` (percent of allocatable) in normal mode;
wide mode adds `CPU/A` and `MEM/A` (allocatable itself).

Requests and limits sum the regular containers and the restartable init
containers (sidecars), because metrics cover exactly those containers. Plain
init containers are left out: they are not running when usage is measured.
A missing request counts as zero. A limit counts only when every counted
container sets it; otherwise the pod is unlimited and the cell is `n/a`.

### 2. Cell format

CPU is always in millicores (`250m`, `1500m`) and memory always in MiB
(`128Mi`), so each column holds one unit and reads as a Kubernetes quantity.
Percentages are whole numbers without a `%` sign (`45`); the `%` is in the
title, as in k9s. With PR #82's detection, `45%` would read as text and sort
`100%` before `45%`; a plain number sorts as a number. CPU cells such as
`250m` also read as durations in #82, which tries durations first; that is
harmless because every cell has the same unit.

`n/a` marks a value kc cannot compute: no metrics for the object yet (a new
pod), no request, limit or allocatable to compare with, or metrics that
cannot be read. #82 treats `n/a` as a missing cell, so it sorts last.

### 3. Poll interval and lifetime

One LIST of PodMetrics or NodeMetrics per scope (cluster, kind, namespace)
every 15 seconds, metrics-server's default resolution; polling faster returns
the same numbers. The interval is fixed, not a config key: no one has asked
to change it, and a config key would need the defaults kept in sync in three
places. A new key can come with #51 if the overview needs another interval.

A poller runs while some list read its snapshot within the last two
intervals. A list reads the snapshot when it loads its rows, and each poll
marks the reading lists dirty, so a list on screen reloads (from the cache)
and keeps the poller alive. A list that is not on screen does not reload and
the poller stops 30 seconds later. When it stops, it marks its last readers
dirty once more; a list still on screen reloads and starts it again. Polls
are skipped while a dialog is open (`models.Activity`), like list reloads.
The poller runs in its own goroutine and is tied to the app context.

Lists in the same scope share one poller, so both panels on the same
namespace cost one request. The pods of a node use their own poller: their
cache is selector-scoped, so it cannot share pod specs with the all-namespace
list.

### 4. metrics.k8s.io not served, forbidden or failing

Before each poll kc asks the RESTMapper (cached discovery, no request)
whether `metrics.k8s.io/v1beta1` serves the resource. If not, or if the LIST
returns NotFound, the list shows no usage columns at all, as the issue asks.
The poller keeps checking discovery, so columns appear once metrics-server is
installed and discovery refreshes.

If the API is served but the LIST fails (Forbidden, or 503 while
metrics-server is down), the columns stay and every cell says `n/a`. The
failure is logged at INFO once per change of state and never reported to the
user as an error message: a forbidden API would otherwise raise a red message
every 15 seconds.

### 5. Merging into the server Table

The usage columns are appended to the server Table (`tablecache.RowList`)
before kc picks visible columns and sorts: new `TableColumnDefinition`s with
type `string` and priority 0 (normal) or 1 (wide), and one cell per row.
Wide mode and PR #82's column sort then treat them as server columns with no
extra code. The merge copies the column list and each row's cells, because
the rows can share memory with the cache.

Requests, limits and allocatable come from the shared controller-runtime
cache (`Cluster.ListByGVR`), read in the poller goroutine once per poll. Each
row looks up a precomputed snapshot by `namespace/name` (nodes by name).

The metrics client is `Cluster.Dynamic()` with unstructured PodMetrics and
NodeMetrics. #51 can call the same fetch for its capacity rows.

## Alternatives considered

- **Typed client from `k8s.io/metrics`.** Adds a module for two small list
  types that unstructured access reads in a few lines. The dynamic client
  already exists per cluster.
- **All six pod columns in normal mode.** Too wide for a two-panel layout.
  Wide mode shows them.
- **`45%` cells.** Reads better, but sorts as text under #82.
- **A cell hook that renders usage at draw time** (like the Age column).
  Repaints would not need a reload, but the values would not be in the
  RowList, so #82 could not sort by them.
- **Polling from a global ticker for every open folder.** Folders on the
  navigation stack are not visible and would keep polling. Tying the lease to
  list reloads needs no UI wiring and stops by itself.
- **Hide the columns on Forbidden.** The user would not know usage exists;
  `n/a` with an INFO log is clearer and stable between polls.
- **Pod specs from the Table rows.** Rows carry metadata only
  (`includeObject=metadata`), so the requests are not there.

## Consequences

- Opening a pod list now also starts a full pod informer in the shared cache
  for that scope (for requests and limits), next to the metadata-only Table
  informer. For all namespaces on a big cluster that costs memory until the
  pool TTL evicts the cluster. The poll also deep-copies the cached pods once
  per interval. If that turns out too heavy, the percentages can move to wide
  mode only, or the poller can read the informer's indexer without copies.
- The columns appear a moment after the list (after the first poll, about
  150 ms on a small cluster), and values are up to 15 seconds old.
- A snapshot older than two intervals (after the poller stopped) shows empty
  cells until the next poll, instead of old numbers.
- Sorting by usage works once PR #82 lands, without changes here.
- #51 can reuse `fetchUsage` and the per-scope poller for its capacity rows.
