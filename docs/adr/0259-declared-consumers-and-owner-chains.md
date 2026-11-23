# 0259. Declared consumers and operator owner chains

- Status: Accepted
- Date: 2026-11-23
- Issue: #259
- Amends: [0193](0193-requires-and-affected-workloads.md) (used-by reads
  pods only), [0195](0195-custom-resource-health.md) (a custom resource
  groups only the pods it owns directly)

## Context

Used-by on a ConfigMap, Secret, claim or ServiceAccount lists the pods
that use it and their controllers. A Deployment scaled to zero, or a
CronJob between runs, runs no pod and is missing, although the next
rollout or run will use the object. That is the case where someone
edits or deletes a ConfigMap because "nothing uses it".

The diagnosis of a custom resource with a `health:` entry counts the
pods it controls directly. Most operators own a Deployment or
StatefulSet instead, so the diagnosis said "0 owned" for a database
with three running pods.

## Decision

1. **Template consumers in used-by.** For a ConfigMap, Secret,
   PersistentVolumeClaim or ServiceAccount, used-by also reads the
   Deployments, StatefulSets, DaemonSets, Jobs and CronJobs of the
   namespace and lists those whose pod template names the object. It
   uses the same template readers as requires (`M-d`), so the two views
   cannot disagree. The relation says `pod template: <field>`; a
   running pod keeps its plain field (`volumes[cfg]`), and a controller
   edge keeps `controller of ...`. A workload found both ways is one
   row with both relations. Rows are merged by GVR, namespace, name and
   UID: a Deployment recreated under the same name is a second row, not
   a merge with the old instance's pods.

2. **Partial access.** Each source type is read on its own. The workload
   types the user may not list give one `(not listed)` row that names
   them and says the result is incomplete (a user who may read only
   pods would otherwise see five such rows), and the other types still
   answer. Any other read error fails the folder as before. When pods are denied
   the templates still answer, which is better than the empty result
   before.

3. **Owner chains for custom resources.** The diagnosis of a custom
   resource follows controller owner references down from the object's
   UID through the Deployments, ReplicaSets, StatefulSets, DaemonSets
   and Jobs of its namespace, at most four hops, and counts the pods of
   every object it reaches. Edges match by UID only, never by name or
   label; a UID is visited once, so a cycle ends. An `owner-chain`
   finding links every object on the chain, and an
   `owner-chain-partial` finding names the types that could not be
   read, because pods behind them are missing from the count.

4. **Reads.** All reads go through the shared cache and the per-type
   scope decision of [0056](0056-namespace-scoped-access.md). The
   used-by folder watches the five workload types in the target's
   namespace; the diagnosis of a custom resource watches the five chain
   types in its namespace. No cluster-wide watch is added: every target
   here is namespaced.

## Deviations from the issue

- **Info is not changed.** The issue asks to make the resolved group
  available to Info too. Info shows one object's fields and usage; the
  owner chain is a diagnosis question, and threading it into Info
  would add five list reads to every cursor move on a custom resource.
  The diagnosis row is one key away.
- **Bare ReplicaSets and custom templates are not scanned.** A
  Deployment's template already declares what its ReplicaSets do, and
  a custom resource's template has no common shape. Both are still
  found through their running pods.
- **No chain through custom intermediate types.** The walk follows
  built-in controllers only. Following an operator's own child CRs
  would need a list of every served type, which is exactly the
  inference from unknown semantics the issue rules out.

## Consequences

- `related_templates.go` (template consumers, relation merge) and
  `owner_chain.go` (bounded walk, partial findings) in models.
- Used-by on a ConfigMap costs five more cached list reads in its
  namespace.
