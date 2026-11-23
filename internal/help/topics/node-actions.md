# Node actions

In the node list (`/nodes`), `M-n` opens a menu to cordon, uncordon or
drain nodes. It acts on the marked nodes, or on the node under the
cursor when none is marked. Use it before node maintenance: cordon stops
new pods from landing on a node, drain moves the running pods away.

## How it works

- `Cordon...` sets `spec.unschedulable`, like `kubectl cordon`. Running
  pods stay; new pods are not scheduled there.
- `Uncordon...` clears it again, like `kubectl uncordon`.
- Nodes already in the wanted state are left alone. If all of them are,
  the dialog says `Nothing to do`.
- Cordoned nodes are shown in red, like nodes that are not ready.

Every action that changes a node ends in the red confirmation dialog
with the nodes, the notes and the equivalent kubectl command.

## Drain options

`Drain...` first asks for its options:

- Grace period: seconds each pod gets to stop. `-1` (the default) uses
  each pod's own.
- Timeout per node: a duration such as `90s` or `5m`; `0` waits as long
  as it takes. The default is `5m`, where kubectl has none.
- Ignore DaemonSet pods: on by default, because nearly every node runs
  one and kubectl would refuse the drain otherwise.
- Delete emptyDir data: evicts pods with emptyDir volumes; their data is
  lost.
- Force: evicts pods without a controller; nothing recreates them.

`Enter` makes kc read the pods on the nodes while the options stay
open. When kubectl would refuse some of them (for example "Pods with
local storage"), nothing changes: the options stay as you set them, and
each refused pod is listed in red under the option that lets the drain
evict or skip it, with the focus on the first such option. Tick it and
press `Enter` again, or cancel. A pod that no option helps, such as one
whose DaemonSet kc cannot read, is listed above the buttons.

Otherwise the options close, and the red dialog shows what the drain
does, how many pods it evicts and the `kubectl drain` command, and what
the evictions touch:

- the workloads, by the controller of the evicted pods;
- each PodDisruptionBudget that covers them, with its status as read
  now: how many disruptions it allows, current and desired healthy pods,
  expected pods, how many covered pods are not ready and its
  `unhealthyPodEvictionPolicy`, and how many evictions will wait until
  replacements are healthy;
- how many pods no budget covers, and how many bypass budgets (pending,
  finished or terminating pods);
- warnings for a pod that more than one budget covers (the Eviction API
  refuses it and the drain stops there), for a budget whose status is
  stale, and for namespaces whose budgets you may not read (their pods'
  budgets are unknown, not absent).

The budget lines are a snapshot. The API server decides each eviction
when it arrives, and the preview does not say whether replacements can
be scheduled.

## Drain progress

A drain cordons all the nodes first, then evicts their pods node by node
through the Eviction API, which respects PodDisruptionBudgets. Mirror
pods and, when chosen, DaemonSet pods stay.

A dialog shows one line per node: its phase (`waiting`, `cordoning`,
`evicting`, `drained`, `failed`, `canceled`), and how many pods were
evicted, remain and are blocked. Below it are the pods whose eviction
was refused, with the API server's message. A refusal that names a
disruption budget also shows that budget as it was when the drain was
prepared. A 429 without a budget cause (the API server sheds load) is
shown as such. kc asks again every five seconds.

## Keys

- `M-n` or `Esc n` opens the menu; `Up`, `Down` and `Enter` choose.
- In the drain options: `Up`, `Down` or `Tab` move between fields, type
  to edit a number, `Space` toggles a check box, `Enter` checks the
  pods and opens the red dialog.
- `Esc` closes the menu or the options without a change.
- In the progress dialog: `Esc` cancels the drain; the nodes stay
  cordoned. When it is done, `Enter` closes it.

## Limits

- Read-only mode refuses `M-n` before the menu opens.
- The nodes stay cordoned after a drain, as with kubectl. Uncordon them
  when the work is done.
- You need patch on nodes and create on `pods/eviction`.

## See also

- [Confirmation dialog](confirm.md)
- [Read-only mode](read-only.md)
- [Access](access.md)
