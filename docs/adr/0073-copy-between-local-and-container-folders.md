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
   runs in the debug helper always uses the walk, because the helper exits
   after a minute without a session command, which a long `tar` would not send.
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

3. **Direction: container to local only.** Rows of a local panel offer no
   `F5`, so nothing is copied into a container yet. Upload needs a write path in `podfs` (`tar xf -` or a write command) and has
   to respect the read-only mode of #42, which is not on `main` yet.

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
   destination, synced, and renamed over it only when complete; this is the
   approach of PR #75 (`internal/atomicfile`). A directory entry is assembled in
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
- **`tar` inside the debug helper.** It works with a background loop that
  touches the heartbeat file during the stream, but it adds a script that runs
  with `CAP_SYS_PTRACE` in someone else's pod. Left for later.
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
- **A progress line on the command line instead of a dialog.** Leaves the
  panels usable during the copy, but there is no place for the per-entry result
  and the shell line belongs to the terminal. A dialog that can be hidden is a
  possible follow-up.

## Consequences

- `internal/filexfer` is the place for the stage 2 upload: the same name
  rules apply to an archive built from a local tree, and `podfs` needs a write
  path (`tar xf -` in the container, or a helper command).
- The temporary-file logic duplicates the core of `internal/atomicfile` from
  PR #75, which is not on `main`. Once #75 merges, `writeFile` should call it
  (through an `os.Root` variant, since `atomicfile.Write` takes a path).
- A copy through the debug helper is cut when reading a single file takes
  longer than the helper's idle timeout (60 s); the entry then fails and leaves
  nothing. Large files are better copied from a container that has a shell and
  `tar`.
- A session closed under the copy (the helper exited, the pod restarted) fails
  the remaining entries; the next listing of the folder opens a new session.
- The dialog blocks the panels while the copy runs. The copy itself does not
  block the UI.
- A rename keeps the inode of the staged file, owned by the local user; local
  hard links, extended attributes and ACLs of a replaced file are lost, as with
  any atomic replacement.
