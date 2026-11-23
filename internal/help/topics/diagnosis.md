# Diagnosis

`/diagnosis` is a row inside a Service, a Pod, a Deployment, ReplicaSet,
StatefulSet, DaemonSet, Job or HorizontalPodAutoscaler (`autoscaling/v2`),
and inside a namespaced custom resource
that has an entry in the `health:` configuration. It collects what the
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

In a narrow panel the name, resource and namespace columns are cut first
and the evidence gets the rest of the line. The bottom status line shows
the full text of the row under the cursor.

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
containers too), and a claim that is missing or not Bound. A Deployment
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

## Keys

- `Enter` on a cited object opens its own folder; `/..` comes back.
- `F3` on a cited object shows its YAML.
- `F3` on a `log` row opens the logs viewer for that container.
- `M-j`, `M-b` and `M-d` work on cited objects.

## Limits

- Only API objects are read. A clean result does not prove that DNS,
  the network, probes, nodes or the application work.
- Pods are matched by controller owner UID (through the ReplicaSets of
  a Deployment), events by UID. A recreated object with the same name
  does not inherit the old one's pods or events.
- Up to ten pods with problems are explained; the rest are counted.
- Data you may not read is shown as unavailable, never as absent.
- CronJobs have no diagnosis; look at their Jobs.

## See also

- [Cluster overview](overview.md)
- [Timeline](timeline.md)
- [Owners, used by, requires](related.md)
- [Logs viewer](logs-viewer.md)
