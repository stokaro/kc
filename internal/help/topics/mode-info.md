# Info mode

Info mode is mc's Info panel for kc. It shows the load of what the other
panel looks at: bars for CPU, memory, pods and disk, a short usage
history, and the pods that use the most memory. The right panel starts
in this mode, so a glance tells you whether a namespace, node or
workload is short of resources.

## What it covers

- By default, the other panel's namespace scope. The label above the
  bars says what each covers, for example `Cluster capacity (all nodes,
  all namespaces)` and `Top pods by memory in namespace shop`.
- With the other cursor on a node: that node.
- On a Deployment, StatefulSet, DaemonSet, ReplicaSet, Job or CronJob:
  its pods (found through owner references), their usage against their
  limits (requests when a pod has no limit) and ready of desired pods.
- On an object of a custom kind that controls pods, such as an Argo
  Rollout: the same bars, titled as kubectl names the object
  (`Pods of rollouts.argoproj.io shop/web`). kc follows the controller
  references of the pods through any namespaced kind, directly or
  through ReplicaSets or other kinds in between, and counts a pod for
  every workload in its chain. A kind becomes a workload once kc has
  seen one of its pods in this connection, or when discovery lists its
  scale subresource, so a Rollout scaled to 0 shows `scaled to 0`. The
  desired count comes from the CRD's scale subresource
  (`specReplicasPath`); a kind without one shows the ready pods only and
  says `no desired count: <kind> declares no scale subresource`. A row
  of a kind that controls no pods keeps the namespace scope. A pod whose
  owner was deleted and created again under the same name (another UID)
  does not count for the new owner.
- On an `/overview` finding: that finding, its value, how it is
  computed and the first objects behind it. Long lines wrap at the
  panel width, as the rest of Info does.

Info keeps the node, workload or finding it shows while the other cursor
is on a row that is not an object, such as `/..` after a `cd` or a
folder: the frame and the label go on naming the context and namespace
it was read in until the cursor reaches another object. After a pod or
another object for which Info shows the namespace, Info keeps nothing
and follows the other panel's namespace.

## How to read the bars

Under CPU and memory, each line names its base:

- `pods 2.3/10.4 Gi allocatable, 22%`: usage of the pods on the nodes
  against allocatable. This line fills the bar.
- `node 6.0/14.9 Gi capacity, 40%`: the node's own usage, system
  daemons included, against capacity.
- `req 25%, lim 116% (overcommitted)`: requests and limits of the pods
  against allocatable.

A state word follows the line it belongs to. When another line needs
attention, a mark after the bar's percentage names it: `! lim` warns,
`!! node` fails. Without pod usage the bar shows the requests.
Percentages are truncated, as `kubectl describe node` prints them.

For a workload the lines sum its pods: `2 pods use 11Mi of req 32Mi, no
limit`. The sum is taken from the exact values. Each pod row under "Top
pods" is rounded down to whole Mi, as `kubectl top` prints it, so two
pods of 5.6Mi show as 5Mi each, while their sum shows as 11Mi.

In a narrow panel (under 48 columns, for example a 25% panel) Info is
compact: the lines under a bar start two columns in, and a `node` or
`req`/`lim` line shows only when it needs attention or says why a value
is `n/a`. A mark after the bar still names it. The history shows once
it has a line, without `collecting, 1 sample` first. Top pods leave out the
namespace that the title names, and a long pod name goes under its
numbers. Widen the panel (`M-w`) to see every line.

The line under the scope dates the usage by metrics-server's own time:
`usage measured 25s ago over 15s, read 3s ago`. A sample older than two
minutes is marked stale and not used. `7 of 10 pods measured` means the
sum is a lower bound. For a workload the time is the oldest sample of
its own pods, so it does not depend on the node metrics or on other
namespaces.

## How to read the history

Under each CPU and memory bar a line such as
`history, % of allocatable · 21 samples over 5m` heads a sparkline. The
newest sample is at the right, and each cell is a fixed slice of the
last 15 minutes. The line starts at the oldest sample, so it grows from
the left until it spans the whole window. The mark after the frame says
how much it spans, `▕ ... ▏ 5m of 15m`, then `▕ ... ▏ 15m`. A full cell
is 100% unless the mark names a lower top: `top 10%` means every sample
was at most 10%, and a full cell is 10%. The top is the smallest of 10,
25, 50 and 100% that holds every sample in the window. A `·` is a gap: a
missed poll or metrics that were not available. `collecting` means one
sample so far; `stale, last 3m ago` means no recent sample.

## Bars and history as images

In a terminal that shows Kitty graphics (iTerm2 3.7 or later, kitty,
Ghostty), Info draws the bars and the history as images in the style of
Grafana, as the dashboard draws its charts:

- Each bar is a bar gauge: a dark rounded track, filled in green, in
  orange when the bar warns and in red when it fails (the states of the
  text colors). On CPU and memory a yellow tick marks the requests and
  a red tick the limits, on the bar's own scale; a value above the
  scale is a small arrow at the end of the track. A workload bar of
  usage against its limits has the limit at its end.
- The history is a sparkline two rows high: a thin line from sample to
  sample over a fading area, on a dark panel. The scale (`top 10%`) is
  next to its top row and the span (`5m of 15m`) next to its bottom row.

The percentages, the numbers and the other lines stay text. While the
panel width changes (`M-w`, a resize of the window) the bars are text;
the images come back a moment after the width stops changing. A frozen
panel (`C-x f`) keeps the text. Dialogs and menus cover the images as
they cover text.

`charts.graphics` decides, as for the dashboard (see
[Dashboard](dashboard.md)): `text` keeps Info as text, `kitty` draws
images in a terminal kc does not recognize. The dashboard's `b` key
does not change Info. If the bars show question marks, boxes or empty
space, the terminal does not support Unicode placeholders: set
`charts.graphics: text`.

## Keys

- `Down`, `Up` select a top pod on screen; `Enter` or a click opens it
  in the other panel.
- `PgUp`, `PgDn`, `Home`, `End` and the wheel scroll long content.
- On a finding, `Enter`, `F3` and `M-c` in the list act on it.
- `C-x p` pins the scope; `C-x f` freezes; `C-x w` sets a window.

## Limits

- The capacity bars need list and watch on nodes and pods in all
  namespaces. In a namespace scope, pods readable in that namespace are
  enough for the workload and pod usage; the capacity bars then say what
  they cannot read. kc asks first and starts nothing it cannot read.
- `C-x m` shows which metric sources the context has
  ([Metric sources](metric-sources.md)).
- Pods being deleted, such as those a drain evicted, leave the top pods
  at once. They still count under Pods and in the capacity until they
  end, since they hold the node until then.
- Info stays current while a dialog is open beside it: a drain or a
  delete shows in the bars and the top pods at once. The metrics polls
  wait for the dialog to close, so the usage age keeps growing until
  then. The disk is read once when Info starts, also behind a dialog,
  and then once a minute while no dialog is open.
- The history is kept in memory while the panel is in Info mode, for
  this session only. CPU and memory are recorded apart: a bar without
  a percentage (no requests or limits) has no history line, and the
  other keeps its own. A history restarts when the base of its
  percentage changes.
- With `metricsHistory.backends` set, Info also shows a workload's past
  usage from Prometheus. `no series` means Prometheus returned none for
  the metric; `no finite samples in this window` means it returned a
  series whose values are all `NaN` or infinite. Neither is zero usage.
  It covers `metricsHistory.window`, or the investigation window
  (`C-x w`) while one is set; the heading names it. For a custom kind
  the history uses the kube-state-metrics owner series: `kube_pod_owner`
  when the object owns its pods, `kube_replicaset_owner` or
  `kube_job_owner` when a ReplicaSet or Job is in between. Those series
  name the kind without its API group, so a custom kind named like a
  built-in one (StatefulSet) and a chain with more kinds in between have
  no history, and Info says why.

## See also

- [Overview](overview.md)
- [Investigation window](investigation-window.md)
- [Metric sources](metric-sources.md)
- [Dashboard](dashboard.md)
- [Pin and freeze](pin-freeze.md)
- [Panel mode](panel-mode.md)
