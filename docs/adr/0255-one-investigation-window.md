# 0255. One investigation window for the dashboard, Info and Logs

- Status: Accepted
- Date: 2026-11-23
- Issue: #255

## Context

The investigation window (`C-x w`, ADR 0187) already narrows the Logs
panels. Info only labels its session sparklines against it, and its
Prometheus history keeps the configured `metricsHistory.window`. The
dashboard (#252, ADR 0252) has its own zoom steps and pause. The user
cannot look at one incident interval across charts, logs and events,
and has no events beside the charts.

The overview engine behind Info and the dashboard already lists the
scope's Events through the shared cache. A Deployment's revisions are
in its `/rollout` folder, built from its ReplicaSets (ADR 0264). For
other kinds kc records revisions only while the object's `/timeline`
runs (ADR 0191).

## Decision

1. **One time model.** `investigation.Window` stays the only window. The
   dashboard follows it while it is set: a live window (`2h`) ends now
   and moves, a fixed one (`09:00 .. 09:30`) is shown as it is, and its
   title names it with the zone offset (`WINDOW 2026-11-23 09:00:00 ..
   09:30:00 +01:00`, `LIVE window 2h`).
   - Zooming, panning, or pausing a live window detaches the view: the
     range stays where it is and the title adds `≠ window`.
   - `w` in the dashboard makes the shown range the window, live while
     the view is live and fixed while it is paused. Logs and Info panels
     then read that period too.
   - The cursor on a fixed window keeps it. `Space` goes live and leaves
     it. Turning the window off keeps the range the view showed.
   - A window change cancels the Prometheus read in flight. A late
     result of the old range is dropped by its key.
2. **Prometheus gets explicit bounds.** The dashboard reads the window's
   start and end at the plot's resolution (`promhistory.RangeFor`).
   Info's workload history reads the window's length ending at its end
   instead of `metricsHistory.window`, and its heading names the window.
   The query budget is unchanged: still one range read per refresh, and
   at most once per step and per 15 seconds while live. A changed span
   reads at once.
3. **Local history stays where it is.** A window outside the session
   samples shows `nothing recorded in this range; kept 10:00:00–10:59:00`
   and an empty chart. It never moves to the samples there are.
4. **Events beside the charts.** The engine keeps the scope's Events
   with what they are about. A workload's Events include those of its
   ReplicaSets or Jobs and its pods, following the owner chain with UIDs,
   so the Events of a recreated object of the same name do not count. A
   node's include its pods'. At most 2000 are kept, the newest.
   - The dashboard gets an Events widget: a lane on the charts' cell grid
     (`▲` warning, `◆` rollout step, `•` other), then the Events of the
     range, or of the cursor cell, newest first, with the repeat count of
     a series.
   - Cells before the oldest Event the API server kept are `·`, and the
     title says `kept from 09:10:00`: the API server keeps Events about
     an hour by default, so an empty lane before that is unknown, not
     quiet. A range older than every kept Event says so.
5. **From a marker to the evidence.** On an Event, `o` or `Enter` shows
   the involved object in the other panel. If kc cannot open that kind,
   it shows the Event itself. For a rollout step of a Deployment, kc
   points at its `/rollout`. For other kinds it reports whether it
   recorded the object's revisions since before the Event (`/timeline`),
   or that the revision was not recorded and why.
   - `l` sets the window to five minutes either side of the Event (or the
     cursor cell) and shows the logs of its pod or workload: the object
     in the other panel, and the dashboard's panel in Logs mode
     following it.
   - Leaving keeps the dashboard's state, so `C-x g` returns to the same
     cursor and range.
6. **One cell grid.** Both charts and the lane use the narrower plot
   width (`chart.Chart.PlotCells`), so a cursor cell is one time span
   everywhere.

## Deviations from the issue

- **No backend events.** Events come from the Kubernetes API through
  the shared cache. Events stored in Prometheus, Loki or an event
  exporter need a configuration of their own; nothing asks for one yet.
- **Revisions are not opened directly.** kc says where the revisions are:
  the Deployment's `/rollout`, or the object's `/timeline` when it
  recorded them. It does not move the panel into that folder and select
  the revision of the Event's time. That needs a row lookup by time and
  a navigation path into a sub-folder that the link code does not have.
  It is a follow-up.
- **Diagnosis and Describe keep their ranges.** As in ADR 0187, they are
  not time-ranged views. A historical cursor does not change them, and
  the dashboard's status line keeps saying `now` for the current
  numbers.
- **No separate events view.** The Events widget of the dashboard and
  the existing `/overview` warnings cover it; a panel of its own would
  duplicate the Events list.

## Consequences

- The dashboard needs 17 rows for every widget at once (two charts of
  four rows, the events, the pods); below that it pages. 80x24 shows
  all.
- `w` and `l` are new keys in the dashboard. No new global chord.
- No new watch: the Events come from the cache the overview engine
  already holds.
