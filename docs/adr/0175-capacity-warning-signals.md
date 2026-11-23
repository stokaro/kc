# 0175. Name the signal behind a capacity warning

- Status: Accepted
- Date: 2026-11-23
- Issue: #175

## Context

After ADR 0165 and 0174 the Info bar and `/overview` share one capacity
calculation, but each still shows one color for several signals:

- An Info CPU or memory bar is colored by the worst of its lines
  (`InfoBar.Health`). Modest pod usage with limits above allocatable
  draws a red usage bar, although the warning belongs to the limits. The
  lines under the bar are not colored at all.
- An `/overview` capacity row shows the sum over the nodes in its value
  and the worst node's state word (`overcommitted`, `full`, ...). That
  word does not say which line caused it or that it comes from one node
  and not from the sum.

The issue asks for per-signal coloring, separate labels for requests and
limits, an overall attention indicator, an explanation when a row's state
comes from one node (with a count and a route to it), and severity that
can be read without color, also under selection.

## Decision

### 1. Info: each line has its own state

`InfoBar.Health` is now the state of what the bar shows: pod usage, or the
requests when pod usage is not known. The bar and its percentage use it.
The new `DetailHealth` and `MoreHealth` color each line under the bar by
its own state. Requests and limits share the line `req 25%, lim 116%
(overcommitted)`, which is colored by the worse of the two. Its state word
already names the value it belongs to (ADR 0165). Lines whose usage is
n/a stay plain.

### 2. Info: an attention mark names another signal

`InfoBar.Attention` is the worst state of all lines. When the line that
causes it is not the one the bar shows, `AttentionText` names it after
the percentage, in that line's color:

```
Memory  █████░░░░░░░░░░░░░  22% ! lim
        pods 2.3/10.4 Gi allocatable, 22%
        node 6.0/14.9 Gi capacity, 40%
        req 25%, lim 116% (overcommitted)      <- colored as a warning
```

There is one `!` for a warning and two for a failure. The signals are
`pods`, `node`, `req` and `lim`. Among lines in the same state, `lim`
comes first, then `pods`, `node` and `req`, the same order as the state
word (ADR 0174).

### 3. /overview: the state names the line and the node count

A capacity row's state is the state of its first node in Enter's order
(worst first), with the line that causes it and how many nodes are in that
same state: `pods full, 1 of 2 nodes`, `lim overcommitted, 1 of 2 nodes`.
With a single node the count is left out (`node high`). Each node in the
list ends with its own `<line> <state>`. The value stays the sum, so a
healthy sum can now be read next to a state that says it comes from one
node. Enter opens the list with that node first. F3 explains this.

### 4. Without color

- Every non-ok state has a word in the text: `full`, `high`,
  `overcommitted`, `under-req`.
- Info adds `!` or `!!` and the signal name.
- Unavailable usage reads `n/a` with the reason, and a healthy line has
  no word.
- The `/overview` state column holds the word and the signal. A selected
  row, whose colors the cursor replaces, still says both.

## Deviations from the issue

- **Warning and failure share a hue.** Both use the shared row-health
  styles of object lists: warning is `#FF5C5C`, failure is bold
  `#FF5C5C`. Skins do not change these styles. Giving warnings their own
  color (amber, say) would change every object list, not only Info, so it
  is left as a follow-up. In this change the two are told apart by text:
  `!` / `!!` and `full` against `high` / `overcommitted` / `under-req`.
- **One line for requests and limits.** The issue suggests separate
  labels for each. The shared line already carries the word next to the
  value it belongs to. Splitting it adds a line per bar to a panel whose
  height is already tight (ADR 0165), so only its color is new.
- **The Info total keeps judging the sum.** The node count is added to
  `/overview`, where Enter leads to the nodes. Info's scope bar judges the
  sum and has no node list. Its per-node view is the other panel's cursor
  on a node.
- **No selection styling work in Info.** The Info panel has no cursor. In
  `/overview` the state text already survives selection, so the row
  styles are unchanged.

## Alternatives considered

- **Keep the worst-of-all color on the bar and add text only.** The issue
  is the color itself: a red usage bar reads as high usage.
- **Show the aggregate's own state on the row and the worst node in the
  value.** The row's color would then hide a full node until Enter. Naming
  the node count keeps the warning visible and says where it comes from.
- **A separate attention column or row.** It costs width or height in a
  half-width panel. The mark after the percentage costs 6 to 7 cells.

## Consequences

- Tests that read `InfoBar.Health` as "worst of all lines" now read
  `Attention`. The capacity row state strings in `/overview` are longer.
- New tests check each combination of usage, requests, limits and node
  usage in the model (`TestInfoBarSignals`), the rendered colors and marks
  (`TestWidgetColorsEachSignal`), and a two-node case with a healthy sum
  and one full node (`TestOverviewCapacityOneNodePressured`).
- Follow-up: a distinct warning color for row health across all lists.
