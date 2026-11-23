# Viewer

`F3` opens the full-screen viewer for the row under the cursor: an
object's YAML, a ConfigMap or Secret key, a Helm values file, a field
description, a file in a container or a local file. The text is
highlighted by its type. Container logs have their own viewer.

## How to read it

- After a search the right end of the top line shows the text and
  which match is in view, for example `/image (2/5)`.
- The line above the key bar shows the kubectl command that prints the
  same content, with a copy button at its right. For a local file it
  is `cat` with the file's path.
- The key bar at the bottom shows the keys that fit on its line; this
  page lists them all.
- Binary content is shown as a hex dump of the first 4 KiB, with the
  size, the detected content type and the SHA-256.

## Keys

- `Up`/`Down` or `k`/`j` scroll one line; `PgUp`/`PgDn` (`C-b` too)
  one page; `Home`/`End` (`g`/`G`) go to the start or the end.
- `Left`/`Right` or `h`/`l` scroll sideways; `C-a` and `C-e` go to
  the start and the end of the lines. The mouse wheel scrolls.
- `F7`, `/` or `C-f` opens the search dialog. `n` or `Shift-F7` goes
  to the next match, `N` to the previous one.
- `M-a` copies the whole content; `M-c` copies the kubectl command.
- Drag with the left mouse button to select text; releasing copies
  the selection. A click or `Esc` clears it.
- `F2` opens the viewer options: color theme and wrap mode.
- `F4` edits the object with `kubectl edit`, or a local file in your
  editor, where that applies.
- `F3`, `F10` or `Esc` closes the viewer. A lone `Esc` closes it once
  the Esc timeout passes, so `Esc` followed by a digit still works as
  a function key.

## Search

The dialog takes the text and a mode: normal, regular expression or
wildcard. Options are case sensitive, backwards and whole words.
`Tab` moves between fields, `Space` toggles an option, `Enter` searches
and `Esc` cancels. An invalid regular expression keeps the dialog open
with the error.

## Limits

- The viewer is read-only; `F4` hands the object or file to an editor.
- A local file over 2 MiB or a container file over 512 KiB opens in
  pages, as in mc's viewer: kc reads 256 KiB blocks around the visible
  lines and keeps at most 16 in memory. The top line says so at its
  right, with the position, for example
  `paged, 1.2 GiB, highlighting off · line 120/4000`. A block on its
  way shows as `~ reading...`.
- In a paged file `Home`, `End`, `PgUp` and `PgDn` work on the whole
  file. After `End` the position shows as a share of the bytes (`~97%`)
  until the lines are counted (local files only). `F7` searches from the
  current position with its progress on the top line; `Esc` cancels it.
- A paged file has no syntax colors and no mouse selection, and `M-a`
  does not copy it; save it with `F5`.
- To save the content to a file, press `F5` on the row in the panel.
- Wrap mode starts from `viewer.mode` in the configuration.

## See also

- [Viewer options](viewer-options.md)
- [Logs viewer](logs-viewer.md)
- [Diff viewer](diff-viewer.md)
- [Copy](copy.md)
