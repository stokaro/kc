# 0258. Compare the visible or marked rows of a panel

- Status: Accepted
- Date: 2026-11-23
- Issue: #258
- Supersedes: the whole-list rule of [0196](0196-compare-resource-sets.md)

## Context

ADR 0196 added `C-x d` (compare the lists of the two panels) and `C-x c`
(compare the active panel's list with another context). Both read the
whole lists: a row filter (`M-f`, `C-z`) and marked rows did not narrow
the comparison, so a filter that shows two of ten objects still compared
ten. Issue #258 asks for visible, marked and whole-list modes, a frozen
selection with explicit handling of objects deleted or replaced during
the read, a result that states what each side evaluated, and accurate
documentation of the one-context-per-app model.

What the code does: `panelListSide` builds a `setdiff.Set` from the
panel's folder and reads the objects through `models.ListObjects`. The
row filter lives in the folder (`models.RowFilterable`); it is evaluated
against `RowFacts` that include the table cells of the server-side
listing, so column terms such as `cpu>500m` can only be evaluated where
the panel's table exists. Marks live in the list widget
(`Panel.GetSelectedItems`), the same set `F5` and `F8` use. Object rows
carry their UID (`ObjectRow.UID`).

The README said "Each panel has its own context"; the help page and ADR
0196 say the context is per app. The help page was right.

## Decision

1. **The panel decides its scope.** When a comparison starts, each panel
   contributes:
   - `marked`: its marked rows of the compared resource, when any are
     marked. Marks win over a filter, as they do for `F5` and `F8`.
   - `visible`: with no marks and an active row filter, the rows the
     folder serves under that filter. These are the rows the panel shows,
     taken from the folder itself, so the filter is never evaluated a
     second time against different facts.
   - `whole list` otherwise.
2. **`counterparts`.** A side without its own selection facing a side
   with one contributes only the objects whose key the other side
   selected. This applies to the other panel of `C-x d` and always to
   the other context of `C-x c`. Without it, filtering the left panel to
   two Deployments would report the eight hidden ones as "only right".
   A missing counterpart is genuinely "only" on the selecting side.
   When both panels are narrowed, each keeps its own selection.
3. **Frozen identities.** The selection records namespace, name and UID
   of each row at the key press. After the read, a member whose object
   is gone is `unavailable` ("deleted after it was selected"), and a
   member whose object has another UID is `unavailable` ("replaced after
   it was selected", with both UIDs). Such a key is unavailable on both
   sides and never counted as only on the other side. Objects created
   after the key press are not added. Rows without a UID are matched by
   name. In lists across namespaces the key is namespace/name, so equal
   names in two namespaces stay two members.
4. **Explicit whole-list mode.** `C-x a` compares the two whole lists
   (mnemonic: all). The Command menu also has "Compare whole list with
   context...", without a chord. `C-x a` is added to the keys custom
   commands cannot bind.
5. **The result states the scopes.** The title carries each side's scope
   and counts (`visible 2 of 10 (filter: web)`, `marked 3 of 40`,
   `counterparts 2 of 12`, `whole list 10`, plus `N gone since
   selected`). The command line shows the state counts and both scopes.
6. **Documentation.** The README context section now says kc works in
   one context at a time; `C-x c` is described as a read of another
   context, not a second live panel.

The selection logic is in `internal/setdiff/scope.go` (pure, tested with
objects); the UI only captures the selection (`panelSelection`,
`pairSelections` in `internal/ui/app_compare_sets.go`).

## Deviations from the issue

- **No mode prompt before each comparison.** The issue suggests an
  explicit mode choice. A prompt on every `C-x d` costs a keystroke in
  the common case where the mode is obvious from the panel (marks or
  filter present or not). kc picks the mode from what the panel shows
  and names it in the result; `C-x a` is the deliberate whole-list
  choice.
- **The other context does not re-evaluate the filter.** Column terms
  need the server-side table of the panel's list, which the other
  context's read does not produce, and evaluating only some terms would
  silently compare a different set. The other side contributes the
  counterparts of the selected keys instead.
- **No per-panel contexts.** As in ADR 0196, and as tracker #270 scopes
  it out, only the documentation was corrected.

## Alternatives considered

- **Each side strictly its own visible set** (no counterparts). Simple,
  but one filtered panel against an unfiltered one reports every hidden
  object as one-sided, which is noise, not evidence.
- **The union of both selections' keys on both sides.** It mixes two
  intents and makes the counts hard to explain.
- **Re-applying the filter to the objects read.** Name, label and
  namespace terms could be re-evaluated on the objects, column terms
  cannot; two filter engines would disagree on edge cases. Taking the
  folder's served rows is exact.
- **Marking by name only.** Loses the replacement case and merges equal
  names across namespaces.

## Consequences

- A comparison with marks or a filter still lists the whole resource
  through the cache (as before), then narrows it; no extra API calls.
- Hidden marked rows: marks of rows the filter hides are reported by
  the widget as it reports them to `F5`/`F8`; the comparison follows the
  same rule.
- Follow-ups: a mode indicator in the panel frame before the key press
  if users find the automatic mode surprising.
