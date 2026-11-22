# 0073. Copy files between local and container folders

- Status: Accepted
- Date: 2026-11-22
- Issue: #73

## Context

kc browses container filesystems through `internal/podfs`: a long-lived exec
session runs a small shell script in the container (or, when the image has no
shell, in a busybox debug helper that reads the target through `/proc/1/root`
and that kc adds only after the user agrees). `F5` exports one viewable item
(a manifest, a log snapshot, one container file of at most 512 KiB) through a
path dialog. A directory, or several files, needed a separate `kubectl cp`.

The issue asks for a local directory in one panel and a container directory in
the other, with `F5` copying the marked entries in the background, with
progress, cancellation and a result per entry. Stage 1 is container to local;
stage 2, local to container, follows once the transfer model is stable.

It left open: how a local directory gets into a panel, how the bytes travel,
what happens to existing local files, symbolic links and permissions, and how
progress and cancellation look.

Stage 2 (local to container) adds more open points: how the bytes get into
the container and what happens when the image has no `tar`; whether the debug
helper can write; how a write into the cluster is confirmed and how it fits
the read-only mode of #42 (`docs/adr/0042-read-only-mode.md`); what "no partial
output" means on the container side; and which permissions the copies get.

## Decision

1. **Entering a local directory: `M-l`.** It opens the directory of the last
   copy (or kc's working directory) in the active panel. The panel gets one
   folder per directory level from `/` down, so `Back` walks up the tree and,
   from `/`, returns to the cluster folder the panel came from. `M-l` in a local
   folder leaves the local tree in one step. `Esc l` is the fallback, as for the
   other Meta keys. The panel path reads `local:/abs/dir`, so it is never
   mistaken for a cluster path. The listing is read only and has the columns
   Name, Size, Mode and Modified; directories are shown as `/name`, links to
   directories as `~name` and other links as `@name`, as in mc.

2. **Mechanism: `tar` over exec, else the filesystem session.** For a session
   in the target container, kc runs `tar cf - -C <dir> ./<name>` in a second
   exec stream, like `kubectl cp`, and extracts the stream itself. When that
   stream fails before its first entry (no `tar` in the image), kc copies the
   entry through the existing session instead: it walks directories with the
   session's listing and reads files with its `cat` command. A session that
   runs in the debug helper streams busybox `tar` from `/proc/1/root/<dir>`
   under a small script that touches the helper's heartbeat file every 10 s
   while `tar` runs, because the helper exits after a minute without a session
   command (stage 2; stage 1 always used the walk there). It falls back to the
   walk the same way.
   The copy reuses the panel's session, so consent given for the helper is
   reused and a copy never adds a helper itself; when one is needed and not yet
   agreed to, the copy refuses and says to open the folder and start it.

   Both producers feed one writer (`internal/filexfer`), so every safety rule
   is written once:

   - archive names are cleaned of a leading `./`; absolute names, names with a
     `..` element (even one that stays inside, such as `a/../b`), NUL or `\`,
     and names outside the copied top-level entry fail the entry;
   - all local writes go through an `os.Root` opened on the destination
     directory, so no path and no symbolic link can reach outside it;
   - symbolic links are never created. They, devices, pipes and sockets are
     listed as "left out" in the result. A tar hard link is written as a copy of
     the file it names (inside the same entry);
   - an existing local symbolic link at a path the copy would write is refused
     with an error, never followed and never replaced.

3. **Direction: both.** `F5` in a container folder with a local panel on the
   other side downloads; `F5` in a local panel with a container folder on the
   other side uploads (decisions 8 to 12). With any other pair `F5` keeps its
   export behavior, and on a local row it says to open a container folder in
   the other panel.

4. **Existing files.** The dialog counts the marked entries that exist in the
   destination. Without conflicts it offers `Copy` and `Cancel`. With conflicts
   it offers `Skip existing` (the default focus), `Overwrite` and `Cancel`.
   - `Skip existing` leaves an existing file or directory untouched and
     reports the entry as skipped.
   - `Overwrite` replaces an existing file, and merges into an existing
     directory: files of the copy replace local files of the same name, other
     local files stay. A read-only local file (no owner write bit), a local
     non-regular file in the way, or a file where the copy has a directory (and
     the reverse) fails the entry.

5. **No partial output that looks finished.** Each file is written to a
   private temporary file (`.<name>.kc-*.tmp`, 0600, `O_EXCL`) next to its
   destination, synced, and renamed over it only when complete, through
   `atomicfile.WriteIn`, the `os.Root` variant of `atomicfile.Write` (#75). A directory entry is assembled in
   a private staging directory `.<name>.kc-partial-*` next to the destination
   and renamed into place (or merged, for `Overwrite`) only when the whole tree
   arrived. A failure or a cancel removes the staging directory. Any error in a
   directory (an unreadable file, `tar` exiting non-zero) fails the whole entry.

6. **Permissions.** A copied file gets the owner bits of its source mode and
   nothing else: 0644 becomes 0600, 0755 becomes 0700, setuid, setgid and
   sticky are dropped. Directories are created 0700. Modification times of
   files are kept. A copy is never more open than its source and never readable
   by other local users, the same rule PR #75 applies to exports.

7. **Progress, cancellation, result.** The copy runs as a `tea.Cmd`; a grey
   dialog shows the source (context, pod, container, directory, entries) and
   the destination before the start, then the current entry (n of m), the
   number of files written and the bytes received, redrawn every 200 ms. `Esc`
   or `C-c` cancels: the entry in progress is removed, the rest are reported as
   canceled ("not started"), and the filesystem session is dropped because a
   read may have stopped halfway. The dialog stays open with one line per entry
   (copied with file and byte counts, skipped, failed with the reason,
   canceled) and the left-out names under it; `Enter` or `Esc` closes it and the
   local panel is refreshed.

8. **Upload mechanism: a tar stream kc builds, unpacked by a script.** For
   each marked top-level entry kc builds a tar stream from the local tree and
   runs `sh -c <script> kc-copy <dir> <name> <staging> new|replace <heartbeat>`
   in a second exec stream with that archive on stdin. The script unpacks it
   with `tar -xf -` into a private staging directory `.<name>.kc-partial-*`
   next to the target and only then moves the entry into place: `mv` for a new
   entry or a replaced file, and `tar -cf - | tar -xf -` from the staging
   directory to merge into an existing directory. The last archive member is a
   marker file (`.kc-upload-complete`) beside the entry; the script moves
   nothing without it, because a stream cut between two members looks like a
   complete archive to `tar`. An exit of the script (failure, cut stream,
   signal) removes the staging directory.

   The archive side follows the download rules: everything is read through an
   `os.Root` on the local directory, no symbolic link is followed (a file is
   opened and compared with its `Lstat` result, so a file swapped for a link
   is refused), links, devices, pipes and sockets are left out and listed,
   names are single path elements, and member names are `<name>` and
   `<name>/...`, never absolute and never with `..`. A file that shrinks while
   it is read fails its entry.

   Existing entries are decided from a fresh listing of the container
   directory when the copy starts, not from the dialog: `Skip existing` leaves
   them alone, `Overwrite` replaces a file and merges a directory, and an
   existing symbolic link, a non-regular file or a file/directory mismatch
   fails the entry. The script checks again before it moves anything.

9. **No `tar` in the image: the copy fails.** The script checks `command -v
   tar` first and the entry fails with "the container has no tar; copying
   into it needs tar in the image, as kubectl cp does". The remaining entries
   fail with the same message without another exec. kc does not fall back to
   writing through the session's line protocol, and does not add a debug
   helper for a container that has a shell.

10. **Through the debug helper: busybox `tar` under `/proc/1/root`, with a
    heartbeat.** When the folder's session runs in the helper (a container
    without a shell, after the user agreed for browsing), the same script runs
    in the helper with the target directory spelled `/proc/1/root/<dir>`. A
    background loop touches the helper's heartbeat file every 10 s while the
    script runs, so the helper does not exit after its 60 s idle timeout during
    a long copy; the loop stops with the script. A copy never adds a helper
    (decision 2).

11. **Confirmation and read-only mode.** An upload changes the cluster, so its
    dialog is red, like `models.Confirmation` dialogs, and `Cancel` has the
    focus: Enter alone writes nothing. It names the source directory and
    entries, the context, pod, container (with "through the debug helper") and
    target directory, and shows the equivalent `kubectl cp <local> <ns>/<pod>:<dir>/<name> -c <container>`
    for the first two entries (through the helper: the helper container and the
    `/proc/1/root` path). The skip and overwrite buttons are those of decision 4.
    Read-only mode refuses the upload through `refuseReadOnly` with the pod's
    exec check (exec is a write for `access.Policy`), for the current context
    and for the context the container folder belongs to; the check runs when
    `F5` is pressed and again when the user confirms, so a context that turned
    read-only while the dialog was open writes nothing. `F5` stays enabled on
    local rows in read-only mode, so the key explains the refusal.

12. **Permissions in the container.** Copies keep the permission bits of the
    local file (0644 stays 0644, 0755 stays 0755) without setuid, setgid and
    sticky. Headers carry uid and gid 0 without names: `tar` running as root
    creates root-owned files, as any root process would, and `tar` running as
    another user creates files as that user. Programs in the container read
    the files under their own user, so dropping group and other bits, as the
    download does for the local machine, would break them.

## Alternatives considered

- **A `/local` row in the panel root.** Discoverable, but the root is the list
  of resource types of the panel's namespace scope; a filesystem row there
  mixes two kinds of tree, and it would have to be added to every root
  variant.
- **`cd /path` on the shell line.** Closest to mc, but PR #92 gives `cd` on the
  shell line to cluster paths, and the shell line runs a real shell whose own
  `cd` must keep working.
- **Only the filesystem session, no tar.** One code path and the helper for
  free, but the session's base64 line protocol is slow for large trees and
  costs one round trip per file. `tar` is what `kubectl cp` uses and is in most
  images that have a shell.
- **Only tar, as `kubectl cp` does.** Fails for distroless images and for
  containers that need the helper, which kc can already browse.
- **Only the walk through the debug helper (stage 1).** No script with
  `CAP_SYS_PTRACE` in someone else's pod, but one file that takes longer than
  the helper's idle timeout could not be copied, and the helper already runs
  kc's session script with the same rights. Stage 2 streams `tar` there with a
  heartbeat loop.
- **Keep the source mode (as `kubectl cp` does).** Group- and other-readable
  copies of container files (keys, tokens, configs) on a shared machine. The
  user can `chmod` a copy; a leak cannot be undone.
- **Recreate symbolic links whose target stays inside the destination.**
  Needs target resolution relative to each link and still makes the result
  depend on the local tree. Leaving them out is predictable and the result
  names every one.
- **Keep the completed part of a failed directory.** Faster to resume, but the
  directory then looks complete. The result list already says what failed; the
  user can copy the readable subdirectories one by one.
- **Write directly into an existing directory on `Overwrite`.** Simpler, but a
  failure in the middle would leave a mix of old and new files.
- **Upload by plain `tar -xf - -C <dir>`, as `kubectl cp` does.** Simplest,
  but a canceled or failed copy leaves half a tree that looks finished, the
  rule download keeps locally.
- **Upload through the session's line protocol (`base64 -d` per file).** Works
  without `tar`, but needs a write command in the session script, a shell loop
  per line for large files and a second protocol to keep in step. Images with a
  shell and without `tar` are rare; the message names the gap.
- **Add the debug helper for a container that has a shell but no `tar`.** It
  changes the pod for good and adds `SYS_PTRACE`; asking for that to copy a
  file is out of proportion. The user can use `kubectl cp` with an image that
  has `tar`.
- **Keep the local owner (`kubectl cp` sends the local uid and gid).** `tar`
  as root then chowns the copies to a uid that means nothing in the container.
- **Drop group and other bits on upload too.** Consistent with download, but a
  web server that reads its files as another user would fail.
- **A progress line on the command line instead of a dialog.** Leaves the
  panels usable during the copy, but there is no place for the per-entry result
  and the shell line belongs to the terminal. A dialog that can be hidden is a
  possible follow-up.

## Consequences

- `internal/filexfer` holds both directions; `podfs` has two extra exec
  streams next to the session's shell (`TarStream` for download, `ExtractTar`
  for upload), both through one `execFunc` that tests replace with a fake
  stream. The extract script is tested by running it with the local `sh` and
  `tar`; a container's busybox or GNU `tar` is not exercised by unit tests.
- An upload needs `sh`, `tar`, `mkdir`, `mv` and `rm` in the image (or the
  helper). The merge into an existing directory follows the container's
  `tar` rules: a read-only file there is replaced when its directory is
  writable, and symbolic links inside the existing directory are handled as
  that `tar` handles them. Inside the container, writes cannot reach the
  local machine; the container's own filesystem is the limit.
- Through the debug helper an absolute symbolic link inside the target's tree
  resolves in the helper's filesystem, not the target's, as for browsing. An
  existing top-level link is refused; deeper links in a merged directory can
  send files into the helper, which exits with them.
- If the container runtime kills the script without a signal it can trap
  (for example the pod is deleted), a hidden `.<name>.kc-partial-*` directory
  can stay behind in the target directory.
- The upload's progress counts the bytes kc sent; the container may still be
  writing the last of them when the counter stops.
- A download through the debug helper that falls back to the walk (its `tar`
  stream failed before the first entry) is still cut when reading a single
  file takes longer than the helper's idle timeout (60 s); the entry then fails
  and leaves nothing.
- A session closed under the copy (the helper exited, the pod restarted) fails
  the remaining entries; the next listing of the folder opens a new session.
- The dialog blocks the panels while the copy runs. The copy itself does not
  block the UI.
- A rename keeps the inode of the staged file, owned by the local user; local
  hard links, extended attributes and ACLs of a replaced file are lost, as with
  any atomic replacement.
