# 0241. Row health from the informer objects

- Status: Accepted
- Date: 2026-11-23
- PR: #241
- Supersedes: the health source in ADR 0038, Consequences ("Health is read from the object in the cached Table row")

## Context

ADR 0038 reads row health (the row colors and the `C-z` faults filter) from the object embedded in the cached Table row. The table reader asks the server for `includeObject=Metadata` (`internal/tablecache/reader.go`), so that object carries metadata only: no `status` and no `spec`. Every health rule then saw an empty object. Rows were never colored, and `C-z` kept nothing, even for pods in `ImagePullBackOff`. The demo recording on kind showed it.

## Decision

1. When an object list builds its rows, it reads the full objects of the list from the shared informer cache (`Cluster.ListByGVR`, which reads the indexer of the list's own informer) and gives each row the object with the same namespace and name. Health rules, the row stylers and the faults filter read that object.
2. Only kinds with a health rule (a built-in rule in `healthRules` or a health adapter, ADR 0195) read objects. Other kinds do not touch the cache for this.
3. A row whose object is not in the cache, or a read that fails or passes `customColumnsLookupTimeout`, falls back to the row's metadata, as before. The failure is logged at V(1); the list still renders.
4. The table request stays `includeObject=Metadata`.

## Alternatives considered

- Ask the server for `includeObject=Object`: every table response and every cached row would carry the full manifest, for all kinds, which is the memory cost `Metadata` was chosen to avoid. The informer already holds these objects.
- One GET per row: N requests per refresh, and stale or missing data while they run.
- Print the columns on the client from the informer objects: loses server-side printing, which kc relies on for CRD columns.

## Consequences

- Health is as fresh as the informer cache. The list refreshes on that informer's events, so the colors follow the object changes that trigger the refresh.
- Each refresh of a list with a health rule does one indexer list and a map lookup per row. This is in memory, but linear in the list size.
- A kind that gets a health rule starts reading objects without other changes.
