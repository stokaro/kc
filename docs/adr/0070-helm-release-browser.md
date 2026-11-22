# 0070. Browse Helm releases from their storage Secrets

- Status: Accepted
- Date: 2026-11-22
- Issue: #70

## Context

A Helm release is spread across many resource lists, and its values,
stored manifest and revision history are not visible in kc at all. Issue
#70 asks for a read-only Helm folder in the panel's namespace scope with
values, manifest, notes, history and links to the deployed objects. It
leaves open how kc reads releases (an optional `helm` executable or a
library/storage adapter), where the folder appears, which views a release
has, and how a manifest object opens its live object.

Constraints from AGENTS.md: reads go through the shared controller-runtime
cache (no ad-hoc watches), every real client request logs an INFO line, work
stays off the Bubble Tea update goroutine, and identity uses the full GVR.

## Decision

### 1. Decode Helm's storage records in kc

kc reads the Secrets that Helm's default storage driver writes: type
`helm.sh/release.v1`, label `owner=helm`, one Secret per revision named
`sh.helm.release.v1.<release>.v<revision>`. The `release` key holds the
release as JSON, gzipped and base64 encoded (under the API's own base64
layer). `internal/helm` decodes this into a small struct with only the
fields kc shows: name, namespace, version, info (status, times,
description, notes), chart metadata, user-supplied values, manifest and
hooks. Helm's JSON time (`""` for zero) has its own type. The decompressed
size is capped at 64 MiB.

The Secrets are listed from the shared cache with a label selector, and the
folders add an event handler (filtered to `owner=helm`) to the shared
Secret informer, so a new revision shows up without a separate watch.
Decoding runs in the folders' background loads and is memoized per Secret
UID and resourceVersion.

### 2. Where `/helm` appears

`/helm` is the first row of the panel root (scoped to the panel's
namespace, or all namespaces) and of `/namespaces/<name>`. Nothing is read
until it is entered.

### 3. Views per release

- Release list columns: Name, Namespace, Revision, Status, Chart, App
  version, Updated, from the newest stored revision (as `helm list -a`).
  `F3` shows the stored status (as `helm status`).
- A release folder (newest revision): `values` (user-supplied, YAML),
  `manifest`, `notes`, `hooks`, `/resources`, `/history`. Each viewer
  title and the Stored content column say "stored in revision N", so
  stored content is not mistaken for live state.
- `/history`: every stored revision, newest first, with status, chart and
  description. Entering a revision gives the same views for that revision
  (without `/history`).
- `/resources`: the objects of the stored manifest, in manifest order. Each
  is resolved to its GVR with the REST mapper (namespace defaults to the
  release namespace for namespaced kinds, and is dropped for cluster-scoped
  ones). A Live column says `yes`, `missing`, `denied`, `type not served` or
  `?`. `F3` shows the object as stored. Objects are found from the manifest,
  not from labels, so unlabeled objects are listed too.
- Commands shown: `helm list -a -n <ns>` (or `-A`), `helm status`,
  `helm history`, `helm get values|manifest|notes|hooks <r> -n <ns>
  [--revision N]`, and `helm get manifest ... | kubectl get -f -` for
  `/resources`. `--kube-context` is added when the panel's context is not
  kubectl's current one.

### 4. Opening a live object

A resource row is a link (`models.ObjectLink`), drawn with an `@` prefix
like a symlink in mc. `Enter` sends `models.OpenObjectRequest`; the app
moves the other panel to the object's namespace scope and type list and
selects the object (retrying while the list loads). Focus stays on the Helm
panel so the user can walk through the links. A missing object still opens
its type list and shows a message; a type the cluster does not serve shows
a message and moves nothing. No new key binding is needed.

### 5. The live check uses GETs, not informers

The Live column issues one GET per manifest object when `/resources` loads
(each logged at INFO). Starting a shared informer for every kind in a chart
would watch those kinds cluster-wide for a one-off answer. After a kind is
denied or times out, its other objects are not read. The column is not
refreshed by events; re-entering the folder reads it again.

### 6. Access and errors

The shared cache watches Secrets cluster-wide. Before relying on it, kc
probes once with a direct list (`limit=1`, `owner=helm`) in all namespaces,
and, when that is forbidden, in the panel's namespace. The folder then
shows one actionable row: either "Secrets can be listed in <ns> but not in
all namespaces" or "listing Secrets is forbidden", with the `helm list`
command to use instead and the API server's message on `F3`. Until the
probe has succeeded no informer is started, so a denied user does not get
a Secret informer that retries forever. Other notices: no releases (naming
the unsupported drivers), a revision that cannot be decoded (the row stays
and its history is still reachable), a revision no longer stored, and a
release that was uninstalled.

### 7. Read-only

kc does not install, upgrade, roll back, uninstall or test releases. These
change cluster state through Helm's own logic (hooks, three-way merges,
history pruning) and are separate follow-ups.

## Alternatives considered

- Import `helm.sh/helm/v3` (or v4) and use its storage driver: exact
  decoding and computed values for free, but it pulls in a very large
  dependency tree (the chart engine, OCI and registry clients, many
  Kubernetes modules pinned to Helm's versions) for a few struct fields.
  The record format has been stable since Helm 3.0.
- Shell out to an optional `helm` executable: needs the binary and a
  matching kubeconfig and identity, runs a process per view, and bypasses
  the shared cache, so lists cannot update from events.
- ConfigMap and SQL drivers: not supported in this version. ConfigMaps
  would need a second cluster-wide informer for a rare setup; SQL is outside
  the cluster. The empty-list notice says so.
- Computed values (`helm get values --all`): needs Helm's coalescing rules
  (null deletes, globals, subchart scopes); a simple merge would show
  values Helm would not use. Left out rather than shown wrong.
- `/helm` only under `/namespaces/<name>`: harder to find, and it would not
  follow the panel's namespace scope.
- Open the live object in the same panel: loses the place in the release;
  the issue asks for the opposite panel.
- Read the live state through the shared cache: one cluster-wide informer
  per kind in the chart, which a forbidden kind would also keep retrying.

## Consequences

- No new module dependency. If Helm changes its record format (a new
  `helm.sh/release.v2` type), kc shows nothing for those releases until
  `internal/helm` learns it.
- Browsing `/helm` keeps a cluster-wide Secret informer in the shared cache.
  The existing Secrets folder already does the same.
- A forbidden answer is kept for the life of the `/helm` folder; after
  access is granted, leave and re-enter `/helm`.
- Follow-ups: ConfigMap driver, computed values, rollback/uninstall through
  `models.ConfirmedAction`, and a release entry in the F9 menu once #48 lands.
