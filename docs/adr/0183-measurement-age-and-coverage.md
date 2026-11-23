# 0183. Measurement age and coverage of usage data

- Status: Accepted
- Date: 2026-11-23
- Issue: #183

## Context

kc dated a metrics snapshot when its poll finished. PodMetrics and
NodeMetrics carry their own `timestamp` and `window`. metrics-server can
keep answering with old samples after it stops scraping a node, so a
successful read every 15 seconds looked current while the numbers were
minutes old. The node capacity sums also counted a node's pod usage as
known when at least one of its pods had PodMetrics. With 3 of 10 pods
measured, the sum of those 3 was shown as the node's pod usage.

The issue also asks for live, reconnecting, stale, unavailable and partial
states for every table, overview row and Info view, and for watch health.
#219 has since added unknown overview targets for denied or unsynced
sources, and startup states.

## Decision

1. **Measurement time from the metrics.** A snapshot keeps the read time
   (`at`) and, separately, the newest and oldest `timestamp` of its objects
   and the newest one's `window`. Each pod or node value keeps its own
   timestamp.
2. **Stale samples are not current usage.** A snapshot whose newest sample
   was 2 minutes old or more when kc read it is stale (`UsageStaleAfter`).
   metrics-server measures every 15 seconds, so that age means it stopped.
   The overview then treats pod or node usage as unknown, with the reason
   `metrics-server's newest sample was 4m old when read`. That reaches F3,
   the capacity rows and the Info bars the same way as other unavailable
   reasons. In pod and node lists, a value whose own sample is that old shows
   `n/a`. Recovery needs no special step: the next read with a recent sample
   is current again.
3. **The age shown is the measurement's.** The overview dates its usage by
   the older of two times: the newest node sample and the newest cluster-pod
   sample. The read time stays separate. Info shows both:
   `usage measured 25s ago over 15s, read 3s ago`. A stale sample reads
   `usage measured 4m ago: stale, metrics-server returns old samples, read
   1s ago` in the warning color. Metrics without timestamps read `usage read
   5s ago, measurement time not reported`. The Info history (#177) dates
   samples by measurement, so rereading one sample adds nothing.
4. **Partial coverage is visible.** Node capacity amounts count the running
   pods that have PodMetrics and the pods that should. A partial sum keeps
   filling the bar, because it is a lower bound worth seeing. It is marked:
   `pods 2.3/10.4 Gi allocatable, 22%, 7 of 10 pods measured` in Info, and
   `pods 22% (7/10 measured)` in the `/overview` capacity rows. A workload's
   unavailable usage says `2 of 3 pods measured` instead of "not measured
   for every pod". A node with none of its pods measured stays unknown, as
   before.

## Deviations from the issue

- **No reconnecting or watch-health state.** The shared controller-runtime
  informers do not report a reconnect to their readers. A watch error
  handler has to be installed in the cluster pool before the informers
  start. That is a change to the cache layer, and every list would need a
  place to show it. A quiet watch is not marked stale, since nothing in kc
  dates informer data by its last event. #219 already marks unsynced and
  denied sources as unknown in the overview.
- **Stale values are not kept as marked last-known numbers.** Overview rules,
  capacity states and list columns compare numbers with thresholds and sort
  them. A stale number in those places would be judged as current. kc
  shows the age (Info) and the reason (F3, bars) instead. Keeping a marked
  value would need a second, inert display form in every column and row.
- **No per-object age in lists.** A stale pod shows `n/a`, like a pod
  without metrics. An age column for every pod would mostly repeat "15s".
- **Container usage in the overview's pod rules** follows the snapshot's
  staleness, not each pod's sample. metrics-server drops pods it cannot
  scrape rather than returning them old, so the snapshot-level check covers
  the case seen in practice.

## Alternatives considered

- **Date the snapshot by the oldest sample.** One slow node would make the
  whole cluster look stale. The newest sample shows whether metrics-server
  measures at all. The oldest is kept for a later per-node check.
- **Treat partial coverage as unknown.** It hides real usage on nodes where
  most pods are measured. A marked lower bound tells more.

## Consequences

- `InfoSummary` gains `UsageWindow` and `UsageReadAt`; `UsageAt` now means
  the measurement time.
- `capacityAmounts` carries the measured and eligible pod counts, so every
  view that sums capacity reports coverage the same way.
- Follow-ups: a watch error handler in the cluster pool that feeds a
  "reconnecting" state to lists and the overview; per-node staleness from
  each NodeMetrics timestamp.
