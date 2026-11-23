# 0269. Drain impact and PodDisruptionBudget refusals

- Status: Accepted
- Date: 2026-11-23
- Issue: #269

## Context

A drain (`internal/nodeaction`) already preflights the pods of the
nodes with kubectl's rules and evicts through the Eviction API. The
confirmation said only that the API "respects PodDisruptionBudgets", and
the progress dialog listed refused pods under "usually by a
PodDisruptionBudget", whatever the 429 was. Issue #269 asks to show the
affected workloads and the budgets before the drain, to keep the API's
answer for each refusal and connect it to the budget, and to make
multiple budgets, stale status and unreadable budgets explicit, without
inferring that every 429 is a budget and without promising a successful
drain.

## Decision

1. **Impact in `PrepareDrain`.** After the existing selection, kc groups
   the pods it plans to evict by controller and matches them with the
   PodDisruptionBudgets of their namespaces, the way the Eviction API
   does in kube-apiserver:
   - a budget covers a pod of its namespace whose labels its selector
     matches; a missing, empty or invalid selector covers nothing (the
     disruption controller counts every pod for an empty selector, but
     the Eviction API ignores such a budget, and the eviction is what the
     drain does);
   - pending, succeeded, failed and terminating pods bypass budgets and
     are counted apart;
   - a pod with more than one budget is a warning: the Eviction API
     refuses it with an error and the drain stops there;
   - a budget whose `status.observedGeneration` is behind its generation
     is marked stale: the API refuses its pods until the controller
     catches up.
2. **Budget lines** show the status as read (`disruptionsAllowed`,
   `currentHealthy`, `desiredHealthy`, `expectedPods`), the covered and
   not-ready pods, `unhealthyPodEvictionPolicy` (IfHealthyBudget when
   unset), and an estimate of how many evictions wait for replacements:
   covered pods that need the allowance (a not-ready pod does not, with
   AlwaysAllow or while the budget is met) beyond `disruptionsAllowed`,
   or all of them while the status is stale. The dialog says "as read
   now".
3. **Reads.** `nodeaction.Env.Budgets` lists a namespace's budgets.
   `models.NodeActionTarget` backs it with `resolveListScope`, the scope
   a PodDisruptionBudget folder would use: the shared cache, a namespace
   cache, or a Forbidden error without a request. Each namespace is read
   once per preparation. A namespace kc may not read is a warning ("their
   pods' budgets are unknown, not absent") and its pods count neither as
   covered nor as uncovered. A user who may list budgets in all
   namespaces gets the shared cluster-wide informer, which the overview
   uses already; budgets are few and small.
4. **Refusals during the drain.** A 429 is a budget refusal only when its
   status carries the `DisruptionBudget` cause. Its message is the API's,
   and `PodNote.Evidence` adds the budget that covered the pod when the
   drain was prepared ("when planned: ..."), never as the reason. A 429
   without that cause reads "429 without a disruption budget cause:
   <message>" (API Priority and Fairness sheds load with 429 as well).
   The progress header is now "Refused by the API server".
5. **Behavior unchanged.** The same confirmation, the same refusal rules
   up front, the same eviction requests and retries. The preview reads
   only.

## Deviations from the issue

- **No separate browsable impact view.** The issue asks for an
  "inspectable impact view". The confirmation is where the decision is
  made, and the lines are bounded (3 workloads, 4 budgets, 2 warnings of
  each kind, then "... and N more"). A folder under the node that lists
  pods with their budgets is a possible follow-up; it would need its own
  refresh rules while the drain is not running.
- **Controllers, not top-level workloads.** Pods are grouped by their
  controller reference (a ReplicaSet, not its Deployment). Resolving the
  owner chain would read ReplicaSets, which start another cluster-wide
  informer for a line of text.
- **A pod with several budgets warns and does not refuse the drain.** The
  Eviction API refuses it, so the drain will stop there, but the budgets
  can change before kc reaches the pod, and the issue asks to keep the
  existing refusal rules.

## Consequences

- The estimate can be wrong in both directions: the controller updates
  the status between reads, each accepted eviction lowers the allowance,
  and replacements may not become healthy or schedulable at all.
- Follow-ups: a browsable impact folder under a node; resolve
  ReplicaSets to Deployments when the ReplicaSet informer already runs.
