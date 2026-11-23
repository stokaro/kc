# 0273. Marks and severity jumps in the logs viewer

- Status: Accepted
- Date: 2026-11-23
- Issue: #273

## Context

The full-screen logs viewer keeps a bounded buffer of records (ADR 0179) with stable record numbers (`logview.Seq`), a filter, a context view and field actions (ADR 0188), and Solo since #271. To find a burst or come back to a few important records, a user scrolls or searches text. The issue asks for a time histogram with bucket navigation, a jump to a timestamp, jumps to the next warning or error, bounded session marks with next/previous, an elapsed-time reference record, and a copy of the marked records with their source.

The claims hold on current main: there is no mark, no severity jump and no histogram, and the record in focus is inferred from the search match, the follow state or the top line.

## Decision

1. **Severity jumps.** `e` and `E` go to the next and previous shown record whose level is warn or worse. The level is read the way the formatted view reads it: the first level key of the format (`level`, `severity`, `lvl` and the others), normalized by the same labels, numeric pino and bunyan levels included. A text line has no level; kc does not guess one from words such as `ERROR`, which would match messages that only mention errors.
2. **Marks.** `m` marks the record in focus, or unmarks it. At most 100 marks (`logsMaxMarks`). A mark is a record number: it stays on its record when the filter, the view mode or Solo change the display lines, and it holds no copy of the line, so it does not bypass the retention limits. A mark whose record was dropped is forgotten, and the next mark action says how many went; it never moves to another record. A restarted stream (F2 options) clears the marks.
3. **`u` and `U`** go to the next and previous marked record that is shown. **`c`** copies the marked records in record order, each as it arrived (with the kubelet timestamp when the stream has timestamps) and, in a merged log, with its `[pod/container]` source.
4. **A pointer.** The record a jump or a mark went to is highlighted (`viewer.Widget.SetPointer`, drawn as the selection is) and is the record in focus for the mark and field keys until the user scrolls or searches. It is kept as a record number and mapped to its display line on each draw. Without a pointer, a forward jump searches from the top line of the view and a backward one from its bottom line, so a match already on screen is found first.
5. **Keys.** Plain letters, as the viewer's `j`, `k`, `g`, `G`, `n` and `N` are: `e`, `E`, `m`, `u`, `U` (the lnav keys for the same actions) and `c`. They are free in the viewer, and plain letters take no modifier key from custom commands. The status line counts the marks; the footer shows `m`, `u` and `e`.

## Deviations from the issue

- **No histogram and no jump to a time.** Most container logs in a kc buffer are text without a reliable time: the kubelet timestamp exists only with `--timestamps`, and JSON time fields come in many formats and zones. A histogram whose buckets drop the untimed records, or mix two clocks, would describe the buffer wrongly. Buckets also need a layout in the full-screen modal next to the log, the same split layout #271 deferred. The investigation window (`C-x w`, ADR 0187) already limits what a stream reads to a time range; #255 (dashboard lane) owns the shared time model.
- **No elapsed-time reference record.** It depends on the same record times.
- **No visible mark gutter.** A gutter column would change every display line, which copy and search work on. The pointer highlights the line a mark or jump went to, and `u` and `U` walk the marks.
- **Severity of text lines is not recognized**, as said in decision 1.

## Consequences

- `logview.Format.Severity`, `logview.Log.Severity` and `logview.Log.Next` are available for later views.
- The record in focus is now explicit after a jump or a mark; scrolling or searching returns to the old inference.
- Follow-ups: a histogram and time jumps once records carry a parsed time with its origin (kubelet or field) and the full-screen viewer has a side pane; klog-style level prefixes (`E1123 ...`) as a recognized text severity.
