# 0177. Info history: labels, time window and gaps

- Status: Accepted
- Date: 2026-11-23
- Issue: #177

## Context

The Info panel draws a sparkline under the CPU and memory bars. It had no
label. It kept the last 60 percentages without timestamps and drew the last
`width` of them, so the time it covered changed with the pane width. It
skipped polls without usage, so two measurements minutes apart sat next to
each other as if collection were continuous. A single sample drew nothing,
with no explanation.

## Decision

1. **Samples keep their time and base.** A series stores `(time, percent)`
   pairs and the base of the percentage, which the bar now reports
   (`InfoBar.Base`: `allocatable` for a scope or node, `limits` or
   `requests` for a workload, the bases of #169). When the base changes,
   the series starts again, because the numbers no longer compare. A poll
   without measured usage adds nothing. It never adds a zero.
2. **A fixed window.** The sparkline always covers the last 15 minutes. It
   has at most one cell per 15-second metrics poll (60 cells). A narrower
   pane puts several polls into one cell and shows their peak. A wider pane
   does not stretch the line, because cells between polls would otherwise
   look empty. Cells are aligned to absolute time, so a sample does not jump
   between cells as the clock moves.
3. **Gaps are visible.** Two samples more than 2.5 polls apart (37.5 s) are a
   gap. The cells between them show `·` and the label counts the gaps. Up to
   that spacing, an empty cell continues the previous value, so normal poll
   jitter shows no gap. Cells before the first sample stay blank. When the
   newest sample is older than 37.5 s, the newest cells are gaps and the
   label adds `stale, last <age> ago`.
4. **A label under each sparkline**:
   `% of allocatable · 21 samples over 5m · 1 gap · 15m session`. It names
   the base, the number of samples in the window, and their actual span
   (oldest to newest, not the full window). It ends with the retention, which
   is held in memory for this session. With one sample it reads
   `% of allocatable: collecting, 1 sample so far`. When only older samples
   are kept it reads `no sample in the last 15m`.
5. **Separate histories.** Each history stays keyed by context and scope,
   node, or workload with its full GVR, as before, so switching never joins
   them.

`models.UsagePollInterval` exports the metrics poll interval, so the history
and the poller cannot drift apart.

## Alternatives considered

- **Mean instead of peak per cell.** A mean hides a short spike, which is
  what someone looks for in a usage trace.
- **Stretch the window to the pane width (one cell per poll, more minutes
  when wider).** The covered time would change with the width again, which
  is the problem the issue describes.
- **Record an explicit "missing" sample per failed poll.** The widget reads
  the summary every second and cannot tell a skipped poll from a slow one.
  The spacing between real samples tells it directly.
- **Persist the history.** The issue keeps this to the local session.
  Longer collection belongs to a monitoring system.

## Consequences

- Each sparkline takes one more line (its label).
- Samples are recorded only while a panel is in Info mode. Time spent in
  another mode shows as a gap when the panel comes back, which is what
  happened.
- The same rendering can serve a longer history later. Only the window and
  the retention in the label would change.
