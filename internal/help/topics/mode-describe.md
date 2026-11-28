# Describe mode

A panel in Describe mode shows `kubectl describe` for the object under
the cursor of the other panel. Move through pods, nodes or Deployments on
one side and read their status, conditions and events on the other,
without opening a viewer for each one.

## How to read it

- The frame title is the path of the described object. The text is the
  same as `kubectl describe <type> <name> -n <namespace>` prints.
- While the first result for a new object loads, the old text stays and
  the header says `loading...`. After the other panel switched context,
  the panel shows `Loading...` instead: the frame already names the new
  context, and the old text came from the old one.
- The footer shows the line position and how far you are, for example
  `12/80 • 15%`. `^` and `v` in the frame mean more text above or below.
- A row that is not an object (a folder, a container, `/..`) shows
  "Select a resource to describe.", also when a `cd`, a namespace or
  context switch or a jump from the history put the other cursor there.
- When the describe fails, for example because the object was deleted,
  the text is replaced by the error.

## How it works

- kc describes in the background, so moving the cursor quickly does not
  block the screen. Only one describe runs per panel; a newer selection
  waits for it, and the older result is dropped.
- When the object changes in kc's cache, the panel describes it again,
  at most once a second. An object whose resourceVersion did not change
  is not described again.
- A refresh keeps the scroll position. A new object starts at the top.
- While a dialog is open, refreshes wait.

## Keys

- `Up`, `Down`, `PgUp`, `PgDn`, `Home` and `End` scroll the text.
- `Left` and `Right` scroll long lines sideways.
- `C-x p` pins the panel to this object; `C-x f` freezes the display.
- `M-F1` or `M-F2` opens the panel options, where you can change mode.

## Limits

- New events alone do not change the object's resourceVersion. They
  appear with the next change of the object or when you select it again.
- Describe sends the same API requests as kubectl, with a 10 second
  timeout per request.
- The panel is read-only: `F3`, `F4`, `F5` and `F8` do nothing here. Use
  the list in the other panel for those.

## See also

- [Panel mode](panel-mode.md)
- [Manifest mode](mode-manifest.md)
- [Pin and freeze](pin-freeze.md)
- [Viewer](viewer.md)
