# 0350. Info bars and history as Grafana images

- Status: Accepted
- Date: 2026-11-24
- PR: #350
- Builds on: 0320 (Kitty placeholders for charts), 0347 (Grafana-style chart images), 0342 (Info behind dialogs)

## Context

Since 0320 and 0347 the dashboard draws its charts as images in the
style of Grafana in a terminal that shows Kitty graphics Unicode
placeholders. Info mode, which the right panel starts in, still drew
its bars as block characters and its history as an eighth-step
sparkline in one row, next to a dashboard that looks like Grafana. The
user asked for Info to look the same: bar gauges and stat-panel
sparklines, with the text kept as text.

Info differs from the dashboard in three ways. Two Info panels can be
on screen at once, beside the dashboard's images in the same terminal.
Info lives in a panel that dialogs and menus cover, that can be
swapped, resized (`M-w`), frozen and switched to another mode.
And a narrow panel (#344) leaves little room.

## Decision

1. **Follow `charts.graphics`.** Info draws images when the dashboard
   would: `auto` in a terminal kc recognizes, `kitty` always, `text`
   never. There is no Info key of its own; the dashboard's `b` toggle
   stays the dashboard's. With images off, Info renders the same text
   as before.
2. **Pictures over the text cells.** The layout is the text layout.
   A bar gauge covers exactly the cells of the text bar; the label, the
   percentage and the numbers stay text. A sparkline takes two rows and
   exactly the cells of the text sparkline's frame; the scale
   (`top 10%`) moves next to its top row and the span (`5m of 15m`)
   next to the bottom row. Its top row keeps the text sparkline under
   the placeholders, so the line reads as text until the picture
   arrives and while a resize settles. Nothing can overflow the panel,
   and the compact layout of #344 is unchanged.
3. **The pictures.** `chart.Gauge` draws a dark rounded track
   (Grafana's panel color) filled with the level color (green, orange
   when the bar warns, red when it fails, the states of the text
   colors), with an opacity gradient and a solid end. Requests and
   limits, now part of `models.InfoBar` as marks, are ticks in the
   dashboard's request and limit colors that stick out of the track; a
   mark above 100% is an arrow at the end. `chart.Spark` draws the line
   and the fading area of a stat panel on a dark rounded panel. Both
   reuse the painter, the palette and the antialiasing of 0347, and are
   transparent outside the track and the panel, so the cell background
   of the skin shows around them.
4. **One image set per panel.** Each `Panel` gets its own
   `kittygfx.Images` with a block of 16 IDs below the dashboard's
   (`0xC0` and `0xD0`; the dashboard keeps `0xF0` and up). The set
   follows the panel object, so a swap keeps IDs apart. A transmit's
   return names its set (`Images.Owns`), and only that set records it.
5. **Routing.** The app hands every cell size report and every
   transmit return to the open dashboard and, as a
   `panelcontent.TerminalMsg`, to the widgets of both panels, before
   dialogs and the dashboard see the message. A widget that took one of
   its own invalidates the panel's render cache.
6. **Deletes.** Images are deleted when the panel leaves Info mode
   (`panelcontent.ImageOwner`), when one key's picture leaves the screen
   (`Images.Forget`: a bar of a different scope, Info loading), when the
   panel width changes, and on exit. After a width change the bars are
   text until the width has stayed put for 300 ms, as in the dashboard.
   A transmit that reaches the terminal after a delete marks its ID
   again, so the next delete covers it.
7. **Covering.** Dialogs and menus composite over the placeholders
   cell by cell; the cells beside a dialog keep their row and column
   diacritics and their image. A dialog shadow redraws the text under
   it in gray, which would make a placeholder name another image, so it
   turns placeholders into spaces (`kittygfx.Blank`). A frozen panel
   (`C-x f`) takes the widget's text view
   (`panelcontent.TextViewer`).
8. **Few transmits.** Rasters and PNGs run off the event loop, one per
   key in flight, as in 0320. A picture starts only when its raster key
   changes: a gauge's key holds the fill and the marks in whole pixels,
   and the sparkline's cells are aligned to absolute time, so a still
   history keeps its picture until a sample arrives or the window moves
   a cell. A transmit coming back never starts a raster.
9. **Review.** `TestInfoPanelLooks` draws Info panels through a fake
   Kitty terminal and, with `-dump DIR`, writes them to PNG files with
   each placeholder replaced by the tile of the image the terminal
   holds.

## Alternatives considered

- **An Info key of its own.** A second switch for the same terminal
  capability; users who set `charts.graphics: text` because their
  terminal shows boxes would have to find it too.
- **One image of the whole bar block.** Fewer images, but the text
  inside would be drawn into the picture in another font, and every
  change of a number would send the whole block.
- **A one-row sparkline image.** At 34 px per row the line has little
  room for shape; two rows show it, at the cost of one more line per
  history.
- **Sharing the dashboard's image set.** Keys would collide between the
  dashboard and two Info panels, and leaving Info would delete the
  dashboard's pictures.

## Consequences

- The history block is one line longer with images than as text.
- Each Info panel sends up to six pictures; a live Info sends a gauge
  when a bar moves by a pixel and a sparkline per new sample, a few KB
  each.
- An Info widget kept while the dashboard is open goes on updating its
  pictures, as its text does.
- A terminal that claims Kitty graphics but lacks Unicode placeholders
  shows boxes in Info too; `charts.graphics: text` fixes both.
