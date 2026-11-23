# Copy

`F5` copies to and from your machine. On an object, a log or a viewer
it saves that content to a local file. Between a container folder and a
local panel (`M-l`) it copies the marked files and directories, in
either direction, as mc copies between two panels.

## Copy to a local file

`F5` on an object (its YAML), a log, an open viewer, or one container
file opens the Copy dialog: `Copy "<subject>" to:` and a file path. A
container file streams whole, whatever its size; the copy fails only
when the stream stalls for 30 seconds.

- The path starts in the other panel's directory when that panel shows
  the local filesystem, else in the system temp directory.
- The path must be absolute and its directory must exist.
- `Overwrite an existing file` starts off. On an existing file kc says
  "The file exists; check Overwrite to replace it".

## Copy between a container and a local panel

Open a container's filesystem in one panel and a local directory
(`M-l`) in the other. Mark entries with `Ins` and press `F5`.

From the container to local, a gray dialog shows the context, pod,
container, source directory and entries, then `to:` with the local
target directory, which you can edit. A line counts the entries that
already exist there. `Overwrite existing entries` replaces files and
merges into directories; unchecked, existing entries are skipped.

From local into the container, the dialog is red, because it writes
into a running container. It names the source, the context, pod,
container and target directory (the other panel's directory, fixed),
shows the `kubectl cp` equivalent and the conflict count. `Cancel` has
the focus.

## How it works

- kc copies with `tar` over exec, as `kubectl cp`. Copying from a
  container without `tar` falls back to reading through the file
  browser. Copying into a container needs `tar` in the image.
- Each file is written to a temporary file and renamed only when it is
  complete, so a canceled or failed copy leaves nothing that looks
  finished.
- Symbolic links, devices, pipes and sockets are never copied; the
  result lists them as left out.
- Local copies keep only the owner permission bits (0644 becomes
  0600). Copies into a container keep the local permission bits.
- While a copy runs, the dialog shows the current entry, files and
  bytes. Afterward it lists each entry: copied, skipped, failed with
  the reason, or canceled.

## Keys

- `F5` opens the dialog. `Ins` marks entries first.
- `Tab` and `Shift-Tab` move between fields; `Space` toggles a check box.
- `M-w` toggles Overwrite (the underlined letter).
- `Enter` copies, `Esc` cancels.
- During a copy, `Esc` or `C-c` cancels; the entry in progress is
  removed. `Enter` or `Esc` closes the result.

## Limits

- Read-only mode refuses copies into a container.
- A copy never starts the debug helper of a container without a shell.
  Open the folder and agree to the helper first.
- Copying into a container needs create on `pods/exec`.

## See also

- [Container filesystem](container-fs.md)
- [Local files](local-files.md)
- [Viewer](viewer.md)
- [Read-only mode](read-only.md)
