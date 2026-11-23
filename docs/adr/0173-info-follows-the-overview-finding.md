# 0173. Info describes the selected overview finding

- Status: Accepted
- Date: 2026-11-23
- Issue: #173

## Context

The right panel starts in Info mode (ADR 0166). When the left panel shows
`/overview`, Info keeps showing the capacity bars of the scope, whatever
finding the cursor is on. `infoTarget` narrows the summary only for an
`ObjectItem` that is a node or a supported workload, and an
`OverviewFindingItem` is neither. This matches what the issue says.

## Decision

- `OverviewFindingItem` implements `models.FindingPreviewer`. Its
  `FindingPreview` reads the overview folder's latest report once and
  returns the row's title, scope, value, state and health. It also returns
  the first paragraph of the calculation (what the rule checks, with its
  thresholds), the first eight targets in the order Enter lists them, the
  total count, the unavailable reason with verification commands, and the
  number of input commands.
- `infoTarget` puts the preview on `Target.Finding`, and the widget renders
  it instead of the bars. Any other row leaves `Finding` nil, and the bars
  come back.
- The preview is built from the report of the overview folder that shows
  the row, not from the Info widget's own engine. Value, state, scope and
  target count therefore come from one snapshot, the same one the row
  shows.
- It is built synchronously while the target is resolved, on each
  selection change and on each one-second tick. There is no asynchronous
  load, so a later selection cannot receive an earlier selection's data.
- Unavailable findings list read-only checks:
  - `kubectl auth can-i list <resource>`, in the finding's scope, when the
    reason is forbidden;
  - the finding's first input command.
- A finding with no targets says "Nothing matched. Checked:" and shows the
  calculation's first paragraph.
- A finding with nothing to check (n/a, ADR 0172) says "Not applicable"
  with its reason, such as "cert-manager is not installed", instead of
  "Nothing matched".
- The rows inside `/attention` and the `/N quiet checks` folders (ADR 0172)
  are the overview's own finding rows, so they preview the same way.

## Deviations

- No structured threshold field. Every rule states its thresholds in the
  first paragraph of its calculation text, which the preview shows. A
  separate field would need every rule edited, while another lane (#190) is
  changing the rules.
- The Info panel shows the keys but does not take them. Enter, F3 and M-c
  act in the list, where the cursor is. Sending them from the Info panel
  would need a new route from one panel's widget to the other panel's
  navigator. It would also duplicate keys that already work one Tab away.
- The preview does not mark checks that have nothing to check. That
  distinction comes with #172 (PR #221). Once it merges, the preview can
  show the not-applicable reason as well.

## Consequences

- With the default layout, moving through `/overview` explains each row in
  the right panel without opening F3.
- The Info engine keeps running while a finding is shown, so its usage
  history continues and the bars come back at once.
- The preview adds no cluster reads: it formats a report already in memory.
