# 0249. Report gaps and stale ends in session history coverage

- Status: Accepted
- Date: 2026-11-23
- Issue: #249

## Context

Info prints a line under the measurement time that says how much of the
investigation window (`C-x w`) the session usage history covers
(ADR 0177 and 0233 define the sparkline and its labels). The issue
reports that the line says "covered" when the window holds no sample at
all, or has a long gap, and that the earliest sample of either metric
stands for both. Confirmed on main: `windowCoverage` compares the
window start only with `history.first()`, the oldest sample of CPU and
memory together, and never looks at the rest of the samples.

## Decision

Coverage is computed per metric in `series.coverage` (new file
`internal/ui/panelcontent/info/window_coverage.go`), with the tolerance the
sparkline already uses: a sample covers the `gapAfter` (37.5s, two and a
half polls) that follows it. Two samples no more than `gapAfter` apart
are continuous; a last sample older than `gapAfter` at the end of the
window is stale. These are the same rules the sparkline uses for `·`
cells and for `stale, last 3m ago`.

For the interval from the window start to its end (or now, for an open
window), each metric reports:

- the first covered instant, and that the window starts before it
  (`from 11:58:00 only`); samples older than the 15 minutes the
  sparkline shows do not count, as before;
- the holes after that (`1 gap`, `3 gaps`);
- a last sample that no longer covers the end (`stale since 11:58:00`);
- no covered instant at all (`no samples`).

The line says `covered by the session usage history` only when both
metrics cover the whole interval. When both describe the same problem it
keeps the existing short form, `session usage history from 11:55:00 only
(incomplete)`. When they differ it names each:
`session usage history: CPU covered, memory from 11:58:00 only
(incomplete)`. A history without any sample keeps "no session usage
history yet", and one with samples but none in the window "no session
usage history for it".

`history.first`, which only this line used, is removed.

## Alternatives considered

- A single combined coverage (a timestamp counts when either metric has
  a sample). That is what hid a missing memory history behind a longer
  CPU one.
- A tolerance of one poll interval. A single late poll would then turn
  "covered" into a gap while the sparkline draws the cell as continuous;
  the two would disagree.
- Counting gaps over the whole kept history instead of the window. The
  line answers a question about the window; the sparkline label already
  reports the gaps of its own 15 minutes.

## Consequences

- The line can be longer when the metrics differ; it wraps like the rest
  of the head.
- Today CPU and memory are recorded together, so they differ only after
  a percentage base change. #250 records them apart, and this per-metric
  coverage already handles that.
- Which time range the sparkline draws is unchanged.
