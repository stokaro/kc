# 0186. Sort across namespaces

- Status: Accepted
- Date: 2026-11-22
- Issue: #186

## Context

ADR 0037 (decision 3) made the namespace the outer sort key of lists that span
namespaces: all namespaces, and the pods of a node. Sorting such a list by CPU,
memory or restarts sorts inside each namespace block, so it cannot answer
"which pod uses the most memory in this scope?". The issue asks for an explicit
"group by namespace" option, a global order when it is off, documented
placement of missing values, deterministic tie breakers, stable row identity,
and persistence with the other view settings.

What the code does today, checked on main:

- `orderRowIndices` (internal/models/object_sort.go) sorts by the column, then
  runs a second stable sort by namespace. Typed comparisons (quantities, counts,
  ratios, durations, IPs) are already there, and CPU/MEM usage cells are
  ordinary columns, so they sort by value.
- Missing cells (empty, `<none>`, `n/a` from the usage columns) already come
  last in both directions.
- Row IDs of lists across namespaces are already `namespace/name`, and the
  cursor and the marks follow the row ID, so same-named objects in different
  namespaces keep their identity whatever the order.
- kc has one namespace scope per panel (one namespace or all), not a set of
  selected namespaces.

## Decision

1. **A `groupByNamespace` view setting, on by default.** `objects.groupByNamespace`
   in the config (default `true`, the current behavior) and a per-panel value
   that starts from it. When on, lists across namespaces are grouped as before.
   When off, the sort column orders all rows at once. Lists of one namespace and
   cluster-scoped lists are not affected.
2. **One comparator for both layouts.** The sort is a single stable sort with the
   key chain: namespace (grouped layout only), the sort column, name, namespace.
   Sorting by `namespace` gives the same order in both layouts: namespaces in the
   chosen direction, names ascending inside.
3. **Missing values last in both directions**, unchanged from 0037, and now
   stated in the README. Ties break by name, then by namespace, so the order does
   not depend on the order the cache returns the rows in.
4. **How to switch.** "Group by namespace" in the Left/Right menu (checked while
   grouped, disabled for lists that do not span namespaces) and a "Group by
   namespace: Yes/No" row in the panel options (`M-F1`/`M-F2`), shown only for
   lists that span namespaces. Save as defaults (`C-s`) in the panel options
   writes `objects.groupByNamespace`. No new key binding.
5. **Switching changes only the order.** The sort column, the row filter, faults
   only, the namespace scope and the data source stay as they are. The folder
   re-renders from the shared cache; no new request or watch.
6. **The folder reports whether grouping applies** from its last listing, so the
   menu does not ask the RESTMapper on the update goroutine.

## Alternatives considered

- **Global sort by default.** Answers the issue's question with no extra step,
  but changes what every existing user sees in `-A` lists and departs from
  `kubectl get -A`. Kept grouping as the default; the setting is one toggle away.
- **Encode the grouping in the order string** (for example a `ns:` prefix).
  Reuses the existing plumbing, but mixes two settings in one value, breaks the
  "Sort by" choices in the panel options and makes saved orders harder to read.
- **A new key binding.** Most free Meta letters are already taken, and the
  toggle is not something an operator flips many times a minute. The menu and
  the panel options are enough; a key can be added later.
- **Treat "namespace" as the toggle** (choosing Sort by Namespace means grouped,
  anything else global). Removes grouped browsing by restarts or age, which the
  issue asks to keep.

## Consequences

- Decision 3 of ADR 0037 becomes the default of a setting instead of a fixed
  rule. 0037 stays accepted; this record extends it.
- The per-panel value lives in the panel's config copy like the sort order, so it
  is kept while kc runs and written to the config only by Save as defaults.
- Not done: no visible marker of the layout in the panel header besides the menu
  check and the panel options row. If users ask for it, a small mark next to the
  Namespace column title is the natural place.
- The issue's "selected namespaces" maps to kc's one namespace scope per panel.
  If kc gains a multi-namespace selection, it uses the same setting, as long as
  the folder reports that it spans namespaces.
