# Row filter

`M-f` opens the Filter dialog for the object list in the active panel.
The filter hides the rows that do not match and keeps following live
updates. Use it to cut a long list down to what you are looking at, for
example "API pods in prod that restarted more than three times".

## Syntax

The filter is a list of terms separated by spaces. A row must match
every term.

- `nginx`: the name matches the regular expression, ignoring case. In a
  list across all namespaces it also sees `namespace/name`.
- `!nginx`: the name does not match.
- `-l app=web`: the labels match a label selector, as `kubectl -l`.
  Several `-l` terms combine.
- `-n web,api`: the namespace is one of these. In a list of all
  namespaces this narrows the view; `namespace!=kube-system` leaves one
  out.
- `<column><op><value>`: a column compares with a value. The column is
  its title in lower case, spaces as dashes: `cpu>500m`, `mem>=1Gi`,
  `restarts>3`, `status=Running`, `node=worker-1`, `age<10m`.

Operators are `=`, `!=`, `>`, `>=`, `<` and `<=`. `=` and `!=` take a
list: `status=Pending,Failed` keeps either one, and
`status!=Running,Completed` keeps neither. Values compare the way the
column sorts: CPU and memory as quantities, restarts as counts, ages as
time, the rest as text in any case.

Spaces inside parentheses stay in the term, so `^(web| api)` is one name
term and `-l env in (a, b) web` is a selector plus a name term. To match
a space in a name, write `\s`.

Example: `-n prod,stage -l app=api restarts>3 !canary`

## How it works

- A row with an empty, `<none>` or `n/a` cell matches no column term,
  `!=` included. A cluster-scoped row matches no namespace term. A term
  never makes the view wider.
- The dialog refuses a column the list does not have and a value the
  column cannot hold (`cpu>fast`), and says why.
- Hidden wide columns still filter, so hiding a column does not drop its
  term.
- The active filter shows in the bottom border (`Filter: !nginx`).
- The filter belongs to the folder: it stays while you look at its
  objects and come back. A new listing starts unfiltered.
- Marked rows that the filter hides are unmarked.
- `M-c` copies the kubectl command with the label selector as `-l`.
  Terms kubectl has no flag for are listed after it in a comment:
  `# kc also filters: -n prod,stage, cpu>500m`.

## Keys

- `M-f` or `Esc f` opens the dialog with the current filter.
- `Enter` applies the text. An empty text clears the filter.
- `Esc` closes the dialog and keeps the filter as it was.
- A click on `Filter:` in the bottom border clears the filter.
- `C-z` shows only the rows in a fault state; it combines with the text
  filter and shows as `faults` in the border.

## Limits

- The filter works on rows kc already has; it does not ask the API
  server for other objects.
- There is no owner term yet.

## See also

- [Panels](panels.md)
- [Panel options](panel-options.md)
- [Context and namespace](context-namespace.md)
