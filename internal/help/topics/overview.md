# Cluster Overview

`/overview` is the first row of every panel root. It shows one row per
check about the panel's context and namespace scope: node capacity,
broken pods and workloads, risky settings and recent warnings. Use it as
the first stop when you are called about a cluster and want to know
where to look.

`M-i` opens it from any list, for the namespace of that list (all
namespaces for a list across namespaces). `kc overview` starts kc there;
add `-n <namespace>` or `-A`.

## How to read it

The first row is `/attention`, the count of the whole report. Then come
four sections, each with a header that names its scope and its counts:

- Capacity: CPU, memory, pods and disk per node. Always the whole
  cluster (`Capacity · cluster`).
- Health: nodes not ready, under pressure or cordoned; pods not ready,
  pending, OOMKilled or restarting; workloads unavailable or rolling
  out; failed Jobs and CronJobs; claims and volumes in a bad phase.
- Risks: PDBs that block eviction, pods without requests or memory
  limit, containers near their limits, claims near full, single
  replicas without a PDB, quotas, HPAs and cert-manager Certificates.
- Activity: warning events and pod restarts inside `overview.window`.

The other sections follow the namespace scope (`Health · namespace
shop`). A node or volume check there says `(cluster)`.

Each row has a name, a value and a state. The color is that of object
lists: normal is ok, the not-ready color a warning, the failed color
failed. A dim row means kc could not read the source; `F3` gives the
reason. `n/a` with the value `none` means there was nothing to check,
which is not a pass. `(partial)` means some of the types could not be
read.

A check that needs an add-on is `n/a` while the cluster does not serve
its type, for example `cert-manager is not installed` for the
Certificate check. kc notices an add-on that is installed or removed
while it runs at the next discovery refresh
(`kubernetes.discovery.refresh`, 30s), or at once after `C-r`. When an
add-on is removed, its objects go with it: they are not listed as
recovered.

A capacity row takes the state of its worst node, for example
`node full, 1 of 2 nodes`. Its bases are those of the Info panel:
`pods 22% req 25% lim 116% of alloc, node 40% of cap`. A node is
`under-req` when its pods use well above their requests.

Targets change over time. A pod or Deployment that is still coming up
is `starting` for `overview.startupGrace` (5m) and is not colored.
Later the value says how long the state has lasted (`for 12m`), or
`seen 3m this session` when the API server gives no date. A target that
went away is listed as `recovered` for ten minutes. When a source cannot
be read, its last targets stay listed as unknown: a gap is never a
recovery. A recreated object (new UID) starts over.

Checks that found nothing fold into a `/N quiet checks` row at the end
of their section.

## Keys

- `M-i` (or `Esc i`) opens the overview for the active list.
- `Enter` on a row lists exactly the objects behind it, with the reason
  for each. `/..` goes back.
- `Enter` on a warning event opens the object it is about in the other
  panel. An event names its object by UID. When the object was deleted
  and a newer one has the same name, kc opens the list but does not
  select the newer object, and says that it was replaced. Move the
  cursor to the row to look at the newer object.
- `F3` on a row explains how the value is computed, its scope, and the
  kubectl commands that show the inputs.
- `M-c` copies all input commands of the row in focus. The command line
  reads `Inputs (N):` because no single command gives the value.
- `C-z` keeps only the rows in a fault state.
- `C-r` recomputes at once and reads API discovery again, so a type
  installed or removed since counts without waiting for the refresh.
  `C-r` in the Info panel does the same for its summary.

## Limits

- The overview reads its own cache: cluster-wide for each type you may
  list and watch everywhere, else only the namespace. Node capacity
  needs every node and every pod, so it is `n/a` with namespace access.
- Usage needs metrics-server; disk and claim use need get on
  `nodes/proxy`. The stats are skipped above `overview.statsMaxNodes`.
- It recomputes at most once a second and stops when nothing shows it
  for `kubernetes.clusters.ttl`. Thresholds are under `overview:` in
  the configuration.

## See also

- [Attention](attention.md)
- [Quiet checks](quiet-checks.md)
- [Info mode](mode-info.md)
- [Diagnosis](diagnosis.md)
- [Access](access.md)
