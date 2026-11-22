# 0167. Idle menu bar colors

Status: Accepted

Supersedes the idle-bar part of decision 6 of ADR 0162.

## Context

ADR 0162 drew the always-visible menu bar in the open-bar palette: white titles on cyan with yellow hotkey letters. A screenshot of mc with the bar idle shows plain black titles on cyan and no highlighted hotkey letters. mc switches to the white and yellow palette only when the bar is opened.

## Decision

While no menu is open, the bar row is black on cyan, titles and padding alike, with no hotkey highlight. Once the bar is opened (F9, `Esc 9`, `9 PullDn`, a click on a title), the titles, drop-downs and selection use the palette of ADR 0162, decision 6.

## Consequences

The idle bar reads as a quiet header instead of competing with the panels. The hotkey letters are not visible until the bar opens; `M-<letter>` and F9 work as before.
