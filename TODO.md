# kc Development Plan

New work is tracked in GitHub issues; this file keeps the milestones and the
items that have no issue yet.

## Milestone 2 — UI Navigation on the Model
- [x] Function bar: compute capability-aware enablement for view/edit/delete/create actions and gray out unavailable ones (`panelFunctionKeys`, covered by `function_bar_test.go`).

## Milestone 3 — Terminal Follows Navigation
Terminal context manager for the integrated PTY session (`internal/ui/termctx`).
- [x] Env-based sync (`KUBECONFIG`, `--context`, `--namespace`) for the PTY.
- [x] Kubeconfig-copy approach, guarded by the terminal mode setting (`termctx.ModeCopy`).
- [x] Unit tests for the context manager (`termctx/manager_test.go`).

## Backlog
- [ ] mc-style pull-down menu bar on F9 — #48
- [ ] Describe/Manifest panel modes: refresh when the object changes, describe off the UI thread — #47
- [ ] Logs panel mode that follows the other panel's selection — #49
- [ ] Top/metrics: usage columns and sorting by consumption — #44, #37
- [ ] Extensible actions: built-in object actions and richer custom commands — #33, #46
- [ ] Favorites: add and remove favorite resource types from the UI (today only `resources.favorites` in the config). The hotlist (`C-\`, #41) saves locations, not resource types.

Dropped as no longer useful:
- [x] Per-image shell probe cache for podfs: a missing shell now leads to the helper consent row (#21, #26), and the failed exec costs one request per session.
- [x] Selection sync options: Describe/Manifest already follow the other panel's selection; pinning a panel is covered by #49.
- [x] API group hierarchy under `/groups/...`: no demand; the root already lists types with their group.
- [x] Selection replay helper: only one replay site is left (`app_update.go`).

## Definition of Done
- [ ] Non-trivial logic unit-tested per AGENTS.md.
- [ ] Docs updated (README) as features land.
- [ ] UI stays responsive under list updates and watches.
