# Command mode

A panel in Command mode runs one of your custom commands and shows its
output in the panel, in a small terminal. It is the place for commands
you want to keep in view, such as `kubectl top pods` repeated every few
seconds next to a pod list.

## How it works

- Pick a command from the user menu (`F2`) or press its hotkey. The
  panel switches to Command mode and runs it through `sh`.
- Global, sticky and selector commands run in the active panel. A
  namespace command runs in the other panel and starts again when the
  namespace changes.
- The command gets `KUBECONFIG` for kc's context and the namespace of
  the selected row, so `kubectl` needs no extra flags.
- The frame title is the command's name; a namespace command adds
  `(ns: shop)`. A `<3` in the header beats when the command runs; it
  is red after a non-zero exit. The watch interval follows it.
- `watchInterval` in the config reruns the command at that interval.
  The "Watch" row in the panel options changes it for this panel.
- When the command ends, `onExit` decides: `keep-open` (the default)
  leaves the output on screen, `restore` and `close` return the panel to
  List mode.

## Interactive commands

A command with `interactive: true`, such as a shell, can take the
keyboard:

- `Enter` gives it the keyboard; the function bar shows "Enter Focus".
- While it has the keyboard, every key goes to the command.
- `Esc` `Esc` (twice within the Esc timeout) gives the keyboard back to
  kc. A single `Esc` still reaches the command.

## Keys

- `F2` opens the user menu to start another command.
- `M-F1` or `M-F2` opens the panel options: "Panel mode" leaves Command
  mode, "Watch" sets the interval (Off, 1s to 10s, 20s to 1m).
- `C-1` or `C-2`, or the Left and Right menus of `F9`, switch the panel
  to another mode. Starting another command in the panel stops the
  previous one.

## Limits

- Command mode appears in the mode list only after a command was
  started in that panel.
- `F3`, `F4`, `F5`, `F7` and `F8` do nothing in this mode.
- Commands with `confirm` or `prompts` ask before they run; a confirmed
  command cannot also use `watchInterval` or `type: namespace`.
- In read-only mode, commands marked `dangerous: true` are hidden and
  refused.

## See also

- [Custom commands](custom-commands.md)
- [Confirmation](confirm.md)
- [Panel options](panel-options.md)
- [Read-only mode](read-only.md)
- [Shell and terminal](shell.md)
