# 0280. No terminal graphics for charts yet

- Status: Declined
- Date: 2026-11-23
- Issue: #280

## Context

#280 asks for an optional Kitty Graphics Protocol renderer for the
full-screen charts. A bounded probe would choose between `auto`, `text`
and `kitty`. Plot rectangles would be rasterized as images, and titles,
axes and legends would stay text. Image IDs, placements, frames and
transfer volume would all be bounded, and nothing stale may survive a
redraw, resize, modal, suspend or exit.

The text charts of #252 (`internal/ui/chart`, ADR 0252) draw braille
at two by four dots per cell, with gaps, peaks, reference lines and a
cursor. #255 and #254 will add events and several series to the same
charts.

## Decision

Do not add a graphics renderer now. Close the issue; open a new one
when the conditions below hold.

## Why

- **Bubble Tea gives no ordering seam.** In the pinned v2.1.2 the
  render loop calls `p.flush()` (queued `tea.Raw` output) and then
  `renderer.flush` (cells) on the same tick (`tea.go`, the ticker case
  of the render goroutine). A full redraw after a placement can clear
  the image, and raw cursor moves invalidate the renderer's cursor
  model. Making this reliable means a renderer adapter or an upstream
  change, and owning that seam costs more than the charts it serves.
- **kc's overlays cover the plots.** Menus, the help window, toasts and
  confirm dialogs are drawn over the screen. With images, every one of
  them needs the placements hidden and restored, or correct clipping.
  This touches the modal manager and every full-screen view, not only
  the dashboard.
- **The data does not gain resolution.** The samples come from
  Prometheus steps sized to the plot width (#253). More pixels per cell
  would draw the same samples more smoothly; they would not show a
  spike the braille chart misses, since pooling keeps each column's peak.
- **The verification is manual.** The issue asks for documented direct
  terminal, SSH and multiplexer combinations. Those cannot be checked
  in CI, and an untested combination would leave debris on screen or
  lose input after a probe. That is worse than a lower-resolution chart.
- **The chart model is still moving.** Events (#255) and several series
  (#254) change what a plot contains. A second renderer should consume a
  settled model, not chase it.

## What stays possible

`chart.Chart` is a value: time range, points, hold, reference lines,
scale and cursor, with `Cell` exposing what each cell covers. A raster
backend can consume the same value later, so this decision does not
close the path; it only declines to open it before it pays off.

## When to revisit

Open a new issue when:

1. #254 and #255 have merged and the chart model is settled.
2. Bubble Tea (or a narrow adapter kc is willing to own) orders raw
   output after the cell frame, or offers a placement hook, and kc can
   prove that order on first paint, full redraw and resize in tests.
3. There is a concrete chart that text cannot show well, such as a
   heatmap of a log histogram (#273), rather than smoother lines.

## Consequences

- Charts stay text: braille, or block eighths with `b`.
- No terminal probing at startup, so no probe debris and no lost input.
- No image lifecycle to manage across modals, resize and suspend.
