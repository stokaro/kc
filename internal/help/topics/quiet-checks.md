# Quiet Checks

A healthy cluster has many checks with nothing to report. The overview
folds them into one row at the end of each section, `/N quiet checks`,
so the rows that matter stay on screen. `Enter` on that row lists them
when you want to see what was checked.

## How it works

A check moves into its section's quiet row when it is either:

- ok with no objects behind it (it was computed and found nothing), or
- `n/a`: there was nothing to check, for example the CronJob checks in
  a namespace without CronJobs.

Checks that are ok but list objects stay visible. The capacity rows
list every node, so they never fold.

The quiet row shows the counts of what it holds, such as `9 ok, 3 n/a`,
and the state `hidden`. It is dim.

The section header still counts every check of the section, quiet ones
included.

When a check turns quiet, it moves into the quiet list, even while the
cursor is on it. When it finds a problem again, it comes back to its
place in the section. Other rows keep their order.

A check with recovered targets is not quiet: it stays in place for the
ten minutes the overview lists the recovered objects, then folds.

## Keys

- `Enter` on `/N quiet checks` lists the folded checks. `/..` returns
  to the quiet row.
- `Enter` on a check lists the objects behind it, if any.
- `F3` on a check explains the value. For an `n/a` check it says that
  nothing was checked.
- `M-c` copies the input commands of the check in focus.

## Limits

- There is no key to unfold the quiet checks in place; they always
  open as a list.
- An unavailable check is never quiet. It stays in its section and is
  counted in `/attention`.

## See also

- [Cluster overview](overview.md)
- [Attention](attention.md)
