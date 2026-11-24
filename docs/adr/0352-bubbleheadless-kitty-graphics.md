# 0352. Kitty graphics in the headless driver

- Status: Accepted
- Date: 2026-11-24
- PR: #352

## Context

The screenshot catalog (`screenshots/capture.sh`) and the README images
are color snapshots that `cmd/bubbleheadless` renders from its terminal
emulator. Since ADR 0320 the dashboard draws its plots as Kitty graphics
images in iTerm2 3.7, kitty and Ghostty. The driver hides the outer
terminal from kc, so `charts.graphics: auto` drew text charts there, and
the catalog showed block and braille plots that no user of those
terminals sees. `charts.graphics: kitty` alone does not help: the
emulator (bubbleterm over `charmbracelet/x/vt`) ignores the graphics
commands and stores a placeholder as an ordinary cell, so the snapshot
would show the placeholder glyph.

The user asked for the dashboard screenshots to show the charts as a
real terminal shows them, from what kc sends, not from images pasted in
afterwards.

## Decision

bubbleheadless implements the part of the Kitty graphics protocol that
kc uses (ADR 0320), and the color snapshot draws the images.

1. **Its own PTY.** The driver opens the PTY (`creack/pty`, already in
   the module graph through bubbleterm) and starts the child on it as its
   controlling terminal, then gives the emulator a reader instead of the
   PTY (`emulator.NewFromPipes`). bubbleterm keeps its PTY private and
   x/vt's APC hook is not reachable through it, so this is the one place
   where the commands can be read.
2. **An output filter.** The reader takes `ESC _ G ... ESC \` out of the
   child's output and runs it; every other byte reaches the emulator
   unchanged. It is a byte state machine, so a sequence cut across reads
   is handled, and an ESC that is not the string terminator cancels the
   command as in a VT parser. It also hands the emulator whole grapheme
   clusters: x/vt ends a cluster at the end of every write, so a
   placeholder whose diacritics came in the next PTY read lost them, and
   one cut at the end of a row pushed the frame's border to the next
   line. The filter never cuts its reads inside a cluster and holds the
   last cluster of the output until more output follows or the output
   pauses for 20 ms.
3. **The commands.** Transmit (`a=t`, `a=T`) of PNG data (`f=100`) sent
   directly, in chunks (`m=1` up to `m=0`, each chunk decoded alone, as
   kitty does); virtual placements (`U=1` with `c`, `r`, on `a=T` or
   `a=p`); delete by ID (`d=i` keeps the data, `d=I` frees it). Deletes
   by screen position do not touch virtual placements, as in the
   protocol. No replies: kc sends `q=2`. Other formats, compression,
   files, placed images and placement IDs are not implemented; such a
   command changes nothing.
4. **Placeholders in the snapshot.** A cell whose content starts with
   U+10EEEE takes its image ID from the foreground color (16 or 256
   colors, or 24 bits) and its row, column and high ID byte from up to
   three diacritics, with kitty's rules for diacritics left out. It is
   drawn as its tile of the image, fit into the placement box with the
   aspect ratio kept and centered, over the cell background. Every other
   cell stays text, so the cursor, "no data" and menus over a chart look
   as in a terminal. A placeholder without an image is blank, never a
   missing-glyph box. The diacritics table is kittygfx's, read backwards,
   so the encoder and the decoder cannot disagree.
5. **Cell size.** The filter answers `CSI 16 t` with the snapshot's cell
   (9x18 pixels) and `CSI 14 t` with the screen in those pixels, and the
   PTY reports the same pixel size. kc then rasters each image at exactly
   the size it is drawn at, and nothing is scaled.
6. **A check for scripts.** The driver command `images` lists the images
   with their size, `shown` for those the screen shows and `scaled` for
   those not sized to the snapshot's cells. The dashboard scenes wait
   until the screen shows their images unscaled, so a capture fails
   instead of writing text charts when the images stop coming.
7. **The capture runs kc with `charts.graphics: kitty`** in its temporary
   HOME. `auto` still draws text in the driver: the outer terminal stays
   hidden.

## Alternatives considered

- **Draw the chart rasters into the PNGs after the capture.** It would
  show what kc's raster code makes, not what kc sends to a terminal and
  where it places it, and it would not see a broken transmit or a wrong
  placement.
- **Fork bubbleterm, or register x/vt's APC handler.** A fork is a
  dependency to maintain for one hook; the filter in front of the
  emulator needs nothing from it.
- **Answer the protocol's query (`a=q`).** kc's `auto` goes by the
  environment and never queries, and the driver hides the environment on
  purpose, so a reply would change nothing for kc.

## Consequences

- The catalog shows the charts as iTerm2 3.7, kitty and Ghostty draw them
  at 18-pixel cells, a device pixel ratio of 1 in ADR 0347's terms; a
  Retina terminal draws finer lines.
- The emulator's view of the PTY changed: bubbleheadless sets the window
  size itself, in the snapshot's pixels (9x18 per cell, where bubbleterm
  reported 8x16).
- A change to how kc sends images (a new key, another format) needs the
  driver to follow; its tests feed kc's own encoder through the filter,
  the emulator and the renderer, so a mismatch fails there.
