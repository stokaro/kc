# 0248. Prometheus history without finite samples

- Status: Accepted
- Date: 2026-11-23
- Issue: #248

## Context

The issue reports a panic in the Info pane when a successful Prometheus
range query returns a series whose samples are all `NaN` or infinite,
or a series with `values: []`. Confirmed on main: `points` drops the
non-finite samples and returns an empty, non-nil slice; `Fetch` and
`promHistoryLines` test only for nil; `promSeriesLine` then reads
`ps[len(ps)-1]` and panics with index -1, which ends the TUI. Its peak
would also have been `-Inf`.

## Decision

1. The provider keeps the distinction it already makes, now documented
   on `History.CPU` and `History.Memory`: nil means Prometheus returned
   no series; empty and not nil means it returned a series without one
   finite sample. Neither becomes a zero.
2. `promSeriesLine` guards both at the rendering boundary, whatever the
   provider returns: nil shows `no series`, empty shows
   `no finite samples in this window`. The sibling metric renders as
   before, and the coverage line still counts the missing steps of both
   series (an empty series is missing every step).
3. The `kube_pod_owner` check that explains "no series at all" still
   runs only when both series are nil, since a series that exists shows
   the pods are tied to the workload.

## Alternatives considered

- Normalize an empty series to nil in `points`. That removes the panic
  but reports "no series" for a series that exists, and triggers the
  owner check with a misleading explanation. The renderer would still
  depend on the provider for its safety.
- Render an all-gap sparkline. It looks like a measurement that was
  missing for an hour, while the cause is in the query or exporter; a
  plain sentence is clearer.

## Consequences

- An all-`NaN` metric no longer terminates kc; it is shown as missing
  data next to a valid sibling.
- The help page for the Info mode explains both states.
