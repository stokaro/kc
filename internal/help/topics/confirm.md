# Confirmation dialog

Before kc changes the cluster, it shows a red dialog that says what will
change and where. Nothing is sent until you choose the action button.
The dialog is used by the workload and CronJob actions, node actions,
custom commands with `confirm`, the pod filesystem debug helper, the
node shell, and stopping a port forward.

## How to read it

From top to bottom:

- The title is the question, for example `Scale Deployment?` or
  `Drain 2 nodes?`.
- The details are label and value pairs: the kubeconfig context, the
  namespace and the object. Check the context first: it is the cluster
  that will change. Some actions add what changes, such as
  `Replicas: 3 -> 0` or `From revision` and `To revision`.
- The notes say what the change does and what stays behind, for example
  "Scaling to 0 stops every Pod of the Deployment" or "the nodes stay
  cordoned".
- `Same as:` is the kubectl command that makes the same change, with
  `--context` and kc's impersonation flags where needed. Use it to
  check the change or to run it yourself later. A custom command shows
  `Command:` instead, with the command as it will run.

The buttons are the action, named after it (`Scale`, `Restart`, `Stop`,
`Drain`), and `Cancel`.

## Keys

- `Cancel` has the focus when the dialog opens, so `Enter` alone never
  changes anything.
- `Tab`, `Shift-Tab`, `Left` or `Right` move the focus to the action
  button; `Enter` then runs it.
- A click on a button runs it.
- `Esc` cancels.

## Limits

- In read-only mode kc refuses every red dialog before it opens, and
  again on confirm if the context became read-only meanwhile.
- The dialog shows what kc read when it opened. If the object changed
  since, the action still runs with the values shown.
- The API server decides each request: a missing permission shows as an
  error after you confirm.
- `F8` delete has its own Yes/No dialog, with the same keys plus `M-y`
  and `M-n`; see Delete. For a namespace, a CRD, a PersistentVolume or
  a node it says what goes with the object and waits until you typed its
  name.
- Create namespace (`F7`) is a red input dialog that names the context;
  `Enter` creates. See Create namespace.

## See also

- [Delete](delete.md)
- [Create namespace](namespace-create.md)
- [Workload actions](workload-actions.md)
- [Node actions](node-actions.md)
- [Custom commands](custom-commands.md)
- [Read-only mode](read-only.md)
