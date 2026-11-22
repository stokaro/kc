# 0071. Format and filter JSON logs in the logs viewer

- Status: Accepted
- Date: 2026-11-22
- Issue: #71

## Context

Many containers write one JSON object per line. The logs viewer appended the
lines as plain text, so records were hard to read and there was no way to hide
the irrelevant ones. Issue #71 asks for a raw and a formatted view,
include/exclude filters and one expression syntax for field extraction, with
filtering that changes only the display. It leaves open the expression
language, the filter syntax, the keys, how copy tells originals from the
display, and how the work fits the open logs changes: #76 (issue #31) rewrites
`internal/ui/logs_viewer.go` to reopen streams with new options, #32 merges
several pods into one stream, and #49 puts logs in a panel.

Constraints: no new heavy dependency, no work on the update goroutine beyond
string handling, invalid JSON must stay visible, and the change must move onto
#76's viewer with a small edit.

## Decision

1. **A new package, `internal/logview`, is a pure line transformer.** It has no
   UI imports. `logview.Log` keeps every line as a `Record` (the original
   text, a `Source` with pod and container, the kubelet timestamp prefix, and
   the parsed object) plus the view settings. Each record gives at most one
   display line, so the display is rebuilt from the kept records whenever the
   mode or the query changes. The stream is never reopened for that.
2. **JSON detection.** A line is JSON when, after spaces and an optional RFC
   3339 timestamp (what `kubectl logs --timestamps` adds), it is exactly one
   JSON object. Arrays, scalars, truncated or trailing data stay text. Lines
   over 256 KiB (`MaxParseBytes`) and values nested deeper than 100 levels are
   not parsed and stay text. Keys keep their order; numbers keep their exact
   text (`json.Number`). Invalid UTF-8 inside strings becomes U+FFFD, as
   `encoding/json` does; the raw line keeps its bytes.
3. **Formatted view.** Leading columns first: time (dimmed), level (upper
   case, padded, colored by severity; pino/bunyan numbers 10-60 map to
   trace-fatal), message. The first present key of each column wins:
   `time ts timestamp @timestamp t`, `level lvl severity loglevel @l`,
   `msg message @m`. The other fields follow as `key=value` in record order,
   nested objects as dotted keys, arrays as compact JSON, and values with
   spaces, quotes, `=` or control characters Go-quoted. Control characters in
   decoded values are escaped, so a value can neither break the line nor send
   escape sequences to the terminal. Text lines are shown unchanged apart from
   invalid UTF-8. The columns are a `logview.Format` value; they are not a
   config key yet (see Consequences).
4. **Raw view** shows the original lines. The filter still applies; extraction
   does not. `M-v` switches between the two; search and follow mode stay.
5. **One query line for filters and extraction.** Terms are separated by
   spaces; a record is shown when it matches every include term and no
   exclude term:
   - `word` or `"quoted text"`: the raw line contains it, any case;
   - `/re/`: the raw line matches the RE2 expression; the term ends at the
     slash before a space or the end, so it may hold spaces and quotes;
   - `path=v1,v2`: the field equals one of the values, any case;
   - `path~re`: the field matches the expression;
   - `!term`: exclude;
   - `.path`: show these fields instead of the record (formatted view, JSON
     records only; a record with none of them is shown whole).
   Paths are a small dotted syntax: `.msg`, `.http.status`, `.items[0].name`,
   `.["key with spaces"]`, `.` for the whole record. Consecutive names also
   match a literal dotted key (`http.status` finds `{"http.status":200}`, the
   OpenTelemetry style). Field terms never match a text line: as include they
   hide text lines, as exclude they keep them; the footer shows
   `N of M lines`, so nothing disappears silently. Parse errors are shown
   under the dialog field and the dialog stays open.
6. **Keys:** `M-v` raw/formatted, `M-g` filter dialog ("grep"), `M-a` copies
   the shown lines (labeled "shown lines"), `M-A` (Meta-Shift-a, reported as
   `alt+A` or `alt+shift+a`) copies the original records that pass the
   filter, byte for byte. The footer lists all four.
7. **The filter dialog is a windowed modal over the viewer**, a
   `TextInputModal` with the query syntax as hint and live validation. Its
   background is drawn from the viewer on every frame. On main, messages go to
   the top modal; the dialog therefore passes `logStreamMsg` on to the viewer,
   so the stream keeps running and the filter applies to lines that arrive
   meanwhile.
8. **The viewer widget gets `SetLines`**, which replaces the body but keeps the
   search query (matches are recomputed) and the scroll position. A new logs
   viewer starts with no lines instead of one empty line. Search matches the
   text without ANSI codes.

## Alternatives considered

- **jq (gojq) or JMESPath.** Both are full languages and bring a dependency
  (gojq is large; go-jmespath is unmaintained). The issue needs field
  extraction, not transformation; a dotted path covers it and can grow into a
  jq subset later without breaking existing queries.
- **kubetui-style prefixes** (`log:`, `!log:`, `jq:`). More typing for the
  common case; `level=error` reads better than `jq:.level=="error"`.
- **Separate include, exclude and expression fields.** Three inputs make a
  larger dialog and the same query cannot be typed in one go. One line also
  fits a future command-line flag (`kc logs --filter`).
- **Include terms combined with OR.** OR is available inside one field term
  (`level=error,warn`); across terms AND narrows, as in search engines.
- **F8 for raw/parse, as in mc's viewer.** The function keys stay as they are
  today; Meta keys are free and do not collide with the panel F8 (delete).
- **`M-f` for the filter.** Taken by the row filter (#93).
- **Drawing the dialog inside the viewer** like the search dialog. It would
  avoid the forwarding in decision 7 but duplicate the dialog frame code; the
  modal stack already exists.
- **Parsing lazily at render time.** Toggling would reparse every line; keeping
  the parsed object costs memory but makes switching instant.

## Consequences

- Copy distinguishes the two results: `M-a` the display, `M-A` the originals.
- Memory: each kept JSON record holds its parsed object next to the raw line.
  The buffer is unbounded, as the widget's line buffer already is; the bounded
  buffer of #31 (`viewer.logs.buffer`) should trim `logview.Log` first and
  then call `SetLines`.
- Search runs on the display lines, so in the formatted view it finds the
  formatted text. The widget now strips ANSI color codes before matching, so
  the level colors (and a container's own colors) neither match nor split a
  match; this applies to every viewer.
- `Esc v`, `Esc g` and `Esc A` are not fallbacks inside the viewer: the modal
  takes `Esc` first, as it does for `M-a` today.
- The #76 and #32 follow-ups below were done when this change was merged
  onto main. For #32, `logmux.ParseFormatted` splits the colored
  `[pod/container]` prefix off each merged line, and `Format.SourceStyle`
  shows the source in the color `logmux` gives it. The status puts the view
  status before the stream state: `tail 200, formatted, 12 lines (ended)`.
- Follow-up after #76 merges (it rewrites `logs_viewer.go`):
  - in `handleChunk`, replace `v.inner.AppendLines(m.lines)` with
    `v.appendRecords(m.lines)`; route the `[kc] logs stream failed` line the
    same way so it survives a view switch;
  - in `SetLogOptions`, after clearing the content, call `v.log.Reset()` and
    `v.inner.SetLines(nil)`: a reopened stream starts with no records, the
    mode and the query stay;
  - in `statusText`, append `", " + v.logViewStatus()` and drop the
    `if base == ""` fallback this change adds to `FooterStatus`;
  - #76 sends stream messages to their viewer directly, so drop the
    `logStreamMsg` case in `logsFilterDialog.Update`.
- Follow-up after #32: pass each line's pod and container as
  `logview.Source` to `Log.Append`; the formatted and raw views then show
  `[pod/container]` in front, and `Originals` still returns the lines as read.
  Filter terms on the source (`@pod=...`) can be added then.
- Follow-up after #49: a logs panel can reuse `logview.Log` as is.
- Follow-up: config keys for the default view and the columns
  (`viewer.logs.json.mode`, `viewer.logs.json.columns`), added with #76's
  `viewer.logs.*` keys to avoid two edits of the same config block.
