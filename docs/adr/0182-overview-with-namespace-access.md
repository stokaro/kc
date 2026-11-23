# 0182. Overview with access only inside a namespace

- Status: Accepted
- Date: 2026-11-23
- Issue: #182

## Context

ADR 0051 gives the overview its own pool entry, a cache that lists and
watches every overview kind in all namespaces, and filters by namespace
afterwards. For a user whose grants are a Role and RoleBinding in one
namespace, none of those informers syncs. After 30 seconds every row said
"the cache did not sync (no list or watch permission?)", although normal
browsing (ADR 0056) shows the same pods, Deployments and events through a
namespace-scoped cache. The reflectors also kept retrying forbidden lists
for as long as the overview was open.

The building blocks exist: `cluster.ListWatchAccess` answers "may this user
list and watch this type in all namespaces / in namespace X" with
SelfSubjectAccessReviews cached per identity (ADR 0056), a pool entry with
`Key.Namespace` set builds a cache limited to that namespace, and #219 turns
a finding whose source is denied or unsynced into `n/a` and keeps its last
targets as unknown instead of resolving them.

The Info panel mode has its own gate (ADR 0166 §7, #169): it needs nodes
and pods in all namespaces and does not start its engine without them.

## Decision

### 1. A scope per overview kind, decided before any informer starts

When the overview engine connects, it asks for each of its 15 kinds:

| all namespaces | in the namespace | kind or view          | cache                 |
| -------------- | ---------------- | --------------------- | --------------------- |
| allowed        | (not asked)      | any                   | cluster-wide overview |
| unknown        | (not asked)      | any                   | cluster-wide overview |
| denied         | allowed/unknown  | namespaced            | overview, namespace   |
| denied         | denied           | namespaced            | none                  |
| denied         | (not asked)      | cluster-scoped        | none                  |
| denied         | (not asked)      | all-namespaces view   | none                  |

`models.chooseOverviewScope` holds the table. It differs from
`chooseListScope` of ADR 0056 in one row: an unknown answer for all
namespaces keeps the cluster-wide cache even in a namespace overview. A
failed review must not turn an administrator's overview into a namespace
one whose capacity rows are all `n/a`; the API server then decides, as it
did before.

The reviews run on the engine's goroutine, never on the UI thread. An
administrator answers every kind with the one `*`/`*` review that
`ListWatchAccess` asks first. A namespace-only user costs up to four
reviews per kind (list and watch, all namespaces and the namespace), once
per identity: the answers are cached on the context's `Cluster`.

### 2. Two pool entries at most, each only when a kind needs it

The engine leases the cluster-wide overview entry (`kc-overview`, as
before) for the kinds whose scope is shared, and an entry with the same
selector key and `Key.Namespace` set for the namespace kinds. An entry no
kind needs is not acquired, so a namespace-only user starts no
cluster-wide informer at all, and a kind that is denied gets no informer
and no retries. The keys carry the context, the namespace and the
impersonation, so another namespace, context or identity never reads an
entry built for a different scope. Both leases are given up together when
the engine stops, as in ADR 0051.

A denied kind is `n/a` with "not permitted to list and watch" (cluster
scoped), "... in all namespaces" (all-namespaces overview) or "... in all
namespaces or in namespace X". Opening the overview of a namespace never
lists Namespace objects, so a typed or known namespace name is enough.

### 3. Cluster-wide rows never use one namespace's pods

Pods read from the namespace cache are flagged on the input
(`overviewInput.onlyIn`). The capacity rows (cpu, memory, pods) and the node
summaries need every pod on a node; with pods from one namespace they say
"pods: readable only in namespace X; this needs every one in all
namespaces" instead of a load that looks low. Node rows that need only
nodes (not ready, pressure, cordoned) still work when nodes are readable.

### 4. Rows built from several kinds are partial, not blocked

`/workloads unavailable` and `/workloads rolling out` list Deployments,
StatefulSets and DaemonSets; `/workloads single replica, no PDB` lists
Deployments and StatefulSets and needs PDBs. One forbidden kind used to make
the whole row `n/a`. Now the row counts the kinds it can read, its value
ends in `(partial)`, and F3 names the kinds it left out ("Partial, not
counted: ..."). It is `n/a` only when none of the kinds can be read, or a
required one (PDBs) cannot. A target of a kind that becomes unreadable is
listed as unknown, not recovered, as #219 does for a whole row.
Such a row is never "not applicable" (ADR 0172) because the readable
kinds hold no object: the kinds that cannot be read may hold some.

### 5. The Info preview of a finding (ADR 0173)

The preview shows the reasons above under "Not available" and suggests
the matching check: `kubectl auth can-i list <resource>` for a
cluster-scoped kind, with `-A` when the kind is needed in all namespaces
(also for capacity with pods readable in one namespace), and with
`-n <namespace>` when it is not readable in the namespace either. A
partial row lists what it leaves out under "Partial, not counted".

## Deviations from the issue

- The issue asks to read "supported custom-resource data" through namespace
  caches. The only custom resource the overview reads is cert-manager's
  Certificate; it follows the same table as every other kind.
- The Info panel mode keeps its gate. Its bars are node capacity, which a
  namespace-only user cannot have (section 3), so starting its engine would
  only add `n/a` rows; the gate's message already names the missing access.

## Alternatives considered

- Use `chooseListScope` unchanged. A failed review would then narrow an
  administrator's namespace overview and blank its capacity rows. Rejected.
- Share the browsing cache of the namespace (ADR 0056 entry without a
  selector key). It holds unstructured objects and table rows; the overview
  lists typed objects (ADR 0051 §5), so one entry would hold both forms
  anyway. The overview keeps its own entry, with the same lifetime rules.
- Sum capacity from the visible pods and mark it partial. A node's load is
  the sum over every namespace; a partial sum reads as headroom that may not
  exist. Rejected.

## Consequences

- A namespace-only user sees that namespace's pod, workload, job, PVC,
  quota, HPA, PDB, certificate and warning-event rows, live, with no
  forbidden watches in the background.
- Mixed grants (for example nodes through a ClusterRole, pods through a
  Role) give the node rows and the namespace rows; capacity says which
  input is missing.
- The engine's `overviewConn` reads per kind (`Reader(gvr)`) and reports
  `OnlyNamespace(gvr)`; a test fake implements both.
- Access answers are kept for the life of a `Cluster`. A grant added while
  kc runs is seen after the context is reopened, as for browsing.
