# 0253. Read usage history at the resolution a chart shows

- Status: Accepted
- Date: 2026-11-23
- Issue: #253

## Context

The issue asks for a bounded query layer for the coming dashboard and
Info: resolution from the viewport and zoom, shared identical requests,
explicit units, effective range, CPU averaging window and memory peaks,
contributor coverage per step, object lifetime in the identity, and
bounds on concurrency, series, samples and response size, with
cancellation of obsolete work.

What `internal/promhistory` does on main (ADR 0197):

- `Fetch` reads one workload over the configured window cut into 60
  steps (`DefaultPoints`), with the end aligned to the step.
- CPU is `rate(...[max(5m, step)])` summed over the pods the owner
  series tie to the workload; memory is the working set at each step.
  A memory spike between two steps is not seen, and the Info block's
  "peak" is the largest value of those samples.
- The pod count is one instant `count(max_over_time(podset[window:step]))`:
  the pods seen at any time in the window. Two owned pods with one
  measured still look like a full sum.
- The Info key is context, backend, GVR, namespace, name and window. A
  workload deleted and recreated under the same name shows the old
  object's history.
- A response is at most 4 MiB and 8 series; each Info read is canceled
  when the cursor moves and dropped by a sequence number.

## Decision

New files next to `Fetch`; `Fetch` and the Info block stay as they are.

1. **`Range` and `RangeFor(start, end, cells)`.** One step per chart
   column: the span divided by the columns, rounded up to a whole second,
   at least 15 seconds. The end is aligned down to the step and the start
   is the first step at or before the asked start, so reads within one
   step ask for the same points and the asked start is covered. The
   columns are capped at `MaxPoints` (480): a wider terminal does not ask
   the server for more points. Zooming is a narrower `start..end`, which
   gives a finer step down to 15 seconds.
2. **`Client.Usage(ctx, Scope, Range)`.** Scopes: a namespace, a workload
   (the owner join of ADR 0197), a pod, and a node (pods that
   `kube_pod_info` places on it). One read is five range queries, at
   most three at a time:
   - CPU: `sum(rate(container_cpu_usage_seconds_total[w]) ...)` with
     `w = max(5m, step)`. `Usage.RateWindow` reports `w`: the chart says
     "5m average", and a peak of CPU is the largest such average, never a
     burst inside one window.
   - Memory: the working set at each step.
   - Memory peak: `max_over_time((<memory>)[step:res])` with
     `res = max(15s, step/10)`. `Usage.PeakResolution` reports `res`; at a
     15 second step the peak is the memory series itself. A spike shorter
     than `res` can still fall between sub-steps; the chart states the
     resolution instead of claiming the true maximum.
   - Measured: `count(sum by (namespace, pod) (<memory per pod>))`, the
     pods with samples at each step.
   - Expected: `count(<pod set> and on (namespace, pod)
     (kube_pod_status_phase{phase="Running"} == 1))`, the running pods
     kube-state-metrics knows at each step.
   `Usage.Coverage()` compares the two per step: full, partial (some pods
   measured), empty (none), or unknown when the expected count cannot be
   read. A step with no running and no measured pod is full: nothing ran.
   A failed usage query fails the read; failed counts leave coverage
   unknown and the usage visible.
3. **Lifetime.** `Scope.UID` and `Scope.Since` carry the live object's
   UID and creation time. A read never reaches before `Since`: the range
   starts at the first step after it, and `Usage.Clipped` says so. The
   owner series name objects, not UIDs, so data from before the creation
   belongs to an earlier object of the same name and cannot be told
   apart. A rollout keeps the Deployment's UID and creation time, so its
   history stays one series across ReplicaSets.
4. **`Shared`.** Reads with the same key share one request: a caller
   with the key of a running read waits for it, a caller with the key of
   a result younger than its TTL gets that result, and a read is canceled
   when every caller waiting for it has gone. It keeps the first caller's
   deadline. At most 4 reads run at once (`DefaultConcurrency`) and at
   most 64 results are kept (`DefaultCacheEntries`); errors are never
   kept. `Client.UsageKey` holds the backend, its labels, the scope with
   its UID and the range; the caller adds the cluster key with the
   impersonation, as Info already does.
5. **Bounds kept from ADR 0197.** 4 MiB per response, 8 series per
   query; `Usage` refuses a range query that returns more than one
   series. NaN and infinities are gaps.

The queries were run against Prometheus 3.5.0 with backfilled series (a
Deployment with two running pods of which one has cAdvisor samples, a
second Deployment in the same namespace, a 30 second memory spike between
two 2 minute steps):

| query    | result over 13 steps                                   |
| -------- | ------------------------------------------------------ |
| memory   | 100 MiB at every step: the spike is not on a step      |
| peak     | 500 MiB at the step that holds the spike               |
| measured | 1 at every step                                        |
| expected | 2 at every step: the coverage is partial               |
| node n1  | measured 2, expected 2 (one pod of each Deployment)    |

The other Deployment's pod was never summed into the first.

## Deviations from the issue

- **Info keeps `Fetch` for now.** The history-correctness issues (#248
  to #251) change the Info block and `Fetch` at the same time. Moving
  Info onto `Usage` and `Shared` is a follow-up once they have landed;
  the dashboard (#252) is the first user.
- **No refresh cadence in the layer.** Refresh belongs to the view: a
  paused dashboard does not refresh, a live one rereads once per step.
  The layer only makes identical reads cheap.
- **No per-step contributor list.** Counts per step answer "is this sum
  complete"; naming the missing pods needs a per-pod query and belongs
  to a drilldown.
- **No ambiguity flag for backend labels.** Lifetime clipping removes the
  case kc can detect (an object recreated under the same name). Two
  clusters in one server are handled by the configured labels and by
  the warning of ADR 0257.
- **Late results across scope changes** are dropped by the consumer with
  a sequence number, as Info does; `Shared` cancels a read nobody waits
  for any more.

## Alternatives considered

- **Max-pool finer reads on the client.** Reading at 15 seconds and
  folding to the chart width moves up to 40,000 points per series for a
  week. The subquery keeps the work on the server and the response at
  one point per column.
- **`max_over_time` on the raw memory series per pod, then sum.** The sum
  of per-pod maxima overstates the workload's peak when the pods peak at
  different times. Summing first, then taking the maximum, measures the
  workload's own peak.
- **An expected count from the owner series alone.** It counts completed
  Job pods and pods that never started, so a finished CronJob would
  always look partial. Intersecting with `phase="Running"` counts the
  pods that should have samples.
- **A generic singleflight package.** `golang.org/x/sync/singleflight`
  does not cancel a read when its callers leave and has no result cache.

## Consequences

- One chart costs five small range queries per read; identical charts
  cost nothing extra.
- Coverage depends on kube-state-metrics' `kube_pod_status_phase` and
  `kube_pod_info`; without them coverage is unknown and the chart says so.
- Follow-ups: Info on `Usage` after #248 to #251; requests and limits
  history and throttling (#254).
