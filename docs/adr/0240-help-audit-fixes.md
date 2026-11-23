# 0240. Fixes for the differences the help pages found

- Status: Accepted
- Date: 2026-11-23
- Pull request: #240

## Context

Writing the F1 pages ([0237](0237-context-help.md)) from the code found
places where the code, the README and the ADRs disagree. 0237 lists them
under its consequences and left them for follow-ups. One more turned up
since: `pkg/appconfig.Default()` sets `terminal.follow`, `terminal.mode`
and `panel.table.mode`, and `config-default.yaml` had none of them.

## Decision

1. **The "Panel Mode" dialog is removed.** It was a list chooser opened
   by `showPanelModeModal`, which went away when the panel options got
   their "Panel mode" row and the Left and Right menus their mode
   entries ([0048](0048-menu-bar.md)). The dialog stayed registered and
   mapped to a help page, but nothing opened it. Its model, its two
   registrations and the help mapping are gone; `PanelModeSelectedMsg`
   stays, since the panel options send it. The `panel-mode` page
   describes the modes and where to pick one.

2. **Dangerous commands are hidden from the `F2` user menu.**
   [0042](0042-read-only-mode.md) says "the F9 menu"; it was written when
   F9 opened the custom commands, before 0048 moved them to F2. The code
   is right and the README is changed: in read-only mode a command with
   `dangerous: true` is left out of the user menu, `F2` and Command >
   User menu are dim when nothing else applies, and its hotkey shows the
   read-only refusal instead of "not available here". The menu bar lists
   no custom command itself.

3. **Select group and Unselect group work.** `+` and `-` in a list, and
   the File menu entries, open a dialog with a shell pattern (`*` at
   first). OK marks, or unmarks, every row whose name matches; the `..`
   row is never marked. The pattern uses `path.Match` syntax, matches the
   whole name and ignores case, as quick search does. An object row
   matches by its name, other rows by their first cell. A bad pattern is
   refused in the dialog; a pattern that changes no row says so. `*`
   and `C-a`, which only changed a copy of the rows, now change the marks
   in the table, so `F2`, `M-n` and `F5` see them. `+`, `-` and `*` go to
   the shell line while it holds typed text, as in mc.

4. **The built-in hotkey list is checked against the key handling.**
   `M-d` (Requires; the fields of a log line) and `M-z` (back in the logs
   viewer) were handled but a custom command could take them, and its
   hotkey runs first. A test in `pkg/appconfig` scans `internal/ui` for
   the Meta key strings and the case labels of the `C-x` chord handlers
   and fails when the list misses one or names a key nothing handles.
   `M-A` is matched by key code and listed by hand. The custom commands
   page lists every reserved key, and a test checks it.

5. **Stopping a port forward is allowed in read-only mode.** The forward
   runs in kc; stopping it changes nothing on the cluster.
   `models.Confirmation` gets `Local`, which the stop action sets, and the
   confirmation dialog skips the read-only refusal for it. Starting a
   forward stays refused (0042). The other gates were checked: opening a
   local file in the editor and copying from a container to a local
   directory were already allowed; everything else that is refused
   writes to the cluster.

6. **`Enter` on a leaf row does what `F3` does.** On a row that cannot
   be entered (a `logs` row, a ConfigMap or Secret key, a file in a
   container or a local directory) `Enter` runs the panel's View action,
   as mc opens a file. The `shell` row of a container already ran on
   `Enter`, so a container folder is now consistent. Rows that can be
   entered, action rows and links keep their own `Enter`, and where `F3`
   is not offered `Enter` still does nothing.

7. **`config-default.yaml` lists every default.** It and the README
   sample gain `panel.table.mode`, `terminal.follow` and `terminal.mode`.
   `defaults_test.go` compared a hand-picked list of fields, and those
   were not on it. It now walks every field of the file as written
   against `Default()`, with `UnmarshalStrict`, so a field without a
   sample line fails. The one exception, `resources.columns`, an older
   spelling of `objects.columns`, is named in the test.

## Alternatives considered

- **Wire the Panel Mode dialog to a key.** Every place it could go has a
  mode chooser already: the panel options, the Left and Right menus and
  `C-1`/`C-2`. A third chooser would be one more thing to keep in step.
- **Keep the F9 wording and list custom commands in the Command menu.**
  The menu bar would grow with every command and duplicate F2, which is
  mc's user menu key.
- **A regular expression for Select group.** mc uses shell patterns, and
  the row filter (`M-f`) already takes regular expressions.
- **Enter opens only `logs` rows.** It would leave a key row and a file
  row with an `Enter` that does nothing, unlike mc.
- **Mark the stop action read-only safe in the UI by its type.** A field
  on the confirmation keeps the decision with the action that knows it.

## Consequences

- 0237's list of findings is settled; its body is unchanged.
- A new Meta key or `C-x` chord in `internal/ui` needs an entry in
  `builtinHotkeys` and on the custom commands page, or the tests fail.
- A new config field needs a line in `config-default.yaml` before the
  tests pass.
- `Enter` on a leaf row opens a viewer where it did nothing before.
