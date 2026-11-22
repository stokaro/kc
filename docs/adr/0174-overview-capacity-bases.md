# 0174. /overview CPU and memory rows use the Info bases

- Status: Accepted
- Date: 2026-11-22
- Issue: #174

## Context

ADR 0165 gave the Info panel mode one base per line: pod usage, requests
and limits against allocatable, node usage against capacity, truncated
percentages, and `under-req` from pod usage against pod requests. It left
`/overview` as it was. The issue asks to make `/overview`, its node list
(Enter) and F3 agree with Info, with one shared calculation.

What the code did before this change:

- `capacityResourceRule` printed `use 57% req 25% lim 116%`: node usage
  (NodeMetrics, system daemons included) against allocatable, rounded.
- `capacityFlag` judged `under-req` on that node usage against pod
  requests. On the #165 reference node that is 57% against 25%, so the
  node looked under-requested although its pods used 22%.
- The Info mode had its own copies of the per-node figures
  (`overviewNodeSummary`, `nodePodUsage`) and its own state logic in
  `capacityBar`. Only the thresholds were shared.
- `/overview` did not read PodMetrics of every namespace, so it could not
  compute pod usage per node in a one-namespace scope.

## Decision

### 1. One calculation

`nodeLoads` now computes, per node and per resource, one
`capacityAmounts`: pod usage, node usage, requests, limits, allocatable,
capacity, and why a usage is not known. `judgeCapacity` turns it into
truncated percentages and a state per line; `sumCapacity` adds nodes up.
The `/overview` rows, the node list under them and the Info bars all read
these (`internal/models/overview_capacity.go`). The Info node summary
keeps the same `capacityAmounts` instead of its own fields.

### 2. The row text names each base

```
/nodes memory   pods 22% req 25% lim 116% of alloc, node 40% of cap   overcommitted
```

The node list prints the same text per node, followed by the node's
state word. F3 shows the same value, explains each figure and its base,
and lists the commands for every input: `kubectl describe nodes`,
`kubectl top pods -A`, the pod-to-node mapping
(`kubectl get pods -A --field-selector ... -o custom-columns=...`),
`kubectl top nodes`, and the nodes' capacity and allocatable. It notes
that `kubectl top nodes` prints node usage against allocatable, while kc
uses capacity (ADR 0165).

### 3. States

A node's state is the worst of its four lines, with the Info rules:
requests `full` / `high`, limits `overcommitted`, pod usage `full` /
`high` / `under-req`, node usage `full` / `high` against capacity. The
word is `full` for a failure; among warnings `overcommitted` comes before
`under-req` before `high`, as before. `under-req` needs pod usage; usage
outside pods only moves the node line. The row's state stays the worst
node, as before. All percentages are truncated, and the states compare
the printed numbers.

### 4. Missing usage stays n/a

- Without PodMetrics of every namespace the pods figure is `n/a` with the
  reason; it never falls back to node usage.
- A pod without PodMetrics yet counts as zero, as in `kubectl top pods`
  and ADR 0165. But a node with pods of which none has PodMetrics has pod
  usage `n/a` ("no PodMetrics for the pods on node X yet"), so a node that
  metrics-server cannot reach is not shown as idle. A node without pods
  has pod usage 0.
- A node missing from NodeMetrics has node usage `n/a` with its own
  reason. A total is known only when it is known for every node, as the
  old `use` total was.

### 5. /overview polls PodMetrics of every namespace

`measuredUsage` always reads PodMetrics of every namespace now, for both
engines; for an all-namespaces scope that is the scope's own poll. The
pollers are shared by key (cluster, kind, namespace), so while the Info
panel is open (the default right panel, ADR 0166) the all-namespaces poll
already runs and `/overview` adds no request. With Info off,
`/overview` of one namespace adds one PodMetrics list of every namespace
every 15 seconds; a forbidden answer backs off to five minutes (ADR
0166), and the row then says why pod usage is not known.

## Deviations from the issue

- **Totals keep their own state.** The `/overview` row's state is its
  worst node (so Enter leads to it); the Info bar of a scope judges the
  sum of the nodes. Per node both are the same; the issue's "totals and
  per-node values use the same definitions" holds for the bases and
  formulas. Making the Info bar take its worst node's state would hide
  the scope's own usage behind one node.
- **No partial totals.** When one node lacks usage, the total says `n/a`
  instead of summing the nodes that are known. Summing a subset needs a
  "3 of 4 nodes" label in both views; it is left as a follow-up.
- **No new acceptance-test layer for F3.** F3 prints the finding's value,
  explanation and commands; the tests check those fields of the finding
  rather than rendering the F3 viewer.

## Alternatives considered

- **Keep node usage against allocatable in /overview** (kubectl top
  nodes). It is the mixed base that made the reference node look
  under-requested.
- **Show only pod usage in /overview.** Hides system overhead that can
  still push a node into eviction; the node line catches it.
- **Read pod usage only in an all-namespaces /overview.** Avoids one poll
  when Info is off, but then the same row means different things by scope.

## Consequences

- `/overview` and Info show the same figures and the same state for every
  node. ADR 0165's consequence that they disagree no longer holds; its
  decisions stand.
- The row value is longer (about 50 characters) and is cut in a narrow
  panel; the node list and F3 show it whole.
- `/overview` judges node usage against capacity, and `usageWarnPercent`
  / `usageFailPercent` now also apply to pod usage. A node that used 85% of
  allocatable through system daemons was `high` before; it is `high` now
  only if its usage reaches 80% of capacity.
- Follow-up: partial totals with a node count.
