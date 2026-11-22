# 0037. Sort object lists by any column

- Status: Accepted
- Date: 2026-11-22
- Issue: #37

## Context

Object lists come from server-side Table output. `objects.order` allowed only
`name`, `-name`, `creation` and `-creation`. The issue asks for sorting by any
column in both directions, a shortcut to cycle the sort column, a mark on the
sorted column header, and value sorting for numbers, ages and quantities. It
leaves open whether kc remembers the sort per resource type.

Constraints: the column set differs per resource and per columns mode (normal
or wide), stored configs must keep working, all-namespace lists group rows by
namespace, and the table component measured titles in bytes.

## Decision

1. **Order format.** `objects.order` is a column key with an optional leading
   `-` for descending. The key is the column title in lower case with inner
   spaces as dashes (`restarts`, `nominated-node`). `name`, `age`, `namespace`
   and `creation` have a fixed meaning; `creation` is `age` reversed. A list
   without the column sorts by name. The old four values keep their meaning.

2. **Value comparison.** Integer, number and boolean columns compare as
   numbers; date columns as durations. Age compares the creation timestamp.
   Built-in columns are mostly typed `string`, so for those kc picks the first
   kind that reads every present cell: number, ratio (`1/2`), leading count
   (`3 (5m ago)`), duration (`5m`, `2d4h`), quantity (`250m`, `1Gi`), IP
   address, then text (case-insensitive). Empty cells and placeholders
   (`<none>`, `<unknown>`) come last in both directions. Ties keep name order.

3. **Namespace grouping stays the outer key** in all-namespace lists. Sorting
   by `namespace` orders the groups and keeps names ascending inside.

4. **Keys.** `M-o` sorts by the next shown column, ascending, and wraps
   around. `M-r` reverses the direction. `Esc o` and `Esc r` are the fallbacks
   where Option does not send Meta. When the active panel shows no object list
   the keys go to the shell line as before.

5. **F2 dialog.** The Objects section has "Sort by" (the shown columns of the
   current list) and "Reverse" (Yes/No), as mc's Sort order dialog has a
   Reverse box. Enter applies to the panel; Ctrl+S saves the order as the
   default for all lists.

6. **Header mark.** The sorted column shows `↑` (ascending) or `↓`
   (descending) after its title, the signs mc's UTF-8 skins use. The table
   now measures and cuts titles by runes.

7. **Scope of the sort.** The sort belongs to the panel, as other view options
   do, and follows it through navigation. It is not remembered per resource
   type.

## Alternatives considered

- **k9s keys (Shift+O, Shift+N, Shift+A, ...).** Upper-case letters are typed
  into kc's shell line, so they cannot be panel keys. mc has no default sort
  keys; Meta keys match kc's `M-c`, `M-s` and `M-w`.
- **Far Manager keys (Ctrl+F3 to Ctrl+F12).** Many terminals, including
  macOS Terminal, do not send Ctrl with function keys.
- **Sorting by the cell text.** Cheap, but `10` sorts before `9` and `5m`
  after `1h`, which is what the issue asks to fix.
- **Kind from the column title only** (for example "CPU" means quantity).
  Fragile for CRDs. Detecting the kind from the cells works for any printer
  column, and the column type still decides where the server sets one.
- **Remembering the sort per GVR in the config.** Answers the open question,
  but needs a new map in the config, defaults kept in sync in three places,
  and rules for removed CRDs. Falling back to name for lists without the
  column already gives a usable result per resource. Left as a follow-up.
- **ASCII marks (`'` and `,` as in mc's default skin, or `^` and `v`).** They
  read poorly next to upper-case titles. Arrows needed only rune-aware title
  widths in the table.

## Consequences

- One saved order such as `-restarts` sorts pods and falls back to name for
  other resources. Cycling with `M-o` from there starts on the next column.
- A wide-only column can be the sort key in normal mode; the rows still sort
  by it, but no header shows the mark.
- Column kind detection scans the column once per listing, linear in rows.
- Per-resource sort memory remains open.
