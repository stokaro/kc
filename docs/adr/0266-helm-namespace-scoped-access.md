# 0266. Read Helm releases with namespace-only Secret access

- Status: Accepted
- Date: 2026-11-23
- Issue: #266
- Supersedes: section 6 of [0070](0070-helm-release-browser.md)

## Context

`/helm` reads Helm's release Secrets from the shared cache, which lists and
watches Secrets in all namespaces. Before using it, the reader probed with a
direct list in all namespaces. When that was forbidden it probed the panel's
namespace too, but only to change the message: a user with a Role on Secrets
in one namespace got "Secrets can be listed in <ns> but not in all
namespaces" and no releases. Every other namespaced folder already chooses a
cache limited to the namespace for such a user
([0056](0056-namespace-scoped-access.md)).

The issue asks to apply that decision to Helm reads and watches, keep the
shared cache for users who may use it, keep decisions per namespace, keep
all-namespace failures explicit, and release cache leases when the view
closes.

The claims were checked against main (ae8ed93): the probe and rejection are
in `internal/models/helm_reader.go`; the access decision is
`Deps.resolveListScope`; the namespace cache is a pool lease from
`Deps.ForNamespace`.

## Decision

1. The Helm reader decides the cache per namespace scope with
   `resolveListScope(secrets, namespace)`, the same SelfSubjectAccessReview
   decision as other folders. Cluster-wide list and watch: the shared cache.
   Only the namespace: a namespace-scoped pool entry. Denied: no cache.
2. After the decision, one direct list (`limit=1`, `owner=helm`) in the
   chosen scope confirms it. An unknown review answer is no permission, and
   with all namespaces selected the shared cache would otherwise be used
   blindly. A forbidden list is a denial; the notice quotes it. When the
   review denied but the list works (list without watch), the notice says
   that an access review denied list or watch.
3. Reads (`cache.List`) and watches (`startFilteredInformer`) use the Deps of
   that decision. The watch factory starts nothing until the decision exists,
   as before.
4. Decisions are kept per namespace inside one reader. A denial is kept;
   other errors are retried on the next load. A denial in one namespace does
   not change another namespace's answer (with all namespaces selected, a
   release folder resolves the release's own namespace).
5. The releases folder owns the reader. Its `Close` releases every namespace
   lease the reader acquired; a load that finishes after `Close` releases its
   lease at once. Folders below it share the reader and do not release. The
   navigator closes them first.
6. Messages: "listing and watching Secrets in <ns> is forbidden" or "... in
   all namespaces is forbidden", with a hint to open `/helm` in a permitted
   namespace for the second, the `helm list` command and the API server's
   answer. All-namespace denials stay a notice row, never an empty list.

## Deviations from the issue

- "Changing namespaces reevaluates the appropriate scope": a namespace change
  builds a new root and a new `/helm` entry, so a new reader decides again.
  No code is needed for that. RBAC changes during a session are still not
  noticed until the cluster is rebuilt, as 0056 records for all folders.
- The issue suggests the namespace probe was the only problem. The cluster-
  wide probe also bypassed the per-cluster review cache that every other
  folder shares; the reader now uses that cache, so an admin pays no extra
  review after the first folder.

## Alternatives considered

- **Keep the two direct probes and switch to `ForNamespace` when only the
  namespace list works.** Smaller, but it checks `list` only. An informer
  with `list` and no `watch` syncs once and never updates (0056). It would
  also be a second access policy next to `resolveListScope`.
- **Drop the confirming list.** Saves one request per scope, but an unknown
  review with all namespaces selected would start a cluster-wide Secret
  informer that retries forever for a user who may not use it.
- **One reader per folder.** Each release and revision folder would acquire
  and release its own lease. Sharing the reader keeps one decision and one
  decode cache per `/helm` visit.

## Consequences

- A namespace-only user can browse releases, revisions, values, manifests,
  resources and history in a permitted namespace. Requests for Secrets stay
  in that namespace.
- Tests: unit tests cover each decision (shared, namespace, denied, unknown
  review with a forbidden list, transient error), independence of
  namespaces, lease release on `Close` and after it. An envtest test with an
  impersonated user that has a Role on Secrets in one namespace lists and
  watches releases there, gets explicit denials in another namespace and in
  all namespaces, and leaves no lease open. A second envtest test covers a
  user with no Secret access.
- The Live column of `/resources` still uses direct GETs of the shared
  client; it never needed the cache.
