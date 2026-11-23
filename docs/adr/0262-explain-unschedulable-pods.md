# 0262. Explain why a pending pod cannot be scheduled

- Status: Accepted
- Date: 2026-11-23
- Issue: #262

## Context

The workload and pod `/diagnosis` reported `PodScheduled=False` with the
scheduler's reason and message and its events, then told the operator to
check selectors, affinity, taints and capacity by hand. The diagnosis
input had no nodes. Issue #262 asks for read-only checks of the
constraints kc can read (nodeSelector, required node affinity,
taints and tolerations, requests against allocatable, claim facts), each
with its evidence, kept apart from the scheduler's decision, with
incomplete results when a source cannot be read, and distinct scheduled,
gated and unschedulable states.

What the code has: diagnosis reads go through the related folder's
`scopedReader`, which resolves each (type, namespace) pair to the shared
cache, a namespace cache or a Forbidden error without a request
(ADR 0056). `podRequestsAndLimits` already computes effective requests
with `k8s.io/component-helpers/resource` (init containers, sidecars,
overhead, pod-level resources), as the overview capacity does.
`k8s.io/component-helpers/scheduling/corev1` (pinned v0.37.2) has the
scheduler's own taint/toleration and required node affinity matchers.

## Decision

1. **The scheduler's finding stays first.** `unschedulable/<pod>` keeps
   the condition reason and message and the `FailedScheduling` events;
   in a pod's diagnosis its next step says the node checks below explain
   it, in a workload's diagnosis it points to the pod's `/diagnosis`.
2. **Node checks only in the pod's own diagnosis, and only for an
   unschedulable pod** (`PodScheduled=False`, not gated, no node name,
   not terminal). A workload with many pending pods does not multiply
   the node reads.
3. **Checks, in the default scheduler's filter order** (NodeUnschedulable,
   TaintToleration, NodeAffinity, VolumeBinding/VolumeZone as far as
   bound PVs go, NodeResourcesFit):
   - cordoned node without a toleration for
     `node.kubernetes.io/unschedulable`;
   - first `NoSchedule`/`NoExecute` taint not tolerated, using
     `FindMatchingUntoleratedTaint` (comparison operators off, the
     default);
   - `nodeaffinity.GetRequiredNodeAffinity(pod).Match` for
     `spec.nodeSelector` and the required node affinity; a term kc cannot
     parse makes the result incomplete;
   - the required node affinity of the PVs of bound claims;
   - effective requests plus the requests of non-terminal pods bound to
     the node against `status.allocatable`, per requested resource, and
     the pod count. A node that reports no ephemeral storage is not
     checked for it. Usage is never used.
   Each node is counted under its first rejecting check, as the
   scheduler's "0/N nodes are available" message counts.
4. **Findings.** `sched/nodes` summarizes ("0 of 12 nodes pass (9 ...,
   3 insufficient cpu)") with the pod fields it read as evidence and up
   to five passing nodes; one `sched/<reason>` finding per reason cites
   up to five nodes with the exact taint, label mismatch, volume or
   numbers. `sched/unevaluated` always lists what kc does not check for
   this pod: another scheduler, pod (anti-)affinity, topology spread,
   host ports, dynamic resource claims, unbound claims' topology, the
   pod count when it was not read, preemption, volume count limits and
   pods assumed but not bound.
5. **Incomplete, never "no node fits".** Nodes that cannot be read stop
   the checks with an incomplete finding. Pods of all namespaces that
   cannot be read leave capacity unchecked and make the summary
   incomplete. A PV that cannot be read is reported and its affinity not
   checked.
6. **Gated pods.** A pod with `spec.schedulingGates` (or
   `PodScheduled=False` reason `SchedulingGated`) gets a `gated/<pod>`
   warning naming the gates and no node checks. Previously it was an
   error "cannot be scheduled".

## Reads and cluster load

The checks read nodes, and the pods of all namespaces when the pod
requests anything, from the shared cache through the scoped reader. A
user who cannot list them cluster-wide gets an incomplete finding and no
request. The pods informer of all namespaces is the one the cluster
overview already uses; it starts on demand when a pending pod's
diagnosis is opened by a user who may list pods everywhere. The folder
does not add watches for nodes or all pods: a refresh on every pod change
in the cluster would redraw the diagnosis constantly. The pod, its
events and claims still refresh it, and `C-r` rereads everything.

## Deviations from the issue

- **Node checks only in the pod's diagnosis,** not in every workload
  diagnosis, to keep the per-workload cost bounded.
- **Unbound claims are not evaluated.** Delayed binding
  (WaitForFirstConsumer) and provisioning topology need StorageClass and
  CSI topology semantics; they are listed as unevaluated. #268 covers
  storage explanations.
- **No preemption, inter-pod affinity, topology spread or host-port
  simulation.** Each needs scheduler state (nominated pods, other pods'
  labels by topology domain) kc would have to approximate. They are
  named as unevaluated instead of being half-checked.

## Alternatives considered

- **Running the scheduler framework in-process.** Accurate, but a large
  dependency and still blind to the scheduler's cache and plugin
  configuration; it would invite reading the result as the decision.
- **Per-node field-selector pod caches** (as the node's `/pods`
  folder). One cache per node does not scale to a capacity check over
  all nodes.
- **Listing every node as a row.** Unreadable on large clusters;
  grouping by reason with five examples keeps the evidence and the
  counts.

## Consequences

- Opening a pending pod's diagnosis can start the nodes and all-pods
  informers in the shared cache, which then stay for the cluster's
  lifetime as other shared informers do.
- The checks follow the default scheduler; `sched/unevaluated` says so
  for a pod with another `schedulerName`.
- Follow-ups: StorageClass `allowedTopologies` and WaitForFirstConsumer
  checks with #268; nominated node and preemption hints.
