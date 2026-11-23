# Custom commands

`F2` opens the user menu, as in mc. It starts with kc's built-in actions
for the selected object (Object Actions, such as the CronJob actions),
then the custom commands you define in `~/.kc/config.yaml`. The Command
menu of the menu bar (`F9`) opens the same menu with "User menu". A
command runs in a terminal in
the other panel, with `KUBECONFIG` set to kc's context and the selected
item's namespace, so `kubectl` needs no extra flags. Until you define
your own, kc ships `Top Nodes` and `Top Pods`.

```yaml
commands:
  - name: "Scale"
    command: "kubectl scale {{.Resource}} {{.Name}}
      -n {{.Namespace}} --replicas={{.Input.replicas}}"
    type: "sticky"
    showFor:
      resources: ["deployments", "statefulsets"]
    hotkey: "M-1"
    prompts:
      - name: replicas
        label: "Replicas"
        default: "1"
    confirm: "Scale {{.Name}} to {{.Input.replicas}}?"
    dangerous: true
```

## How it works

- Templates: `{{.Name}}`, `{{.Namespace}}`, `{{.Kind}}`, `{{.Group}}`,
  `{{.Version}}`, `{{.Resource}}`, and `{{.Context}}`, `{{.As}}`,
  `{{.AsUID}}`, `{{.AsGroups}}` for tools that do not read KUBECONFIG.
- With marked rows, a command acts on them; `{{range .Items}}` loops
  over them. Only commands with `supportsMultiSelection: true` are
  offered then.
- `type` sets when the command reruns: `selector` reruns when the
  cursor moves, `sticky` keeps the selection it started with,
  `namespace` restarts on a namespace change, and `global` ignores the
  selection.
- `showFor` limits the command to some resources. The menu groups
  commands by type, and shows each hotkey next to its command.
- `prompts` opens one dialog before the command runs, with up to 8
  fields, read as `{{.Input.<name>}}`. A field with `choices` is a list.
  Commands with prompts end in `...` in the menu. Answers are inserted as
  typed, so quote them where the shell needs it.
- `confirm: true`, or a message, shows the red dialog first, with the
  context, namespace, object and the command as it will run. It cannot
  be combined with `type: namespace` or `watchInterval`, which rerun on
  their own.
- `dangerous: true` marks a command that changes the cluster. Read-only
  mode hides it from the user menu and refuses its hotkey; `F2` is dim
  when nothing else applies. kc cannot tell what a shell command does,
  so a command without the flag runs in read-only mode too.

## Hotkeys

`hotkey` runs the command without the menu, where the cursor is:
`M-<key>` (`Esc <letter>` works for `M-<letter>`; `Esc <digit>` stays a
function key), `C-M-<key>`, `F11` to `F24`, or a chord such as `C-x 1`
or `C-x C-<letter>`. kc refuses keys without a modifier, single
`C-<letter>` keys, `F1` to `F10`, and the keys kc uses itself:

- `M-a`, `M-A`, `M-b`, `M-c`, `M-d`, `M-e`, `M-f`, `M-g`, `M-H`, `M-i`,
  `M-j`, `M-k`, `M-l`, `M-n`, `M-o`, `M-p`, `M-r`, `M-s`, `M-t`, `M-u`,
  `M-v`, `M-w`, `M-x`, `M-y`, `M-z`;
- `C-x a`, `C-x c`, `C-x d`, `C-x f`, `C-x n`, `C-x p`, `C-x w`, `C-x C-d`.

Bad and duplicate hotkeys are reported on the command line at startup
and ignored.

## Keys

- `F2` opens the menu; `Up`, `Down` and `Enter` run a command.
- `Esc` closes the menu.
- In the prompt dialog: `Tab` or `Down` go to the next field, `Left`,
  `Right` and `Space` cycle a choice, `Enter` runs, `Esc` cancels.
- In the red dialog, `Cancel` has the focus.

## Limits

- In a viewer or a Logs panel, `F2` opens the options dialog instead.
- Commands are not checked against RBAC.

## See also

- [Diagnostic commands](diagnostics.md)
- [Confirmation dialog](confirm.md)
- [Command mode](mode-command.md)
- [Workload actions](workload-actions.md)
- [Read-only mode](read-only.md)
