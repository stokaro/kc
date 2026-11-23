# 0267. Rank and group diagnosis findings before the bound

- Status: Accepted
- Date: 2026-11-23
- Issue: #267

## Context

A workload diagnosis sorted its pods by name and explained the first ten
that had findings; the rest were counted. A common warning on early pods
could hide a distinct error on a pod late in name order. Issue #267 asks
to rank before applying the bound, to group repeated symptoms with
counts and identities, to say what is hidden, and to keep incomplete
findings visible, without changing which checks run for a pod.

## Decision

1. **Every pod is checked.** `podProblems` runs the existing pod checks
   on all pods of the workload. The checks are unchanged. The cost is
   the same per-pod work on the cached pods, already in memory.
2. **Symptom key.** Two findings are the same symptom when they have the
   same severity, the same code with the pod's name segment replaced by
   `*`, and the same title with the pod's name removed. The title carries
   the reason (`ImagePullBackOff`, `exit code 137 (OOMKilled)`), so
   different reasons are never merged. The detail (a free-form message)
   is not part of the key; when members' details differ, the group says
   so and keeps the first pod's.
3. **Ranking.** Groups are ordered by severity, then by the number of
   pods, then by the first pod's name, then by code. Pods are sorted by
   name first, so the representative pod and the order are the same on
   identical reads. The report's stable severity sort keeps this order.
4. **Bounds.** Ten groups are expanded (`maxSymptomGroups`). A group of
   more than one pod gets the code `group/<code with *>`, the title of
   the first pod plus `(N pods)`, and links to up to 20 pods
   (`maxGroupPods`) with "N more pods are not cited". The remaining
   groups go into one `more-symptoms` finding with one link per group
   to its first pod (up to 50), which opens it directly. A single-pod
   group keeps its original code and text.
5. **Unchanged.** Status findings, source-unavailable findings (such as
   `events-unknown` and `pods-unknown`) and the scheduling explanation
   are not grouped or bounded.

## Deviations from the issue

- **Continuation by links, not paging.** The diagnosis folder is a flat
  list of rows with no paging. The hidden symptoms are one finding whose
  rows open the first pod of each symptom, and the pods' own
  `/diagnosis` explains them fully. Paging would need folder state that
  survives refreshes.
- **A group cites up to 20 pods, not all.** "Navigation to all
  represented Pods" for a DaemonSet of hundreds of pods would make one
  finding hundreds of rows; the count is exact and the workload's pods
  folder lists the rest.

## Consequences

- Row IDs of repeated symptoms change from `<kind>/<pod>/...` to
  `group/<kind>/*/...`, so a cursor on such a row moves after an
  upgrade; a symptom that one pod has keeps its ID.
- A symptom whose title has no pod name (none today) would group across
  pods by code alone; new findings must keep the pod name in the title
  or the code.
