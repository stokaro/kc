# Local Files

A panel can list a directory of your own machine. Use it to copy files
to and from a container, or to view and edit a local file next to the
cluster.

`M-l` (or `Esc l`) opens the directory of the last copy, or kc's
working directory, in the active panel. `M-l` again returns the panel
to the cluster folder it came from. The Left and Right menus have the
same entry as "Local files", after Info.

## How to read it

- The panel path reads `local:/abs/dir`, so it is never taken for a
  cluster path.
- The columns are Name, Size, Mode and Modified. Directories come
  first.
- Directories show as `/name`, links to directories as `~name` and
  other links as `@name`, as in mc.
- An `error` row says why the directory could not be read.

## Keys

- `Enter` opens a directory and views a file, like `F3`. `/..` goes up
  one level; from `/` it returns to the cluster.
- `F3` shows a text file in the viewer.
- `F4` opens the file in `$KUBE_EDITOR`, else `$EDITOR`, else `vi`,
  the editor `kubectl edit` uses. The editor saves the file in place,
  and kc rereads the listing when it exits. An editor with arguments,
  such as `code --wait`, works.
- `F5` with a container folder in the other panel copies the marked
  files and directories (or the row under the cursor) into the
  container.
- `C-t` or `Ins` marks a row; `*` inverts the marks; `+` and `-` mark
  or unmark the names that match a pattern such as `*.log`.
- `M-l` leaves the local tree in one step.

## Limits

- `F3` and `F4` refuse directories, special files and binary files (a
  NUL byte in the first 8000 bytes). `F3` opens a file over 2 MiB in
  pages; see [File and YAML viewer](viewer.md).
- `F4` on a local file does not touch a cluster, so read-only mode
  allows it.
- After an edit or a copy kc reads the listing again.
- kc does not delete, rename or create local files; `F8` works on
  cluster objects only.

## See also

- [Container filesystem](container-fs.md)
- [Viewer](viewer.md)
- [Copy](copy.md)
- [Panels](panels.md)
