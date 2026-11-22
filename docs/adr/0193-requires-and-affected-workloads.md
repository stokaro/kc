# 0193. Requires folder, affected workloads and partial related results

- Status: Accepted
- Date: 2026-11-22
- Issue: #193

## Context

ADR 0039 added `M-j` (owners) and `M-b` (used by). Issue #193 asks to extend
them: forward dependencies of a pod with the exact field that names each
target, the workloads behind the consuming pods, a bounded depth walk with
cycle detection, opening related objects in the other panel, distinct
reporting of missing, replaced, unsupported and forbidden targets, and an
indication when a restricted user sees only part of a result.

What the code did before this change, checked on main:

- `PodReferences` already found the ConfigMaps, Secrets, PVCs and the
  ServiceAccount of a pod, but recorded only the kind of link (`volume`,
  `env`, `envFrom`), not the field.
- Used-by of a ConfigMap listed pods only; the Deployment behind them was one
  `M-j` per level away.
- The owners folder already marked an owner that is gone or replaced by an
  object with another UID, and a kind the cluster does not serve.
- Both folders read through the unscoped shared cache of the context. For a
  user who may list a type only in some namespaces (ADR 0056), the shared
  informer for that type never syncs: the lookup waited for the request
  timeout, and the folder's watch kept an informer retrying a forbidden list
  in the background. A restricted user could not tell "nothing uses this"
  from "kc could not look".

## Decision

### 1. `M-d` opens `<object>/requires`

A third related folder lists what the selected object names in its spec.
Built-in finders, registered with `models.RegisterDependsOn` next to
`RegisterUsedBy`:

| Object | Requires | Field |
|---|---|---|
| Pod | ConfigMaps, Secrets, PVCs, ServiceAccount | `volumes[<name>]`, `volumes[<name>].projected`, `volumes[<name>].ephemeral`, `<containers>[<name>].envFrom`, `<containers>[<name>].env[<VAR>,...]`, `imagePullSecrets`, `serviceAccountName` |
| Pod | Node | `spec.nodeName` (unscheduled pods name none) |
| Deployment, ReplicaSet, StatefulSet, DaemonSet, Job, CronJob | the same as a pod, from the pod template | `template.<field>`; ephemeral claims are left out, their names depend on the pod |
| PersistentVolumeClaim | PersistentVolume | `spec.volumeName` |

One row per target; several fields that name the same target are joined.
Each target is read from the cache: a missing one is a dim `not found` row,
one the user may not read a dim `forbidden` row. The folder watches the
object and the target types (in the object's namespace; nodes and volumes
cluster-wide), so a target that appears or goes away shows at once. A type
with a single target is filtered to that name, so heartbeats of other nodes
or edits of other ConfigMaps do not refresh the folder.

`M-d` and `Esc d` were free on main (checked with
`git grep '"alt+' internal/ui`; no open PRs on 2026-11-22). The folder is
`requires` and the File menu entry `&Requires`, because every letter of
"Dependencies" and "Depends on" is already a hotkey in that menu.

The field names now also appear in the used-by Relation column
(`volumes[cfg], containers[app].envFrom` instead of `volume, envFrom`).

### 2. Used-by lists the controllers of the pods it finds

Used-by of a ConfigMap, Secret, PVC or ServiceAccount keeps its pod rows and
adds one row per controller met on the way up from those pods:
`replicasets.apps/web-1` "controller of pods/web-1-a, pods/web-1-b" and
`deployments.apps/web` "controller of replicasets.apps/web-1,
replicasets.apps/web-2". A controller is listed once however many pods it
runs (identity: GVR, namespace, name, UID; one folder is one context). The
relation names at most three children and counts the rest.

The walk follows controller references only, at most four hops from a pod
(pod, ReplicaSet, Deployment is two), and ends when an object comes back on
the same chain. Each controller is read once per refresh. A controller that
is gone, replaced by another UID, of a kind the cluster does not serve, or
forbidden is listed dim with that reason and ends its chain.

### 3. Every related read goes through the ADR 0056 scope decision

A related folder reads through a `scopedReader`: for each (type, namespace)
pair it asks `Deps.resolveListScope` once, then reads from the shared cache,
from a namespace cache (held until the folder closes), or answers Forbidden
without a request. Watches use the same decisions and skip denied pairs, so
no informer is started that can never sync. All decisions for watched types
are made when the folder is built, in the `tea.Cmd`; the UI thread only reads
them. Rows of a related folder view and enter their object through the
scope of their type, so a namespace-only user can open them.

When a used-by lookup may not list its source type, the folder shows one dim
`(not listed)` row, "forbidden; the result is incomplete", instead of an
error toast or an empty folder.

### 4. A new lookup cancels the one in flight

`openRelated` keeps the cancel function of its lookup. Pressing `M-j`,
`M-b` or `M-d` again cancels the previous lookup; its result, or its error,
is dropped without a toast and its folder is closed. As before, a result
that arrives after the user switched panels or folders is dropped too.

## Deviations from the issue

- **No automatic multi-hop walk.** The issue asks for a configurable depth
  walk over dependencies and dependents. The only automatic walk is the
  controller chain in used-by (bounded, cycle-safe). Everything else is one
  hop per key press: rows of a related folder are objects, and `M-j`,
  `M-b` and `M-d` work on them, so the user walks the graph and the
  breadcrumb shows the path taken (`pods/web/requires/cfg/used-by`). A fan-out
  walk over several hops in a large namespace would read and watch many
  types for one key press, and its result is hard to read in a terminal
  during an incident.
- **Same panel, not the opposite one.** ADR 0039 chose the same panel so
  the other panel keeps what the user had there; the breadcrumb keeps the
  originating object and the path visible, and `/..` goes back. Nothing in
  this issue changes that trade-off. `C-u` swaps panels when the user wants
  the result on the other side.
- **Used-by does not scan pod templates.** A Deployment scaled to zero that
  names a ConfigMap is not listed under the ConfigMap's used-by: that would
  mean listing and watching six more workload types for every used-by open.
  `M-d` on the Deployment shows the link from its side.
- **Only built-in relations.** As the issue says, no CRD schemas are
  inferred. `RegisterDependsOn` is the hook for more.
- **Same-name replacement of forward targets** cannot be detected: a pod
  names a ConfigMap by name only, so any ConfigMap of that name is the one
  the kubelet would use. UID checks apply where a UID is recorded (owner
  references, the PersistentVolume claimRef).

## Alternatives considered

- **Watch every possible target type of a pod** (ConfigMaps, Secrets, PVCs,
  ServiceAccounts, nodes) regardless of the spec. Starts informers the pod
  does not need; the folder watches only the types the object names.
- **Direct GETs for the existence checks** instead of the cache. One request
  per target per refresh, and no live updates; AGENTS.md asks for the shared
  cache. The cost of the cache is an informer per target type, the same the
  type's own list uses (for Secrets that is every Secret of the cluster in
  memory, as when `/secrets` is opened).
- **Report a forbidden used-by source as an error toast** (the old
  behavior after a timeout). The folder would open empty, which reads as
  "nothing uses this".

## Consequences

- Opening `/requires` of a pod starts informers for the types it names, in
  the scope the user may read. Opening used-by of a ConfigMap reads the pods'
  controllers from the cache (ReplicaSets, Deployments, Jobs ...), which
  starts those informers if no list of them was open.
- A namespace-only user gets answers in a fraction of a second where the
  lookup used to wait for the request timeout, and leaves no retrying
  informer behind.
- Follow-ups: Ingress backends and TLS secrets and ServiceAccount secrets as
  requirements; workload templates as users (behind an explicit action);
  verbs on related rows; user-defined relations for CRDs.
