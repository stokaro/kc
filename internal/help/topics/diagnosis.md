# Diagnosis

`/diagnosis` is a row inside a Service, a Pod, a Deployment, ReplicaSet,
StatefulSet, DaemonSet or Job, and inside a namespaced custom resource
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

A running pod whose `Ready` condition is not `True` gets a finding. It
names the containers that are not ready (sidecars too) and cites the
`Ready` and `ContainersReady` conditions and the warning events, such as
`Unhealthy`; kc does not run the probes, so it never claims one failed.
A pending readiness gate is reported apart from the containers, since
another controller sets it. A pod that became unready less than
`overview.startupGrace` ago is `starting`; completed, failed and
terminating pods get no readiness finding.

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
