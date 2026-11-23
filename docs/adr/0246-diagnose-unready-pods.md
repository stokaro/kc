# 0246. Report unready running pods in workload diagnosis

- Status: Accepted
- Date: 2026-11-23
- Issue: #246

## Context

The issue reports that a running pod with `Ready=False` and a container
that reports `ready=false` gets "No problem found by the checks kc runs".
This is true on main: `podFindings` checks scheduling, container waiting
and termination states, restarts and claims, and nothing reads the
`Ready` condition or `containerStatuses[].ready`. A pod behind a Service
that gets no traffic was therefore shown as clean.

The issue asks for a readiness finding that cites the conditions and
containers without claiming a probe failure, keeps readiness gates apart
from container readiness, and treats starting and finished pods sensibly.

## Decision

`readinessFindings` (in `internal/models/workload_diagnosis_readiness.go`)
runs for every pod after the container checks:

1. It applies only to a pod in phase `Running`, without a deletion
   timestamp, whose `Ready` condition exists and is not `True`. Pending
   pods stay with the scheduling and container checks; succeeded and
   failed pods have no readiness; a terminating pod is expected to be
   unready.
2. A readiness gate whose condition is missing or not `True` produces
   `readiness-gate/<pod>`, citing each gate. Its next step points at the
   controller that owns the gate.
3. Containers that count for readiness (app containers and sidecars, that
   is init containers with `restartPolicy: Always`) with `ready=false`
   produce `unready/<pod>`, unless another finding already explains the
   container (crash loop, OOM kill, image pull, failed exit). The
   evidence is the `Ready` and `ContainersReady` conditions with reason
   and message, and for each container its state, `started=false` when
   set, and whether its spec has a readiness or startup probe. The
   pod's warning events are linked, so `Unhealthy` events with the
   kubelet's probe message appear as evidence when they exist. A `logs`
   row is offered per unready container, at most three.
4. When `Ready` is not `True` but every container is ready and no gate is
   pending, `unready/<pod>` says so and suggests the status may be stale
   (the node controller marks pods not ready when a node stops
   reporting).
5. Severity is `warning`. While the `Ready` condition is younger than
   `overview.startupGrace`, the finding is `info` and its title ends with
   `starting <age> (grace <grace>)`, the same shape the Deployment
   availability check uses.

The "no problem" detail now lists readiness among the checks.

## Alternatives considered

- Report readiness for every pod regardless of other findings. A crash
  looping container is always unready, so every crash loop would show
  two findings for one cause. Excluding explained containers keeps one
  finding per cause.
- Error severity. An unready pod loses traffic, but kc cannot tell a
  slow warm-up or a deliberate readiness flip (draining) from a fault.
  A warning, with the startup grace for young transitions, matches the
  replica-unavailable finding of the owning workload.
- Infer the cause from the probe spec ("readiness probe failing"). The
  status does not say a probe failed, only that the container is not
  ready; the probe's own message is in the `Unhealthy` event, which is
  linked when it exists.
- Also report a `Pending` pod stuck in `ContainerCreating`. It is a real
  gap but a different check (it needs volume and sandbox events); it
  belongs with #268.

## Consequences

- An unready pod is no longer reported as clean. The owning Deployment's
  `unavailable` finding now gets the "pod problems below" hint when its
  pods are only unready.
- Diagnosis output grows by one finding per unready pod, still bounded by
  `maxDiagnosedPods`.
- Follow-up: pods stuck in `Pending` after scheduling (#268).
