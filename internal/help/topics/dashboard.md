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

## Keys

- `Tab`, `S-Tab`: Move the focus between the charts and the pod list.
- `Enter`: Expand the focused chart to the whole screen, or collapse it. On the pod list: open the dashboard of the selected pod.
- `Left`, `Right`: Move the cursor over the chart. The first press pauses. At an edge the range moves back or forward by a quarter.
- `Home`, `End`: Cursor to the first or last cell.
- `Up`, `Down`: Select a pod in the pod list.
- `Space`: Pause or go live again. Live removes the cursor.
- `+`, `-`: Zoom in or out: 15m, 1h, 3h, 12h, 1d, 7d with Prometheus; 5m, 15m, 1h for the session history.
- `o`: Show the selected pod, else the target, in the other panel, and leave the dashboard.
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

- At small sizes only the focused widget shows; `Tab` moves to the next.
- The dashboard does not show events or changes yet, and does not follow
  the investigation window (`C-x w`).
- Requests and limits history, restarts and throttling are not charted.

Related: [Metric sources](metric-sources.md), [Info mode](mode-info.md),
[Investigation window](investigation-window.md).
