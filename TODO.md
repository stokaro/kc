# kc Development Plan

New work is tracked in GitHub issues; this file keeps the milestones and the
items that have no issue yet.

## Milestone 2 — UI Navigation on the Model
- [x] Function bar: compute capability-aware enablement for view/edit/delete/create actions and gray out unavailable ones (`panelFunctionKeys`, covered by `function_bar_test.go`).

## Milestone 3 — Terminal Follows Navigation
One execution context for every subprocess (`internal/execctx`, ADR 0055).
- [x] Env-based sync (`KUBECONFIG`) for the PTY; the path stays the same across context switches.
- [x] Overlay and copy kubeconfig modes, chosen by `terminal.mode` (`execctx.ModeOverlay`, `execctx.ModeCopy`).
- [x] F4 and custom commands use the selected context and impersonation (#55).
- [x] Unit tests for building the context and each consumer's argv and environment (`execctx/*_test.go`, `ui/exec_context_test.go`).

## Backlog
- [x] mc-style pull-down menu bar on F9 — #48
- [x] Describe/Manifest panel modes: refresh when the object changes, describe off the UI thread — #47
- [x] Logs panel mode that follows the other panel's selection — #49
- [x] Top/metrics: usage columns in pod and node lists — #44
- [x] Top/metrics: sorting by consumption (`M-o` on the CPU, MEM and % usage columns) — #37
- [x] Cluster overview `/overview`: capacity (requests and limits), health, risks, activity, thresholds under `overview:` — #51
- [x] Overview follow-ups: measured usage from metrics.k8s.io, Info panel mode, kubelet stats for disk and volumes — #51
- [x] Overview: cert-manager Certificates not Ready or expiring, Info mode for one workload — #51
- [ ] Overview: CRD workloads as Info targets, pick up cert-manager installed while the overview is open (left over from #51)
- [x] Built-in workload actions: scale, rollout restart, rollback — #33
- [x] Extensible actions: built-in actions for workloads, CronJobs and nodes; custom commands with hotkeys, prompts, confirmation and `dangerous` — #46
- [ ] Favorites: add and remove favorite resource types from the UI (today only `resources.favorites` in the config). The hotlist (`C-\`, #41) saves locations, not resource types.

Dropped as no longer useful:
- [x] Per-image shell probe cache for podfs: a missing shell now leads to the helper consent row (#21, #26), and the failed exec costs one request per session.
- [x] Selection sync options: Describe/Manifest already follow the other panel's selection; pinning a panel is covered by #49.
- [x] API group hierarchy under `/groups/...`: no demand; the root already lists types with their group.
- [x] Selection replay helper: only one replay site is left (`app_update.go`).

## Definition of Done
- [ ] Non-trivial logic unit-tested per AGENTS.md.
- [ ] Docs updated as features land (`docs/` and the help topics in `internal/help/topics/`).
- [ ] UI stays responsive under list updates and watches.
