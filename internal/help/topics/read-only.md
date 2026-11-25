# Read-only mode

In read-only mode kc does not change the cluster from its own keys. Turn
it on for production contexts, so a stray `F8` or a mistyped hotkey
cannot delete or edit anything while you browse.

## How to read it

- The panel frame shows a red `RO` before the context name. Each
  panel shows its own context: with prod on the left and staging on the
  right, only the left frame says `RO`.
- The keys that would write are grayed out in the function bar.
- Pressing one anyway shows "Read-only mode: <action> is disabled".
- A context switch says "Context: <name> (read-only)".

## What it refuses

- `F4` edit (a local file still opens in the editor).
- `F7` create namespace and `F8` delete.
- New port forwards (`M-p`) and node actions (`M-n`).
- Shell and attach in a container (`M-x`, `M-t`), and the node shell.
- Copies into a container (`F5` from a local panel).
- The pod filesystem debug helper for a container without a shell.
- The CronJob actions of the `F2` menu (Run now, Suspend/Resume).
- Custom commands marked `dangerous: true`: they are hidden from the
  user menu and refused when started.

Reading stays allowed: lists, viewers, logs, describe, browsing files in
a container, compare and the overview. So does what changes only kc
itself on your machine: `F8` in `/port-forwards` stops a forward, local
files open in the editor, and copies from a container go to a local
directory.

## How to turn it on

Per context in `~/.kc/config.yaml`, with exact names or globs (`*` is
any run of characters, `/` included, `?` one character):

```yaml
readOnly:
  enabled: false        # true: every context
  contexts:
    - "*prod*"
    - shared-staging
```

- `kc --readonly` makes every context read-only for this run.
- `kc --write` ignores the config and allows writes everywhere.
- Without either flag the config decides. Switching a panel's context
  applies the setting of the new context to that panel.
- An action is refused or allowed by the context of the row it acts on,
  whichever panel is active: `F8` on a prod row is refused while the
  staging panel next to it still deletes.

## Limits

- It is a guard against accidents, not a security boundary. The
  integrated terminal (`C-o`) and commands without `dangerous: true`
  still run whatever they run. RBAC on the cluster is the real limit.
- kc cannot tell what a shell command does. Mark every custom command
  that changes the cluster with `dangerous: true`.
- A command already running in a panel keeps running after a switch to a
  read-only context; only new starts are refused.

## See also

- [Context and namespace](context-namespace.md)
- [Custom commands](custom-commands.md)
- [Access](access.md)
- [Shell and terminal](shell.md)
- [Delete](delete.md)
