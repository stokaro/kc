# 0166. Right panel starts in Info mode

- Status: Accepted; the pause while a dialog is open (point 7) amended by 0342; in point 2, a bad value resets the `layout` section, not the whole file (superseded by [0372](0372-config-load-fails-closed.md))
- Date: 2026-11-22
- Issue: #166

## Context

kc started with both panels in List mode. The Info mode (`C-1` / `C-2`, ADR 0051) shows the load of the other panel's scope, or of the node or workload under its cursor, which is what a user wants next to a list on start. The issue asks for left List and right Info by default, a setting per panel, and a rule for restored panel state and start targets. It left open the config key, which modes are allowed, how a bad value is reported, and what "restored state" means in kc.

## Decision

1. **Config keys.** `layout.leftPanelMode` (default `list`) and `layout.rightPanelMode` (default `info`), next to `layout.menuBarVisible` (ADR 0162). Values are `list`, `describe`, `manifest`, `logs` and `info`, matched without case and surrounding spaces; an empty value is the default for that side. The keys are in `appconfig.Default()`, `config-default.yaml` and the README.

2. **Validation.** An unknown value, `command` included, or a layout where neither panel is `list`, is a load error: the file is ignored, kc starts with the defaults and the existing "Config ignored, using defaults (saving is off)" message names the key and the valid values (ADR 0059). Every mode other than List shows the other panel's selection, so two non-list panels leave nothing to move a cursor in.

3. **No restored state.** kc does not save panel modes between runs; nothing in `~/.kc` records them. The start modes therefore apply on every start. If a later change saves panel state, a restored mode should win over these keys, as the issue asks.

4. **When the mode is set.** `App.Init` loads the config, starts both list widgets, then switches each panel whose start mode is not List through the same `setPanelMode` path as `C-1` / `C-2` and the `F9` panel menu. The navigators keep loading underneath, so switching back to List shows the panel's folder at once. Focus stays on the left panel; `Tab`, `C-1` / `C-2` and the panel menu work as before.

5. **Start targets.** `kc get`, `kc logs` and `kc overview` navigate the left panel after the start modes are set and leave the right panel's mode alone. `kc get TYPE NAME -o yaml` needs the right panel and still switches it to Manifest. `-n`, `-A` and `--context` only choose the scope, so the right panel's Info mode shows that scope.

6. **No cluster.** Without a context or a reachable cluster the Info mode shows its hint ("Not connected to a cluster."), not an error. When the left panel is at the root or in the contexts list, Info shows the left panel's namespace scope, as it does today when switched on by hand.

7. **Load of the Info mode, measured.** With Info on every start its cost is paid on every start, so it was measured against envtest (a counting proxy in front of the API server, before and after this change):

   - Informers: the Info engine leases its own pool entry (`kc-overview`, no namespace), a cluster-wide cache, and watches 14 kinds across all namespaces whatever the scope: nodes, pods, events, persistentvolumeclaims, persistentvolumes, resourcequotas, deployments, statefulsets, daemonsets, jobs, cronjobs, poddisruptionbudgets, horizontalpodautoscalers, replicasets (metadata only), plus cert-manager Certificates when served. The namespace scope only filters reads.
   - Periodic requests: NodeMetrics (cluster-wide) and PodMetrics (in the scope) every 15 s; since #169 also PodMetrics of all namespaces every 15 s when the scope is one namespace; the kubelet stats summary once a minute for each node, at most five at a time, skipped above `overview.statsMaxNodes` (50) nodes. Recomputes read the cache only.
   - Lifetime: the engine runs while the panel is in Info mode, visible or not. The widget ticks once a second, also behind dialogs and the fullscreen terminal; metrics polls and recomputes pause while a dialog is open. Leaving the mode stops the engine at once, the metrics pollers two intervals later and the cache one `kubernetes.clusters.ttl` later. Focus does not matter.
   - A user with only namespace-scoped RBAC: the informers never sync. Over two minutes the Info mode alone caused about 210 forbidden requests (a list and a watch for each of the 14 kinds every 15 to 30 s) and the panel showed four bars with "nodes and pods cannot be read" and, after 30 s, "the cache did not sync (no list or watch permission?)". Without Info the same two minutes had none.

   Fixes in this change, each small and independent of the Info widget:

   - Before the engine starts, kc asks with SelfSubjectAccessReviews (the cached `ListWatchAccess` of ADR 0056) whether the user may list and watch nodes and pods in all namespaces. On a clear denial the engine, its cache and its metrics pollers are not started, and the panel shows "Overview unavailable: not permitted to list and watch nodes and pods in all namespaces". A failed review counts as allowed. Measured again: no forbidden request from Info.
   - A metrics list answered with forbidden is asked again after five minutes instead of every 15 s. This covers #169's all-namespaces PodMetrics poll for users who may list pods but not PodMetrics everywhere, and the usage columns of lists.
   - The kubelet stats rounds stop after one in which every node answers forbidden for `nodes/proxy`.
   - Root counts read `apiserver_storage_objects` from `/metrics`. A failed read was retried on every count, which for a user without get on `/metrics` meant a 403 about every two seconds while a root was shown (found during the measurement; not caused by Info). A failure now holds reads off for the peek interval, a 401 or 403 for ten minutes.

## Alternatives considered

- A nested `layout.panels.left.mode` / `layout.panels.right.mode`. It reads well but adds two levels for one value each; flat keys match `menuBarVisible`, and `panel.width.leftPercent` / `rightPercent` already use the left/right prefix.
- Keys under `panel:`. `panel` holds settings both panels share (skin, table mode, scrolling); a per-side start mode is a screen layout choice, like mc's Layout dialog.
- Accepting `command`. Command mode needs a command definition and has its own entry path; a start mode cannot name one.
- Allowing two non-list panels. It starts kc in a state the user must leave with `C-1` / `C-2` before doing anything.
- Ignoring a bad value with a warning and keeping the rest of the file. kc's loader never mixes a partly valid file with defaults (ADR 0059); one rule for every key is easier to reason about.
- Saving the current modes on exit, like mc's "Auto save setup". The issue only asks for a default; saving would need a decision on which modes are worth restoring (a Logs panel without a pod is not) and is left for later.

- Scoping the Info engine to the other panel's namespace (namespaced informers). The node bars need every pod on a node, so a namespaced cache cannot fill them; it would change what Info shows and touch the engine's data rules.
- Starting the engine only while the panel is visible. It needs the widget to know about the fullscreen terminal and dialogs; left as a follow-up below.

## Consequences

- The right panel starts in Info mode; users who prefer two lists set `layout.rightPanelMode: list`.
- The demo GIF (ADR 0158) shows two lists until `docs/demo/record.sh` is run again.
- On a connected start with full read access, the cluster-wide Info cache and the 15 s metrics polls start with kc. Users on very large clusters who do not want that set `layout.rightPanelMode: list`.
- Follow-up: pause the Info engine while the panel cannot be seen (fullscreen terminal, a panel at 100 % width), and consider a longer metrics interval for Info than for `/overview`.
- Tests cover the defaults, the setting and its validation, the no-cluster hint, focus, the panel menu, the start targets, the access gate (unit and envtest with a namespace-only user), the metrics back-off, the stats stop and the `/metrics` hold-off.
