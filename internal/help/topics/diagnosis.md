# Diagnosis

`/diagnosis` is a row inside a Service, a Pod, a Deployment, ReplicaSet,
StatefulSet, DaemonSet, Job or HorizontalPodAutoscaler (`autoscaling/v2`),
an Ingress, a Gateway API HTTPRoute, a Flux Kustomization or HelmRelease,
an Argo CD Application, and inside a namespaced custom resource that has
an entry in the `health:` configuration. It collects what the
object, its pods, their containers, claims and events say about a
problem, with the evidence for each finding. Use it when a row is red
and you want to know why without reading five describes.

## How to read it

The folder starts with summary rows, then the findings, then a dim note
on what was not checked. Each finding is one row with its severity:
`error`, `warning`, `info`, or `incomplete` when a source could not be
read. Its title says when it was observed. Under it follow:

- the objects it cites, with the exact status field or event as the
  evidence,
- for a container failure, `log` rows for that container of that pod:
  the current log, and the previous one after a restart,
- a `maybe` row with a possible explanation. It is a correlation by
  owner, not a confirmed cause,
- a `next` row that says what to inspect.

In a workload, kc runs the same checks on every pod and groups equal
symptoms: the same severity, the same check and the same title apart
from the pod's name. A group shows the first pod's evidence, says
`(15 pods)` in its title and cites up to 20 of the pods; when their
messages differ, it says so. A group means the pods show the same
symptom, not that they share a cause. Findings are ranked by severity,
then by how many pods share them, then by the first pod's name, so
identical reads give the same order and an error of one pod is never
hidden behind a warning many pods share. The ten highest-ranked symptoms
are expanded; `more-symptoms` lists the rest, each with a link to its
first pod. Findings about unreadable sources stay.

The evidence gets the rest of the line. In a narrow panel it is cut
first, down to 16 columns, and only then the name, resource and namespace
columns. The bottom status line shows the full text of the row under the
cursor.

In a Service, the summary shows the type and IP families, selector,
selected and ready pods, endpoint counts and each port with its
targetPort. Findings cover:

- no pod matches the selector, with pods that share a selector key,
- selected pods that are not ready,
- a named targetPort that does not resolve on some pods,
- pod IPs no EndpointSlice lists, stale endpoints, a missing IP family.

Every backend pod and EndpointSlice follows the findings. ExternalName,
headless and selectorless Services get an explanation, not a failure.

In a workload or pod, findings cover a rollout that does not progress,
a replica failure, unavailable replicas, a failed Job, an unschedulable
pod with the scheduler's message, image pull and start errors, an OOM
kill, a crash loop or a restart in the last hour, a failed exit (init
containers too), and a claim that is missing or not Bound (the pod's
own diagnosis explains the claim further, see Storage below). A Deployment
whose availability is younger than `overview.startupGrace` is `starting`.

An OOM kill is an `error` while the container is terminated by it or
crash loops after it. When the container runs again, the kill is a
`warning` for the first hour and `info` after that: history that shows
the memory was once too small, with the time of the kill. The `next` row
cites `limits.memory` when the container has one; without it, the kill
came from a pod-level limit or from memory pressure on the node.

A running pod whose `Ready` condition is not `True` gets a finding. It
names the containers that are not ready (sidecars too) and cites the
`Ready` and `ContainersReady` conditions and the warning events, such as
`Unhealthy`; kc does not run the probes, so it never claims one failed.
A pending readiness gate is reported apart from the containers, since
another controller sets it. A pod that became unready less than
`overview.startupGrace` ago is `starting`; completed, failed and
terminating pods get no readiness finding.

## Ingresses and HTTPRoutes

The routing diagnosis follows the path a route declares, from its class
or parent Gateways to its backend Services. The summary shows each rule
(host, path and match type, backends with port and weight), the
IngressClass or each parent with the listeners that accept the route,
the Gateway's `Accepted` and `Programmed` conditions and the route's
own status per parent. Conditions keep their reason, transition time
and the generation they describe.

Findings cover:

- an IngressClass that does not exist, no class and no default class,
  or the deprecated class annotation,
- a backend Service that does not exist, or a port it does not expose,
- a parent Gateway that does not exist, a `sectionName` or `port` that
  names no listener, and listeners that refuse the route: protocol,
  `allowedRoutes` kinds and namespaces (`Same`, `All`, `Selector`) and
  hostnames that do not intersect,
- `Accepted`, `ResolvedRefs` or `Programmed` that are `False` or
  `Unknown`, or describe an older generation, and a parent with no
  status from any controller,
- a reference to another namespace without a ReferenceGrant there,
- a TLS Secret that does not exist (only its metadata is read, never
  its data), a TLS host without a rule, and no load balancer address.

Resource backends, non-Service backendRefs, non-Gateway parents (mesh)
and extension filters are listed as `info`, not checked. Services,
Gateways, ReferenceGrants or namespaces you may not read, and Gateway
API types the cluster does not serve, give `incomplete` findings,
never a missing object.

`Enter` on a backend Service opens its Service diagnosis.

## A pod that cannot be scheduled

The `PodScheduled=False` condition, its message and the scheduler's
events (`FailedScheduling`) stay the first finding: they are the
scheduler's decision. In the pod's own `/diagnosis`, kc then runs its
own read-only checks against every node, in the order of the default
scheduler's filters, and counts each node under the first check that
rejects it:

- a cordoned node (`spec.unschedulable`) without a toleration for it,
- a `NoSchedule` or `NoExecute` taint the pod does not tolerate,
- `spec.nodeSelector` and the required node affinity,
- the node affinity of the PersistentVolumes of bound claims,
- the pod's effective requests (the larger of the init containers and
  the app containers, plus overhead) plus the requests of the pods bound
  to the node, against the node's allocatable, and the pod count.

`sched/nodes` sums it up, for example `kc's node checks: 0 of 12 nodes
pass (9 no match for spec.nodeSelector ..., 3 insufficient cpu)`; one
finding per reason follows, citing up to five nodes with the evidence
(`taint gpu=true:NoSchedule not tolerated`, `allocatable cpu 4,
requested by 7 bound pods 3500m, this pod 1`). Capacity uses requests,
never measured usage. When nodes pass every check kc runs, the scheduler
rejected them for a reason kc does not check, and the `sched/unevaluated`
finding lists those: pod affinity and anti-affinity, topology spread,
host ports, dynamic resource claims, the topology of unbound claims,
another scheduler, preemption, volume count limits and pods the
scheduler has assumed but not yet bound.

If kc may not list nodes, the pods of all namespaces or a volume, the
check is `incomplete`, never "no node fits". The pods of all namespaces
are read only when the pod requests resources. These reads come from the
shared cache and do not refresh the folder on their own; `C-r` reads
them again. A workload's diagnosis names the pending pod and points to
its `/diagnosis` for the node checks.

A pod with `spec.schedulingGates` is reported as waiting for its gates
(a warning), not as unschedulable, and gets no node checks.

## A HorizontalPodAutoscaler

The summary shows the target and min, current, desired and max replicas,
and the last scale time. The findings explain the current state:

- `hpa/at-max` and `hpa/at-min`: the desired count is at a bound.
- `hpa/condition/...`: the controller's own `AbleToScale`, `ScalingActive`
  and `ScalingLimited` reasons and messages, for example
  `FailedGetResourceMetric` (the metrics API did not answer),
  `ScalingDisabled` (the target has 0 replicas), `TooManyReplicas`, or
  `ScaleDownStabilized` and the backoff reasons. These are what the
  controller reports; kc does not show a countdown, since the API does
  not expose the controller's recommendation history.
- `hpa/metric/N`: each `spec.metrics` entry against its value in
  `status.currentMetrics`, with the ratio and whether it asks for more or
  fewer replicas. A metric without a reported value is a warning, never
  zero. Several metrics stay separate findings.
- `hpa/requests/N`: a CPU or memory `Utilization` target needs a request
  on every container it counts; the finding names the containers of the
  target's pod template without one.
- `hpa/target`: the target does not exist (error) or cannot be read
  (incomplete).
- `hpa/stale`, `hpa/no-status`: the status describes an older
  generation, or the controller has not reported at all.
- `hpa/behavior`: the configured scale-up and scale-down windows and
  policies, with the API defaults for omitted fields.

kc reads the HPA, its target and the namespace's events from the cache.
It does not query the metrics APIs and changes nothing.

## Storage: a claim or a pod that uses one

A PersistentVolumeClaim has a `/diagnosis` row too. It and a pod's own
`/diagnosis` follow each claim the pod uses through its StorageClass,
its PersistentVolume, the CSIDriver and the VolumeAttachment on the
pod's node, and tell the stages apart:

- `storage/.../wait-for-consumer` (info): the class binds with
  `WaitForFirstConsumer` and the pod is not scheduled yet. Waiting is
  expected; the scheduling findings explain a pod that stays Pending.
- `storage/.../provisioning` (warning): the provisioner has not created
  a volume yet; `provisioningfailed`, `failedbinding` and the like
  (error) quote the claim's own warning event.
- `storage/.../class-missing`, `static`, `no-class`: the class does not
  exist, the claim asks for a pre-created volume (`storageClassName: ""`)
  and none matches, or the claim names no class.
- `storage/.../pv-missing`, `pv-failed`, `lost`: the bound volume does
  not exist, is `Failed`, or the claim is `Lost`.
- `storage/.../attach-error` (error): the VolumeAttachment's
  `status.attachError`, with its time; `attaching` (info) while it is
  not attached yet; `no-attachment` (info) when there is none and the
  driver needs one. A driver whose CSIDriver says `attachRequired: false`
  needs no attachment, and kc says nothing.
- `volume/<pod>/failedmount`, `failedattachvolume`, `failedmapvolume`
  (error): the kubelet's or the attach/detach controller's event for a
  pod that is Pending on its node. A workload's diagnosis shows these
  too.

Bound does not mean mounted: kc reports the mount events it finds and
does not inspect the node, the filesystem, the provisioner or the CSI
driver. A StorageClass, volume, CSIDriver or VolumeAttachments kc may
not read make the stage `incomplete`, never missing. VolumeAttachments
are listed only for a pod on a node with a CSI volume.

## Keys

- `Enter` on a cited object opens its own folder; `/..` comes back.
- `F3` on a cited object shows its YAML.
- `F3` on a `log` row opens the logs viewer for that container.
- `M-j`, `M-b` and `M-d` work on cited objects.

## Flux and Argo CD

In a Flux Kustomization or HelmRelease the summary shows the path, prune
setting, inventory size, the applied and attempted revisions, the Ready
condition with reason and time, the source with the revision it offers,
and the object that manages this one. Findings say when:

- reconciliation is suspended,
- the status is for an older generation of the spec (`stale-status`),
- Ready is not True, or Stalled is True, with the controller's message,
- a newer revision was attempted and not applied,
- the source is missing, not ready, not readable, or offers a revision
  other than the applied one,
- a `spec.dependsOn` target is missing or not ready.

The source and the dependencies that are ready are listed at the end;
Enter opens them.

In an Argo CD Application the summary shows the sources, sync status
and revision with the time Argo CD compared them, health, and the count
of resources out of sync. Findings report OutOfSync, unhealthy, a failed
last sync and the Application's conditions. OutOfSync is Argo CD's own
comparison, shown with its time.

kc does not fetch sources or render manifests, so it never says "in
sync" by itself. A Ready condition means the last apply succeeded, not
that the live objects match Git. To compare one object with its desired
state, render it yourself (`kustomize build`, `helm template`), open
the file in a local-files panel (`M-l`) and press `C-x C-d` with the
live object in the other panel ([Compare objects](compare-objects.md)).

## Limits

- Only API objects are read. A clean result does not prove that DNS,
  the network, probes, nodes or the application work. A routing
  diagnosis does not test DNS, TLS handshakes or reachability, and does
  not read controller-specific annotations or policies. To check what
  NetworkPolicies say about one client pod and one server pod, put them
  under the cursors of the two panels and press `C-x n`
  ([NetworkPolicy between two pods](network-policy.md)).
- Pods are matched by controller owner UID (through the ReplicaSets of
  a Deployment), events by UID. For a custom resource kc follows
  controller references through the Deployments, ReplicaSets,
  StatefulSets, DaemonSets and Jobs of its namespace, at most four
  hops; a type you may not list makes the chain partial, and the
  diagnosis says so. A recreated object with the same name
  does not inherit the old one's pods or events.
- Ten symptom groups are expanded, each citing up to 20 pods; the rest
  are listed with one pod each.
- Data you may not read is shown as unavailable, never as absent.
- CronJobs have no diagnosis; look at their Jobs.

## See also

- [Cluster overview](overview.md)
- [Timeline](timeline.md)
- [Owners, used by, requires](related.md)
- [Logs viewer](logs-viewer.md)
