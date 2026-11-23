# 0275. Context view rows map to their own records

- Status: Accepted
- Date: 2026-11-23
- Issue: #275

## Context

The logs viewer's context view (ADR 0188) shows the lines around a record. When older records were dropped, `contextDisplay` puts a notice row first ("older lines were dropped to stay within the limits"), but `focusRecord` indexed `context.lines` with the display row directly. Every row then pointed at the next record: `M-d` on beta opened gamma's fields, the notice row opened alpha, and the last record had no fields at all. The issue's trace is correct on current main.

## Decision

1. `logsContext.noticeRows` says how many rows come before the first context line (one when the context is incomplete, else zero), and `logsContext.seqAt` maps a display row to a record through it. A notice row maps to no record, so `M-d` there says "no line to inspect".
2. `showContext` scrolls the anchor into view with the same offset.
3. Field actions (include, exclude, follow, copy) all start from `focusRecord`, so they use the shown record's sequence number.

## Deviations from the issue

The issue suggests an explicit row-to-sequence map. One notice row is the only non-record row the context view has, so an offset in one place (next to the code that adds the row) is enough; a map would need to be rebuilt in step with the same code. If more notice rows are added, the map becomes the better shape.

## Consequences

- Retention after the context opened does not change the shown rows (the context is a snapshot); an action on a record dropped since then reports that the line was dropped, as before.
- The test drives each row of an incomplete context, for the merged and the same-source context, and checks the fields dialog opens on that row's record.
