# 0252. A full-screen dashboard of CPU and memory

- Status: Accepted
- Date: 2026-11-23
- Issue: #252

## Context

Info shows usage bars and 15-minute sparklines beside a list. The issue
asks for a separate full-screen view: Cluster and Workload layouts with
a status summary, CPU and memory charts, top consumers and an
evidence/events area; keyboard focus, an expanded chart, a shared
cursor, zoom and a paused state that can still be inspected; units,
scope, source and coverage on every chart; opening an object in
Commander; and a return to unchanged panels. It also asks to evaluate
ntcharts v2 against plain rendering.

The pieces exist: the overview engine (namespace-scoped caches, ADR
0182, and the access gate of ADR 0257) has every pod with its usage,
requests and limits; #253 added `promhistory.Usage` with ranges sized to
a chart, coverage and lifetime clipping; #257 tells the user which
sources a context has.

## Decision

1. **An application view, not a panel mode.** `C-x g`, "Dashboard" in
   the Command menu, and `kc dashboard` (`-n`, `-A`) open it. It takes the
   screen like the node shell does: `App.View` draws it, and while it is
   open it gets the keys and the mouse. It never touches the panels, so
   their paths, modes, marks, filters and pins are as they were when it
   closes. A help window (`F1`) opens over it.
2. **Targets.** The active panel's namespace scope, narrowed to the row
   under its cursor: a node, a workload (Deployment, StatefulSet,
   DaemonSet, ReplicaSet, Job, CronJob, by full GVR) or a pod. Enter on a
   consumer opens that pod's dashboard on a stack; it keeps the range and
   the pause.
3. **Widgets.** A title bar (target, context, source, `LIVE` or `PAUSED
   at`), a status line, a CPU chart, a memory chart and the top pods by
   memory (not for a pod). From 120 columns the charts sit side by side;
   below that they stack; when the body is too low for both, only the
   focused widget shows and `Tab` pages. At 80x24 all three fit.
4. **Data.**
   - The engine gives a new `OverviewInfo.Amounts(UsageFocus, n)`: usage,
     requests, limits (marked partial when a pod has no limit),
     allocatable for a node or the cluster, measured of running pods, the
     identity of the object, and the top pods. It is read once a second
     on the UI goroutine and does no I/O; the engine runs only while the
     dashboard is open.
   - With a Prometheus backend for the context (the `metricsHistory`
     config, `C-x m`), the view reads `Usage` once for both charts at the
     resolution of the plot width: one sample per half cell in braille. A live view rereads once its
     range moved, at most every 15 seconds and at most once per step; a
     paused view reads only a range it has not read; a failed read is
     retried after 30 seconds. Reads of the same key share one request
     for 10 seconds (`promhistory.Shared`, keyed with the cluster key and
     impersonation). A workload or pod read waits for the engine to name
     the object's UID and creation time, so history starts at the
     object's creation.
   - Without a backend, or after a failed read, the charts show the
     Metrics API samples the dashboard recorded in this session: one per
     new engine reading, the last hour, at most 32 targets. The range
     then zooms through 5m, 15m and 1h. The title bar says why.
5. **Labels.** Each chart title names the unit, the CPU averaging window
   or the memory peak resolution, the coverage (`complete`, `partial
   N/M steps`, `gaps N/M steps`, `coverage unknown`), and `created HH:MM`
   when the range was clipped. Requests, limits and allocatable are
   dashed lines of the current values; the help page says they are of
   now. A gap is blank and marked `·` on the time axis; a zero draws the
   lowest dot; a chart without values has no scale.
6. **Interaction.** `Tab`/`S-Tab` focus, `Enter` expand or drill, `Left`
   and `Right` move a cursor (the first press pauses; at an edge the
   range pans by a quarter), `Space` pause or live, `+`/`-` zoom, `o`
   open in Commander, `b` blocks or braille, `r` reread, `F1` help.
   `Esc` collapses, then removes the cursor, then leaves a drilldown,
   then closes. The mouse clicks a cell or a pod and zooms with the
   wheel. While paused the status line and the charts keep the values of
   the pause, and the cursor reads them.
7. **Open and restore.** `o` closes the dashboard and shows the object in
   the other panel, which takes the focus (the link path of the Helm and
   Event views). The model stays: `C-x g` on the same row restores the
   stack, range, pause and draw mode; another target or context starts
   anew and keeps the session history.
8. **Rendering: plain lipgloss, no chart library.** `internal/ui/chart`
   draws braille (two samples per cell, four dots high) or block eighths,
   pools samples per column by their maximum, holds a sample over finer
   columns up to a bound, leaves gaps blank, draws reference lines and a
   cursor column. It is about 300 lines with tests.

## Why not ntcharts v2

ntcharts v2.9.0 was tried in a scratch module:

- Its line charts join points across a gap; only its log axis breaks a
  line. A missing scrape would look like data.
- Its time series line mode averages the points that fall into one
  column. A memory spike between columns is averaged away, which is what
  the dashboard must not hide.
- It pulls in bubblezone v2 and golang.org/x/image, for mouse zones and
  canvas images kc does not use.
- Its model owns the axes and styles; the source, coverage and reference
  lines would sit outside it anyway.

Bending it means wrapping most of it. The own renderer keeps one place
for gaps and peaks, and leaves room for the optional graphics of #280
behind the same chart model.

## Deviations from the issue

- **No events or evidence area.** #255 brings the shared time contract
  and the events lane; adding a second event reader here would be
  replaced.
- **No series selection.** Each chart shows one series and its
  reference lines; selecting series matters once #254 adds more.
- **Requests and limits are those of now.** Their history comes from
  kube-state-metrics in #254.
- **No own window state.** The dashboard does not follow the
  investigation window (`C-x w`); #255 makes both one.
- **Not in panel history.** Opening an object goes through the existing
  link path; returning to the dashboard is `C-x g`, not a back key in
  Commander.

## Consequences

- One more full-screen view with its own keys, documented on its help
  page; `C-x g` and `kc dashboard` are new.
- While open: one overview engine for the scope (as Info), and with a
  backend five range queries per refresh (CPU, memory, memory peak,
  measured and expected pods), at most once per step and per 15
  seconds.
- A namespace-only user gets the namespace and its workloads; the
  cluster scope is refused as Info refuses it.
