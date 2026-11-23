# 0274. Keep log coverage and stream state visible while searching

- Status: Accepted
- Date: 2026-11-23
- Issue: #274

## Context

`LogsViewer.FooterStatus` returned the inner viewer's search summary whenever a search was applied, and only otherwise the log status (`statusText`). While a search was active the footer therefore hid the merged-log coverage ("incomplete: 1 streaming, 1 failed"), the stream state (`(reconnecting)`, `(failed)`, `(ended)`) and the retention note ("10000 older dropped"). In-band `[kc]` notices are no substitute: a field filter can hide them. The issue's claim holds on current main.

## Decision

1. While a search is applied, the footer shows the search summary, then ` | `, then the alert: the parts of the status a search must not hide.
   - The coverage of a merged log, only when it is incomplete (waiting, reconnecting or failed sources).
   - The stream state when it is not streaming: connecting, reconnecting, failed, ended.
   - What retention dropped or cut, and "view moved: its lines were dropped".
2. With nothing to alert, the footer is the search summary as before.
3. A narrow footer cuts the search first; when not even 8 cells of the search fit, it shows the alert alone. The full status (options, mode, filter) returns when the search is cleared, and the per-source details stay in the sources dialog (`M-s`).
4. `viewer.Widget.SearchSummary` exposes the unstyled summary, so the logs viewer can compose the line. Other viewers keep `FooterStatusText`.

## Deviations from the issue

- The issue lists retention loss "while searching and filtering". Filtering (without search) already shows the full status, which includes the retention note; only the search path changed.
- The search summary loses its own highlight style on this one line, because both parts now share it. The search state stays readable as `/query (n/m)`.

## Consequences

- An operator searching a log sees the evidence is incomplete or the stream stopped without clearing the search.
- The stream state now comes from one helper (`streamState`) for both footers. It orders failed and ended before reconnecting, as #277 does for the full status.
