# 0165. Info panel CPU and memory rows: bases, overcommit marker, empty scope, rounding

- Status: Accepted
- Date: 2026-11-22
- Issue: #165

## Context

The Memory row of the Info panel mode (`C-1` / `C-2`) was checked against
kubectl on a real cluster. Reference node: capacity 15646208Ki, allocatable
10939904Ki, memory requests 2774Mi, limits 12406Mi, node usage 6144Mi, sum
of pod working sets 2396Mi. kc printed:

```
Memory  ██████████████░░░░░░░░░░  58% overcommitted
        use 6.0/10 Gi, req 26%, lim 116%
Top pods by memory
   no pod usage
```

Four problems:

1. "58% overcommitted" reads as "overcommitted by 58%". The 58% is usage;
   the overcommit comes from the limits (116%).
2. "use 6.0/10 Gi" compares node usage from NodeMetrics (system daemons,
   kubelet and runtime included) with allocatable (capacity minus system and
   kube reserved and the eviction threshold). kubectl top node does the
   same, but the bases differ: the pods used 2.3 GiB.
3. "no pod usage" in a namespace without pods cannot be told from missing
   metrics.
4. "10 Gi" next to "6.0", and percentages that differ by one from kubectl
   describe node (kc rounded, kubectl truncates).

The CPU row had the same shape. ADR 0051 section 10 describes the Info
bars; this record replaces its CPU and memory bar description. `/overview`
stays as it is (decided by the user); it shares the thresholds, not the
text.

## Decision

### 1. One base per line, three lines under the bar

CPU and memory each get three lines under the bar:

```
Memory  █████░░░░░░░░░░░░░░░░░░░  22%
        pods 2.3/10.4 Gi allocatable, 22%
        node 6.0/14.9 Gi capacity, 40%
        req 25%, lim 116% (overcommitted)
```

- `pods` is the sum of the PodMetrics working sets (CPU: usage) of the pods
  bound to the nodes, against allocatable. It shares its base with the
  requests and limits, so the three compare with each other.
- `node` is the NodeMetrics usage against capacity. It includes what runs
  outside pods, so it compares with the whole machine.
- `req` and `lim` are against allocatable, as kubectl describe node prints
  them.

Both usage lines are shown. Each line is at most about 40 characters, so
both fit a half-width panel of an 80-column terminal after the 8-column
indent; a narrower panel cuts the line end, as before. The block takes two
more lines than before per resource; on a short terminal the top-pods list
is cut first.

The bar fills to the pods line. Without pod usage it fills to the requests
and the lines become `req X/Y allocatable, N%`, `lim N%`, and the usage
lines say `n/a` with the reason (one `use n/a: <reason>` line when node and
pod usage are missing for the same reason). The sparkline records only bars
that show usage (`InfoBar.Usage`), not the requests fallback.

The pod usage of a node covers every pod on it, in every namespace, like
its requests. The Info engine therefore also polls PodMetrics of all
namespaces when its scope is one namespace (one extra `list` every 15
seconds while the mode is open; none in an all-namespaces scope, where the
scope's poll is reused). `/overview` does not poll it. Pods that have no
PodMetrics entry yet count as zero, as in kubectl top pods.

### 2. Each state word sits on the line it describes

- The bar's word is the state of what the bar shows: pod usage `full` /
  `high` at `usageFailPercent` / `usageWarnPercent`, or `under-req` when
  the pods use at least `underRequestedFactor` times their requests.
- `(overcommitted)` follows the limits when they exceed
  `limitsWarnPercent`; `(full)` / `(high)` follow the requests at
  `requestsFailPercent` / `requestsWarnPercent`; the node line gets
  `(full)` / `(high)` at the usage thresholds against capacity.
- The bar's color is the worst state of all its lines, so an overcommitted
  node still shows a warning color while its bar reads `22%`.

Under-req now compares pod usage with pod requests. The overview's rule
compares node usage with pod requests; on the reference node that is 57%
against 25%, which looks under-requested although the pods use less than
they request.

### 3. The top-pods note names the cause

When no pod is listed under "Top pods by memory":

- `pods cannot be read: <reason>` when the pod list failed;
- `no pods in <ns>` (`no pods` for all namespaces, `... on node <n>` for a
  node) when the scope has no pods that have not terminated;
- `metrics unavailable: <reason>` when it has pods but none is measured;
  the reason is the PodMetrics error, or `no PodMetrics for these pods yet`.

A workload's note reads `metrics unavailable: <reason>` too.

### 4. One decimal, truncated percentages

CPU cores and memory GiB print with one decimal on both sides at every size
(`6.0/10.4 Gi`, `0.6/4.0 cores`). Values round to one decimal.

Every percentage in the Info mode is truncated, as kubectl describe node
prints it (25.97% is 25%), including the number after each bar. The states
compare the printed numbers with the thresholds, so the text and the word
agree: limits of 100.09% print as `lim 100%` without `(overcommitted)`.

Disk keeps its existing format (`29/915 Gi`), shared with `/overview`.

## Alternatives considered

- **Pick one usage base.** Pods against allocatable alone hides system
  overhead that can still push a node into eviction; node against capacity
  alone does not compare with requests and limits. Both fit, so both are
  shown.
- **Node usage against allocatable** (kubectl top node). Kept by
  `/overview`, but it is the mixed base the issue reports; the Info mode
  labels its bases instead.
- **Keep the overview's flag on the bar.** It would keep "overcommitted"
  next to a usage percentage, which is the confusion of point 1.
- **Round, and document it.** Rounding is closer to the true value, but the
  user compares with kubectl describe node; truncation matches it.
- **Change `/overview` in the same way.** The user decided to keep it.

## Consequences

- The Info mode and `/overview` can show different states for the same
  node: `/overview` judges usage on node usage against allocatable, the
  Info bar on pod usage. Each line now says its base.
- `/overview` still rounds its percentages and still compares node usage
  with pod requests for `under-req`. Follow-up: apply the same base and
  rounding there if the user wants them to agree.
- One more PodMetrics poll of all namespaces while an Info panel of one
  namespace is open. Without permission to list PodMetrics cluster-wide the
  pods line reads `pods use n/a: <reason>` and the bar falls back to
  requests.
- envtest serves no metrics.k8s.io, so the pods and node lines with real
  values are proven by unit tests on the reference numbers, not by a
  headless run.
