# 0039. Owners and used-by folders

- Status: Accepted
- Date: 2026-11-22
- Issue: #39

## Context

kc only goes down a hierarchy (Deployment -> ReplicaSet -> Pod, registered
with `models.RegisterChild`). Nothing goes up to an object's owner or
sideways to the objects that use it. k9s has `Shift-J` for the owner and `u`
for "used by". The issue proposes an owner action and a `/used-by` folder for
Secrets, ConfigMaps, PVCs and ServiceAccounts, built from the shared cache,
and leaves open which keys, which relations, where results open, what it may
cost, and whether CRDs get user-defined jumps.

Constraints from AGENTS.md: data comes from the shared controller-runtime
cache only (no ad-hoc lists or watches), identity uses the full GVR, and API
work stays off the Bubble Tea update goroutine.

## Decision

### 1. Keys

- `M-j` opens the owners of the selected object, `M-b` the objects that use
  it ("used by"). `Esc j` and `Esc b` are the fallbacks where Option does not
  act as Meta, like `Esc c` for `M-c`.
- Letters alone go to the shell line, as in mc, so a plain `j` or `u` is not
  available. `M-j` echoes k9s' `Shift-J` (jump to owner). k9s' `u` would map
  to `M-u`, but open PR #90 takes `M-u` for history forward, so used-by takes
  `M-b`. Checked free on main and in the open PRs on 2026-11-22: taken are
  `M-c`, `M-a`, `M-s`, `M-o`, `M-r`, `M-y`, `M-u`, `M-H`, `M-f`, `M-p`, `M-w`
  and their `Esc` fallbacks.
- On a row that is not an object (a resource type, a container) the keys show
  a short notice instead.

### 2. Results open as a virtual folder in the same panel

Both keys push a folder under the object: `<object path>/owners` and
`<object path>/used-by`. `/..` returns to the object with the cursor where it
was, as for any folder. The breadcrumb stays correct because the folder path
extends the object's path.

- The owners folder lists every owner reference, the controller first, and
  the cursor starts on it. With one owner this is a jump to the owner; with
  several the user picks one. A single folder shape for both cases keeps
  "go back" and the breadcrumb uniform.
- Rows behave like the same object in its own list: Enter opens its child
  folder when the type has one (a ReplicaSet's pods, a Pod's containers), F3
  shows its YAML. They are read-only: kc does not read discovery verbs for
  them (discovery is invalidated every 30 s and would be a request on every
  open), so F4 and F8 stay off. Edit or delete from the object's own list.
- Owners that cannot be opened are listed dim with the reason: a kind the
  cluster does not serve, an owner that is gone, or an object of the same
  name with another UID (the garbage collector treats that as gone too), or a
  namespaced owner of a cluster-scoped object.

The issue suggested opening the owner in the other panel, like mc's `M-o`.
The same panel was chosen so the other panel keeps what the user had there
and the way back is one `/..` away. kc has no Esc-to-parent (Esc starts the
`Esc <key>` sequences), so `/..` is the return path, as in mc. Opening in the
other panel can be added later as a separate key.

### 3. Owner resolution

Owner references carry apiVersion and kind. kc maps them to a GVR with the
cluster's RESTMapper (`RESTMapping(groupKind, version)`), so CRD owners work
without registration. A namespaced owner is in the dependent's namespace; a
cluster-scoped owner has none. Row IDs are
`<group/version>/<resource>/<namespace>/<name>`, because one folder mixes
types and namespaces.

### 4. Supported "used by" relations

Registered with `models.RegisterUsedBy(gvr, UsedByFinder)`, next to
`RegisterChild`:

| Object | Used by | Through |
|---|---|---|
| ConfigMap | Pods | volumes, projected volumes, env, envFrom (init, regular and ephemeral containers) |
| Secret | Pods | the same, plus imagePullSecrets |
| PersistentVolumeClaim | Pods | volumes, and generic ephemeral volumes (claim `<pod>-<volume>`) |
| ServiceAccount | Pods | serviceAccountName (or the deprecated serviceAccount) |
| Pod | Services | the Service selector matches the pod labels; a Service without selector selects nothing |
| Service | Ingresses (networking.k8s.io/v1) | default backend and rule backends |
| PersistentVolume | PersistentVolumeClaim | the volume's claimRef, only when the claim names the volume back and has the claimRef UID |

Other types show "kc does not know what uses ...". Pod templates of
workloads (Deployments and so on), Secrets of ServiceAccounts, Ingress TLS
secrets and CSI secret references are not covered yet.

### 5. Data source and cost

- Lookups read the shared cache of the context (`Cluster.ListByGVR` and
  `GetByGVR`, both informer-backed) and filter by the object's namespace in
  memory. A folder that starts from a selector-scoped cache (the pods of a
  node) switches to the unscoped shared cluster first, because that cache
  sees only part of a type. No namespace-scoped cluster is created, which
  would be a second cache.
- One open costs one pass over the source type in the namespace (for example
  every pod in the namespace for a ConfigMap), O(n) in memory, and at most one
  informer per source type, the same informer the type's own list uses. The
  PersistentVolume case reads two objects by name.
- The folder watches the source types in the namespace (owners: the object
  itself), so it follows changes, and stops the handlers when it is closed,
  like other object folders.
- The folder is built in a `tea.Cmd`; the first load happens there, so the UI
  never waits on an informer sync.

### 6. Cursor placement and #40

`openRelated` loads the rows before the folder opens, then
`selectInOpenedFolder` moves the cursor to the first row with one
`SelectByRowID` call. That is the only place that needs rows to exist. When
the waiting navigate-to-path routine from #40 lands in `internal/ui`, this
call can switch to it without touching the rest.

### 7. User-defined jumps for CRDs

Not in this change. CRD owners already work through the RESTMapper. A
user-defined used-by relation (a config file with a source GVR and a field
path) fits the `RegisterUsedBy` hook, which is the public registration point
TODO.md asks for; it needs its own design for the config format.

## Alternatives considered

- **An owner row inside the object's child folder** (the issue's first
  idea). Child folders exist only for some types, and a Pod's child folder
  lists containers; mixing an owner row in would change every existing child
  folder. A key works on every object.
- **Jumping into the owner's own list** (`/replicasets` with the cursor on
  the owner). The breadcrumb would show a path the user did not walk, and the
  list may load late. The owners folder needs neither.
- **Opening results in the other panel.** Overwrites the other panel; see
  decision 2.
- **Server-side lookups** (field selectors or LIST per open). AGENTS.md
  forbids ad-hoc lists; the cache already holds the data.
- **A namespace-scoped cluster per lookup** (`Deps.ForNamespace`). It would
  start a second set of informers next to the shared cache.
- **Edit and delete on result rows.** Needs discovery verbs per row; left for
  later.

## Consequences

- Owners work for any kind the cluster serves, including CRDs.
- Used-by covers the common core relations; more can be registered in
  `internal/models/related_refs.go` without UI changes.
- On large namespaces a used-by open scans the namespace's pods in memory.
  This is the same data the pod list holds, and no request goes to the API
  server once the informer runs.
- Opening used-by of a ConfigMap starts the pods informer if no pod list was
  open before, with the memory that costs, as opening `/pods` does.
- Follow-ups: workload pod templates as users, ServiceAccount and Ingress TLS
  secrets, verbs on result rows, user-defined relations for CRDs, and the
  switch to the #40 navigation routine.
