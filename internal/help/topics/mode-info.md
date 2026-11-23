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
- On an `/overview` finding: that finding, its value, how it is
  computed and the first objects behind it. Long lines wrap at the
  panel width, as the rest of Info does.

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

The line under the scope dates the usage by metrics-server's own time:
`usage measured 25s ago over 15s, read 3s ago`. A sample older than two
minutes is marked stale and not used. `7 of 10 pods measured` means the
sum is a lower bound. For a workload the time is the oldest sample of
its own pods, so it does not depend on the node metrics or on other
namespaces.

## How to read the history

Under each CPU and memory bar a line such as
`history, % of allocatable · 21 samples over 5m` heads a sparkline. The
frame `▕ ... ▏ 15m` spans the last 15 minutes with the newest sample at
the right. Cells before the first sample stay blank. A `·` is a gap: a
missed poll or metrics that were not available. `collecting` means one
sample so far; `stale, last 3m ago` means no recent sample.

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
- The history is kept in memory while the panel is in Info mode, for
  this session only. It restarts when the base of a percentage changes.
- With `metricsHistory.backends` set, Info also shows a workload's past
  usage from Prometheus. `no series` means Prometheus returned none for
  the metric; `no finite samples in this window` means it returned a
  series whose values are all `NaN` or infinite. Neither is zero usage.

## See also

- [Overview](overview.md)
- [Investigation window](investigation-window.md)
- [Metric sources](metric-sources.md)
- [Dashboard](dashboard.md)
- [Pin and freeze](pin-freeze.md)
- [Panel mode](panel-mode.md)
