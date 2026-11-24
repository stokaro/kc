# 0351. Choose what the dashboard shows from its state

- Status: Accepted
- Date: 2026-11-24
- PR: #351
- Amends: 0321 (point 1, the state in the top bar; Keys, `F2`)

## Context

The right end of the dashboard's top bar says what the charts show:
`LIVE · last 15 minutes`, `PAUSED at 10:49:56 · 15m`, `LIVE window
15m`, `WINDOW <label>`, with `≠ window` when the dashboard left the
investigation window (ADR 0321). Changing it took three places: the
Range menu (`F2`) for the span, `Space` or the Dashboard menu to pause
or go live, and `w` for the investigation window. A click on the top
row did something only on a menu title.

The user asked for a click on the state to open a small pop-up under it
that chooses what the dashboard shows: live, or which range or window.

## Decision

1. **A click on the state opens the Range menu under it.** The
   drop-down hangs under the state, its right edge at the screen's,
   shifted to fit. The top row stays as the dashboard draws it, so the
   state the user clicked stays in view above the menu. It is the menu
   bar's drop-down: keys, hotkeys, the wheel and clicks work as in any
   menu, `Esc` or a click outside or on the state again closes it, and
   `Left` or `Right` moves to the next menu under its title. `F2` and
   the Range title open the same menu under the title, as before.
2. **The Range menu starts with what the dashboard shows**, with that
   item checked:
   - `Live`: the range ends now (as `Space` while paused; it also leaves
     a followed window).
   - `Pause here` (`Space`) while live, `Paused at HH:MM:SS` while
     paused. On a followed window it leaves the window and keeps its end.
   - `Follow the investigation window`, dim while none is set, and `Set
     the investigation window to this range` (`w`). Neither shows when
     the dashboard can neither read nor set a window.

   A separator, the spans by name and the zoom follow, as before.
   `Space` is shown on the item it runs at that moment.
3. **Hotkeys.** `l`, `p`, `f`, `w`, `i` and `o`. `w` is the set item,
   as the key `w` and the Dashboard menu's "Set the investigation
   window" are.
4. **`menubar.Bar.OpenAnchored`.** It drops a menu down with its right
   edge at a column of the bar row; `Bar.Anchored` tells the caller not
   to draw the bar row over its own. Any move to another menu drops the
   anchor, so the rest of the menu bar behaves as before.
5. **No decoration.** kc marks nothing as clickable besides what looks
   like a menu title or a function key, so the state cell keeps its
   look. The F1 page says that it opens the menu.

## Alternatives considered

- **The Range menu under its title.** The menu would open at the far
  left of the screen, away from the click.
- **A pop-up of its own.** A second implementation of keys, hotkeys,
  mouse and drawing that the menu bar already has.
- **The bar row drawn while the pop-up is open.** It covers the state
  the user clicked, so the menu would hang under nothing.
- **A separate Time menu.** One more title on a short bar, with the
  spans split from the live and window choices they go with.

## Consequences

- The Range menu is longer by four items and a separator.
- Live, pause and the window can be chosen in two places each: by key
  and in the Dashboard menu as before, and in the Range menu.
