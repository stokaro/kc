# 0171. Scope labels in /overview and Info

- Status: Accepted
- Date: 2026-11-23
- Issue: #171

## Context

`/overview` of a namespace mixes two kinds of findings. The capacity rows,
the node checks and the PersistentVolume check cover the whole cluster.
Every other rule filters to the namespace scope. Nothing on screen said
which kind a row was, and F3 printed `Scope: namespace <ns>` for every
finding, including the capacity rows that ignore the namespace. The Info
panel mode had the same problem. Its title names the context and
namespace, its bars sum every node, and its top-pods list is filtered to
the namespace. In an empty namespace, busy bars sat above "no pods in <ns>".

What the issue asks for:

- scope labels for Capacity and for the sections that hold cluster checks;
- separate labels for the Info bars and the top pods;
- an F3 scope per finding;
- a visible way to switch namespace, keeping Enter as the drill-down.

## Decision

### 1. One table of cluster-wide findings

`overviewClusterScopes` (`internal/models/overview_scope.go`) maps each
cluster-wide finding ID to what it covers:

- capacity cpu, memory and pods: every node and every pod on it, in all
  namespaces;
- disk and the three node checks: every node;
- `health/pvs`: every PersistentVolume.

Every other finding covers the namespace scope. A test checks that every
listed ID exists, so renaming a rule cannot drop its label silently.

### 2. Section headers and row titles

A section header names its scope:

- `Capacity · cluster` when all its rows are cluster-wide;
- otherwise the namespace scope, `Health · namespace shop` or
  `Health · all namespaces`.

In a section that mixes the two kinds, the cluster rows get ` (cluster)`
after their title, for example `/nodes not ready (cluster)`. Only the
displayed cell changes. The row ID and the path that Enter pushes stay
the same, so navigation, history and the existing Enter drill-down do not
change.

### 3. F3 reports the finding's own scope

The Scope line comes from the table:

- `cluster: every node and every pod on it, in all namespaces, whatever
  the namespace scope (namespace shop)` for capacity;
- `namespace shop` (or `all namespaces`) for namespace findings.

### 4. Switching namespace

kc already switches namespace through the panel's namespace scope: click
the namespace in the bottom border, or `cd -n <ns>`. `M-i` opens the
overview of a list's namespace. kc gets no new key. Instead, F3 ends with
that hint, and the status line on a section header shows it.

### 5. Info labels

`InfoSummary` gets `BarsTitle` and `TopTitle`, set by the model from the
same report:

| Focus          | Bars                                           | Top pods                                       |
| -------------- | ---------------------------------------------- | ---------------------------------------------- |
| scope          | `Cluster capacity (all nodes, all namespaces)` | `Top pods by memory in namespace shop`         |
| node           | `Node n1 (all its pods, all namespaces)`       | `Top pods by memory in namespace shop on node n1` |
| workload       | `Pods of deployments.apps shop/web`            | `Top pods of deployments.apps shop/web by memory` |

The widget prints `BarsTitle` above the bars and `TopTitle` as the
top-pods heading. The panel title is unchanged: context and namespace,
or the node or workload.

Both labels belong to the summary, so they change in the same refresh as
the title. A context change opens a new source, which shows `Loading...`
until its first report and keeps no label from the old context.

## Deviations from the issue

- **Labels always show, not only when the scopes differ.** In an
  all-namespaces scope the bars and the top pods both cover the cluster,
  yet the two labels still read differently ("all nodes" against "all
  namespaces"). Always showing them costs one line and keeps the layout
  stable when the scope changes.
- **No new namespace-switch action.** The existing ones (border click,
  `cd -n`, `M-i`) are now named where the user looks for them. A second
  key would duplicate them.
- **No scope column.** A fourth column costs width in a half-width panel.
  The header and the `(cluster)` suffix carry the same information.

## Alternatives considered

- **A `scope` field set by each rule.** It would spread the
  classification over eight rules. The table keeps it in one place, and
  the existence test guards it.
- **Move the node and PV checks into a separate "Cluster" section.** It
  changes row order and the user's established layout (ADR 0051), and it
  splits node health away from pod health.

## Consequences

- Section header cells and some row title cells are longer. Tests that
  read them now expect the labels.
- A new cluster-wide rule must be added to `overviewClusterScopes`.
  Without that entry it is labeled with the namespace, which would be
  wrong.
