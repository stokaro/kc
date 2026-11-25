# Pin and freeze

Describe, Manifest, Logs and Info panels follow the cursor of the other
panel. That is usually what you want, but in an investigation you may
want to keep one workload on screen while you browse related objects.
Pin does that. Freeze keeps a picture of a panel at one moment while the
data moves on, so you can read it or compare it later.

## Pin

`C-x p` pins a panel to what it shows now. Browsing in the other panel no
longer changes it, and the frame title starts with `[pinned]`.

- The key acts on the active panel when it is in Describe, Manifest,
  Logs or Info mode, else on the other panel.
- A container or log row pins its pod. Info pins the namespace scope and
  the row under the other cursor.
- kc names the pinned object by context, resource type, namespace, name
  and UID, and watches that name in its cache.
- A change to the pinned object rereads the panel, even when the other
  panel shows something else.
- When the object is deleted, or a new object with another UID takes
  its name, the panel freezes. The title says `[pinned, deleted]` or
  `[pinned, replaced by uid 1a2b3c4d]`. kc never shows the new object in
  place of the old one.
- Switching between Describe, Manifest, Logs and Info keeps the pin.
  List mode, Command mode and `C-x p` again drop it, and the panel
  follows the other cursor again.
- A context switch of either panel keeps the pin: the pinned panel stays
  on the pinned object's context, and its border names it. After
  unpinning it follows the other panel's context.

## Freeze

`C-x f` freezes a panel's display. The header says
`FROZEN 14:02:11 +01:00` instead of the live status.

- Keys and the mouse do not reach a frozen panel, and the other panel's
  cursor does not change it.
- Behind the picture a Logs panel keeps reading its stream within its
  line limits, and Info keeps collecting its samples. Describe and
  Manifest do not reread.
- `C-x f` again returns to live data with the scroll position, filter
  and options the panel had. It catches up with the other cursor if it
  moved.
- A panel frozen because its pinned object was deleted or replaced
  stays frozen; `C-x p` unpins it.

Pausing a log (scrolling up) only stops autoscroll. Pinning keeps the
target. Freezing keeps the picture.

## Keys

- `C-x p` pins or unpins.
- `C-x f` freezes or returns to live data.
- "Pin target" and "Freeze display" in the Left and Right menus (`F9`)
  do the same for that panel and show a check mark while on.

## Limits

- Pin and freeze work only on Describe, Manifest, Logs and Info panels.
  A list cannot be frozen.
- Pinning does not start a `/timeline` recording.

## See also

- [Investigation window](investigation-window.md)
- [Panel mode](panel-mode.md)
- [Logs mode](mode-logs.md)
- [Info mode](mode-info.md)
- [Timeline](timeline.md)
