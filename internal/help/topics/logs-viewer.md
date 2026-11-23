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
prefix that names its source. A pod's containers are added once they
start; new pods join, deleted pods leave. Lines from kc itself start
with `[kc]`: `+ pod/container` when a stream opens, `- pod/container`
when it ends, `~` while it reconnects.

Lines that hold one JSON object are shown formatted: time, level
(colored by severity), message, then the other fields as `key=value`.
Other lines are shown as they are. `M-v` switches to the raw lines and
back; the filter, the search and follow mode stay.

The status line shows the stream options (`tail 200, since 1h,
timestamps`), the view (`formatted, 12 of 40 lines`), the filter, and
the stream state: `(connecting)`, `(reconnecting)`, `(ended)` or
`(failed)`. A merged log starts with its coverage, for example
`incomplete: 50 streaming, 20 waiting (limit 50)`.

The last line shows the `kubectl logs` command that prints the same
stream. A merged log shows it with `-l <selector> --all-containers
--prefix`.

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
