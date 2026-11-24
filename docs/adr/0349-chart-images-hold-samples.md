# 0349. Chart images hold their samples

- Status: Accepted
- Date: 2026-11-24
- PR: #349
- Amends: 0347 (point 3, "Lines, not steps")

## Context

A live dashboard chart drawn as an image always ended some seconds
before its right edge. Its samples sit at the time the Metrics API
measured them, which metrics-server takes 15 to 40 seconds before kc
reads it; Prometheus points sit on their step. The time axis ends at
now. ADR 0347 draws the usage as a line from sample to sample, so the
line stopped at the newest sample and the rest of the plot read as
missing data.

The text chart has no such strip. It holds a sample over the cells
after it for `Chart.Hold` (ADR 0177): 2.5 poll intervals for the session
samples, 1.5 steps for Prometheus. The raster of ADR 0320 did the same
per pixel column, and the raster of 0347 kept that only on its dense
path, with more samples than half the pixel columns. The reference
lines, read per pixel column, already reached the edge.

## Decision

1. **A sample holds for `Hold`.** The raster ends each run of joined
   samples with its last sample held flat for `Hold`, with the area
   under it, as the text chart holds it over its cells. The plot cuts
   what is after `End`. A newest sample fresher than `Hold` reaches the
   right edge; an older one stops `Hold` after it, and the rest of the
   plot is the empty grid with "no data", where the text chart has its
   gap.
2. **Into the range too.** The newest sample before `Start` holds into
   the range, as in the text chart.
3. **The hold is no sample.** Points stay at the sample times. The end
   of a hold gets no point and does not count towards the three samples
   or the 40 px spacing that make a line draw its points.
4. **Every line.** The per-row lines (`Chart.Series`) use the chart's
   `Hold` and hold the same way; so do Prometheus series, whose `Hold`
   the dashboard sets from the step.

## Alternatives considered

- **End the line at the newest sample, as Grafana does.** Prometheus
  scrapes arrive on time, so Grafana's lines end close to now. The
  Metrics API's samples are tens of seconds old when they arrive, so
  every live chart would end in a strip, and the image would disagree
  with the text chart and with its own reference lines.
- **Hold only the newest sample.** The image would still disagree with
  the text chart, and with its own dense path, at a gap inside the
  range.
- **Place the samples at the time kc read them.** It shifts every sample
  by the measurement age, so the chart would put the usage later than
  it happened, out of line with the Events lane under it.

## Consequences

- A sample without neighbors is a point with a short flat line after
  it, about two readings long.
- The image and the text chart cover the same time, so the words "no
  data", which the text chart places, sit in what the image leaves
  empty.
