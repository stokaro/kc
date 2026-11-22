# 0046. Hotkeys, prompts and confirmation for custom commands

- Status: Accepted
- Date: 2026-11-22
- Issue: #46

## Context

Custom commands (`commands:` in `~/.kc/config.yaml`) are reachable only from
the commands menu. They cannot ask for a value, and a command that changes
the cluster runs without a question. The issue, written from a comparison
with k9s plugins, proposes `key:`, `confirm:`, `dangerous:`, `inputs:`, the
template fields `.Context`, `.Container` and `.Columns.<NAME>`, and asks
whether command files should be loaded per context.

Constraints:

- kc imitates Midnight Commander. Its user menu has hotkeys, shows dialogs
  for `%{...}` macros, and uses `C-x` as the prefix of two-key commands.
- Many keys are taken already, on main and in open pull requests (`M-c`,
  `M-s`, `M-w`, `M-o`, `M-y`, `C-x C-d` and others), and each `M-<letter>`
  also answers to `Esc <letter>`. A user key must not shadow them.
- Open pull requests change the same code: #79 rewrites the body of
  `RenderCommand` and of the menu callback, #88 wraps `RenderCommand` in
  `RenderCommandIn` and adds `.Context`, #97 moves the menu from F9 to F2,
  #80 and #96 change `handleConfirmResult`, #81 rewrites `appconfig.Load`
  and `App.Init`, #94 adds the `C-x` prefix for `C-x C-d`.

## Decision

1. **Configuration.** Three optional fields on each command:

   ```yaml
   hotkey: "M-1"                 # or "C-x 1"
   prompts:
     - name: replicas            # {{.Input.replicas}}
       label: "Replicas"
       default: "1"              # may be a template: "{{.Name}}-copy"
       choices: ["1", "3", "5"]  # optional
   confirm: true                 # or a message, itself a template
   ```

   The names follow the task brief and mc rather than the issue: `hotkey`
   (mc's user menu hotkey), `prompts` and `.Input.<name>` for the answers.
   `confirm` accepts `true`/`false` or a string (`appconfig.CommandConfirm`
   with its own JSON methods); it is omitted when unset, so saving a
   configuration does not add `confirm: false` to every command. At most 8
   prompts, so the dialog fits on a small terminal.

2. **Hotkey notation.** mc notation, normalized by `appconfig.ParseHotkey`:
   `M-<char>` (case-sensitive, `M-H` is not `M-h`), `C-M-<char>`, `F11` to
   `F24`, or a chord `C-x <char>` / `C-x C-<letter>`. Refused: keys without a
   modifier (they would take typing on the command line), single
   `C-<letter>` keys (kc and the shell line use nearly all of them; mc users
   put their own keys behind `C-x`), `F1` to `F10`, modifiers on function
   keys, and the built-in keys listed in `builtinHotkeys` (`M-a M-A M-b M-c
   M-e M-f M-g M-H M-j M-k M-l M-n M-o M-p M-r M-s M-t M-u M-v M-w M-x M-y`,
   `C-x C-d`).

3. **Validation at load, without changing the configuration.**
   `App.Init` calls `loadCommandHotkeys`, which runs
   `appconfig.ValidateCommands` on the loaded commands. Every problem is
   logged; the first one (with a count of the rest) goes to the command line
   as an error. A bad, duplicate or built-in hotkey is left out of the hotkey
   table, the command stays in the menu. The commands themselves are not
   modified, so "save as defaults" never rewrites or drops what the user
   wrote. `appconfig.ValidateCommand` checks what a command needs to run:
   valid and unique prompt names, defaults that are one of the choices, no
   `.Input.<name>` without a prompt, and no `confirm` together with
   `type: namespace` or `watchInterval` (see 7). The menu still lists such a
   command, and picking it shows the reason instead of running it.

4. **A hotkey is a shortcut into the menu.** `runCommandHotkey` opens the
   commands menu of the active panel and picks the command's entry
   (`CommandSelectorModel.selectCommand`), all inside one update, so nothing
   flickers. The menu's rules (`showFor`, the selection, the namespace, the
   panel the command runs in) apply unchanged, and the hotkey keeps working
   after #79 and #97 change the menu. When the entry is not offered, the
   command line shows `<name>: not available here`. Hotkeys work in the
   panels only, not in the fullscreen terminal or while an interactive
   command has the focus. The menu shows each command's hotkey on the right
   and marks commands with prompts with `...`, as mc does for items that
   open a dialog.

5. **Keys after Esc.** `Esc <letter>` runs the `M-<letter>` hotkey, like the
   built-in keys. `Esc <digit>` stays the F-key, so `M-<digit>` hotkeys have
   no fallback.

6. **`C-x` chords.** kc has one `C-x` prefix (#94, for `C-x C-d`): it arms
   while the shell line is empty, and the next key completes or cancels it;
   an unbound key is dropped, as in mc. `handleCtrlXKey` runs kc's own
   chords first and then the user's `C-x` hotkeys. The prefix is checked
   before the global keys, so `C-x C-s` does not start the quick search.

7. **Prompt answers are bound into the template text.** When the dialog
   closes, `cmdtemplate.Bind` replaces `.Input.<name>` in the command and
   the confirmation message: an action that reads only answers and literals
   (`{{.Input.n}}`, `{{.Input.image | printf "%q"}}`) is evaluated and becomes
   plain text, and in an action that also reads other fields the reference
   becomes a quoted string literal. The rest stays a template for
   `RenderCommand`. The bound command is what the panel stores, so watch
   ticks and namespace follow rerun it with the same answers and do not ask
   again. Answers are inserted as typed; the command has to quote them where
   the shell needs it.

8. **Confirmation.** The red `ConfirmModel` (same dialog and colors as other
   confirmations, `Cancel` focused) shows the title (`Run <name>?` or the
   rendered message), the context, the namespace and object of the row, and
   the rendered command, wrapped to the screen width. `Run` starts the
   command. A confirmation approves one run, so kc refuses `confirm`
   together with `type: namespace` or `watchInterval`, which rerun the
   command on their own.

## Alternatives considered

- **`.Input` as a field of `CommandContext`.** The natural place, but the
  answers would have to travel through `Panel.StartCommand`, the command
  widget and `RenderCommand`, whose signature #88 already changes and whose
  body #79 rewrites. Binding into the text needs no change there and keeps
  restarts consistent.
- **`models.ConfirmRequest` and `showConfirm`.** `ConfirmedAction.Confirm`
  returns an error, not a `tea.Cmd`, so it cannot start the command widget;
  enqueuing the start would wait for the next message. Changing
  `handleConfirmResult` would collide with #80 and #96. A small wrapper,
  `commandConfirmModel`, uses the same `ConfirmModel` and handles the answer
  in place.
- **Hiding invalid commands from the menu.** Clearer, but the filter loop is
  the line #79 changes. Refusing on use with the reason is as safe and tells
  the user what to fix.
- **Dropping invalid commands from `cfg.Commands` at load.** A later save
  would write the reduced list back and lose the user's entries.
- **Single `C-<letter>` hotkeys.** They would take shell line editing keys
  and kc's own (`C-o`, `C-u`, `C-s`, `C-t`, `C-r`, ...); `C-x` chords give
  room without that.
- **Plain letters or digits as hotkeys, as in mc's user menu.** mc reads
  those inside the open menu. Here the menu has no hotkey letters yet, and
  outside the menu plain keys type into the command line.
- **Asking again on every restart.** Namespace follow and watch ticks would
  open a dialog every few seconds.

## Consequences

- Users bind their commands to keys, ask for input in one mc-style dialog,
  and see the exact command before a dangerous one runs.
- `builtinHotkeys` must grow with every new built-in key; a forgotten entry
  lets a user key shadow it silently.
- The confirmation and prompt defaults use the row under the cursor (and the
  namespace for namespace commands), the same target the menu passes on
  main. When #79 lands, `guardCommand` should receive the marked rows
  (`targets`) instead of `item`, so the dialog shows what runs.
- Left for follow-ups, listed on the issue: `dangerous:` (needs the read-only
  mode of #42), `.Context` (comes with #88), `.Container` and
  `.Columns.<NAME>`, background commands, and command files per context.
