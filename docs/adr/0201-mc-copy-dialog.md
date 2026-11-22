# 0201. mc's Copy dialog layout and dialog widget skin

- Status: Accepted
- Date: 2026-11-23
- Issue: #201

## Context

The issue compares kc's copy dialogs with mc's `F5` Copy dialog. mc puts the title in the top frame line, shows a left-aligned prompt, black-on-cyan input lines, check boxes, separators joined to the frame (`├───┤`), and one row of bracketed buttons with the default marked `[< OK >]` and hotkey letters in blue. kc's "Copy to Local File" dialog had a centered prompt, a dark gray input with an orange cursor, a hint line and filled gray button blocks. The container copy dialog listed details and chose the conflict policy with buttons (`Skip existing`, `Overwrite`, `Cancel`).

What the code did: the `Modal` frame already matched mc (title centered in the top border, gray margin, black shadow). The differences were inside the dialogs. Each dialog drew its own buttons: `TextInputModal` (used by the copy, namespace, port forward, row filter, log filter and workload dialogs) with filled blocks, and the confirmation, command prompt, drain and transfer dialogs with ad-hoc bracket rows. There were no shared input, check box or separator widgets, and no input history.

While this was in review, the user added a requirement: the copy-to-local dialog must propose the directory of the other panel when it shows the local filesystem, and the system temp directory otherwise, not the last copy directory or the working directory.

## Decision

1. New package `internal/ui/dialog` with mc's dialog skin and the shared widgets:
   - `Skin`: `Normal()` is mc's dialog skin (black on light gray, focus black on cyan, hotkeys blue, input lines black on cyan). `Danger()` is mc's error dialog skin for red dialogs (white on red, focus black on light gray, hotkeys yellow).
   - `Label` takes mc's `&` hotkey marker (`"&OK"`, `"Over&write"`).
   - `Button` draws `[ Label ]`, or `[< Label >]` for the default button; buttons in a row are one column apart and centered. No filled blocks: an unfocused button has the dialog background.
   - `RenderCheckbox` draws `[x] Label`; `RenderRule` draws the separator; `NewInput`/`InputStyles` give the cyan input line with the terminal's own cursor (no orange cursor color).
   - `Form` lays out text, input lines, check boxes, rules and button rows and handles the keys and clicks every dialog shares: `Tab`/`S-Tab` and Up/Down move the focus, Left/Right move along a button row, `Space` toggles a check box or presses a button, `Enter` presses the focused button or else the default button, `M-<letter>` picks the widget with that hotkey (the plain letter too while no input line has the focus), a click focuses an input line or toggles a check box, and releasing the button over a push button presses it. `Esc` stays with the `Modal`.
2. `Modal` joins rules to the frame. Content that implements `ModalRules` (`RuleRows() []int`) gets `├`/`┤` on those rows. `TextInputModal`, the copy dialogs and the wrappers around `TextInputModal` report their rules.
3. `TextInputModal` is rebuilt on `Form`: prompt, cyan input line, hint or error line, a rule, `[< OK >] [ Cancel ]` with hotkeys from the button labels. Every dialog built on it changes with it. The confirmation dialog, the command prompt and the drain dialogs use the shared button renderer, so their default button is marked too.
4. Copy to a local file (object YAML, logs, viewer content): title `Copy` in the frame, prompt `Copy "<subject>" to:`, the target input, an error line, a rule, `[ ] Overwrite an existing file`, a rule, `[< OK >] [ Cancel ]`. The option starts off: `Enter` alone on an existing file shows "The file exists; check Overwrite to replace it" instead of replacing it.
5. Copy from a container to a local directory: the details (context, pod, container, source directory, entries), `to:` with the local target directory as an editable input line, a line that counts the entries that exist in that directory (recounted while the user edits it), a rule, `[ ] Overwrite existing entries`, a rule, `[< OK >] [ Cancel ]`. The target must be an existing directory. Unchecked means skip existing entries.
6. Copy from a local directory into a container (red dialog): the details including the fixed target directory, the `kubectl cp` equivalent, the conflict count and the overwrite check box when entries exist, then `[ OK ] [< Cancel >]`. Cancel is the default button and has the focus. In red dialogs hotkeys need Meta (`Form.AltOnly`), so a stray letter cannot start a write into the cluster.
7. Default target of the copy-to-local dialog: the other panel's directory when it shows the local filesystem (`M-l`), else `os.TempDir()`. `M-l` keeps its own start directory (last copy directory, then the working directory); the two no longer share `defaultCopyDir`.

## Alternatives considered

- Restyle each dialog in place. Rejected: the same bracket, focus and hit-test code was copied into five dialogs already, and every new dialog would copy it again.
- Copy mc's widgets that kc cannot honor: source mask with "Using shell patterns", "Follow links", "Dive into subdir", "Preserve attributes", "Stable symlinks", "Background". kc copies the marked entries (no mask), never follows symbolic links (they are listed as left out), always keeps the owner permission bits and already runs copies in the background with a cancelable progress view. Showing those options would be fake, so they are left out; the issue asked for that too.
- A `[^]` history button. kc keeps no input history for these fields, and the issue said to leave the button out without one. A small per-dialog history would also need a history list popup; that is follow-up work.
- An editable target for the copy into a container. Counting conflicts in another container directory needs another listing through the exec session, and a typo would write to an unexpected place in a running container. The target stays the directory of the other panel; navigate there to change it.
- Keep the old conflict buttons (`Skip existing` / `Overwrite`). mc decides the policy with options and has one OK button; a check box also works when the user edits the target and the conflict count changes.
- Keep the old overwrite-always behavior of the copy-to-local dialog. mc asks before replacing a file; a check box that starts off is the same safety in one dialog.
- Drop `TextInputModalButtonStyles`. It is kept as a deprecated, ignored field so that pull requests in flight that still set it keep compiling; the row filter and log filter dialogs still set it.

## Consequences

- Every input dialog now looks like mc's, and new dialogs get the skin, hotkeys and mouse handling from `dialog.Form` instead of drawing their own.
- `Enter` in the object copy dialog no longer replaces an existing file without the option. README describes the new behavior.
- The default copy target is the temp directory when no local panel is open, so an export no longer lands in the directory kc was started from.
- Follow-ups: remove `ButtonStyles` from `row_filter_modal.go` and `logs_viewer_json.go` and then the deprecated type; move the drain options, view options and other hand-drawn dialogs onto `dialog.Form`; add input history with a `[^]` button if users ask for it.
