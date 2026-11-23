# Logs mode

A panel in Logs mode streams the log of the pod or container under the
cursor of the other panel. Move through the pods of a namespace on one
side and watch each log on the other, without opening a viewer per pod.

## How it works

- A pod row streams the container `kubectl logs <pod>` picks: the one
  named by the `kubectl.kubernetes.io/default-container` annotation,
  else the first container.
- A container row, or a container's `logs` row, streams that container.
  A pod's `logs (all containers)` row streams every container.
- Any other row shows "Select a pod or container in the other panel to
  show its log." and stops the stream.
- The stream opens after the cursor rests on a row for 300 ms, so
  scrolling through a list does not open a stream per row. Moving on
  closes the old stream.
- The stream follows new lines and starts with the `viewer.logs.tail`
  last lines (200 by default).
- When the connection drops while the container still runs, kc
  reconnects and continues after the last line it showed.

## How to read it

The header shows the stream state:

- `loading...` while the stream opens;
- `paused, End follows` after you scrolled up;
- `reconnecting` while kc waits to reconnect;
- `ended` or `failed` when the stream stopped;
- for all containers, which sources stream, for example
  `incomplete: 3 streaming, 1 failed`.

Lines of an all-containers stream start with `[pod/container]`. The
footer counts lines dropped to stay within `viewer.logs.maxLines` and
`viewer.logs.maxMemoryMiB`, for example `• 10000 older dropped`.

With an investigation window (`C-x w`) the panel reads the log from the
start of the window instead of its tail, and the header shows the window.

## Keys

- `Up`, `Down`, `PgUp`, `PgDn`, `Home` scroll; scrolling up pauses
  following.
- `End` goes to the newest line and follows again.
- `F3` (Expand; File menu: Expand logs) shows this session in the
  full-screen [logs viewer](logs-viewer.md): the lines the panel keeps,
  then every new line, from the same stream. Nothing is read again.
  Closing the viewer leaves the panel streaming where it was. When the
  panel moves to another pod or its options change, the viewer says so
  and stops following. The viewer has the filter, fields, search and
  `M-m` Solo; the stream options stay in the panel (`F2`).
- `F2` opens the options: panel mode and width, theme, line mode,
  Container, Tail lines, Since, Timestamps and Previous. `M-F1` or
  `M-F2` opens the same dialog.
- `C-x p` pins the panel to this pod; `C-x f` freezes the display.

## Limits

- Tail, since and timestamps stay when the cursor moves to another pod.
  A chosen container and Previous are cleared, since another pod may
  not have them. "All containers" stays.
- JSON formatting, the log filter (`M-g`), the fields view (`M-d`) and
  the raw toggle (`M-v`) are only in the full-screen logs viewer. Press
  `F3` in the panel to expand its session, or `Enter` or `F3` on a
  `logs` row for a viewer with its own stream.
- An expanded viewer that falls more than 8192 lines behind the stream
  skips the oldest of them and says `[kc] N lines skipped`.
- The user menu is not on `F2` in this mode; `F9` shows it disabled.

## See also

- [Viewer options](viewer-options.md)
- [Logs viewer](logs-viewer.md)
- [Log sources](logs-sources.md)
- [Investigation window](investigation-window.md)
- [Pin and freeze](pin-freeze.md)
- [Panel mode](panel-mode.md)
