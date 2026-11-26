# Custom commands

You can define custom shell commands in `~/.kc/config.yaml` and execute them via the **F2** user menu (also under **Command** in the `F9` menu bar). Commands run in a terminal panel and can use templates to reference the selected item.
By default, kc ships with `Top Nodes` (global) and `Top Pods` (namespace-scoped) until you define your own commands.

## Configuration Example

```yaml
commands:
  - name: "Top Pods"
    command: "kubectl top pods -n {{.Namespace}}"
    type: "namespace" # Sticky for the current namespace
    watchInterval: 5s
    showFor:
      resources: ["pods"]

  - name: "Top Nodes"
    command: "kubectl top nodes"
    type: "global" # Independent of selection
    watchInterval: 5s

  - name: "Shell ($SHELL)"
    command: "$SHELL"
    type: "global"
    interactive: true
    onExit: "restore" # Return to previous panel mode when the shell exits

  - name: "Describe Resource"
    command: "kubectl describe {{.Resource}} {{.Name}} -n {{.Namespace}}"
    type: "selector" # Reruns when selection changes
    location: "panel"
    onExit: "keep-open"

  - name: "Restart Deployment"
    command: "kubectl rollout restart deployment {{.Name}} -n {{.Namespace}}"
    type: "selector"
    showFor:
      resources: ["deployments"]
    dangerous: true # Hidden from the F2 user menu and refused in read-only mode
```

Mark every command that changes the cluster with `dangerous: true`. In read-only mode such a command is missing from the `F2` user menu, `F2` and Command > User menu are dim when nothing else applies, and its hotkey is refused. kc cannot tell what a shell command does, so a command without the flag runs in read-only mode too.

## Command Types
- **selector**: Triggered by selection, reruns on change. Good for `describe`, `get -o yaml`, etc.
- **sticky**: Starts with current selection but keeps running.
- **namespace**: Sticky for the current namespace, restarts on namespace change.
- **global**: Independent of selection. Good for cluster-wide commands like `top nodes`.

## Hotkeys, prompts and confirmation

```yaml
commands:
  - name: "Scale"
    command: "kubectl scale {{.Resource}} {{.Name}} -n {{.Namespace}} --replicas={{.Input.replicas}}"
    type: "sticky"
    showFor:
      resources: ["deployments", "statefulsets"]
    hotkey: "M-1"            # or a C-x chord: "C-x 1"
    prompts:
      - name: replicas       # read as {{.Input.replicas}}
        label: "Replicas"
        default: "1"
      - name: wait
        choices: ["no", "yes"]
    confirm: "Scale {{.Name}} to {{.Input.replicas}}?"   # or: confirm: true
```

- `hotkey` runs the command without the menu, in mc notation: `M-<key>` (Meta/Alt; `Esc <letter>` works as well), `C-M-<key>`, `F11`–`F24`, or a chord `C-x <key>` / `C-x C-<letter>`. The command must apply where the cursor is, as in the menu. kc refuses keys without a modifier, single `C-<letter>` keys, `F1`–`F10`, and keys kc uses itself (for example `M-c`, `M-d`, `M-s`, `M-w`, `M-z`, `C-x C-d`; [Custom commands](../internal/help/topics/custom-commands.md#hotkeys) lists them all); bad and duplicate hotkeys are reported on the command line at startup and ignored. The menu shows each hotkey next to the command.
- `prompts` opens one dialog before the command runs (up to 8 fields). `default` may use the template variables (`"{{.Name}}-copy"`); `choices` turns a field into a list that `Left`/`Right`/`Space` cycle through. The answers are inserted as typed, so quote them in the command where the shell needs it. Commands with prompts end in `...` in the menu.
- `confirm: true`, or a message, shows the rendered command in the red confirmation dialog first. `Cancel` has the focus. A confirmation approves one run, so it cannot be combined with `type: namespace` or `watchInterval`, which rerun the command on their own; kc refuses to run such a command.

## Templating
Available variables: `{{.Name}}`, `{{.Namespace}}`, `{{.Kind}}`, `{{.Group}}`, `{{.Version}}`, `{{.Resource}}`.
The execution context is available as `{{.Context}}`, `{{.As}}`, `{{.AsUID}}` and `{{.AsGroups}}` (a list), for tools that do not read `KUBECONFIG`. Commands already run with `KUBECONFIG` set to the context of the panel the item was selected in and the namespace of the selected item (else the panel's), so `kubectl` needs no extra flags. Each panel has its own context; a running command keeps the one it started with. Commands without a selected item are run as written, without template expansion.

A command acts on the marked rows when any are marked, otherwise on the highlighted row. `.Items` lists them in panel order, each with its own `Name`, `Namespace`, `Group`, `Version` and `Resource`; the top-level variables repeat the first item. Commands with `supportsMultiSelection: true` are offered for several marked rows; other commands are hidden from the F2 user menu then.

```yaml
  - name: "Describe marked pods"
    command: "{{range .Items}}kubectl describe {{.Resource}} {{.Name}} -n {{.Namespace}}; {{end}}"
    type: "sticky"
    supportsMultiSelection: true
    showFor:
      resources: ["pods"]
```

## Diagnostic commands

A command with a `diagnostic:` block runs a local tool once and shows its parsed output in a report. See [Diagnostic commands](../internal/help/topics/diagnostics.md).

```yaml
commands:
  - name: "KRR recommendations"
    type: "sticky"
    showFor:
      resources: ["deployments", "statefulsets", "daemonsets", "namespaces"]
    hotkey: "C-x k"
    diagnostic:
      adapter: krr
      args: ["krr", "simple", "-f", "json", "--logtostderr", "-n", "{{.Namespace}}"]
      timeout: 10m      # default 5m
```
