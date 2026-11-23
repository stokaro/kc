# 0321. A dashboard that looks like kc and explains itself

- Status: Accepted
- Date: 2026-11-23
- Issue: #252

## Context

The full-screen dashboard (`C-x g`, ADRs 0252 to 0255) was tested in
envtest and merged on green CI. In a real terminal it looked like
another program and was hard to read:

- No kc chrome: a gray tab strip on top, a plain key list at the
  bottom, no panel frames, no blue.
- The range was 15 minutes, the session had a minute of samples, and
  90% of each chart was empty. The scale fitted the request (0.05
  cores), so a usage of 0.002 cores was one dot.
- A dotted run on the time axis meant "no data", and nothing said so.
- The Events lane had no label, `·` meant "before the oldest kept
  Event", and the status line said `lim partial` and `3/3 pods
  measured`.
- The top pods had two number columns without headers.
- The only way to another target was the top pods list. There was no
  way up, no breadcrumb, and no workload list.
- The charts showed one sum. Which workload used the most, how close it
  was to its request or limit, and whether its pods were healthy were
  not on the screen.

## Decision

The dashboard keeps its data, its sources and the promises of ADRs 0252
to 0255, and changes how it looks and how it is navigated.

### Layout at 150x42

The screen of a namespace on a kind cluster, a minute after opening:

```
  Dashboard     Range     View     Help                                                         kind-kc-demo › namespace shop  LIVE · last 15 minutes
┌─ Workloads in shop ───────────────────────────────────────────────────┐┌─ CPU, cores · 50s of 15m collected so far ────────────────────────────────┐
│Name                      Ready    CPU  of req  Memory  of req Restarts││Now:  █ usage 0.002  ┄ request 0.04 (5% used) ↑ above                      │
│web                         2/2      0      0%    11Mi     34%        0││      ┄ limit missing on some pods                                         │
│api                         1/1  0.002     20%   588Ki      7%        0││ 0.003│                                                                   ↑│
│checkout                    0/1      -       -       -       -        0││      │▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃▃│
│  ⚠ ImagePullBackOff                                                   ││      │████████████████████████████████████████████████████████████████████│
│                                                                       ││      │████████████████████████████████████████████████████████████████████│
│Pods of Deployment web (Enter to open)                                 ││ 0.001│████████████████████████████████████████████████████████████████████│
│web-6989b69646-dgqgj        1/1      0      0%   5.6Mi     35%        0││      │████████████████████████████████████████████████████████████████████│
│web-6989b69646-fvz9j        1/1      0      0%   5.5Mi     34%        0││      │████████████████████████████████████████████████████████████████████│
│                                                                       ││     0│████████████████████████████████████████████████████████████████████│
│Containers of pod web-6989b69646-dgqgj                                 ││      └22:57:36──────────────────────22:58:01──────────────────────22:58:26│
│  nginx  ready  0 restarts  running since 18:59:30                     │└─── Metrics API, 3 samples · 3 of 3 pods measured · no Prometheus (C-x m) ─┘
│                                                                       │┌─ Memory · 50s of 15m collected so far ────────────────────────────────────┐
│Containers of pod web-6989b69646-fvz9j                                 ││Now:  █ usage 12Mi  ┄ request 56Mi (21% used) ↑ above                      │
│  nginx  ready  0 restarts  running since 18:59:32                     ││      ┄ limit missing on some pods                                         │
│                                                                       ││  20Mi│                                                                   ↑│
│                                                                       ││      │                                                                    │
│                                                                       ││      │                                                                    │
│                                                                       ││      │▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅▅│
│                                                                       ││  10Mi│████████████████████████████████████████████████████████████████████│
│                                                                       ││      │████████████████████████████████████████████████████████████████████│
│                                                                       ││      │████████████████████████████████████████████████████████████████████│
│                                                                       ││     0│████████████████████████████████████████████████████████████████████│
│                                                                       ││      └22:57:36──────────────────────22:58:01──────────────────────22:58:26│
│                                                                       │└─── Metrics API, 3 samples · 3 of 3 pods measured · no Prometheus (C-x m) ─┘
│                                                                       │┌─ Events in the last 15 minutes: 2 · ▲ warning ◆ rollout • other ──────────┐
│                                                                       ││                                                                           │
│                                                                       ││       ← 2 earlier Events, listed below                                    │
│                                                                       ││Time      Type      Reason             Count  Message                      │
│                                                                       ││22:54:40  ▲ Warning Failed              1039  Error: ImagePullBackOff      │
│                                                                       ││22:54:27  • Normal  BackOff             1038  Back-off pulling image "ngin…│
│                                                                       ││                                                                           │
│                                                                       ││                                                                           │
│                                                                       ││                                                                           │
│                                                                       ││                                                                           │
│                                                                       ││                                                                           │
│                                                                       ││                                                                           │
│                                                                       ││                                                                           │
└────────────────────────────────────── 3 workloads · 4 pods · 3 ready ─┘└──────────────────────────────── the API server keeps them since 18:59:24 ─┘
 1Help          2Range         3Pause         4Expand        5Window        6Braille       7Logs          8              9PullDn       10Close
```

1. **kc chrome.** The top row is a menu bar in kc's colors (`F9`):
   Dashboard, Range, View and Help, with the keys of each item. Right of
   the titles are the breadcrumb (context › namespace › workload › pod)
   and the state: `LIVE · last 15 minutes`, `PAUSED at 20:06:21 · 15m`,
   or the investigation window. The bottom row is the F-key bar, drawn
   by the code that draws the panels' bar, with the actions available
   now: `F1` Help, `F2` Range, `F3` Pause or Live, `F4` Expand or
   Restore, `F5` Window, `F6` Images, Blocks or Braille (the next
   drawing), `F7` Logs, `F9` PullDn,
   `F10` Close. Every widget sits in a frame on kc's blue with its title
   in the top border and a note in the bottom border. The focused
   widget's title is drawn like a panel's cursor row (black on cyan).
   Only the plot area of a chart is black. Where the terminal shows
   images (ADR 0320), the image fills that plot area; the frame, the
   legend, the value labels, the time axis, the "no data" band, the
   arrows of a line above the scale and the cursor column stay text.
   The cursor is not in the picture: `Chart.TextCells` leaves its
   column to the text chart, and the dashboard keeps a `RasterKey` of
   each picture (the points, the scale, the range and the cell size),
   so a cursor move rasterizes and sends nothing. Only new data or a
   new size sends an image.
2. **A target list for navigation.** The left column (at 120 columns
   and more; below that, under the charts) lists what the target is made
   of: the workloads of a namespace or of all namespaces (pods without a
   workload as pods), the pods of a node or a workload, the pod of a
   pod. `Up` and `Down` select, `Enter` opens the dashboard of the
   selected row, `Backspace` (or the `..` row) goes up: pod, its
   workload, the namespace scope. Going up past the target the dashboard
   opened for replaces it with its parent. `Esc` keeps its order from
   ADR 0252. When the workloads leave room in the list, the pods of the
   selected one are listed under them (`models.UsageAmounts.PodRows`),
   then the containers of the selected pod or of the selected
   workload's pods (`models.UsageRow.Containers`): ready or not, the
   restarts, the state now and why each last stopped. A short namespace
   does not leave the column empty.
   Names get their natural width first. The list asks for the width of
   its longest name and every column, up to half the screen; when that
   is not enough it drops Restarts (also written under a row that is not
   well), then CPU, before it shortens a name. A name too long even
   then is shortened in the middle, keeping the columns that leave it
   20 cells.
3. **Usage against request and limit, per row.** Each row shows CPU and
   memory now and the share of the request in use (`of req`). Over the
   request is yellow; at 90% of the limit or more, red. Above each
   chart a legend says the same for the whole target in words:
   `Now: █ usage 0.002  ┄ request 0.04 (5% used) ↑ above  ┄ limit
   missing on some pods`, with `over the request` and `near the limit` in the same
   colors. A legend wider than its chart wraps to a second or third
   line, which the plot gives up, instead of ending in `…`. With the
   cursor on, the usage is the cell's peak, `usage 0.4 (highest)`; a
   cell where nothing ran says `usage none` and one without a sample
   `usage no sample`, never `0 (highest)`. The source note in the
   bottom border shortens `3 of 3 pods measured` to `3/3 pods` and then
   drops its last notes when the chart is narrow.
4. **Health next to usage.** Each row shows the ready pods and the
   restarts, and a second line names the problem when there is one: a
   waiting reason such as `ImagePullBackOff` or `CrashLoopBackOff`,
   `Pending`, `not ready`, with the restarts and the containers last
   killed by `OOMKilled`. It comes from the pods the overview engine
   already reads (`models.UsageAmounts.Rows`); there is no new watch.
5. **A scale that fits the usage.** The value axis fits the usage with
   a tenth of headroom, and a request or limit up to three times above
   it. A line further above is not drawn; an `↑` in its color at the top
   right says it is above the chart, and the legend adds `↑ above` after
   its value. A usage of 0.002 against a request of 0.04
   fills the chart instead of one dot.
6. **A time range that fits the data.** While the session history is
   shorter than the range, a session chart starts at its first sample
   (at least 30 seconds), and the title says `1m of 15m collected so
   far`. With Prometheus or an investigation window the range is as
   chosen.
   The session history is kept per target, and each read records the
   rows of its list under their own keys too: a namespace read stores a
   sample of each of its workloads and pods (the first 64 rows, most
   memory first), a workload read one of each of its pods. A workload
   or pod opened from the list therefore starts with the minutes its
   parent already collected. The alternative, sampling only pods and
   summing them for any target, would rebuild a workload from pods that
   came and went and would need the pod list of every past sample; the
   rows are already summed by the engine, so storing them costs one
   sample per row per poll and no new reads. The store keeps up to 160
   targets, the one sampled longest ago dropped first.
7. **No data is labeled.** A plot column without a sample is shaded
   dark gray, and a run of them wide enough says `no data`. The time
   axis is a solid rule with its first, middle and last time on it.
   In text, the usage area is solid. A full braille cell is a grid of
   dots with gaps in most terminal fonts, which reads as a texture, so
   text charts start in blocks: full blocks under the edge, eighths at
   the edge. Braille stays one `F6` away for its two samples per cell
   at the edge, and its cells under the edge are full blocks too.
8. **Events with labels.** The frame title counts the Events and
   explains the marks (`▲ warning ◆ rollout • other`). The lane is on
   the charts' time axis, and the line under it names the marks that
   fit, warnings first (`Failed ×285`). Before the oldest Event the API
   server keeps, it says `no Events kept before 18:59`; when a fitted
   chart starts after some Events of the range, `← 2 earlier Events,
   listed below`: the chart may cover seconds of a range of minutes, so
   the label says where those Events are, not when the chart starts.
   The lane has no label of its own: the frame title names it. The list
   has column headers: Time, Type, Reason, Object (from 100 columns),
   Count, Message. Count shows only when an Event of the list happened
   more than once, and then says 1 for the others. With the charts side by side, the lane follows the
   left chart's axis.
9. **The plot stays separate from the frame.** `chart.Chart` stays the
   model of a chart, and `Chart.Render` draws the value labels, the plot
   cells and the time axis. The dashboard draws the frame, the title,
   the legend and the source note around it, so another renderer of the
   plot cells does not touch the chrome.

### Keys

Unchanged from ADRs 0252 and 0255, plus `Backspace` (or `^H`) to go up,
the F-keys above, `F2` for the Range menu and `F9` for the menu bar.
`Enter` on a list row drills down; on a chart it still expands it.
`Tab` moves the focus through the list, the charts and the Events.

## Consequences

- The top pods list and the status line are gone: the target list and
  the legends replace them. The list is sorted by memory, as the top
  pods were.
- The list shows the usage of now. A breakdown over time (stacked per
  workload) needs a series per row from Prometheus or the session and
  is left for later.
- `bubbleheadless` draws braille, dashes and `◆` itself, so color
  snapshots show the charts as a terminal does.
- The help page describes the new screen, the list and the keys.
