# Log Fields

`M-d` in the logs viewer opens the "Log fields" dialog for one line. It
lists every field of a JSON line with its value and turns a field into
a filter, a copied value, a trace link or a view of the lines around it.

Use it to follow one request or trace ID through the log without typing
the value, or to see what happened just before an error.

## How it works

The line in focus is the active search match (`F7`, `n`), else the last
line while the view follows, else the top line of the view. The dialog
title is the line itself, with its `[pod/container]` source in a merged
log.

Fields are listed flattened, as `path = value`, in key order. The cursor
starts on the first field named in `viewer.logs.correlationFields` that
the line has (`trace_id`, `traceId`, `request_id` and similar).

Filters built here use the exact operator, `field=="value"`: case,
commas, spaces and regex characters in the value count. They show up in
the [log filter](logs-filter.md) dialog and can be edited there.

Following a request sets the filter to that one term. The status line
then says what it searches: the kept lines, their time span when the
stream has timestamps, and the number of sources of a merged log
(excluded ones counted apart). Older history the viewer dropped, or
that it never read, is not searched.

The context view shows up to 10 lines before and after the line,
whatever the filter. `>` marks the line, `*` other lines that pass the
filter, the rest are indented context. The view stays still while new
lines arrive; they are kept for later. A `[kc]` line says when older
lines were already dropped.

## Keys
- `Up`, `Down`, `PgUp`, `PgDn` move between fields.
- `Enter` adds an exact include term for the field to the filter.
- `-` adds an exclude term.
- `f` follows the request: the filter becomes that one term.
- `c` copies the value.
- `t` copies the trace URL from `viewer.logs.traceURL`, with `{trace}`
  replaced by the value, escaped for a URL.
- `x` shows the context from all sources, `s` from the line's own
  container only.
- `Esc` closes the dialog.
- `M-z` in the viewer returns to the filter and the line before the
  last of these actions, up to 20 steps back. When that line was
  dropped or the filter hides it, the status line says so.

## Limits
- A text line has no fields; only `x` and `s` work on it.
- kc copies the trace URL; it does not open a browser. Without a
  `traceURL`, or one without `{trace}`, the dialog says so.
- The context comes from the lines the viewer keeps; it makes no
  request.
- The Logs panel has no field actions; open the full-screen viewer.

## See also
- [Logs viewer](logs-viewer.md)
- [Log filter](logs-filter.md)
- [Log sources](logs-sources.md)
