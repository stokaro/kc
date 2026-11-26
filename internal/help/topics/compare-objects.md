# Compare objects

`C-x C-d` compares the object under the cursor of the left panel with
the object under the cursor of the right panel, as mc's "Compare files".
kc opens a read-only unified diff of the two YAML manifests. Use it to
see why two Deployments behave differently, or what differs between the
same ConfigMap in staging and production (switch one panel to the
other context). Each side is read from its own panel's cluster.

One side can be a local file instead: put one panel on a local folder
(`M-l`) with the cursor on a YAML or JSON file, such as the output of
`kustomize build` or `helm template`, and the other on the live object.
kc picks the file's document for that object (same API group, a kind of
that resource, same name, and the same namespace or none) and compares
it. A file without such a document, or with several, gives an error
that says so. The header names the file with its SHA-256 and
modification time, and the live object with its `resourceVersion`.
API server defaults are not applied to the file, so fields the server
filled in show as differences.

## How to read it

The diff starts with a header:

- `# left:` and `# right:` name each object by context, resource type
  with group and version, namespace and name.
- `# view:` is `configuration` or `raw`.
- `# omitted:` lists the fields kc left out of either side.

A header line wider than the screen wraps at its commas; the rest lines
up under the value. The diff lines below do not wrap.

Then follows a normal `diff -u`: lines starting with `-` are only on
the left, lines with `+` only on the right, with three lines of context
around each change. `No differences in the configuration view.` means
the two objects match apart from the omitted fields.

The configuration view, the default, leaves out the fields the API
server writes:

- `metadata.uid`, `metadata.resourceVersion`, `metadata.generation`
- `metadata.creationTimestamp`, `metadata.managedFields`,
  `metadata.selfLink`
- `status`

Nothing else is removed or reordered. Annotations such as
`last-applied-configuration` stay, and the order of containers, env
vars and ports counts as a difference. The raw view compares the
objects exactly as read, `managedFields` included.

The last line shows a shell command that prints the same diff:
`diff -u <(kubectl get ...) <(kubectl get ...)`.

The diff is a snapshot: it stays as it was read, also when the objects
change in the cluster.

## Keys

- `C-x C-d` opens the diff. Without an object under each cursor (or a
  local file and an object), kc says so.
- "Compare objects" in the Command menu (`F9`) does the same.
- `n`, `Space` or `Enter` go to the next change; `p` or `Backspace` to
  the previous one.
- `r` switches between the configuration and the raw view.
- `s` adds `status` back, or removes it again.
- `F7` searches; `Shift-F7` continues the search.
- `M-a` copies the whole diff, `M-c` the shell command.
- `F3`, `F10` or `Esc` close the viewer.

## Limits

- Lists are compared in their order; a reordered list shows as a
  change.
- There is no side-by-side view.

## See also

- [Compare lists](compare-lists.md)
- [Diff viewer](diff-viewer.md)
- [Viewer](viewer.md)
