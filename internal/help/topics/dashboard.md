# Dashboard

The dashboard shows CPU and memory over time for the namespace scope of
the active panel, or for the node, workload or pod under its cursor, on
the whole screen. Open it with `C-x g`, "Dashboard" in the Command menu
(`F9`), or start kc with `kc dashboard` (`-n` for a namespace, `-A` for
all of them). `Esc` returns to the panels as you left them.

## What it shows

- **The title bar**: the target, the context, where the charts come
  from, and the state: `LIVE` with the range, or `PAUSED at` the time
  the charts stopped.
- **The status line**: the current usage with the requests and limits
  of the pods that have not terminated, and how many running pods were
  measured. With the cursor on a chart it shows the time span of the
  cursor cell and the largest value in it.
- **CPU cores** and **Memory**: the usage as a filled line, with the
  requests (yellow), the limits (red) and, for a node or all
  namespaces, the allocatable (blue) as dashed lines. The limits are not
  drawn when a pod has no limit: their sum is then no ceiling. Requests
  and limits are those of now, not of the past.
- **Events**: the Kubernetes Events of the target in the range. A
  workload's include those of its ReplicaSets or Jobs and of its pods; a
  node's include its pods'. The lane under the title is on the charts'
  cells: `▲` a warning, `◆` a rollout step (a Normal Event of a
  Deployment, ReplicaSet, StatefulSet or DaemonSet), `•` another Event.
  A repeated Event marks its first and last time. Below it the Events of
  the range are listed, newest first, with how often they happened; with
  the cursor on a chart, those of the cursor cell.
- `kept from 09:10:00` is the oldest Event the API server still keeps
  (about an hour by default). Cells before it show `·`: kc does not know
  what happened then, which is not the same as nothing.
- **Top pods by memory**: the pods that use the most memory now, from
  the metrics API. Not shown for a pod.

## Where the lines come from

When the context has a Prometheus backend (see Metric sources, `C-x m`),
the charts read it at the resolution of the screen: one sample per half
cell in braille, one per cell with blocks. Each chart title says:

- for CPU, the averaging window (`5m avg`, or longer at wide ranges);
- for memory, `peak per 2m at 15s`: the largest working set in each
  step, sampled at that resolution, so short spikes are not lost;
- the coverage: `complete` when every running pod was measured at every
  step, `partial N/M steps` when some were missing, `gaps N/M steps`
  when nothing was measured, or `coverage unknown` without
  kube-state-metrics;
- `created 09:41` when the object is younger than the range: kc does
  not show the history of an earlier object with the same name.

Without a backend, or when Prometheus fails, the charts show the
metrics API samples kc collected while the dashboard was open in this
session (one every 15 seconds, the last hour), and the title bar says
why. A chart never draws a zero for missing data: a gap is blank, and
the time axis marks it with `·`.

## The investigation window

With an investigation window set (`C-x w`), the dashboard shows it: the
title bar says `LIVE window 2h`, or `WINDOW 2026-11-23 10:00:00 ..
10:45:00 +01:00` for a fixed one. The charts, the Events and Prometheus
read that period. Zooming, panning or pausing a live window leaves it,
and the title adds `≠ window`. `w` makes the range you see the window,
so the Logs and Info panels show the same period. A window older than
the session history shows `nothing recorded in this range` rather than
moving to now.

The numbers in the status line stay those of now: the dashboard has no
snapshot of the cluster at another time.

## Keys

- `Tab`, `S-Tab`: Move the focus between the charts, the Events and the pod list.
- `Enter`: Expand the focused chart to the whole screen, or collapse it. On the pod list: open the dashboard of the selected pod.
- `Left`, `Right`: Move the cursor over the chart. The first press pauses. At an edge the range moves back or forward by a quarter.
- `Home`, `End`: Cursor to the first or last cell.
- `Up`, `Down`: Select a pod in the pod list, or an Event.
- `Space`: Pause or go live again. Live removes the cursor.
- `+`, `-`: Zoom in or out: 15m, 1h, 3h, 12h, 1d, 7d with Prometheus; 5m, 15m, 1h for the session history.
- `o`: Show the selected pod or the object of the selected Event, else the target, in the other panel, and leave the dashboard. An Event about a kind kc cannot open shows the Event. For a rollout step kc says where its revisions are: the Deployment's `/rollout`, else the object's `/timeline` if kc recorded it.
- `Enter` on an Event: the same as `o`.
- `l`: Show the logs around the selected Event, or around the cursor: the investigation window becomes five minutes either side, the object goes to the other panel, and this panel turns to Logs mode.
- `w`: Make the shown range the investigation window.
- `b`: Draw with blocks instead of braille, for fonts without braille, and back.
- `r`: Read Prometheus again now.
- `F1`: This page.
- `Esc`: Collapse an expanded chart, then remove the cursor, then leave a pod's dashboard, then close.

With the mouse: click a chart to put the cursor there, turn the wheel
over a chart to zoom, click a pod to select it and click it again to
open its dashboard.

`C-x g` on the same row reopens the last dashboard with its range, its
pause and the pod you opened. The session history is kept until kc
exits.

## Limits

- Below 17 rows only the focused widget shows; `Tab` moves to the next.
- Events come from the Kubernetes API only, and only while the API
  server keeps them.
- A Deployment's revisions are in its `/rollout`. For other kinds kc
  records revisions only while the object's `/timeline` runs, so a
  rollout before that has no recorded revision.
- Requests and limits history, restarts and throttling are not charted.

Related: [Metric sources](metric-sources.md), [Info mode](mode-info.md),
[Investigation window](investigation-window.md), [Timeline](timeline.md),
[Rollout](rollout.md).
