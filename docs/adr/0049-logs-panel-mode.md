# 0049. Logs panel mode that follows the other panel's selection

- Status: Accepted
- Date: 2026-11-22
- Issue: #49

## Context

Describe and Manifest are panel modes that show the object selected in the
other panel. Logs were only available in the fullscreen viewer opened on a
container's `/logs` row. Issue #49 asks for a Logs panel mode: move through
pods on one side and watch the log on the other.

Open questions and constraints:

- Where the mode goes in the cycle and how it is reached.
- What a pod with several containers shows (#32 merges containers and is in
  progress in parallel).
- How the stream is switched when the selection moves, without a stream per
  row while scrolling and without I/O on the Bubble Tea goroutine.
- Two related PRs are open: #78 (issue #47) moves Describe and Manifest onto
  a background loader with a generation counter, `BackgroundMsg` delivery and
  `OnSelectionChanged` returning a `tea.Cmd`; #76 (issue #31) adds previous,
  since, timestamps and tail to `models.LogsSpec` and the logs viewer. The
  Logs mode must land on main without them and move onto them later with a
  small change.
- On main a widget message reaches only the active widget, and is dropped
  while a dialog or the fullscreen terminal is open. A stream reads with one
  command per chunk, so one dropped message ends the stream for good.

The first part (PR #109) left F2 options and multi-container pods open.
The second part adds them once #76 (log options) and #111 (`internal/logmux`)
were on main: which key opens the options in a panel where F2 is the user
menu, how they combine with the panel options, which choices survive a
selection change, and how "all containers" is streamed. Decisions 8 to 11
record it; decision 7 is superseded by them.

## Decision

1. **Mode and keys.** `PanelModeLogs` is a new panel mode, labeled "Logs",
   between Manifest and Command in `panelModeOrder`: List, Describe,
   Manifest, Logs, Command. It is reached with the existing `C-1` / `C-2`
   cycle and the panel mode chooser. No new key. Like Describe and Manifest it
   is read-only: view, copy, edit, delete and create are masked.

2. **Package.** The widget is a new package,
   `internal/ui/panelcontent/logs`. It uses the shared `viewer.Widget` in
   plain mode, as the fullscreen logs viewer does, with the same follow rule:
   new lines scroll to the end while the view is at the end; scrolling up
   pauses that and `End` resumes it.

3. **What is streamed.** `logs.Target` maps the other panel's selection to a
   `models.LogsSpec`:
   - a core/v1 pod object row: the pod, container left empty;
   - a container row (`models.ContainerItem`, which now records its
     namespace, pod and container through `SetPodContainer`/`PodContainer`):
     that container;
   - a container's `/logs` row (`models.LogsProvider`): its spec.
   Anything else shows the hint "Select a pod or container in the other panel
   to show its log." and stops the stream.
   For a pod the opener picks the container kubectl picks: the one named in
   the `kubectl.kubernetes.io/default-container` annotation when it exists in
   the pod, else the first container (`logs.DefaultContainer`). The pod is
   read from the shared cache. The stream is `tail 200` (the existing
   `models.DefaultLogsTailLines`) with follow.

4. **Switching streams.** Every target change starts a new generation. The
   old stream is canceled at once (its context; the body is closed in a
   command, since closing an HTTP body can wait for a read in progress). The
   new stream opens after 300 ms (`logs.DefaultDebounce`) of rest on the row.
   Results of an older generation (a debounce tick, an open that finishes
   late, a read that was in flight) are dropped, and a stale open or read
   closes its stream. The same target keeps its stream; entering the mode
   again restarts a stream that ended.

5. **Off the UI goroutine.** The app gives the widget a `logs.Source`. It
   runs on the UI goroutine, captures the current cluster and root context,
   and returns an `Open` that runs in a `tea.Cmd`: the cache read for the
   default container, the INFO log line, and `GetLogs(...).Stream`. Each read
   command blocks for one line and takes the complete lines already buffered,
   up to 512, so a busy log does not cost one message per line.

6. **Hooks into main.** Kept small and named so they go away with #78:
   - `App.setPanelMode` follows the other panel's selection when the panel
     enters Logs mode (covers `C-1`/`C-2` and the mode chooser).
   - The `PanelSelectionChangedMsg` case calls `Panel.FollowLogs` on the
     other panel and now returns its commands.
   - Messages of the widget implement `logs.Msg`. A `case logs.Msg` in
     `App.Update` hands them to the Logs widget of both panels
     (`App.deliverPanelLogs`, `Panel.deliverLogs`), and `passesModal` lets
     them through a dialog. The widget ignores messages of another widget.
   - The widget stops its stream in `SetFocus(false)`, which `Panel.SetMode`
     calls when the panel leaves Logs mode, and in `Teardown`.

7. **Multi-container pods and options.** Superseded by decisions 8 to 11,
   taken in the second part of #49. The first part left them out.

8. **F2 in Logs mode opens the options.** In Logs mode `F2` (also `Esc 2`
   and a click on the function bar) opens the options instead of the user
   menu, as `F2` does in the fullscreen logs viewer. The panel shows no rows
   a user menu could act on. The function bar labels the key "Options".
   `Panel.modeAction` maps the action, so every way to press F2 gets it; the
   F9 "User menu" entry is disabled in Logs mode. `M-F1` / `M-F2` and the
   F9 panel options open the same dialog for the left / right panel. No new
   key.

9. **One dialog.** The dialog has the Panel section of the other modes
   (mode, width), so F2 can leave Logs mode as in Describe and Manifest, then
   the Viewer section (theme, line mode) and the Logs section of the logs
   viewer from #76 (tail, since, timestamps, previous), and a new Container
   row. On Enter the app applies the viewer and log options first
   (`App.applyViewerSettings`), then the Panel section; a mode change there
   stops the restarted stream again. `C-s` saves theme, line mode, tail,
   since and timestamps as `viewer.*` defaults, as in the logs viewer, and
   the panel width as before. The widget implements #76's
   `logOptionsTarget` through a small adapter in `internal/ui`
   (`panelLogsOptions`), because the widget package cannot import
   `ui.LogOptions`.

10. **Container choice.** The Container row offers "Default", the pod's
    containers (init, main and ephemeral, read from the shared cache when the
    stream opens), and "All containers". "Default" is what the selected row
    names: its container, or the default container of a pod (decision 3);
    the label says which. A pod's `logs (all containers)` row is a target too
    and streams all containers by default. "All containers" uses
    `internal/logmux` through the same `startWorkloadLogs` as the fullscreen
    merged viewer (#111): one stream per started container, at most
    `viewer.logs.maxRequests` at once, containers that start later are
    followed, each line prefixed with `[pod/container]`. The prefix is plain
    in the panel, because the panel viewer shows plain text. The value is
    `logs.ContainerAll` ("*", never a valid container name).

11. **What a selection change keeps.** Tail, since and timestamps stay for
    the widget's life and apply to every pod the selection moves to.
    "All containers" stays too: it is a way to look at pods, not a property
    of one. A chosen container name and Previous belong to one row and are
    cleared when the selection moves to another row; on another pod they
    would name a container that may not exist or a history that may not
    exist. The fullscreen viewer does not save Previous as a default for the
    same reason. The widget starts with the `viewer.logs` defaults.

## Follow-up after #78 merges

Done when this change was merged onto main: the widget's messages are
`panelcontent.BackgroundMsg`, `Follow` is `OnSelectionChanged`, and the
Logs-specific delivery hooks are gone. `App.followOtherPanelLogs` stays for
the `C-1` / `C-2` cycle keys and calls `Panel.NotifySelection`.

- Make the widget's messages implement `panelcontent.BackgroundMsg` (add
  `WidgetBackground()` to `debounceMsg`, `openedMsg`, `chunkMsg`) and delete
  `logs.Msg`, `case logs.Msg` in `App.Update`, the `passesModal` entry,
  `App.deliverPanelLogs` and `Panel.deliverLogs`: `Panel.DeliverBackground`
  delivers to every widget of both panels and through dialogs.
- Rename `Widget.Follow` to `OnSelectionChanged`; it already has #78's
  signature `(context.Context, panelcontent.Selection) tea.Cmd`. Delete
  `Panel.FollowLogs` and the `FollowLogs` call in the
  `PanelSelectionChangedMsg` case; #78's `NotifySelection` returns the
  active widget's command. #78 notifies only the active widget, which is
  what the Logs widget needs.
- Keep `App.followOtherPanelLogs` only if #78's mode replay in the
  `PanelModeSelectedMsg` case does not cover `C-1`/`C-2`; on main the cycle
  keys do not replay the other panel's selection.
- Use `panelcontent.LoadingStatus` instead of the local `"loading..."`
  constant.
- The debounce stays in the widget: #78's `reload.Loader` loads a target at
  once and limits refreshes of the same target, which is not what a stream
  needs.

## Follow-up after #76 merges

Done. The stream uses `LogsSpec.PodLogOptions()`; the `viewer.logs`
defaults are the widget's initial settings; `logOptionsTarget` is
implemented by `panelLogsOptions` (decision 9). F2 does not go to
`showViewerOptionsModal` as planned here: that dialog has no Panel section,
so F2 could not leave Logs mode. `showPanelLogsOptionsModal` builds the same
Viewer and Logs sections (`App.viewerOptionsConfig`) under the Panel
section.

## Follow-up after #32 merges

Done through `internal/logmux` (#111, which took over #32's merger):
decision 10.

## Alternatives considered

- **Reuse `LogsViewer` from `internal/ui` inside the panel.** It is a modal
  model (`tea.Model`, footer hints, close callbacks) and owns one stream for
  its lifetime. The panel needs a `panelcontent.Widget` that swaps streams.
  The shared `viewer.Widget` gives the same look.
- **Push lines from a goroutine through `deps.Post`.** `Post` only queues a
  command that runs on the next message; with no other message the lines
  would wait. A read command per chunk is the pattern the fullscreen viewer
  uses and works with the Bubble Tea loop.
- **Open the stream at once, without delay.** Holding the arrow key on a
  pod list would open and cancel a stream for every row passed.
- **Put a `BackgroundMsg` type and `DeliverBackground` into `panelcontent` and
  `Panel` now.** That copies #78's API into a second PR and makes the two
  conflict on the same lines; a widget-local marker removed after #78 keeps
  the hook in one place.
- **Show all containers merged for a pod by default.** The default
  container matches `kubectl logs <pod>`; "All containers" is one F2 choice
  away, and the pod's all-containers row selects it.
- **Keep F2 as the user menu and use only `M-F1` / `M-F2`.** The logs viewer
  has its options on F2, and the Logs panel is the same viewer; the user
  menu acts on the panel's own rows, which Logs mode hides.
- **Open the viewer options dialog without the Panel section.** Simpler, but
  F2 would then be the one panel mode whose options cannot switch the mode.
- **Keep every option across selection changes.** Previous on a pod without
  a restarted container fails, and a container name from one pod rarely
  exists in the next; the stream would fail on most rows.
- **Save "All containers" as a default (`viewer.logs.allContainers`).** A new
  config key that has to stay in sync with `config-default.yaml` and the
  README, for a choice that is one key press away. Left out until someone
  asks for it.
- **Colored prefixes in the panel.** The panel uses the viewer in plain mode;
  the merged viewer's styled lines (`LogsViewer.SetStyledLines`) belong to
  the fullscreen logs model, which the panel does not use.

## Consequences

- Moving through pods with a Logs panel open costs one stream per row the
  selection rests on for 300 ms, and the old stream is closed when it moves.
- The `PanelSelectionChangedMsg` case now returns its pending commands
  instead of dropping them.
- The log buffer of a stream is not bounded, as in the fullscreen viewer.
  A long-running followed stream grows until the selection moves.
- The viewer theme and wrap mode are taken when the widget is created and
  changed with F2.
- Opening a stream reads the pod from the shared cache also for a container
  row, to fill the Container choices. When the read fails (for example
  without list access to pods), the stream still opens and the row shows
  only "Default" and "All containers".
- With "All containers" the panel runs up to `viewer.logs.maxRequests`
  streams for one pod while the selection rests on it.
- JSON log formatting, filters (`M-g`) and the raw toggle (`M-v`) of the
  fullscreen viewer are not in the Logs panel; `F3` on the logs row still
  opens the fullscreen viewer for them.
