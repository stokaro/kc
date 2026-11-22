# 0162. Menu bar visible on the top row

- Status: Accepted; the idle-bar colors of decision 6 superseded by 0167
- Date: 2026-11-22
- Issue: #162

## Context

The F9 menu bar (#48, ADR 0048) was drawn only while it was open, like mc with "Menu bar visible" off. mc's default is the other way: the titles stay on the top row and can be clicked at any time. The issue asks for that default, a setting that restores the hidden bar, and a toggle in the Options menu. It left open the config key, whether the toggle is saved, what happens to the bar in fullscreen views, and how a click on the idle bar behaves.

A comparison with mc's screenshots also showed that ADR 0048's colors were wrong: mc draws menu text, shortcuts, the frame and the separators white on cyan, not black.

## Decision

1. **Config key.** `layout.menuBarVisible`, default `true`. `layout` is a new top-level section named after mc's Options > Layout dialog, where mc keeps this switch; later layout switches (key bar, command line) can go there too. The key is in `appconfig.Default()`, `config-default.yaml` and the README.

2. **Layout.** With the bar visible, row 0 holds the titles and the panels start on row 1; the panel area is one row shorter. Everything that turns screen rows into panel rows goes through `App.menuBarRows()` (1 or 0): the panel height, panel row clicks, the scope labels on the bottom border (namespace, context, filter), the panel and shell-line cursor, and the placement of dialogs that are positioned over a panel. Rows counted from the bottom (command line, shell line, function key bar) do not change.

3. **Fullscreen views.** The fullscreen terminal (`C-o`), the node shell and fullscreen dialogs (viewers, logs) cover the whole screen and the bar, as in mc. `menuBarRows()` is 0 while the terminal is fullscreen. Windowed dialogs and the help screen are drawn over a background that includes the bar.

4. **Mouse.** A left button press on a title of the idle bar drops that menu down, as F9 and its hotkey would; the release goes to the open bar, which ignores it. Presses between titles and other buttons on the bar row do nothing and do not reach the panels. With the bar hidden, row 0 is the panels' top border, as before.

5. **Toggle.** Options > Menu bar visible (hotkey `m`, checked while visible) switches the setting at once and saves it to `~/.kc/config.yaml` with the same store as `C-s` in dialogs, so a broken file is never overwritten and the command line says whether the save worked. mc saves its setup on exit by default ("Auto save setup"); saving on change has the same effect without a save-on-exit path in kc. No new key is bound.

6. **Colors.** mc's default skin (`[menu]` in default.ini): titles, entries, shortcuts, the frame and the `├─┤` separators are bright white on cyan; hotkey letters are yellow on cyan; the open title and the selected entry are white on black with a yellow hotkey; entries that do not apply are gray (bright black) on cyan. The idle bar uses the same palette. This replaces decision 5 of ADR 0048.

## Alternatives considered

- `panel.menuBarVisible` or a top-level `menuBar.visible`. The bar is not part of a panel, and a section per screen element would scatter what mc groups in one Layout dialog.
- A session-only toggle with `C-s` to save, as in the option dialogs. A menu entry has no dialog to press `C-s` in, and a switch that reverts on the next start surprises mc users, whose setup is saved automatically.
- Opening a menu on release instead of press. mc reacts to the press; with press the release lands on the open bar, which already ignores releases that did not start on an entry.
- Keeping the bar in the fullscreen terminal. mc hides everything behind `C-o`, and the terminal size would have to change with the setting.
- Hiding the bar automatically on very small terminals. The panel height already has a floor; one more rule would make the hit tests depend on the terminal size as well.

## Consequences

- The panels lose one row by default. Users who prefer the old layout set `layout.menuBarVisible: false` or use the Options entry.
- New code that maps screen rows to panel rows must add `menuBarRows()`; tests at fixed sizes cover the layout with the bar on and off, clicks on titles, panel row clicks and the scope labels.
- The demo GIF (ADR 0158) shows the old layout until `docs/demo/record.sh` is run again.
