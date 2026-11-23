# Viewer options

`F2` in a viewer opens "Viewer Options": the color theme and line mode
of the text, and in a logs viewer also which part of the log to read.
Use it to see older lines, add timestamps, or read the log of the
container instance that crashed before the last restart.

## How to read it

Viewer section:

- Theme: the color theme for YAML and other highlighted text.
- Line mode: Scroll keeps long lines whole and pans with `Left` and
  `Right`; Wrap breaks them at the viewer width.

Logs section, in the logs viewer and in a Logs panel:

- Container: Default, each container of the pod, or All containers.
  Shown when there is more than one choice.
- Tail lines: 50, 100, 200, 500, 1000, 5000 or All.
- Since: Off, 1m, 5m, 15m, 30m or 1h.
- Timestamps: On adds the kubelet time to every line.
- Previous: On reads the log of the previous instance of a restarted
  container, like `kubectl logs -p`.

A configured value that is not in a list, such as `tail: 300`, is added
to the list so it shows and stays.

In a Logs panel the same rows appear in the panel options, under the
panel mode and width, so `F2` can also leave Logs mode.

## Keys

- `Up`, `Down` move between rows.
- `Left`, `Right` or `Space` change the value.
- `Enter` applies to this viewer and closes.
- `C-s` also saves Theme, Line mode, Tail lines, Since and Timestamps as
  the `viewer.*` defaults in `~/.kc/config.yaml`.
- `Esc` closes without changes.

## How it works

- A change of a log option closes the stream and opens a new one with
  an empty view, so the text always matches the options.
- The kubectl command on the last line follows the options, for
  example:

```
kubectl logs web-0 -n shop -c app --tail=200 --since=1h \
  --timestamps -p -f
```

- The logs viewer's status line, at the right of its top line, lists
  the active options and the stream state, such as
  `tail 200, since 1h, timestamps, previous (ended)`.

## Limits

- Previous and the container choice are never saved: they belong to
  one container's history.
- Previous fails on a container that never restarted; the viewer shows
  the error as a line.
- With an investigation window set, a Logs panel reads from the start
  of the window instead of its tail and since.

## See also

- [Viewer](viewer.md)
- [Logs viewer](logs-viewer.md)
- [Logs mode](mode-logs.md)
- [Panel options](panel-options.md)
- [Investigation window](investigation-window.md)
