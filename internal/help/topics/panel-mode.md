# Panel mode

Each panel has a mode that decides what it shows. List mode browses the
cluster. The other modes show something about the row under the cursor
of the other panel, so you can move through pods on one side and read
their description, manifest, log or load on the other.

## How it works

The modes, in the order `C-1` and `C-2` step through them:

- List: folders and object lists, see [Panels](panels.md).
- Describe: `kubectl describe` of the selected object.
- Manifest: the YAML of the selected object.
- Logs: the log of the selected pod or container.
- Info: usage bars for the other panel's scope, node or workload.
- Command: a custom command running in the panel. It is offered only
  after a command was started in that panel.

There is no separate mode dialog. Pick a mode in one of these places:

- `M-F1` (left) or `M-F2` (right) opens the panel options. The first
  row, "Panel mode", cycles with `Left`, `Right` or `Space`; `Enter`
  applies it.
- `F9`, then the Left or Right menu: List, Describe, Manifest, Logs and
  Info, with a mark on the current one. "Local files" there shows the
  local file system in List mode, like `M-l`.
- `C-1` steps the left panel to its next mode, `C-2` the right one.

A panel that switches to Describe, Manifest, Logs or Info picks up the
row under the other panel's cursor at once.

## Keys

- `C-1`, `C-2` step the left or right panel to the next mode.
- `M-F1`, `M-F2` open the panel options with the "Panel mode" row.
- `F9` then `L` or `R` opens the Left or Right menu.

## Limits

- `C-1` and `C-2` work only while both panels are visible.
- Describe, Manifest, Logs and Info are read-only: `F3`, `F4`, `F5`,
  `F7` and `F8` do nothing there.
- `layout.leftPanelMode` and `layout.rightPanelMode` set the mode each
  panel starts in (default: left List, right Info). One panel must
  start in List. kc does not save modes between runs.

## See also

- [Panel options](panel-options.md)
- [Describe mode](mode-describe.md)
- [Manifest mode](mode-manifest.md)
- [Logs mode](mode-logs.md)
- [Info mode](mode-info.md)
- [Command mode](mode-command.md)
- [Pin and freeze](pin-freeze.md)
