# 0265. Explain why an HPA is or is not scaling

- Status: Accepted
- Date: 2026-11-23
- Issue: #265

## Context

The overview flags HPAs at `maxReplicas` or with `ScalingActive=False`
(`hpasRule` in `internal/models/overview_rules.go`). The workload
diagnosis does not cover HPAs. Issue #265 asks for a per-object
explanation of an `autoscaling/v2` HPA: the replica numbers and bounds,
the controller's conditions, configured against reported metrics,
missing requests for resource metrics, target access denial, stale
observations and the configured behavior, without inventing the
controller's recommendation history or a countdown, and without adding a
dashboard or changing anything.

## Decision

1. **`/diagnosis` inside HPAs** of `autoscaling/v2`, built on the
   existing diagnosis folder (`renderDiagnosis`, scoped reads, cited
   objects that open). No new renderer or cache.
2. **Reads.** The HPA; the scale target, mapped from
   `scaleTargetRef.apiVersion`/`kind` through the RESTMapper and read
   from the cache through the scoped reader; the events of the
   namespace. The folder watches the HPA and the events. The target is
   read again on each refresh but not watched: the HPA's status changes
   when the target's scale does.
3. **Findings**, each naming its field:
   - `hpa/at-max` (warning) and `hpa/at-min` (info) from
     `status.desiredReplicas` and the bounds;
   - `hpa/condition/<type>` for `AbleToScale=False` (error),
     `ScalingActive=False` (error, or info for `ScalingDisabled`),
     `ScalingLimited=True` (warning), and the controller-reported
     `ScaleDownStabilized` and `Backoff*` reasons (info). Messages are
     kept as the controller wrote them, with the HPA's warning events.
     The text says these are reported states and that the API exposes no
     countdown;
   - `hpa/metric/<i>` for each `spec.metrics` entry, matched with
     `status.currentMetrics` by type, resource, container, metric name,
     selector and described object. A missing value is a warning
     ("no current value reported"), never zero. The ratio and the
     direction (asks for more or fewer replicas) are shown; the default
     tolerance is mentioned, not applied;
   - `hpa/requests/<i>` for `Utilization` targets of `Resource` and
     `ContainerResource` metrics: containers of the target's
     `spec.template.spec` without the request, or a `ContainerResource`
     container that does not exist;
   - `hpa/target` (error when not found, incomplete when unreadable),
     `hpa/target-unknown` when the kind is not served,
     `hpa/requests-unknown` when the target has no pod template kc can
     read;
   - `hpa/stale` (`status.observedGeneration` behind
     `metadata.generation`) and `hpa/no-status`;
   - `hpa/behavior`: the configured windows, select policy, policies and
     tolerance, with the API defaults for omitted fields marked
     `(default)`.

## Deviations from the issue

- **No metrics API queries.** kc shows what the HPA status reports and
  does not ask metrics-server or adapters itself. A second source would
  disagree with the controller at times and invite reading kc's number
  as the controller's input. Unavailable metrics are visible through the
  missing status value, the `ScalingActive` reason and the events.
- **No recommendation or replica prediction.** The controller's formula
  depends on per-pod readiness, missing-metric handling and the
  stabilization history; kc shows the ratio and the configuration only.
- **Only `autoscaling/v2`.** v1 is a subset served from the same objects;
  the panel lists the preferred version.

## Alternatives considered

- **Extending the overview rule.** It answers "which HPAs", not "why this
  one"; the per-object folder holds the evidence.
- **Watching the scale target.** It would refresh the folder on every
  status change of a busy Deployment; the HPA status already moves with
  the target's scale.

## Consequences

- A user who may not read the target kind gets an incomplete finding
  and the HPA-only findings.
- Follow-ups: link the target's `/diagnosis` from `hpa/at-max`;
  autoscaling/v1 if users list HPAs in v1 explicitly.
