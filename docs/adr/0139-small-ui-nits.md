# 0139. Logs in the panel menus, file size line, drain dialog width

- Status: Accepted
- Date: 2026-11-22
- Issue: #139

## Context
Three small things from a check on kind. The F9 Left and Right menus had no entry for Logs mode, so it was reachable only with `C-1` / `C-2`. The info line of a file in a container folder read `8 B bytes`. The drain progress dialog opened at the width of its first lines (at least 56 columns) and never grew, so the PodDisruptionBudget refusals that arrive later were cut at its edge, even on a 200-column screen.

## Decision
1. The Left and Right menus list `Logs` between Manifest and Info, in the order `C-1` / `C-2` cycle the modes. Its hotkey is `g` (`Lo&gs`): `l` and `o` are taken in these menus by List and Panel options.
2. The info line of a file gives the exact size: `8 bytes`, and from 1 KiB on the rounded size the Size column shows with the exact one, `1.5 KiB (1536 bytes)`.
3. The drain progress dialog opens 100 columns wide when the screen has room, and every line wraps to the dialog width, with continuation lines indented two more columns. The height reserves two lines per listed blocked pod. If the text still does not fit, the blocked list is cut and the last line (how the drain ended, or how to cancel) stays visible.

## Alternatives considered
- Only a wider dialog: a refusal is often longer than 100 columns, and a narrow screen cuts it anyway.
- Only wrapping at the old width: five refusals at 56 columns take three lines each and push the status line out.
- Growing the dialog when a refusal arrives: the window is sized once when it opens; resizing it on progress makes it jump while the drain runs.
- `L` or `o` as the Logs hotkey, like the other modes' first letter: both are taken in these menus.

## Consequences
The drain dialog is wider than its content before the first refusal. A very long list of refusals still loses lines at the bottom of the blocked section, as it already did beyond five pods.
