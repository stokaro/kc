# Container Filesystem

Each container of a pod has a folder with the rows `logs`,
`logs (previous)` (only after a restart), `shell` and `/root`. `/root`
browses the files of the running container, so you can check a config
file, a mounted secret or a log written to disk without `kubectl exec`.

Open a pod, then its `/containers` folder, then a container.

## How it works

kc runs a small `sh` script in the container over one exec stream and
lists directories through it. The shell closes after 60 seconds without
use and opens again on the next listing.

When the image has no shell, `/root` shows `Start a debug helper...`.
`Enter` opens a confirmation that names the context, namespace, pod and
container. After `Start`, kc adds an ephemeral busybox container with
`CAP_SYS_PTRACE` to the pod and reads the target through `/proc/1/root`.
The helper exits after a minute without use, but it stays in the pod
spec until the pod is recreated. kc asks again before it adds another.

## How to read it

- The columns are Name, Type, Size and Modified. Directories come
  first.
- A symbolic link shows its target in the row details.
- `loading...` means a listing is still running; `debug helper` rows
  show the state of a helper that is starting.
- An `error` row has the reason in its details, for example a missing
  command in the image.

## Keys

- `Enter` opens a directory; `/..` goes up.
- `F3` shows a file. The viewer shows the matching
  `kubectl exec ... -- cat <path>` command.
- `F5` saves the file under the cursor locally (export dialog).
- `F5` with a local panel on the other side copies the marked files
  and directories (or the row under the cursor) there. `F5` in the
  local panel copies the other way, into the container.
- `C-t` or `Ins` marks a row; `*` inverts the marks.
- `Enter` on the `shell` row opens a shell in the container, as
  `M-x` does on the container row.

## Copying between a container and a local panel

The copy uses `tar` in the container, like `kubectl cp`, and falls back
to reading files one by one when the image has no `tar`. Copying into a
container needs `tar` in the image.

- A download asks for the local directory (`to:`) and has an
  "Overwrite existing entries" option. Without it, existing entries
  are skipped.
- An upload changes the cluster: its dialog is red and `Cancel` has
  the focus. It shows the equivalent `kubectl cp` command.
- `Esc` or `C-c` cancels a running copy. Nothing of the current entry
  is kept, and the rest are reported as not started.
- The result lists every entry: copied, skipped, failed with the
  reason, or canceled.

## Limits

- `F3` opens a file over 512 KiB in pages: kc reads one block at a
  time with `dd` on the container's file shell, without a new exec
  request. See [File and YAML viewer](viewer.md).
- Symbolic links, devices, pipes and sockets are never copied; the
  result lists them as left out.
- Downloaded files keep only the owner permission bits (0644 becomes
  0600). Uploaded files keep their mode without setuid, setgid and
  sticky bits.
- Browsing needs `pods/exec`. Read-only mode refuses the debug helper
  and uploads.
- Through the helper, an absolute symbolic link resolves in the
  helper's filesystem, not in the target's.

## See also

- [Local files](local-files.md)
- [Shell](shell.md)
- [Copy](copy.md)
- [Read-only mode](read-only.md)
