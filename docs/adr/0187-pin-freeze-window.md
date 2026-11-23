# 0187. Pin panel targets, freeze a display, share an investigation window

- Status: Accepted
- Date: 2026-11-23
- Issue: #187

## Context

Describe, Manifest, Logs and Info panels follow the other panel's cursor.
In an investigation the operator wants to keep one workload visible while
browsing related objects, to look at logs and usage for the same period,
and to keep a result on screen while the data moves on. The issue asks for
three separate things: pin a target, freeze a display, and one time window
for the time-aware panes, each clearly labeled, with incomplete coverage
said out loud.

What the code offers, checked against main:

- Selections reach a follower panel through `Panel.NotifySelection` (from
  `PanelSelectionChangedMsg`, the mode chooser's replay and
  `followOtherPanel`) and end in `notifySelectionListeners`. Info does not
  take the selection: `infoTarget` reads the other panel's cursor on every
  refresh.
- List rows carry the object identity: `models.ObjectItem` (GVR, namespace,
  name) and `models.InstanceItem` (UID, origin). Container and log rows name
  their pod.
- `GetByGVR` reads unstructured objects from the API server, not from the
  cache. The timeline recorder (ADR 0191) already adds event handlers to the
  shared informers.
- Logs: `LogsSpec` has tail and `SinceSeconds`; the resuming stream already
  understands `SinceTime`. The panel keeps its lines within
  `viewer.logs.maxLines` and `maxMemoryMiB` (ADR 0179).
- Info keeps 15 minutes of usage samples per scope, in memory, for the
  session. There is no older history to show.
- The overview and `/diagnosis` count events and restarts over
  `overview.window`.

## Decision

1. **Pin (`C-x p`, "Pin target" in the Left and Right menus).** The key acts
   on the active panel when it is in Describe, Manifest, Logs or Info, else
   on the other panel. The pinned row replaces every selection the panel
   gets (`Panel.pinnedSelection`), so all three ways a selection arrives
   respect it. Info pins the namespace scope and the row under the other
   cursor. The frame title starts with `[pinned]`. Switching between the
   follower modes keeps the pin; List or Command mode, a context switch and
   `C-x p` again drop it. Unpinning replays the other panel's selection.
2. **Identity.** The pin records the cluster key, the full GVR, namespace,
   name and the row's UID. A container or log row pins its pod. A row
   without a UID takes the first UID the cache reports.
3. **Deletion and recreation.** `models.WatchObject` adds an event handler
   for the name to the shared informer of the type, the same mechanism the
   timeline uses, without a watch of its own. The handler stores the last
   sighting and wakes the UI through a one-slot channel. A deletion of the
   pinned UID, or a sighting of another UID, freezes the panel with the
   reason (`[pinned, replaced by uid 1a2b3c4d]`, `FROZEN ...: pinned object
   deleted`). `C-x f` refuses to thaw it, because live data under that name
   is another object; `C-x p` unpins. A sighting of the pinned UID rereads
   the pinned panel, even when the other panel shows something else.
4. **Freeze (`C-x f`, "Freeze display").** The panel keeps the rendered
   content and the frame of that moment. The header shows `FROZEN
   15:04:05 -07:00` instead of the live status, and the live footer is
   hidden. Keys and mouse do not reach the widget, and selections wait.
   Behind the picture, Logs keeps reading within its existing line and
   memory limits, and Info keeps its bounded samples. Describe and Manifest
   do not reread (`RefreshContent` is skipped), because a reread nobody
   sees costs API requests. Thawing rereads, keeps the widget's scroll,
   filter and options (the widget was never touched), and replays the other
   panel's selection if one arrived and the panel is not pinned. The
   picture is cut or padded when the panel is resized.
5. **Investigation window (`C-x w`, Command menu).** It is set in one text
   dialog: `15m` or `2h` (live, ends now, at most 7 days), `START .. END`
   or `START ..` in the local time zone (clock times are today; RFC 3339
   keeps its offset), or empty for off. `internal/investigation` parses and
   labels it, always with the zone offset. It lasts for the session.
   - **Logs panels** read from the start of the window (`SinceTime`, shown as
     `--since-time`) with the whole window as tail. While the window is set,
     the panel's own since and tail are not used. A live window computes its
     start when the target changes, so a redraw does not restart the stream.
     A window that ended does not follow, turns timestamps on, and hides
     lines stamped after the end. The header shows the window, `first line
     HH:MM:SS` when the log starts more than a minute after the window (the
     kubelet no longer has the beginning), and the count of hidden lines.
   - **Info panels** add a line under the measurement time: covered, `usage
     history from HH:MM:SS only (incomplete)`, `no usage history for it
     (incomplete)` or `no usage history yet (incomplete)`. The sparkline
     still shows its 15 minutes; Info cannot show usage from before kc
     measured it.
   - **Describe, Manifest, lists, `/overview` and `/diagnosis`** keep their
     own ranges. Describe shows whatever events the API server still keeps,
     and the overview counts over `overview.window`. They are not
     time-ranged views, so the window does not pretend to change them.
6. **Keys.** `C-x p`, `C-x f` and `C-x w` join `C-x C-d` among kc's own
   chords (`appconfig.BuiltinHotkey`). A custom command bound to one of them
   is reported at startup and ignored, as for every built-in key.

## Deviations

- **Events are not filtered by the window.** Describe's events come from
  kubectl's describe output, and the events list is a server Table. Neither
  can be narrowed to a time range without a new view, so the window does
  not claim them. The README says which panes use it.
- **Window ends in logs need timestamps.** The logs API has no "until". A
  line is cut by its kubelet timestamp, so a fixed window turns timestamps
  on. Merged streams are read past their `[pod/container]` prefix.
- **The window is not stored in saved views (#213).** A saved view is one
  panel's list location and list settings. The window spans both panels and
  usually belongs to one incident, so storing it would reopen old windows
  on new data.
- **Pinning does not start a timeline recording.** Both use the same
  informer handler, but a recording keeps revisions in memory and counts
  against `timeline.maxObjects`. `/timeline` stays an explicit choice.
- **Freeze applies to the follower modes only.** A frozen list would block
  navigation, and the Command mode has its own run and stop controls.

## Alternatives considered

- **Poll the pinned object with `GetByGVR`.** It would be one API request
  per pin per tick, against the project rule to read through the shared
  cache.
- **Freeze by stopping the widget.** A stopped Logs stream loses lines, and
  restarting it does not restore the scroll position. Keeping the widget
  live behind a picture keeps both, within the bounds the widget already
  has.
- **Snapshot the data instead of the rendered picture.** That would need a
  frozen copy of every widget's model. The picture is one screen of text,
  which is a small, fixed size.
- **A picker of preset windows.** Presets cover the live case only. One
  text field takes both kinds, shows the zone, and validates as you type.
- **Apply the window to the overview's activity window.** The overview
  engine is shared and long-lived, and its window is configuration that
  many rules depend on. Changing it per investigation is a separate
  decision.

## Consequences

- One more informer handler per pinned object, removed on unpin; no new
  watch.
- ADR 0192's capture can take "the investigation window" from here: a
  window with an explicit zone, and the pinned identities with their UIDs.
- Follow-ups:
  1. Time-ranged events (an events view or `/diagnosis` events limited to
     the window).
  2. Logs from before the kubelet's retention need a log backend, outside
     kc's scope.
  3. Offering `/timeline` for a pinned object in one key.
