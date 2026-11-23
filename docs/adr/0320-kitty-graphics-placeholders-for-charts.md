# 0320. Chart images through Kitty Unicode placeholders

- Status: Accepted
- Date: 2026-11-23
- Issue: #280 (reopened)
- Supersedes: 0280

## Context

ADR 0280 declined images for the dashboard charts for two reasons:

1. Bubble Tea v2 writes queued `tea.Raw` output and then the cell frame
   on the same tick. A placement drawn at the cursor is cleared or
   corrupted by the next full redraw, and raw cursor moves break the
   renderer's cursor model.
2. kc draws menus, the help window, toasts and dialogs over the screen.
   With placed images, each of them would have to hide and restore the
   placements.

The user asked for images again. The terminal the user works in is
iTerm2 3.7.9, which implements the Kitty graphics protocol with Unicode
placeholders (its binary decodes `U+10EEEE` cells by image ID, row and
column).

## Decision

kc draws the plot rectangle of each chart as an image shown through
Unicode placeholders, and keeps everything else as text.

- **Placeholders are text.** The image is sent once as a virtual
  placement (`a=T,U=1,C=1,c=<cols>,r=<rows>,q=2`). The screen shows it
  through ordinary cells: `U+10EEEE`, a row diacritic and a column
  diacritic, with the image ID as the 256-color foreground
  (`38;5;<id>`). The cells go through the Bubble Tea renderer like any
  text, so a redraw or a resize draws them again, and an overlay drawn
  on top replaces them. This removes both reasons of 0280: no output
  depends on the cursor, and nothing has to be hidden.
- **Every cell carries both diacritics.** The renderer redraws parts of
  lines; a cell that relied on its left neighbor would read the wrong
  row or column.
- **Only transmit and delete are raw output.** They do not depend on the
  cursor. `C=1` keeps the terminal from moving the cursor after the
  placement, which the renderer would not know about. `q=2` keeps
  replies out of the input.
- **The frame switches to a new image only once it is sent.** Each chart
  owns two IDs. A new picture goes to the hidden one; the transmit is a
  `tea.Raw` whose message comes back to Update after Bubble Tea has put
  the bytes in the output buffer, which is written before the next frame
  of cells. Only then do the placeholders switch. A picture the terminal
  still holds (the cursor moved back) switches without sending.
- **A transmit coming back sends nothing.** The live range moves with
  the clock, so a raster made when the transmit returns differs from the
  one just sent. Sending it would bring another return and another
  raster: a loop of about ten PNGs a second that kept the event loop
  seconds behind a resize, so frames of an old size landed in a smaller
  terminal. Only the messages that change the frame (a tick, a read, a
  key, a settled resize, a new cell size) send pictures.
- **A resize deletes the images and waits.** A size change deletes all
  of them, clears the screen, and draws the plots as text until the size
  has stayed put for 300 ms; then the cell size is asked again and the
  pictures of the new size go out. A drag sends no image per step, and
  no placement of an old size is left on the screen.
- **Bounded.** Four IDs (240 to 243), PNG at the terminal's cell size
  (CSI 16 t, answered as an input event; 10x20 until then), at most 1.5
  million pixels per image, sent only when the pixels change. Leaving
  the dashboard, switching to text and exiting kc delete the images
  (`a=d,d=I`).
- **The raster uses the text model.** `chart.Chart.Raster` pools the
  samples per pixel column with the same hold, scale and reference line
  values as the braille renderer. The Prometheus reads keep the braille
  resolution: images draw the same samples more smoothly, as 0280 noted.
- **Config.** `charts.graphics: auto | text | kitty`. `auto` goes by the
  environment and never queries the terminal: iTerm2 3.7 or later
  (`LC_TERMINAL`/`LC_TERMINAL_VERSION`, kept over SSH, or
  `TERM_PROGRAM`/`TERM_PROGRAM_VERSION`), kitty (`TERM=xterm-kitty`,
  `KITTY_WINDOW_ID`), Ghostty (`TERM=xterm-ghostty`,
  `TERM_PROGRAM=ghostty`). Under tmux it asks the tmux server for the
  client's terminal and `allow-passthrough`, and wraps the commands in
  the DCS passthrough.
- **`b`** switches braille, blocks, images (when available), braille.

## Alternatives considered

- **Placed images (`a=T` at the cursor).** The problems of 0280.
- **iTerm2 inline images (OSC 1337).** Cursor-placed as well, with the
  same redraw and overlay problems.
- **Sixel.** Cursor-placed, and not in iTerm2's or kitty's best path.
- **A placeholder query (`a=q,U=1`) for `auto`.** It needs a reply read
  from the input, with a timeout, while the user types. The environment
  check is enough for the terminals that support placeholders today.
- **24-bit image IDs.** A 256-color profile under tmux would downsample
  the color and change the ID; 8-bit IDs survive it.

## Consequences

- iTerm2 3.7+, kitty and Ghostty show images; every other terminal, and
  `charts.graphics: text`, keeps the text charts.
- CI checks the encoding, the cell widths through ultraviolet and
  lipgloss, the raster and the image lifecycle, but not pixels on a
  screen. A terminal that claims support and draws placeholders wrongly
  shows boxes; `b` or `charts.graphics: text` turns images off.
- A live view sends a new picture of each chart about once a second
  while its time range moves; a paused view sends nothing.
