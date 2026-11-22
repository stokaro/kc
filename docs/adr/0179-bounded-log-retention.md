# 0179. Keep long log sessions within a memory limit

- Status: Accepted
- Date: 2026-11-22
- Issue: #179

## Context

Issue #179 says the logs viewer keeps every record it receives, so a long
session on a busy workload grows without bound, and asks for record and byte
limits in the full-screen viewer and the Logs panel, a visible notice when
records are dropped, bounded oversized lines, and copies that do not imply
dropped data is included.

What the code on main did:

- `logview.Log` (ADR 0071) appended every record and never dropped one.
- The full-screen viewer's `viewer.Widget` held each display line twice
  (`rawLines`, `lines`) and also kept the whole log as one joined string,
  `raw`, which `AppendLines` rebuilt with `+=` on every append. It also
  measured the width of every kept line on every append. Each new line
  therefore cost time and garbage proportional to the whole log. The
  full-screen viewer reads one line per message, so this was quadratic in the
  number of lines.
- The Logs panel did not use `logview` at all: it appended lines straight to
  its `viewer.Widget`, with the same costs.
- Every reader (`logmux`, the viewer, the panel) used
  `bufio.Reader.ReadString('\n')`, which holds a whole line in memory however
  long it is. The mux's 256-line output buffer bounds the number of lines in
  flight, not their size.
- Search kept one match record per match, with no bound, and measured the
  text before each match from the start of the line, which is quadratic for a
  long line with many matches.
- Nothing in kc writes logs to disk; the issue's "disk capture" has nothing
  to keep separate.

## Decision

1. **One retention policy, `logview.Window`.** A window has `Limits`
   (`MaxRecords`, `MaxBytes`) and a cost per record. When a record passes
   either limit, the window drops the oldest records until it is a tenth
   below both limits, so a full view drops in batches (one batch per ~10% of
   new lines) instead of on every line. `logview.Log` uses a window for its
   records; the Logs panel uses one for its lines. Both views therefore
   follow the same rules, the same defaults and the same configuration.

2. **The byte limit counts an estimate, `logview.Cost`:** 256 bytes of
   overhead per record, plus twice the line length (the line and its display
   line), plus the line length again for a parsed JSON object. The panel does
   not parse JSON and counts lines as text. The estimate is checked by
   `TestLogsViewerMemoryStaysBounded`, which streams 64 MiB of mixed JSON,
   text and oversized lines with an active filter and search through an
   8 MiB budget: the heap grows by about 8 MiB. Drops do not depend on the
   filter or the view mode, so changing them never drops or restores
   records.

3. **Configuration:** `viewer.logs.maxLines` (default 100000) and
   `viewer.logs.maxMemoryMiB` (default 64), per view. Zero or less means the
   default; there is no "unlimited" value, because an unbounded view is what
   the issue removes. A user who wants more history sets larger numbers.

4. **Dropping lines from the display.** `viewer.Widget.DropFirstLines(n)`
   removes the oldest display lines, moves search matches and the mouse
   selection with them, clears the dropped slots so their strings can be
   collected, and reports whether the top line of the view was among the
   dropped ones. `Log.Append` returns the number of display lines to drop
   (only records that pass the filter have one). Callers append first and
   drop second, so a batch larger than the window still ends up consistent.

5. **Where the user is reading.** While following, the view stays at the
   end. While paused, the view keeps showing the same lines when they are
   still kept. When the line at the top of the view was dropped, the view
   shows the oldest kept line, and the footer (viewer) or header (panel)
   says "view moved: its lines were dropped" until the next scroll.

6. **Incomplete history is visible.** The viewer footer reads, for example,
   `formatted, 90000 lines (10000 older dropped, 1 truncated)`; the panel
   footer appends `• 10000 older dropped`. `M-a` and `M-A` copy only kept
   lines, and the copy message says what is missing:
   `Copied original records (10000 older dropped) (90000 lines) ...`.

7. **Oversized lines.** `logview.ReadLine` replaces `ReadString` in the mux,
   the viewer and the panel. It keeps at most `logview.MaxLineBytes`
   (256 KiB, the JSON parse limit) of a line, reads and discards the rest,
   and appends `[kc] line truncated: <n> bytes, the first 262144 kept`. The
   notice is part of the record, so the raw view and `M-A` show it too: a
   copy never looks like the full line. The viewer and the panel read with
   `logview.ReadLimit` (4 KiB more), so a merged line that the mux already
   truncated is not cut again after its `[pod/container]` prefix is added.
   `Log.Append` and the panel truncate again at `ReadLimit` for lines that
   do not come from a reader. A truncated JSON line is no longer valid JSON
   and is shown as text.

8. **The display cost of an append no longer depends on the log size.** In
   plain mode (every log view) `viewer.Widget` no longer keeps the joined
   `raw` string; it joins the lines only when a theme change needs to
   highlight them. `AppendLines` measures only the new lines for the
   horizontal scroll range; `DropFirstLines` measures again only when the
   widest line was dropped.

9. **Search stays bounded.** A search records at most 100000 matches; the
   summary then shows `(n/100000+)`. Match columns are measured from the
   previous match, not from the start of the line.

## Alternatives considered

- **Bound only the transport (the mux queue).** The issue already rules this
  out: the queue is not where records accumulate.
- **A ring buffer with absolute line numbers in the viewer**, so that drops
  need no renumbering. Search matches, the selection, scroll offsets and
  wrapping all use slice indexes; changing all of them is a larger and
  riskier change than renumbering matches once per batch, which the
  hysteresis makes cheap.
- **Drop one record per new record once full.** Each drop renumbers search
  matches and may re-measure the widest line; doing that per line costs
  O(kept) per line. The tenth-of-the-limit batch gives the same bound with
  amortized constant cost.
- **Measure real memory** (`runtime.MemStats`) to decide drops. It is global
  to the process, covers other views and garbage not yet collected, and
  would make drops depend on GC timing. A deterministic per-record estimate
  is testable and predictable.
- **Show the kept range as times.** Records have a time only with
  `--timestamps` or a JSON time field, so a time range would be missing for
  many logs. A dropped count is always correct; the kept line count is
  already in the footer.
- **Spill old records to disk.** kc has no disk capture today, and writing
  logs to disk by default would put possibly sensitive data on the
  operator's disk without asking. The issue asks to keep any disk capture
  explicit; it is left for a separate feature.
- **Mark a truncated line out of band** (a flag on the record) instead of an
  in-band notice. Lines travel through the mux, a pipe and message channels
  as strings; a flag would need a new type at every hop, and an out-of-band
  flag is lost in a copy. The `[kc] ...` notice style matches the mux's
  existing `[kc] +` and `[kc] -` lines.

## Deviations from the issue

- The "retained range" is shown as the kept line count plus the dropped
  count, not as a time range (see above).
- No disk capture is added; nothing in kc writes logs to disk now.
- The byte limit is an estimate checked by a heap test, not an exact count
  of allocated bytes. Transient copies made while rebuilding the display
  after a filter or mode change (one more display copy for a moment) and
  the viewer's search matches (bounded at 100000) are not counted.

## Consequences

- A view holds at most about `maxMemoryMiB` of log (plus transients), and
  at most `maxLines` lines. Two Logs panels and a full-screen viewer each
  have their own budget, so kc can hold three times the budget at once.
- Older lines cannot be seen again in that view; reopening the stream with a
  larger tail fetches them from the kubelet again, as before.
- ADR 0071's "keeps every line" is replaced by this record for retention;
  its status line says so.
- Follow-ups: the next logs issues (#180 reconnects, #181 source selection,
  #188 field actions) build on `Log` and `Window`. A reconnect must not
  reset the window (the dropped count stays), and context lines for #188
  can only come from kept records, so "evicted context" is reported with
  `Log.Dropped`.
