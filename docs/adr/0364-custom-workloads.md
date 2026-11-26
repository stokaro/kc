# 0364. Custom workloads in Info and the dashboard

- Status: Accepted
- Date: 2026-11-25
- Issue: #364
- Supersedes: in decision 3 of [0051](0051-cluster-overview-certificates-and-workloads.md),
  the depth of four steps and "owners of other kinds end the walk"

## Context

Info and the dashboard show a workload's pods for the six built-in
controllers only (`IsInfoWorkload`). The overview walks a pod's
controller references through those kinds, at most four steps (ADR 0051,
decision 3); an Argo Rollout, a Knative Revision or any other custom
controller ends the walk, so its pods belong to no workload and `C-x g`
on its row opens the namespace.

Issue #364 asks that a custom object that owns pods, directly or through
a ReplicaSet, show its pods in Info and open its dashboard; that the
desired count come from the scale subresource when the CRD declares one,
and that a kind without one say so; that an object scaled to 0 stay a
workload; that a kind that owns no pods keep the namespace scope; that a
recreated owner not get the old owner's pods; that informers start only
for the owner kinds met in pod chains; that the Prometheus history use
the owner series of kube-state-metrics, and say so when a chain cannot
be expressed; and that the walk reach Knative's depth and stay
cycle-safe. The health rules stay built in.

The constraints: the UI decides on the UI goroutine, without I/O, whether
the row under the cursor is a workload; the overview reads only the
shared caches of the cluster pool (AGENTS.md); real requests log an INFO
line; identity is the full GVR.

## Decision

1. **The walk follows any namespaced kind.** A reference to a built-in
   controller resolves as before. Any other kind resolves once the
   engine has mapped it (RESTMapper) to its preferred GVR and scope. A
   kind not mapped yet is recorded during the compute; the engine maps
   it afterwards and the next compute follows it. A cluster-scoped kind
   (a Node owns its mirror pods) or one that is not served ends the
   walk. The walk goes up to eight owners (Knative: ReplicaSet,
   Deployment, Revision, Configuration, Service) and stops at an owner it
   met before. The UID rule of 0051 applies to every kind: an owner whose
   list is read must be in it with the reference's UID, and one whose
   list cannot be read is trusted from the reference and ends the walk.
   A pod counts for every workload in its chain.

2. **Informers only for the kinds the walks meet.** Each mapped owner
   kind gets an informer from the overview's own caches (`GetInformer`,
   not a watch of its own), chosen by the same access decision as the
   overview's kinds (ADR 0182): the shared cache, the namespace cache of
   a namespace-only user, or none, with the reason, and its pods are then
   trusted from the references. The informer holds metadata only
   (`PartialObjectMetadata`), or whole objects when the kind has a scale
   path, because the desired count is a field of the object. A cluster
   with hundreds of CRDs gets informers for the few that own pods in
   scope. Stopping the engine removes its event handlers; the informers
   stay with the caches, as the overview's own do.

3. **The desired count comes from the scale subresource.** Discovery
   tells whether the kind's version serves `<resource>/scale`; the CRD's
   `subresources.scale.specReplicasPath` for that version tells where
   the objects keep the count (a GET of the CRD, logged at INFO). The
   answer is cached per cluster connection and GVR for five minutes and
   read again after a discovery refresh; an informer switches between
   metadata and whole objects when the path appears or goes. A kind
   without a scale subresource shows the ready pods only, with `no
   desired count: podsets.example.com declares no scale subresource`
   under the Pods bar. When kc cannot learn the path (a user who may not
   read CRDs) or the object has no whole number there, the line says
   `desired count unknown:` and why.

4. **Which rows are workloads.** `IsWorkloadTarget(key, gvr)` is true
   for a built-in controller, for a kind an overview engine of the
   cluster connection saw in a pod's chain, and for a namespaced kind
   whose scale subresource discovery lists, so a Rollout scaled to 0 is
   a workload. The overview engines record these kinds in a registry
   keyed by the cluster connection (the pool key without namespace and
   selector), which only grows while kc runs; Info and `C-x g` read it
   without I/O. When Info or the dashboard focus an object of a kind
   with a scale subresource that no walk met, the engine watches that
   kind too, so the object shows `scaled to 0`. A row of a kind that
   owns no pods and has no scale subresource keeps the namespace scope.
   The registry is keyed by the full GVR: a row of another version of
   the kind is not a workload.

5. **Names as kubectl prints them.** A custom workload is
   `rollouts.argoproj.io shop/web` in Info and `rollouts.argoproj.io
   web` in the dashboard, so a custom StatefulSet is not taken for the
   apps one.

6. **Prometheus through the owner series that reach the pods.**
   kube-state-metrics names an owner by `owner_kind` and `owner_name`,
   without the API group. The engines record, per custom kind, the kinds
   seen between a pod and the workload. A kind that owns its pods reads
   `kube_pod_owner{owner_kind="Rollout"}`; one with ReplicaSets or Jobs
   in between joins `kube_replicaset_owner` or `kube_job_owner`, as a
   Deployment and a CronJob do. kc refuses, before any request and with
   the reason, a kind whose pods it has not seen, a kind named like a
   built-in one (its series would match the built-in kind's pods), pods
   reached in more than one way, and longer chains (a Knative Service).
   Info shows the reason as the history's `unavailable:` line, and the
   dashboard plots the session with `no Prometheus history:` and the
   reason in the chart border.

7. **Health rules stay built in.** Custom workloads get the bars and
   the pod list; their health comes from the existing health adapters
   (ADR 0195). Events about a custom workload reach it once its kind is
   mapped.

## Alternatives considered

- **Watch every kind with a scale subresource from the start.** Simple,
  but it starts informers for every scalable CRD of a cluster, most of
  which own no pods in the namespace looked at.
- **Read `/scale` for each object.** It gives the count without the CRD
  and works for a user who cannot read CRDs, but it is one request per
  object per refresh, or a second cache with its own expiry. The CRD is
  read once per kind and the count then comes from the informer.
- **Assume `.spec.replicas` when the CRD cannot be read.** Right for
  most CRDs and wrong for the others; a wrong desired count reads as a
  rollout problem. kc says it does not know.
- **Ask the engine of the panel instead of a registry.** The dashboard
  and Info run separate engines, and `C-x g` decides before the
  dashboard's engine exists. A registry per connection serves both, at
  the cost of knowing a kind only after some engine of the connection
  computed.
- **Resolve owners with the owner folder's per-object GETs.** Real
  requests per pod on each compute, where the walk must read caches.
- **Prometheus by label selector or pod list.** kube-state-metrics has
  no series of a custom owner with its group; the owner series of the
  time are what ties a pod to a workload in 0197.

## Consequences

- Until an engine of the connection has computed, a custom kind is not
  a workload: with Info off and no dashboard open, `C-x g` on a Rollout
  opens the namespace. Info is the right panel's default mode, so its
  engine usually runs.
- While an owner kind's informer syncs, its pods are trusted from the
  references; the UID check applies once the list is read.
- A kind stays a workload after its last pod is gone, until kc exits.
- A namespace-only user who may not read CRDs sees the pods of a
  Rollout without its desired count, and the line says why.
- The overview carries the owner kinds in its input; other rules do not
  read them.
