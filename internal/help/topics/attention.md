# Attention

`/attention` is the first row of the cluster overview. It counts the
whole report in one line, so one warning among thirty quiet checks is
seen at once. `Enter` on it lists only the checks that need you.

## How to read it

The value counts what needs attention:

- `1 failed, 2 warning, 1 unavailable`, in that order, or
- `nothing` when every check passed or had nothing to check.

The state counts the rest, for example `20 ok, 12 n/a`:

- `ok` checks were computed and found no problem.
- `n/a` checks had nothing to check, such as the Job checks in a
  namespace without Jobs. They are not counted as passed.

An `unavailable` check is one whose source kc could not read (forbidden,
not served, not synced yet). It is never counted as passed.

In a namespace where no namespace check applies, the state says
`no workloads in namespace <name>`. The cluster checks (capacity, nodes,
volumes) still run there.

The row takes the color of the worst class it counts: failed, then
warning, then dim for unavailable.

## The attention list

`Enter` opens a list of the checks that need attention: failed first,
then warnings, then unavailable sources, each group in the order of the
overview. The rows are the same overview rows, so the same keys work.
When nothing needs attention, the list says `nothing in attention`.

The list follows the live report. A check that recovers leaves the list,
and a new problem appears without reopening it. A cluster check listed
here always ends in `(cluster)`.

## Keys

- `Enter` on `/attention` opens the list.
- `Enter` on a check lists the objects behind it; `/..` goes back.
- `F3` on a check explains the value and its inputs.
- `M-c` copies the input commands of the check in focus.
- In Info mode, the other panel describes the check under the cursor.

## See also

- [Cluster overview](overview.md)
- [Quiet checks](quiet-checks.md)
- [Info mode](mode-info.md)
