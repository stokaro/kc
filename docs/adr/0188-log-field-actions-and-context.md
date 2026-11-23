# 0188. Log field actions, request following and context

- Status: Accepted
- Date: 2026-11-23
- Issue: #188

## Context

Issue #188 asks for actions on a log record's fields in the logs viewer:
copy a value, include or exclude it, and follow a request or trace ID
(configurable field names), scoped to the selected sources and stated as
such. It also asks for context before and after a match (same source or
merged), a way back to the previous filter and record, filters built from
structured data with correct escaping, and a trace link from a URL
template.

What the code on main does (checked at fd85736):

- `logview.Query` has field terms, but `field=value` compares case-
  insensitively and splits the value at commas, so a value like `a,b` or a
  case-sensitive ID cannot be filtered exactly. A field term whose path
  holds a quoted key (`["a b"]=x`) was read as plain text, because any
  quote before the `=` made the term text.
- A path with a quoted key still joined its neighbors into one dotted key
  (`["a"]["b"]` also found a literal `"a.b"`), so a path could not name
  one value exactly.
- Each kept record gives at most one display line, and records keep their
  source, so a display line maps back to its record. Records have no
  stable identity once older ones are dropped.
- The viewer has no line cursor. The search (`F7`, `n`) has an active
  match.

## Decision

1. **An exact operator, `field=="value"`.** It matches when the field's
   text is exactly the quoted value: case, commas, spaces and regex
   characters count, and `\"` and `\\` are the only escapes. `=` keeps its
   meaning. The field operator is now found outside `[...]` steps and
   their quotes, so quoted keys can be compared.
2. **Quoted keys are exact.** A `["key"]` step is always that one key and
   no longer joins neighbors. `logview.KeyPath` writes the path of a value
   as `.a.b` when every key is a simple name, else as `.["a"]["b.c"]`.
   `logview.FieldTerm` builds the term from a path and a value, so a
   filter built from a record is never retyped or reparsed by hand.
3. **`M-d` opens the fields of the line in focus.** The line in focus is
   the active search match, else the last line while following, else the
   top line. The dialog lists the record's fields, flattened, in key
   order, and starts on the first `viewer.logs.correlationFields` field
   the record has. On a field:
   - `Enter` adds an exact include term to the filter, and `-` an exclude
     term.
   - `f` follows the request: the filter becomes that one term. The status
     line says what it searches: the kept lines, their time span when the
     stream has kubelet timestamps, and the number of sources of a merged
     log, with the excluded ones (ADR 0181).
   - `c` copies the value.
   - `t` copies the trace URL.
   - `x` shows the context from all sources, and `s` from the line's own
     container.
4. **Context is a view of kept records.** `Log.Context` returns up to 10
   records before and after the anchor, whatever the filter. In the view,
   `>` marks the anchor, `*` the lines that pass the filter, and other
   lines are indented; the source prefix stays. If older lines were
   dropped before enough context was found, a `[kc]` line says so. The view
   does not move while new lines arrive; they are kept for later.
5. **`M-z` returns.** Every action records the filter and the record
   before it, up to 20 deep. `M-z` restores the filter and scrolls to the
   record. If the record was dropped meanwhile, or the filter hides it,
   the status line says so. Records carry a sequence number (`logview.Seq`)
   that survives drops, so a dropped record is detected instead of another
   line being shown in its place.
6. **Trace links are copied, not opened.** `viewer.logs.traceURL` is a
   template with `{trace}`. The value is escaped as a URL component (`%20`
   for a space, so it is safe in a path and in a query), and the URL is
   copied with the existing clipboard path (pbcopy, OSC 52 over SSH,
   tmux). If no template is set, the template has no `{trace}`, or the
   value is empty, the dialog says so and stays open.

## Deviations from the issue

- **No browser launch.** kc often runs over SSH or inside tmux, where
  starting a browser on the host is wrong or impossible; copying the URL
  works everywhere, and it is an explicit action.
- **No time range in the trace URL.** The viewer only knows the times of
  lines when the user turned timestamps on, and trace tools find a trace by
  ID alone. `{trace}` is the only placeholder; a time range can follow
  once a target tool needs it.
- **No separate "selected record" cursor.** The search already selects a
  line, and following the end or the top line covers the rest without a
  new navigation mode.
- **Context sizes are fixed at 10 lines each way.** A setting can follow if
  10 proves wrong.
- **The Logs panel gets none of this.** It is a small preview next to the
  list; the full-screen viewer (`F3`) is where an investigation happens.
- **`=` is unchanged.** Making quoted values literal under `=` would have
  changed stored habits (`level="error,warn"`) for little gain; `==` is new
  and unambiguous.

## Consequences

- Filters built from records can be edited in the `M-g` dialog like any
  other filter; the hint there shows `key=="exact"`.
- `["a"]["b"]` no longer finds a literal `"a.b"` key. Unquoted dotted
  names (`a.b`) still find both, as before.
- `viewer.logs.correlationFields` and `viewer.logs.traceURL` are new
  settings, in sync across `Default()`, `config-default.yaml` and the
  README.
- No requests: everything works on the kept records.

Tests:

- `internal/logview`:
  - field listing;
  - exact terms over unusual keys (spaces, quotes, brackets, dots, digits)
    and values (commas, quotes, backslashes, regex characters, `!`, `=~`,
    empty, non-ASCII, tab), each refusing near misses, records without the
    field and text lines;
  - exclude terms as the opposite of include terms;
  - quoted steps not joining their neighbors;
  - context from all sources and from one source, at the edges, after
    drops, and for a dropped record.
- `internal/ui`:
  - following a trace ID and returning;
  - special values, exclude and include building on each other;
  - the context view (marks, the view staying still on new lines, a
    same-source context, returning);
  - a text line;
  - returning to a dropped record;
  - trace URL escaping and the unavailable states;
  - `M-d` through the app.
