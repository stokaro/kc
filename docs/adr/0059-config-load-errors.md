# 0059. Show configuration errors and never save over a file that does not load

- Status: Accepted
- Date: 2026-11-22
- Issue: #59

## Context

kc read `~/.kc/config.yaml` with `appconfig.Load` and ignored its error: a
file that did not parse left the session on defaults without a word. The
option dialogs then saved the whole in-memory configuration with `C-s`, so a
broken file followed by "Save as defaults" replaced the user's file, custom
commands included. Save errors were dropped.

`Load` also had a second parser: when decoding into `Config` failed, it read
the file as a generic map and picked out `viewer.theme`, `viewer.mode` and
`panel.scrolling.horizontal.step`, keeping whatever the failed decode had
already filled in. Its comment said it existed for mixed-case keys, but the
decoder (`sigs.k8s.io/yaml`, through `encoding/json`) already matches keys
without regard to case. In practice the fallback ran only on type errors
(for example `ttl: 120` instead of `ttl: 2m`) and then hid them.

The issue left open how the session should continue, what the recovery path
is, and how a save should treat a file that changed or was repaired while kc
ran.

## Decision

1. **One parser, and a broken file is used not at all.** `Store.Load`
   decodes the file once. On a read or parse error it returns the defaults
   and a `*appconfig.LoadError` that carries the path and the cause. The
   lenient fallback is removed, so a partly valid file never mixes with
   defaults in a way the user cannot predict.

2. **The error goes to the command line.** `App.Init` shows
   `Config ignored, using defaults (saving is off): <path>: <error>` as an
   error toast for 15 seconds and logs it.

3. **Saves are read-modify-write of named fields.** `Store.Update(change)`
   reads the file again, applies a function that sets only the fields the
   dialog owns (viewer theme and mode, panel width, resources and objects
   options, table mode), and writes the result. When the file on disk does
   not load, `Update` returns its `*LoadError` and does not touch the file.
   This is how "not writable" is enforced: the check runs against the file
   at save time, not against a flag set at startup.

4. **Recovery is repair, then save.** Because `Update` reads the file again,
   a user who fixes the file while kc runs can press `C-s` again and the
   change lands in the repaired file. A user who wants to start over moves
   the file away; the next save creates a new one. kc never deletes,
   renames or overwrites the broken file itself.

5. **Unknown keys survive.** `Update` encodes the configuration on top of the
   file as a generic map, merging maps key by key (case-insensitively, so a
   legacy `Viewer:` key is replaced by `viewer:` instead of duplicated).
   Lists, such as `commands`, are replaced as a whole. YAML comments are
   still lost, as before.

6. **Atomic writes.** The new content goes to a temporary file in the same
   directory, is synced, gets the mode of the file it replaces (0644 for a
   new file) and is renamed over it. A symbolic link is resolved first, so a
   dotfiles link stays a link.

7. **Session versus saved is spelled out.** `Enter` in an option dialog
   shows `Applied to this session only; C-s saves as defaults`. `C-s` shows
   `Saved as defaults to <path>` or an error toast that starts with
   `Not saved, applied to this session only:` and names the file and cause.
   The save runs in a `tea.Cmd`, not on the update goroutine, and a mutex in
   the `Store` serializes concurrent saves from one dialog.

8. `appconfig.Save` is removed; `appconfig.Load` stays as a thin wrapper for
   the table demo.

## Alternatives considered

- **A read-only flag set at startup.** Simple, but a file repaired during
  the session would still be refused until restart, and a file that was
  valid at startup and edited by hand later would still be overwritten with
  the in-memory copy. Re-reading at save time covers both.
- **An explicit reset that renames the broken file to a backup and writes
  defaults.** It needs a confirmation dialog stacked on top of the options
  dialog and invents a backup naming scheme. Moving the file by hand is one
  shell command and leaves the user in control; it can be added later.
- **An in-app "reload configuration" action.** Reloading means re-applying
  every setting to two live panels, the viewers and the command list, which
  is a larger change than this issue. Restart and repair-then-save cover the
  need for now.
- **Keep the lenient fallback and only report its error.** It would still
  let a session run on a mix of file values and defaults that the user did
  not choose, and saving that mix is exactly the loss the issue describes.
- **Write the whole configuration, as before, after the check.** Simpler,
  but it drops keys kc does not know (from a newer kc, for example) and
  overwrites hand edits made while kc ran.

## Consequences

- A malformed file is visible at once and its bytes stay unchanged.
- Files that loaded through the old fallback (type errors) now report an
  error and run on defaults until fixed. That is intended.
- Each `C-s` costs one extra file read; the file is small.
- Saved files are written with keys in alphabetical order, as
  `sigs.k8s.io/yaml` already did.
- Follow-up: an in-app reload, and an explicit "reset to defaults" with a
  backup, if users ask for them.
