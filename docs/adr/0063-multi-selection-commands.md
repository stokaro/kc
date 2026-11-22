# 0063. Marked rows as custom command input

- Status: Accepted
- Date: 2026-11-22
- Issue: #63

## Context

A custom command can declare `supportsMultiSelection: true`, and the F9
menu already counts the marked rows when it decides which commands to
offer. The launch path, however, passed only the highlighted row to the
command template, so `.Items` never held more than one item. All items
also shared one GVR, taken from the highlighted row.

The issue leaves open what happens with commands that do not support
multi-selection when rows are marked, which row wins when the cursor is
on an unmarked row, in which order marked rows reach the template, and
whether the template data should gain context or cluster fields.

## Decision

1. Targets. When any row is marked, the command acts on the marked rows
   only. The highlighted row is the target only when nothing is marked.
   This holds for every command, including one without
   `supportsMultiSelection` when exactly one row is marked. This matches
   Midnight Commander, where marked files win over the cursor.
2. Commands without `supportsMultiSelection`. When two or more rows are
   marked, the F9 menu does not offer them. kc does not run them for the
   highlighted row and does not ask which row to use.
3. Order. Marked rows reach `.Items` in the order the panel shows them.
   The table keeps marks in a set, so `SelectedItems` sorts them by row
   index, with the row ID as a tie-breaker.
4. Identity. Each item in `.Items` carries its own name, namespace and
   full GVR (`Group`, `Version`, `Resource`), taken from the object row.
   Same-name objects in different namespaces of an all-namespace list
   stay distinct. The top-level fields repeat the first item.
5. `showFor` is checked against every target, not only the highlighted
   row. A command restricted to pods is not offered when a marked set
   mixes pods and deployments.
6. No context or cluster field is added here. That belongs to #55
   (commands on the selected cluster) and #46 (more template fields).

## Alternatives considered

- Run a single-selection command for the highlighted row when several
  rows are marked. Rejected: the user marked rows on purpose, and acting
  on a different, unmarked row is the bug the issue reports.
- Ask which row to use. Rejected: it adds a modal for a case that a
  command author can fix by setting `supportsMultiSelection`, and #46
  is the place for prompts.
- Add the highlighted row to the marked set. Rejected: it makes the
  target set depend on where the cursor happens to stand.
- Keep marks in insertion order. Rejected: the order in which the user
  pressed Insert is not visible, and the table does not record it.

## Consequences

Batch commands see the whole selection and can loop over `.Items`. The
F9 menu can show fewer commands than before when the marked set mixes
resources that a `showFor` filter excludes. `Kind` stays empty, as
before, because object rows do not expose it. Building the target list
calls `Find` for every marked row; for very large marked sets in big
lists this is linear per row.
