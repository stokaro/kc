# Manifest mode

A panel in Manifest mode shows the YAML of the row under the cursor of
the other panel, the same content `F3` opens in the viewer. Use it to
compare specs while you move through a list, or to keep the manifest of
one object in view next to its related objects.

## How to read it

- The frame title is the path of the shown row. The text is highlighted
  YAML, or the content of a ConfigMap or Secret key and other rows that
  `F3` can show.
- The footer shows the REST API path of the object, for example
  `/apis/apps/v1/namespaces/shop/deployments/web`, and the line
  position.
- `^` and `v` in the frame mean more text above or below.
- While the first content for a new row loads, the old text stays and
  the header says `loading...`.
- A row that cannot be shown says "Selected resource does not expose
  manifest content." With nothing selected the panel says "Select a
  resource to view its manifest."

## How it works

- The manifest is read from kc's cache in the background.
- When the object changes in the cache, the panel reads it again, at
  most once a second. The text is replaced only when it changed, and a
  refresh keeps the scroll position. A new row starts at the top.
- `kc get <type> <name> -o yaml` starts kc with the right panel in
  Manifest mode on that object.

## Keys

- `Up`, `Down`, `PgUp`, `PgDn`, `Home` and `End` scroll the text.
- `Left` and `Right` scroll long lines sideways.
- `C-x p` pins the panel to this object; `C-x f` freezes the display.
- `M-F1` or `M-F2` opens the panel options, where you can change mode.

## Limits

- The panel is read-only. Edit with `F4` and copy with `F5` from the
  list in the other panel.
- For a line-by-line comparison of two objects use `C-x C-d` instead.

## See also

- [Panel mode](panel-mode.md)
- [Describe mode](mode-describe.md)
- [Compare objects](compare-objects.md)
- [Pin and freeze](pin-freeze.md)
- [Viewer](viewer.md)
