# 0233. Info history: label first, framed sparkline

- Status: Accepted; the frame mark extended by 0342
- Date: 2026-11-23
- PR: #233
- Supersedes: the label format and placement in ADR 0177, section "Label"

## Context

ADR 0177 put a label under each CPU and memory sparkline, in the form
`% of allocatable · 4 samples over 40s · 15m session`. In use the block read
badly. During the first minutes of a session the sparkline holds only a few
cells at the right end of an otherwise blank line, so it looks like a stray
horizontal bar. Its label came after it and sat right above the next bar
(`Memory`, `Pods`), so it read as the heading of that bar. A user asked what
the bars were.

## Decision

- The label comes first and starts with `history`:
  `history, % of allocatable · 4 samples over 40s`. With one sample it reads
  `history, % of allocatable: collecting, 1 sample`; with only old samples,
  `history, % of allocatable: no sample yet`.
- The sparkline is framed, `▕ ... ▏ 15m`, so the window it spans and the
  position of the newest samples at its right end are visible even while
  most cells are still blank.
- A blank line ends the block.
- `15m session` is dropped from the label; the frame names the window, and the
  README says the history is kept for the session only.

Everything else in ADR 0177 stays: the fixed 15-minute window, one cell per
poll at most, gaps as `·`, stale marks, and separate histories per scope.

## Consequences

The block takes one more line per bar (the blank line). Text that matched
the old label format must use the new one.
