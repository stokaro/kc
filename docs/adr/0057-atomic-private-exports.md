# 0057. Atomic and private local exports

- Status: Accepted
- Date: 2026-11-22
- Issue: #57

## Context

`F5` saves resource YAML, decoded Secret and ConfigMap values, log snapshots and
container files to a local path. All of them go through `copyToPath` in
`internal/ui/app.go`. It used `os.Create(dest)`, which truncated an existing
file before the content arrived, removed the destination on a copy error (so
the old file was lost as well), and created files with the default 0666 mode
minus the umask, usually 0644. A decoded Secret value was readable by every
local user.

The issue left open: the mode of a new file, whether a replaced file keeps its
mode, and what happens when the destination is a symbolic link.

## Decision

1. All exports use a new package, `internal/atomicfile`. `Write(ctx, path, r)`
   creates a temporary file in the destination directory with
   `os.CreateTemp` (name `.<base>.kc-*.tmp`), copies the stream into it, sets
   the mode, calls `fsync`, closes it, checks the context once more, and
   renames it over the destination. The directory is synced after the rename
   (best effort). On any error, including cancellation, the temporary file is
   removed and the destination is not touched.
2. Every new export is created with mode 0600, whatever it holds. kc does not
   try to tell sensitive content from public content.
3. A replaced file keeps its owner bits and loses all group and other bits
   (`existing & 0600`). A 0644 file becomes 0600, a 0600 file stays 0600, a
   0755 file becomes 0600. A replacement never widens access.
4. An existing file without owner write permission is refused with
   `ErrReadOnly`. `os.Create` failed on such a file before, and a read-only
   file is a sign the user wants to keep it.
5. A destination that is a symbolic link is refused with `ErrSymlink`; the
   link and its target stay as they are. A destination that is a directory or
   another non-regular file is refused with `ErrNotRegular`. Symbolic links in
   the parent directories are followed as usual: the user typed that path.
6. Cancellation is checked before each read, so an export stops even when the
   source stream ignores the context. The success log line is written when
   `copyFinishedMsg` arrives, which happens only after the rename.

## Alternatives considered

- Keep the mode of a replaced file as it is. This matches what most editors
  do, but writing a Secret over an old 0644 export would publish it again.
  Narrowing costs a `chmod` for the rare user who wants a shared export.
- Always 0600, ignoring the old owner bits. Simpler, but it would make a
  deliberately write-only or owner-restricted file more permissive for the
  owner. Masking with 0600 gives the same result for normal files.
- A stricter mode only for Secret content. It needs a "sensitive" flag on
  every content source and silently gets it wrong for ConfigMaps, logs and
  container files that hold credentials.
- Follow a symbolic link and replace its target atomically. It writes outside
  the path the user sees and is the behavior the issue asks to avoid.
- Replace the symbolic link itself with a regular file. Less surprising than
  following it, but it still silently changes what the link pointed to for
  other users of that link. Refusing is explicit; the user can remove the link.
- `os.WriteFile` with 0600 after reading the whole stream into memory. It
  keeps the truncate-then-write window and holds large files in memory.

## Consequences

- An interrupted or failed export leaves the old file and no temporary file
  behind. A crash between create and rename can leave a hidden
  `.<name>.kc-*.tmp` file in the target directory; it is private (0600).
- The rename creates a new inode. A replaced file loses hard links, extended
  attributes and ACLs, and is owned by the current user. kc does not try to
  copy them.
- Export needs write permission on the destination directory, not only on the
  file.
- `fsync` on macOS does not force the disk cache (`F_FULLFSYNC`). This is the
  same guarantee other Go tools give and is enough for this use.
- `internal/atomicfile` can be reused for other local writes, for example
  `pkg/appconfig` saving the config file, which still uses `os.WriteFile` with
  0644.
