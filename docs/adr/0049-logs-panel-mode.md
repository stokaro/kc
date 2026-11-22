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

7. **Multi-container pods and options are left for follow-ups.** F2 Options
   for the Logs panel (pick a container, #31's options) are not part of this
   change.

## Follow-up after #78 merges

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

- Build the spec with #76's `logsSpecWithDefaults(spec, cfg.Viewer.Logs)` in
  `App.panelLogsSource`, and open the stream with `spec.PodLogOptions()`
  instead of the local `corev1.PodLogOptions`. Since, timestamps and tail
  from `viewer.logs` then apply to the Logs panel too.
- Implement #76's `logOptionsTarget` (`LogOptions`, `SetLogOptions`) on the
  Logs widget and route F2 in Logs mode to `showViewerOptionsModal`;
  `SetLogOptions` restarts the stream with a new generation.

## Follow-up after #32 merges

- For a pod with several containers, offer "all containers" through #32's
  merger, selectable in F2 Options, next to the default container chosen
  here.

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
- **Show all containers merged for a pod.** That is #32's job; the default
  container matches `kubectl logs <pod>`.

## Consequences

- Moving through pods with a Logs panel open costs one stream per row the
  selection rests on for 300 ms, and the old stream is closed when it moves.
- The `PanelSelectionChangedMsg` case now returns its pending commands
  instead of dropping them.
- The log buffer of a stream is not bounded, as in the fullscreen viewer.
  A long-running followed stream grows until the selection moves.
- The viewer theme and wrap mode are taken when the widget is created; F2
  viewer options do not reach the Logs panel yet (see the #76 follow-up).
