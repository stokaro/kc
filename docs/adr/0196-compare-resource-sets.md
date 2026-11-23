# 0196. Compare resource sets

- Status: Accepted
- Date: 2026-11-23
- Issue: #196

## Context

Issue #196 asks for two things. First, each panel should keep its own
kubeconfig context: cluster lease, discovery, namespace, read-only policy
and history move to the panel, every action (describe, logs, edit, custom
commands, terminal) resolves from its panel, and the terminal follows the
active panel. Second, a read-only comparison of two resource sets that lists
objects only on the left, only on the right, changed, equal or unavailable,
with the existing manifest diff for a matched pair.

What the code does today: the App holds one cluster (`a.cl`, `a.clLease`,
`a.currentCtx`). The context picker switches both panels
(`internal/ui/context_picker.go`). The panels already keep separate
namespace scopes (`panel_scope.go`). `C-x C-d` diffs the two objects under
the cursors (`app_compare.go`, `internal/manifestdiff`). The cluster pool
(`internal/cluster/pool.go`) can hold several contexts at once, each by its
own key and lease.

Moving the context into each panel touches every action path and the
read-only checks, the terminal, the hotlist and saved views, the menu, and
the frame. The risk is concrete. A panel that shows prod next to a panel
that shows staging makes F8, F4 and the custom commands one Tab away from
the wrong cluster, and every action that still reads `a.cl` would act on
the other side's cluster. kc has deleted a real object before through one
wrong navigation. The motivation in the issue, "see which resources differ
between staging and production", does not need two live panels in two
contexts.

## Decision

1. **The context stays per app.** kc does not move cluster ownership into
   the panels in this change. The context picker still switches both
   panels, and every action keeps using the one context the frame shows.
2. **`C-x d` compares the lists of the two panels** (mc's "Compare
   directories" is `C-x d`). Each panel must show an object list. The two
   lists must name the same group, version and resource. kc does not
   convert between API versions, and a different version is refused with
   both GVRs named. Objects are matched by name. When both lists span all
   namespaces they are matched by namespace and name. A list of all
   namespaces and a list of one namespace are refused. Two different
   namespaces (staging namespace vs prod namespace in one cluster) pair
   with each other by name, and `metadata.namespace` is left out of every
   pair, which the title says.
3. **`C-x c` compares the active panel's list with another context.** A
   context picker (the same choices as the context switcher) picks the
   other side. kc acquires that context's cluster from the pool with kc's
   impersonation, reads the same GVR and namespace there, and releases the
   lease when the read ends. The pool keeps the cluster for its TTL. kc
   does not switch to the context, and no panel shows it.
4. **Reads go through the caches lists use.** `models.ListObjects` decides
   between the shared cache and a namespace cache by the same access
   reviews as an object list, and releases a namespace cache it acquired.
   It refuses with "not permitted to list and watch ..." instead of
   returning nothing. A side that cannot be read (forbidden, not served in
   that context, timeout after 30 seconds) fails the comparison with that
   reason, so its objects are never counted as only on the other side.
5. **Each pair is judged by the manifest diff.** A pair is equal when the
   configuration view of `C-x C-d` (server fields and status left out)
   shows no difference. A pair whose diff cannot be computed, an object
   without a name, and a key that occurs twice on one side are
   unavailable, with the reason. A duplicated key is unavailable on both
   sides, never "only right".
6. **The result is a list in a dialog.** Changed rows come first, then only
   left, only right, unavailable and equal, each sorted by key. The title
   names both sides with their context ("left staging: deployments.apps in
   shop | right prod: deployments.apps in shop"). The command line shows
   the counts. Typing filters. Enter on a pair opens the manifest diff of
   the objects that were read, on top of the list. Esc in the diff returns
   to the list. The kubectl commands of the diff carry each side's
   `--context` and the version (`deployments.v1.apps`). Nothing in the
   result changes the cluster.
7. **Only the newest comparison is shown.** Each run has a sequence number,
   and a result that a newer run replaced is dropped.
8. Both chords are kc's own: custom commands cannot bind `C-x d` or
   `C-x c`. They are in the Command menu as "Compare lis&ts" and "Compare
   with conte&xt...".

## Deviations from the issue

- **No per-panel context, lease, read-only policy or history, and no
  terminal retargeting.** See the context: the risk of acting on the wrong
  cluster is larger than what the comparison needs. `C-x c` gives the
  staging/prod comparison without a second live context on screen. If per
  panel contexts are wanted later, they need their own issue, with every
  action path audited and a test that each action uses its panel's
  cluster.
- **No marked object sets.** The comparison takes the whole lists. A
  filter (`M-f`) narrows what the user sees, but not what is compared;
  comparing marked rows can follow once the selection API settles.
- **The diff of a pair is a snapshot.** It shows the objects as the
  comparison read them. Running `C-x d` again reads them anew.

## Alternatives considered

- **Per-panel contexts as the issue describes.** Rejected for now, see
  above. It is the largest change in the UI layer and the riskiest one for
  a tool with F8.
- **A virtual folder in the panel for the result.** It would fit kc's
  navigation, but a folder outlives the read and would need refresh,
  history and hotlist semantics for something that is a one-off snapshot.
  A dialog list reuses the picker and closes cleanly.
- **Matching across API versions** (for example v1beta1 against v1).
  Rejected: the stored objects may differ only by conversion, and kc would
  report differences the API server introduced.
- **Keeping `metadata.namespace` when two namespaces are paired.** Every
  pair would then be "changed", which hides the real differences.

## Consequences

- Comparing two contexts starts the informer for that one resource type in
  the other context (the same cache an object list would start) and keeps
  it for `kubernetes.clusters.ttl` after the lease is released.
- Owner references and other fields with cluster-generated values (for
  example `ownerReferences[].uid`) make pairs "changed" across clusters.
  The diff shows why. A per-field ignore list is a possible follow-up.
- Follow-ups: compare marked rows; an option to include status; per-panel
  contexts as a separate, audited change if users still need them.
