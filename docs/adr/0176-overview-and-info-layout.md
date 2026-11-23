# 0176. Overview columns, Info layout, scrolling and top-pod links

- Status: Accepted
- Date: 2026-11-23
- Issue: #176

## Context

The issue asks for measurements that stay readable at any terminal size,
for a way to reach all Info content, and for top pods that can be opened.
Its claims, checked against current main:

- **Bars stretch across the pane.** True. `barLine` gave the bar all the
  width left after the label and the percentage, so at a wide pane the
  percentage sat at the far right edge, away from the start of its bar.
- **The Name column is wide and the Value column narrow.** Not quite. In
  the default scroll mode `BigTable` sizes each column to its content and
  gives any spare width to the first column. A wide pane therefore shows a
  wide Name column, but Value always has its full content width. The real
  problem is in a narrow pane: at 80x24 a panel has about 38 columns, a long
  name or a long Capacity value pushes State past the right edge, and the
  row's only severity label is reachable only by horizontal panning.
- **Info truncates lines and drops what is below the pane.** True. `lines`
  cut each line to the width with `ansi.Truncate`, and `clip` dropped lines
  beyond the height. `Update` handled only the widget's own ticks.
- **Top pods are plain text.** True. They were formatted strings.
- **Referenced nodes.** The Info summary names no node objects. Its `node
  6.0/14.9 Gi capacity` lines are node usage totals, and the node scope is
  already the other panel's cursor. There is nothing to link.

A further problem showed up while checking mouse input. The app's
double-click handling enters the row that the panel's list widget has
selected, whatever the panel's mode is. In Info, Describe, Manifest or Logs
mode the list is hidden, so a double click entered a row nobody could see.

## Decision

1. **Shrinkable table columns.** `table.Column` gets `MinWidth`. In scroll
   mode, when the columns are wider than the viewport, the columns that
   have a `MinWidth` shrink, the widest first, down to their minimum, before
   the line needs horizontal panning. A cell cut this way ends in `…`. Zero
   (every existing folder) keeps today's behavior.
   - Overview finding rows (`/overview`, `/attention`, quiet checks): Name
     12 and Value 10. State never shrinks, so at 38 columns every row,
     section headers included, shows its whole state.
   - Objects behind a finding: Name 14, Namespace 8, Resource 8. Value, the
     reason the object is listed, never shrinks.
   - The full text stays available: F3 shows the finding and its
     calculation, and panning still works below the minimum.
2. **Bars of a readable, shared width.** Every bar of one summary has the
   same width, at most 40 cells, so their lengths compare and each
   percentage follows its bar. The width leaves room for each bar's
   percentage and state word. When the attention marks (`! lim`) would
   leave less than 10 cells, the marks move to the next line instead. In a
   very narrow pane the percentage moves there too. Nothing is cut. When
   the bar leaves room on its line, the first line under it moves up next
   to it, so a wide pane uses the spare width for the explanation.
3. **Wrapping, not truncation.** The scope, titles, the lines under each
   bar, the sparkline label, hints and errors wrap at word boundaries with
   their indent. So do the head lines (scope, usage age with its stale
   color on every line, investigation-window coverage) and the lines of
   the Prometheus history block (#197),
   which scrolls with the rest of the content. In a pane too narrow for the usual indent, the lines under
   a bar indent by two cells. A top pod's name wraps under itself.
4. **Info scrolls.** PgUp and PgDn move by a page less one line, Home and
   End go to the start and end, and the wheel scrolls one line, as in the
   viewers. When content is hidden, the frame shows `^` above and `v` below,
   and the bottom border shows the visible lines (`8-26/34`). Scrolling
   never touches the other panel. The preview of an overview finding (ADR
   0173) scrolls the same way, and another finding starts at the top.
5. **Top pods are links.** Down selects the next top pod when it is on
   screen and scrolls one line otherwise; without a selection it starts at
   the first pod on screen. Up selects the previous pod, clears the
   selection above the first one, and only scrolls without a selection, so
   it can always leave the pods. Enter opens the selected pod. A click
   selects a pod and a click on the selected pod opens it. The bottom
   border says `Down selects a pod` or `Enter opens the pod`. Opening sends
   `OpenObjectRequest` with the pod's full GVR (`v1` `pods`), namespace and
   name to the existing link handling: the other panel lists the pods of
   that namespace with the pod selected, and the Info panel keeps the
   focus.
6. **Selection by identity.** The selection is the pod's namespace and name.
   Every top pod is a pod, so that is its full identity. A live update
   keeps the selection while the pod is listed, wherever it moves, and drops
   it when the pod leaves the list. Another scope, node or workload resets
   the scroll offset and the selection. Content that gets shorter clamps
   the offset. A selection that scrolls off screen is dropped, so Enter
   never opens a pod the user cannot see.
7. **Double click only in list mode.** The app enters the selected row on a
   double click only when the panel shows its list.

## Deviations

- **No linked nodes.** The summary shows no node objects to open, as
  described above. A node scope comes from the other panel, which already
  shows the node.
- **No content-proportional column allocation.** `BigTable` is shared by
  every folder. A new width algorithm for all lists is a larger change than
  this issue needs, and other lanes would feel it. The opt-in minimum
  fixes the failure the issue describes, a hidden state, and leaves other
  lists as they are.
- **A second click opens instead of a timed double click.** The app's
  double-click handling belongs to the list (Decision 7). A click on the
  pod that is already selected is the same gesture without a timer, and
  works the same over a slow terminal.
- **The wheel scrolls one line.** The viewers do the same. A larger step
  would skip the short Info content.
- **No full-screen screenshots.** The 80x24, 120x30 and wide checks render
  the Info widget at the pane widths of those terminals (38, 58 and 98
  columns), and `/overview` through `BigTable` at 38 and 120 columns. The
  test checks that every line fits, that each state word, mark and unit is
  there, and that each bar stays at most 40 cells with its percentage next
  to it.
- Menu and dialog wheel behavior stays with #168.

## Consequences

- Info reads nothing new. The layout is computed from the summary it
  already has. `FrameInfo` builds the content once more per render to know
  the line count, which is a few dozen short lines.
- Opening a pod moves the other panel to the pod's namespace (the existing
  link behavior). Info follows the other panel, so when Info showed all
  namespaces, it now shows that namespace. This is how Info works, and the
  list path is the same as Enter on an Overview event.
- A folder that wants narrow-pane columns sets `MinWidth`. Cells are ASCII
  by the table's contract; a cell that is not is cut at a rune boundary and
  padded, never left with a broken rune.
- The double-click fix also stops a double click in Describe, Manifest or
  Logs mode from entering the hidden list's row.
