# 0202. Local files in the Left and Right menus

- Status: Accepted
- Date: 2026-11-22
- PR: #202

## Context

The local filesystem opens in a panel with `M-l`, which is easy to miss. The user asked for it in the Left/Right menus, in the group with List, Describe, Manifest, Logs and Info. mc lists its panel kinds in the same place.

## Decision

The Left and Right menus get "Local files" (hotkey `e`, shortcut `M-l`) after Info. It activates that panel, switches it to List mode when another mode is shown (the local listing is a List-mode folder), and runs the `M-l` toggle. Chosen again, it returns the panel to the cluster. The mark `*` moves from List to Local files while the panel lists local files.

It is not a new panel mode: the local filesystem stays a folder on the panel's navigation stack, so `Back` from `/` still returns to the cluster and the copy code keeps working unchanged.

## Consequences

The hotkey is `e` because `l`, `f`, `c` and `s` are taken in these menus.
