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
- [ ] mc-style pull-down menu bar on F9 — #48
- [x] Describe/Manifest panel modes: refresh when the object changes, describe off the UI thread — #47
- [ ] Logs panel mode that follows the other panel's selection — #49
- [x] Top/metrics: usage columns in pod and node lists — #44
- [ ] Top/metrics: sorting by consumption — #37
- [ ] Extensible actions: built-in object actions and richer custom commands — #33, #46
- [ ] Favorites: add and remove favorite resource types from the UI (today only `resources.favorites` in the config).

Dropped as no longer useful:
- [x] Per-image shell probe cache for podfs: a missing shell now leads to the helper consent row (#21, #26), and the failed exec costs one request per session.
- [x] Selection sync options: Describe/Manifest already follow the other panel's selection; pinning a panel is covered by #49.
- [x] API group hierarchy under `/groups/...`: no demand; the root already lists types with their group.
- [x] Selection replay helper: only one replay site is left (`app_update.go`).

## Definition of Done
- [ ] Non-trivial logic unit-tested per AGENTS.md.
- [ ] Docs updated (README) as features land.
- [ ] UI stays responsive under list updates and watches.
