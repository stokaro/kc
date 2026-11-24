# 0347. Chart images in the style of a Grafana panel

- Status: Accepted
- Date: 2026-11-24
- PR: #347
- Amends: 0320 ("The raster uses the text model"), 0321 (point 1, the plot area and the "no data" band)

## Context

ADR 0320 drew the plot of a dashboard chart as an image from the text
model: the samples pooled per pixel column and held over the columns
after them, a flat filled area under a solid edge, dashed reference
lines, and a gap left to the text chart. ADR 0321 kept the plot black
and the "no data" band as text over the image.

In iTerm2 this read as a console chart at a higher resolution: a black
rectangle, the usage as a step function with a bright 2 px edge and an
opaque dark green fill, and a gray slab with "no data" over the time kc
has no samples for. The user asked for charts that look like Grafana's
time series panel.

## Decision

`chart.Chart.Raster` draws a Grafana time series panel in the dark
theme. The frame, the legend, the value labels and the time axis stay
text around the image, as in 0320.

1. **Panel and grid.** The plot is Grafana's panel color `#181b1f`.
   Grid lines in Grafana's grid color (white at 9%) run through the
   value labels and the time labels. Lines are snapped to whole pixels.
2. **Labels on the grid.** A chart drawn as an image sets
   `Chart.Centered`. Zero then maps to the center of the last plot row
   and the top of the scale to the center of the first row, or of the
   second when that lets the middle labels land on row centers too. The
   value labels are the most labels, at least two rows apart, that land
   on row centers and are round numbers (two significant digits, or a
   whole or half binary unit such as `128Mi`); half the top is the
   fallback. The time axis labels round times of the clock (every 10 s
   to 1 d), each centered on its time, and leaves out a label that would
   stick out of the plot. The text chart uses the same labels, so the
   text shown while a resize settles matches the image.
3. **Lines, not steps.** The usage is a line from sample to sample
   (Grafana's default "linear"), antialiased by the distance of each
   pixel center to the segment, 1.5 px wide at a device pixel ratio
   of 1. The ratio is the cell height over 18 px, so a Retina terminal
   gets about 3 px. Samples further apart than `Hold` are not joined.
   With more samples than half the pixel columns, they are pooled per
   column first. A line of three samples or fewer, or with samples at
   least 40 px apart, also draws each sample as a point.
4. **Area.** The area under the usage fades from 50% opacity at the top
   of the scale to nothing at zero (Grafana's "opacity" gradient mode).
5. **Palette and reference lines.** Basic terminal colors map to
   Grafana's classic palette (`#73BF69`, `#FADE2A`, `#5794F2`,
   `#F2495C`, `#B877D9`). Requests, limits and allocatable are 1 px
   dashed lines. `Ref.Band` shades the plot above a line at 6%; the
   dashboard sets it on the limit, as Grafana shades a threshold.
   The legend swatches use the same colors while the charts are images.
6. **One line per row.** `Chart.Series` are thinner lines under the
   usage. On a workload, a namespace or a node, from the session
   samples, the first eight rows of the target list (pods of a
   workload, workloads otherwise) get a line each in the rest of the
   palette, and the legend counts them (`── 3 pods`). The session
   already records every row of a read under its own key (0321), so
   this reads no new data. A Prometheus read has no history per row and
   draws the sum only. The text charts and the scale ignore the series.
7. **No data is an empty plot.** A gap shows the grid. Only the words
   "no data" stay text, on the picture's background, moved off a row
   that carries a value label.
8. **The cursor stays text.** Drawing it into the picture costs a raster
   and a PNG per key press: about 15 ms for a 1600x612 plot on an M3
   Pro, two thirds of it the PNG (`BenchmarkRasterPicture`), and a
   40 KB transmit per move. That is over the 10 ms we accept per key.
   The cursor column stays a text cell, drawn as a thin light line
   (`Chart.CursorLine`) on the picture's background, and moving it still
   sends nothing.
9. **Review without a terminal.** `go test ./internal/ui/chart -run
   TestRasterLooks -args -dump DIR` writes the picture of several
   datasets inside their text labels to PNG files.

The job API of 0320 is unchanged: the raster and the PNG run off the
event loop, one picture in flight per chart, and a stale result is
dropped.

## Alternatives considered

- **2x supersampling and a downscale.** Four times the pixels to draw
  for the same result; coverage by distance gives the same edges at
  1x.
- **The cursor drawn into the image only once it rests.** It needs a
  timer and a record of which shown picture has the cursor, so the text
  column can go only when that picture is on screen. Possible later.
- **Raw RGBA with zlib (`f=32,o=z`) instead of PNG.** It would skip the
  PNG filters, but iTerm2's support for it is not verified.
- **Labels at the start and the end of the range.** They rarely fall
  on a round time, and the top bar names the range already
  (`LIVE · last 15 minutes`, `PAUSED at 20:06:21 · 15m`).

## Consequences

- The value labels of a chart drawn as images can differ from the text
  chart's, and the time axis has more labels. The text plot keeps its
  own row bands, so in the moments it shows instead of the image (until
  the first picture arrives, and while a resize settles) a line can sit
  up to a row off the label next to it.
- A live chart still sends a picture about once a second; the gradient
  and the antialiasing make it a little larger (40 KB for 1600x612).
- The per-row lines exist only for session samples of rows the
  dashboard has read; a row that left the list has no line.
