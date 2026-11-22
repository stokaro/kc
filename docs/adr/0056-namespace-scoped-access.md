# 0056. Browse namespaces without cluster-wide list access

- Status: Accepted
- Date: 2026-11-22
- Issue: #56

## Context

A user whose only grants are a Role and RoleBinding in one namespace could
not browse that namespace. Every folder used the shared controller-runtime
cache of the context. Its informers list and watch across all namespaces and
filter by namespace afterwards, so for this user they never synced: the pod
list stayed empty, counts never appeared, and after 30 seconds a timeout was
reported. The namespace picker listed Namespace objects and showed an error
toast when that was forbidden.

`Deps.ForNamespace` and namespace-scoped pool entries already existed but had
no callers. The pool built them with an HTTP transport that rewrote every list
and watch path into `/namespaces/<ns>/`, including cluster-scoped ones, so
`/api/v1/namespaces` became a request for a resource that does not exist.

Open questions the issue left:

1. How kc learns that cluster-wide list is forbidden.
2. Which cache a namespace folder uses, and who keeps it alive.
3. What the picker offers when namespaces cannot be listed.
4. How this fits the cache leases of #85 (issue #62), not merged yet.

## Decision

### 1. Ask with SelfSubjectAccessReviews before starting an informer

`cluster.ListWatchAccess` (one per `Cluster`, so per context and
impersonation) answers "may this user list and watch this group/resource in
all namespaces" and "... in namespace X". It sends `list`, and `watch` only
when `list` is allowed. First it asks once for `list`/`watch` on `*`/`*` in
all namespaces: for a cluster admin that one pair answers every type.
Answers are kept for the life of the cluster; errors are not kept. An
evaluation error counts as unknown, not as a denial. Each review logs an INFO
line before it is sent.

The decision (`models.chooseListScope`) is:

| all namespaces | in the namespace | folder namespace | cache                     |
| -------------- | ---------------- | ---------------- | ------------------------- |
| allowed        | (not asked)      | any              | shared                    |
| denied         | allowed/unknown  | a namespace      | namespace-scoped          |
| denied         | denied           | a namespace      | none, the type is hidden  |
| denied         | (not asked)      | all / cluster    | none, the type is hidden  |
| unknown        | allowed/unknown  | a namespace      | namespace-scoped          |
| unknown        | (not asked)      | all / cluster    | shared, the server decides |

An unknown answer never hides a type. In a namespace the namespace cache is
the safe choice: it works for every user who may read there.

### 2. A namespace-scoped pool entry with `DefaultNamespaces`

A pool entry with `Key.Namespace` set now builds its caches with
`cache.Options.DefaultNamespaces = {ns: {}}`. controller-runtime then lists
and watches namespaced types in that namespace only and keeps cluster-scoped
types cluster-wide. The rewriting transport is removed. This is the shared
cache of AGENTS.md, one entry per (context, namespace), shared by both panels
and by counts and lists in that namespace.

The informer of such a cache spans its namespaces and has no single store, so
`ListByGVR` and the resource counts read it through `cache.List` instead of the
informer's indexer.

Where the decision runs:

- `ResourceGroupItem` decides on its count goroutine, before it asks for an
  informer, and keeps the decision until `Close`. A denied type is marked
  forbidden and hidden at once, like a forbidden emptiness peek, instead of
  waiting 30 seconds for an informer that can never sync. This also covers
  forbidden cluster-scoped types in the root.
- `NewNamespacedObjectsFolder` decides when the folder is built (from
  `Enter`). The answer is usually cached by then, because the counts of the
  same folder asked first; otherwise this is at most two reviews bounded by
  a timeout.

Folders below an objects folder (pod containers, YAML, secret keys) inherit
its `Deps`, so they read from the same cache.

### 3. Who keeps the namespace cache alive (until #85)

On main, `Pool.Pin` is one flag per key, and an unpinned entry is evicted
after the TTL even while a folder reads it. A small owner count in the UI
(`ui.scopeHolds`, reached through the new `Deps.HoldScope`) pins a namespace
entry while at least one folder or count holds it and unpins it on the last
release. The objects folder releases in `Close`; a resource count releases in
its `Close`, which the resources folder calls when it is dropped.

### 4. The namespace picker takes a typed name

When the review says namespaces cannot be listed (or listing fails), the
picker opens at once instead of showing an error. It lists "All namespaces",
the kubeconfig context's namespace, the panels' namespaces and the namespaces
used in this session (most recent first, at most ten, in memory only). The
filter line reads "Name or filter"; while the text is a valid namespace name
that is not in the list, it is offered as the last entry ("typed"), after
the names the filter matches; Enter picks the selected entry, so typing a
new name and pressing Enter picks it. A forbidden list shows no toast; another failure still shows one.
No key binding is added; the picker opens as before (click on the namespace
label).

"All namespaces" stays in the list for users who may list across
namespaces; for others its namespaced types show the API server's error, as
before.

`kc -n <ns>` and the context's namespace already start the panels in that
namespace; with the decisions above the root lists the permitted types there.

## Alternatives considered

- **Detect Forbidden from the informer.** controller-runtime's `GetInformer`
  returns before the first list; the reflector retries a forbidden list in
  the background and only logs it. A watch error handler could see it, but
  then the failed informer must be removed and replaced, and every count and
  list would start wrong and switch later. A review before the informer is
  one round trip and starts right.
- **Always use a namespace cache when a panel has a namespace.** Simple, but
  an admin who switches namespaces would build one set of informers per
  namespace instead of sharing the cluster-wide ones.
- **Keep the path-rewriting transport** (docs/designs/namespace-scoped-cache.md,
  now marked superseded). It was chosen so that informers keep a plain store
  for counting. It cannot tell namespaced from cluster-scoped requests
  without a RESTMapper, which is what `DefaultNamespaces` already does inside
  controller-runtime. The price of `DefaultNamespaces` is that a count in a
  namespace cache copies the objects out of the cache (`cache.List`) instead
  of reading the store length; recounts are already coalesced (300 ms), and
  this cost applies only to namespace caches.
- **SelfSubjectRulesReview.** It lists rules for one namespace and cannot
  answer the cluster-wide question; it is also documented as incomplete for
  webhook authorizers.
- **Ask only `list`.** An informer with `list` but no `watch` syncs once and
  never updates, which is worse than an error.
- **Persist recent namespaces in `~/.kc`.** The issue asks for in-memory
  recall; persisting is a separate decision about config files.

## Consequences

- A namespace-only user can browse, count and watch permitted types in a
  namespace. Requests for those types stay in that namespace. Two panels in
  two namespaces use two pool entries.
- For a limited user, the first visit to a namespace sends up to three
  reviews per resource type (cluster-wide list, then list and watch in the
  namespace). An admin sends two in total.
- RBAC changes during a session are not noticed until the context's cluster
  is rebuilt (context switch or restart).
- `PruneNamespaces` (context switch, startup) stops namespace entries before
  the old folders close; this is harmless because those folders are being
  discarded, and their later releases only unpin a key that is gone.
- The read-only and access-view work (#107, #102) has its own review cache
  with a TTL. Once #102 merges, `ListWatchAccess` can be backed by that
  cache; the decision function does not change.

### Follow-up after #85

`Pool.Acquire` returns a refcounted lease and `NamespaceClusterFactory` and
`Deps.ForNamespace` return a release. Then:

1. `models.Deps.acquireNamespace` becomes `return d.ForNamespace(namespace)`.
2. Delete `Deps.HoldScope`, `internal/ui/scope_holds.go` and the
   `HoldScope: a.holdScope` line in `makeDeps`; drop `scopeHolds` from
   `namespaceScopes`.
3. Rename `noScopeRelease` to #85's `noRelease` (both exist after the merge).

Nothing else changes: the folder and the count already store the release and
call it from `Close`, which is the contract ADR 0062 describes.
