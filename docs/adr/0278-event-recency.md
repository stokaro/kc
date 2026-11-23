# 0278. Event recency: sort and show Last Seen by the Event's times

- Status: Accepted
- Date: 2026-11-23
- Issue: #278

## Context

The issue's two claims hold on current main.

1. `rowsFromRowListWithObjects` gave the first column the sort key `name`, and `resolveObjectSort` skipped column zero, because the first column is the name in almost every server table. The Event table starts with Last Seen and has Name as a wide column. Sorting by Last Seen sorted by the Event name.
2. The Last Seen and First Seen cells are relative times that the server printed when it sent the row. The table cache keeps that text until the Event changes, so `10s` stays `10s`. The age hooks only rewrite a column titled Age, from the creation time. Sorting these cells by value is also wrong across rows: a row that arrived an hour ago with `5s` is older than a row that arrived now with `20m`.

The table rows carry metadata only (`includeObject=Metadata`), so the Event times are not in them. The Event list already runs its own informer on the shared cache to refresh, and ADR 0241 reads full objects from that informer for health rules.

## Decision

1. The name column is the column the server marks with `format: name`; a table without the mark has the name first, as before. That column gets the `name` sort key, and `resolveObjectSort` skips it instead of column zero. In the Event table, Last Seen sorts as Last Seen.
2. For the two Event APIs (`v1/events` and `events.k8s.io/v1/events`, both printed with the same table) `loadRowObjects` also reads the Event objects from the list's informer (memory only, no request).
3. `eventOccurrences` computes first and last as the server's Event printer does: first is `firstTimestamp` (or `deprecatedFirstTimestamp`), else `eventTime`; last is `series.lastObservedTime` for a series, else `lastTimestamp` (or `deprecatedLastTimestamp`), else first. A missing time stays missing; the creation time is not used.
4. Sorting by Last Seen or First Seen compares these times: most recent first ascending, oldest first with `-`, missing times last both ways, ties by name. This works in one namespace and across namespaces, grouped or mixed.
5. The age hook is generalized to one time per column. The Last Seen and First Seen cells of an Event row show the time since the Event's own moment and advance on the existing age timer, without API polling. A row without a time keeps the server's cell (for example `<unknown>`).

## Deviations from the issue

- In normal columns the Event table has no Name column, so the panel cannot sort Events by name there; the wide columns (`C-w`) have it and sort by name. Adding a hidden name sort key to a list that does not show the name would make `M-o` cycle to a column the user cannot see.
- Only the Event table gets occurrence times. Other tables with relative date columns (CronJob Last Schedule, Job Duration) keep their cell values; they are not in the issue, and each would need its own field mapping.

## Consequences

- Each refresh of an Event list does one indexer list and a map lookup per row, as health-rule kinds already do. An Event list in a busy namespace keeps the full Events in the informer it already had; nothing new is cached.
- The overview's "events warnings" finding keeps its own "latest of" rule (`eventLastSeen`); it ranks by the newest of all times, which is what a window check needs.
