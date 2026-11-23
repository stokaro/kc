# Dashboard

The dashboard shows CPU and memory over time for the namespace scope of
the active panel, or for the node, workload or pod under its cursor, on
the whole screen. Open it with `C-x g`, "Dashboard" in the Command menu
(`F9`), or start kc with `kc dashboard` (`-n` for a namespace, `-A` for
all of them). `Esc` or `F10` returns to the panels as you left them.

## The screen

- **The top bar**: the dashboard's menus (`F9`), where you are
  (`kind-dev › namespace shop › Deployment web › pod web-2`), and the
  state: `LIVE · last 15 minutes`, or `PAUSED at 10:01:05 · 15m` when
  the charts stopped.
- **The list** on the left: what the target is made of, with its usage
  now. A namespace or all namespaces list their workloads, a node its
  pods, a workload its pods. Each row has the ready pods, the CPU and
  memory used, the share of the request (`of req`), and the restarts.
  A share over 100% is yellow; usage at 90% of the limit or more is
  red. A row with a problem has a second line, for example
  `⚠ CrashLoopBackOff · 7 restarts`, `1 container OOMKilled`, `Pending` or
  `not ready`. The first row, `.. up to …`, goes back to the parent.
  When the workloads leave room, the pods of the selected one are
  listed under them. The bottom border counts the pods and the ready
  ones.
- **CPU, cores** and **Memory**: the usage as a filled area, with the
  request and the limit as dashed lines, and the allocatable for a node
  or all namespaces. The line above each chart says in words what they
  are now: `Now: █ usage 0.4  ┄ request 1 (40% used)  ┄ limit 2`.
  `over the request` is yellow, `near the limit` red.
- **Events**: the Kubernetes Events of the target in the range (see
  below).

The focused frame has its title highlighted. `Tab` moves the focus.

## Reading a chart

- The scale fits the usage. A request or limit far above it is not
  drawn as a line at the top: an `↑` at the top right says it is above
  the chart, and the line above the chart says `↑ above` after its
  value.
- Each request and limit is the value of its time, so a raised request
  steps up where it changed: with Prometheus it comes from
  kube-state-metrics, else from what this session sampled. The limit is
  not drawn where a container had no limit: the sum is then no ceiling,
  and the cursor says `limit missing on some pods`. Today's values are
  never drawn over the past.
- `no data` on a gray band marks the time where nothing was measured.
  A chart never draws a zero for missing data.
- While the dashboard has collected less than the range, the chart
  starts at the first sample and the title says so: `2m of 15m
  collected so far`.
- `no request or limit history: …` means Prometheus has no
  kube-state-metrics container series for the range (or the read
  failed, with the reason). The usage still shows; the lines do not.
- The Prometheus lines sum the app containers. The numbers of now also
  count init containers, sidecars and pod overhead, so they can be
  higher.

## Events

A workload's Events include those of its ReplicaSets or Jobs and of its
pods; a node's include its pods'. The frame title counts them and
explains the marks: `▲` a warning, `◆` a rollout step (a Normal Event
of a Deployment, ReplicaSet, StatefulSet or DaemonSet), `•` another
Event. The lane at the top is on the charts' time axis, and the line
under it names the marks that fit, warnings first (`BackOff ×12`). A
repeated Event marks its first and last time.

The API server keeps Events about an hour by default; the bottom border
says since when. Before that time the lane says `no Events kept before
09:10`: kc does not know what happened then, which is not the same as
nothing. `← 3 Events before the chart starts` means the range has
Events before the first sample of a chart that is still filling; the
list shows them.

The list has the time, type, reason, object, count and message, newest
first. With the cursor on a chart it holds the Events of the cursor
cell.

## Where the lines come from

When the context has a Prometheus backend (see Metric sources, `C-x m`),
the charts read it at the resolution of the screen: one sample per half
cell in braille, one per cell with blocks. The bottom border of each
chart says:

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
session (one every 15 seconds, the last hour), and the border says
`Metrics API, 9 samples · 2 of 2 pods measured` and why.

## The investigation window

With an investigation window set (`C-x w`), the dashboard shows it: the
top bar says `LIVE window 2h`, or `WINDOW 2026-11-23 10:00:00 ..
10:45:00 +01:00` for a fixed one. The charts, the Events and Prometheus
read that period. Zooming, panning or pausing a live window leaves it,
and the top bar adds `≠ window`. `w` (`F5`) makes the range you see the
window, so the Logs and Info panels show the same period. A window
older than the session history shows `nothing recorded in this range`
rather than moving to now.

The list and the line `Now:` stay those of now: the dashboard has no
snapshot of the cluster at another time.

## Keys

- `Enter`: On a list row, open its dashboard. `..` goes up. On an Event, the same as `o`. On a chart, expand it to the whole screen, or collapse it.
- `Backspace`: Go up a level: from a pod to its workload, from a workload to the namespace.
- `Tab`, `S-Tab`: Move the focus between the list, the charts and the Events.
- `Up`, `Down`: Select a row of the list or an Event.
- `Left`, `Right`: Move the cursor over the charts. The first press pauses. At an edge the range moves back or forward by a quarter.
- `Home`, `End`: Cursor to the first or last cell.
- `Space`, `F3`: Pause or go live again. Live removes the cursor.
- `+`, `-`: Zoom in or out: 15m, 1h, 3h, 12h, 1d, 7d with Prometheus; 5m, 15m, 1h for the session history.
- `F2`: The Range menu: pick the range by name.
- `F4`: Expand the focused widget, or restore the layout.
- `o`: Show the selected row or the object of the selected Event, else the target, in the other panel, and leave the dashboard. An Event about a kind kc cannot open shows the Event. For a rollout step kc says where its revisions are: the Deployment's `/rollout`, else the object's `/timeline` if kc recorded it.
- `l`, `F7`: Show the logs around the selected Event, or around the cursor: the investigation window becomes five minutes either side, the object goes to the other panel, and this panel turns to Logs mode.
- `w`, `F5`: Make the shown range the investigation window.
- `b`, `F6`: Switch the drawing: images where the terminal shows them (see below), then braille, then blocks (for fonts without braille), then images again. The View menu picks one by name.
- `r`: Read Prometheus again now.
- `F1`: This page.
- `F9`: The menus: Dashboard, Range, View and Help.
- `Esc`: Collapse an expanded chart, then remove the cursor, then leave a drilldown, then close.
- `F10`: Close.

The bar at the bottom shows the function keys that work now.

With the mouse: click a chart to put the cursor there, turn the wheel
over a chart to zoom, click a row to select it and click it again to
open its dashboard, click a menu title or a function key.

`C-x g` on the same row reopens the last dashboard with its range, its
pause and the drilldown. The session history is kept until kc exits.

## Charts as images

In iTerm2 3.7 or later, kitty and Ghostty the plots are drawn as images
(the Kitty graphics protocol): the usage as a filled area, the requests,
limits and allocatable as dashed lines, the cursor cell lighter. The
frames, the legends, the value labels and the time axis stay text, and
so do the "no data" band, the arrows of a line above the scale, menus,
`F1` and dialogs drawn over the charts. The images show the
same samples as the text charts, only smoother.

`charts.graphics` in `~/.kc/config.yaml` decides:

- `auto` (the default): images when the terminal says it is one of
  those: `LC_TERMINAL=iTerm2` or `TERM_PROGRAM=iTerm.app` with a version
  of 3.7 or later, `TERM=xterm-kitty` or `KITTY_WINDOW_ID`,
  `TERM=xterm-ghostty` or `TERM_PROGRAM=ghostty`. kc does not query the
  terminal for this, so nothing can end up in your input.
- `text`: always braille or blocks.
- `kitty`: always images, for a terminal kc does not recognize.

Under tmux (3.3 or later) images need `set -g allow-passthrough on`, and
the terminal outside tmux must be one of those above; kc asks tmux for
both. Over SSH, `TERM=xterm-kitty` and iTerm2's `LC_TERMINAL` (sent with
the other `LC_*` variables) carry the answer to the remote kc.

If the charts show question marks, boxes or empty space instead of the
images, the terminal does not support Unicode placeholders: press `b`
or `F6` for text, or set `charts.graphics: text`.

## Limits

- Below 24 rows or 60 columns only the focused widget shows; `Tab`
  moves to the next.
- The list shows the usage of now only; there is no history per pod.
- Events come from the Kubernetes API only, and only while the API
  server keeps them.
- A Deployment's revisions are in its `/rollout`. For other kinds kc
  records revisions only while the object's `/timeline` runs, so a
  rollout before that has no recorded revision.
- Restarts and OOM kills show in the list as counts; CPU throttling and
  node pressure are not charted.

Related: [Metric sources](metric-sources.md), [Info mode](mode-info.md),
[Investigation window](investigation-window.md), [Timeline](timeline.md),
[Rollout](rollout.md).
