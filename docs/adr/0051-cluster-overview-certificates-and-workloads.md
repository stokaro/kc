# 0051. Cluster overview: cert-manager Certificates and Info for one workload

- Status: Accepted; in decision 3, the depth of four steps and "owners of other kinds end the walk" superseded by 0364 (custom workloads)
- Date: 2026-11-22
- Issue: #51

## Context

`docs/adr/0051-cluster-overview.md` records the decisions of the first two
overview changes and lists two items it left open: the optional
cert-manager check of step 5 (Certificates not Ready or expiring within 14
days) and the Info panel mode for a single workload (bars for one
Deployment), which needs a grouping of pods by owner that the overview did
not keep. This record is a supplement to that one: it adds decisions and
changes none of the accepted ones. It has its own file because accepted
records are not edited (`docs/adr/README.md`).

Constraints stay those of the first record: objects come from the
overview's own pool entry of the shared controller-runtime cache, identity
is the full GVR, no I/O on the update goroutine, a source the cluster
cannot serve makes the rows that need it dim with the reason.

## Decision

### 1. Certificates are a Risks row read as unstructured objects

`/certificates not ready or expiring` (`risks/certificates`) lists
`certificates.cert-manager.io/v1` objects whose Ready condition is False
(failed), Unknown or missing (warning), or whose `status.notAfter` is less
than `overview.certificateExpiryDays` days away (warning) or past
(failed). The value of each listed certificate joins the reasons, for
example `Ready=False Issuing: <first line of the message>, expires in 3d`.
Targets are sorted by severity, then the soonest expiry. Enter lists them
as object rows (`cert-manager.io/v1/certificates/<ns>/<name>`), so F3, F4
and F8 work as in any list. F3 on the row explains the rule and shows a
`kubectl get certificates.cert-manager.io -o custom-columns=...` command
with the Ready status and notAfter, and `kubectl describe`.

The overview watches the kind as `unstructured.Unstructured` in its own
cache, next to the typed kinds, and reads the few fields it needs by path.
Only `cert-manager.io/v1` is read: it is the only served version since
cert-manager 1.6.

When the cluster does not serve the kind, `GetInformer` fails with a
no-match error and the row is dim with
`certificates.cert-manager.io: not served by this cluster`, like every
other unavailable source. F3 says that this means cert-manager is not
installed. The row is not hidden: a missing row reads as "nothing to
report", while a dim row says the check did not run.

The overview resolves its kinds when it starts. A cert-manager installed
while the overview is open shows after the overview is left and opened
again (one TTL, or a new scope).

### 2. `overview.certificateExpiryDays`, default 14

A flat integer key under `overview:` like the other thresholds, kept in
sync in `appconfig.DefaultOverview()`, `config-default.yaml` and the
README. Zero or negative takes the default (`Normalized`). Days, not a Go
duration: `14d` is not a valid duration, and `336h` reads worse.

cert-manager renews by default when two thirds of a certificate's life
have passed (30 days before the expiry of a 90-day certificate), so a
certificate inside 14 days of its expiry almost always means renewal
fails.

### 3. Pods are grouped by controller owner references

The overview groups the pods in scope that have not terminated by their
chain of controllers: it follows each pod's controller reference through
the kinds it knows (Deployment, ReplicaSet, StatefulSet, DaemonSet, Job,
CronJob, all `apps/v1` and `batch/v1`), at most four steps. A pod counts
for every workload in its chain: a Deployment's pod counts for its
ReplicaSet and the Deployment, a CronJob's for its Job and the CronJob.

An owner whose list the overview reads must be in that list with the
reference's UID; a recreated owner with the same name is not the owner,
and the walk stops. An owner whose list cannot be read (no permission) is
trusted from the reference, and the walk stops there. Owners of other
kinds (for example an Argo Rollout that owns ReplicaSets) end the walk.

The result is a map in the report keyed by `InfoWorkload` (GVR,
namespace, name) with pods, ready pods, desired replicas (Deployment and
StatefulSet `spec.replicas`, DaemonSet `status.desiredNumberScheduled`),
summed usage, requests and limits, and whether a pod has no limit. The
measured pods keep their chain, so the Info mode can list one workload's
pods by memory.

### 4. ReplicaSets as metadata only

A pod names its ReplicaSet; only the ReplicaSet names the Deployment. The
overview adds a cluster-wide metadata-only informer for ReplicaSets
(`metav1.PartialObjectMetadata`), which holds names, UIDs and owner
references and none of the pod templates. No rule needs more of a
ReplicaSet. Without permission to list ReplicaSets, the Info mode of a
Deployment says `pods cannot be grouped by Deployment: replicasets.apps:
<reason>`; the ReplicaSet itself still shows its pods.

### 5. Info for one workload

When the other panel's cursor is on an object of one of the kinds of
section 3, the Info mode shows that workload instead of the scope or the
node (`Target.Workload`, passed to `Summary` as `models.InfoFocus`):

- CPU and memory: the summed usage of its pods against their summed
  limits, flagged `near-limit` at `cpuLimitWarnPercent` /
  `memoryLimitWarnPercent`. When a pod has an app container without that
  limit, the limit sum is no ceiling: the bar then compares the usage with
  the requests and is `under-req` when the usage is at least
  `underRequestedFactor` times the requests. Without measured usage for
  every pod the bar shows the requests of the limits and the reason.
- Pods: ready pods of the desired replicas (`not ready` below), or of the
  running pods when the workload has no replica count (Jobs, CronJobs,
  ReplicaSets).
- The workload's pods by memory, at most five.

There is no disk bar: the workload has no filesystem of its own. The
sparkline history has its own key per workload (scope key, full GVR,
namespace and name), so moving the cursor between workloads keeps each
history.

The scope engine stays the same: the Info mode of a workload in a
namespace list reads the engine of that namespace, so moving the cursor
from a Deployment to the next one swaps nothing.

## Alternatives considered

- Hiding the certificate row when cert-manager is not installed. Cleaner
  on clusters without it, but it breaks the rule that an unread source is
  shown dim, and a user cannot tell "no problems" from "not checked".
- A typed cert-manager client (`github.com/cert-manager/cert-manager`). A
  large module with many dependencies for four fields.
- Watching only Certificates with a `Ready=False` reason through a field
  selector. A CRD only supports field selectors on the fields its
  `selectableFields` declare, and cert-manager declares none.
- Grouping pods by the workload's label selector, as `kubectl top pods -l`
  does. Selectors overlap (two Deployments with `app: web`), and a pod of
  an old ReplicaSet with other labels would be missed. Owner references
  are what the controllers and the garbage collector use.
- Mapping a ReplicaSet to its Deployment by name (`<deployment>-<pod
  template hash>`). Works for the common case but is wrong for adopted or
  renamed ReplicaSets and cannot check UIDs.
- Full ReplicaSet objects. Every Deployment keeps up to ten old
  ReplicaSets with their pod templates; metadata is enough.
- Any owner kind through the RESTMapper (CRD workloads). An owner
  reference names a kind, and mapping it needs discovery, which can mean
  I/O while the Info mode reads the summary on the update goroutine. The
  built-in kinds cover the issue; CRD workloads can follow with a mapping
  done in the background engine.
- Usage of the pods against the requests only. Limits are what OOM kills
  and throttling follow, so the bar compares with them when they bound
  the workload; the per-container near-limit risks stay in `/overview`.
- A Pod as an Info target. One pod has no replicas, and its containers
  are shown by `kubectl top pods --containers`; not asked for.

## Consequences

- Clusters without cert-manager show one more dim row in Risks.
- The overview holds one more cluster-wide informer (ReplicaSet metadata)
  and, with cert-manager, a Certificate informer while it is open.
- The Info mode follows the cursor onto workloads in any list of the
  other panel.
- An aggregate hides one pod at its limit next to an idle one; the
  `/pods near memory limit` and `/pods at cpu limit` rows still catch it.
- Follow-ups: CRD workloads as Info targets (owner kinds through the
  RESTMapper in the engine); pick up a cert-manager installed while the
  overview is open (re-resolve unavailable kinds on a discovery change).
