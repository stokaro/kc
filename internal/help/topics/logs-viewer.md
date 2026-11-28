# Logs Viewer

The logs viewer streams a container log full screen and keeps following
it. `Enter` or `F3` on the `logs` row of a container opens it;
`logs (previous)` shows the instance before the last restart. `Enter` or
`F3` on the `logs` row of a Deployment, StatefulSet, DaemonSet,
ReplicaSet or Job, or on the `logs (all containers)` row of a pod, opens
a merged log of every pod and container behind it.

Use it to read a log while it grows, to narrow it down to the lines that
matter, and to copy what you found.

## How to read it

In a merged log every line starts with a colored `[pod/container]`
prefix that names its source. A pod's containers stream once they
start; a container that crashed and waits to restart shows the lines of
its last run. New pods join, deleted pods leave. Lines from kc itself
start with `[kc]`: `+ pod/container` when a stream opens, `- pod/container`
when it ends, `~` while it reconnects, and a line that says why nothing
is shown yet ([log sources](logs-sources.md)).

Lines that hold one JSON object are shown formatted: time, level
(colored by severity), message, then the other fields as `key=value`.
Other lines are shown as they are. `M-v` switches to the raw lines and
back; the filter, the search and follow mode stay.

The status line, at the right end of the top line, shows the stream
options (`tail 200, since 1h, timestamps`), the view (`formatted, 12 of
40 lines`), the filter, and the stream state: `(connecting)`,
`(reconnecting)`, `(ended)` or `(failed)`. `(reconnecting)` shows only
while a retry is pending; a stream that gave up or was refused shows
`(failed)`. A merged log starts with its coverage, for example
`incomplete: 50 streaming, 20 waiting (limit 50)` or
`0 streaming, 1 waiting (CrashLoopBackOff)`. While a search is
applied it shows the search (`/timeout (2/7)`) and, after `|`, what
the search must not hide: missing sources, a stream that is not
streaming, and dropped lines. A narrow line cuts the search first.

The line above the key bar shows the `kubectl logs` command that
prints the same stream. A merged log shows it with `-l <selector>
--all-containers --prefix`. The key bar shows the keys that fit on its
line, the most used first; this page lists them all.

When a followed stream breaks while the container still runs, kc
reconnects and goes on after the last line it showed, without repeating
it. It tries eight times over about two minutes and does not retry a
refusal such as forbidden or not found. `[kc] lines after <time> may be
missing` marks a gap the kubelet could not fill. Before each attempt kc
checks the container again: when it restarted, stopped or its pod was
replaced during the wait, `[kc] not reconnecting: ...` says why and the
stream ends, so the next instance's lines never continue the old one.

The viewer keeps the most recent lines only, up to `viewer.logs.maxLines`
(100000) and about `viewer.logs.maxMemoryMiB` MiB (64). When either limit
is passed, the oldest tenth goes and the status line counts the dropped
lines (`10000 older dropped`). A line longer than 256 KiB is cut and
ends with `[kc] line truncated: ...`.

## Keys
- `End` follows the end of the log again; scrolling up pauses it.
- `Up`, `Down`, `PgUp`, `PgDn`, `Home` and the mouse wheel scroll.
- `F7` searches; `n` or `Shift-F7` finds the next match, `N` the
  previous one.
- `F2` opens the options: tail lines, since, timestamps, previous, wrap
  and theme. A change restarts the stream from an empty view. `C-s` in
  the dialog also saves tail, since and timestamps as defaults.
- `M-v` switches between the formatted and the raw view.
- `M-g` opens the [log filter](logs-filter.md).
- `M-d` opens the [fields of a line](logs-fields.md); `M-z` returns to
  the filter and the line before the last field action.
- `M-s` opens the [log sources](logs-sources.md) of a merged log.
- `M-m` (Solo) in a merged log shows only the source of the line under
  the cursor; `M-m` again shows every source. The other sources keep
  streaming and their lines are kept, so they come back. The status
  line shows `solo: pod/container`. `M-s` is what stops a source.
- `e` goes to the next warning or error, `E` to the previous one: a
  shown record whose JSON level field (`level`, `severity` and the
  other level keys) says warn, error, fatal or panic. Text lines have
  no level; kc does not guess one from the words in them. Without a
  highlighted line the search starts at the top of the view (`e`) or
  its bottom (`E`).
- `m` marks the line in focus, or unmarks it; up to 100 lines. `u`
  goes to the next marked line, `U` to the previous one. `c` copies the
  marked lines in log order, each with its `[pod/container]` source in
  a merged log, as they arrived (with the kubelet timestamp when the
  stream has timestamps). A mark stays on its record when the filter,
  the view or Solo change; a marked line dropped to stay within the
  limits loses its mark, and kc says how many went. The status line
  counts the marks.
- The line a jump or a mark went to is highlighted, and the field and
  mark keys act on it until you scroll.
- `M-a` copies the shown lines, `M-A` the original records that pass
  the filter, unchanged. Both copy only the kept lines and say when
  older lines were dropped.
- `M-c` copies the kubectl command.
- Drag with the left mouse button to select text; releasing copies it.
- `F3`, `F10` or `Esc` close the viewer and stop the stream.

## Limits
- The command line does not show the source filter of a merged log;
  `kubectl logs` has no flag for it.
- A merged log runs at most `viewer.logs.maxRequests` (50) streams at
  once; the rest wait for a free slot.
- Lines of different sources interleave in arrival order, not by time.
- Dropped lines cannot be seen again in that view. Reopen it with a
  larger tail to fetch them from the kubelet.
- `F3` on container logs is grayed out when the API server says you may
  not read pod logs there.

## See also
- [Logs panel mode](mode-logs.md)
- [Log filter](logs-filter.md)
- [Log fields](logs-fields.md)
- [Log sources](logs-sources.md)
- [Viewer](viewer.md)
- [Viewer options](viewer-options.md)
