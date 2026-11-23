# Diff Viewer

The diff viewer shows a unified diff of two YAML manifests, as
`diff -u` prints it. It opens from `C-x C-d` (the objects under the
two cursors), from a pair in a list comparison (`C-x d`, `C-x c`), and
works like mc's diff viewer. It is read-only.

## How to read it

The first lines name what is compared:

- `# left:` and `# right:` give each object's context, group, version
  and resource, namespace and name.
- `# view:` is `configuration` or `raw`.
- `# omitted:` lists the fields left out of either side.

The `---` and `+++` lines carry the same identity as a path. Each
change is a hunk with three lines of context. Two equal manifests read
`No differences in the configuration view.` The footer counts the
changes (`3 changes`, `change 2/3`).

The configuration view, the default, leaves out what the API server
writes: `metadata.uid`, `resourceVersion`, `generation`,
`creationTimestamp`, `managedFields`, `selfLink` and `status`. Nothing
else is removed or reordered: the order of containers, env vars and
ports counts, and annotations stay. The raw view compares the objects
exactly as read.

The diff is a snapshot: it does not change while you read it, until
you read both objects again.

## Keys

- `n`, `Space` or `Enter` goes to the next change; `p` or `Backspace`
  to the previous one.
- `r` switches between the configuration and the raw view.
- `s` adds `status` to the configuration view, or removes it.
- `C-r` reads both objects again. A failed read keeps the old diff and
  shows the error on the last line.
- `F7` (or `/`) searches. Because `n` moves between changes, search
  continues with `Shift-F7`, and `N` goes back.
- `F2` opens the viewer options: color theme and wrap mode.
- `M-a` copies the whole diff; `M-c` copies the command on the last
  line, `diff -u <(kubectl get ...) <(kubectl get ...)`. In the raw
  view it adds `--show-managed-fields`.
- Scrolling and mouse selection work as in the [viewer](viewer.md).
- `F3`, `F10` or `Esc` closes the viewer.

## Limits

- Each object is read through its own panel's context; kc never
  switches context for a compare.
- The diff is line based, not a semantic YAML merge. A reordered list
  shows as a change.
- There is no side-by-side view.

## See also

- [Compare objects](compare-objects.md)
- [Compare lists](compare-lists.md)
- [Viewer options](viewer-options.md)
