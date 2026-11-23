# Log Sources

A merged log (all pods of a workload, all containers of a pod) reads one
stream per container. `M-s` in a merged logs viewer opens the "Log
sources" dialog: it says which sources the view covers, picks the ones
to follow, sets how many streams run at once, and retries failed ones.

Use it to drop a noisy sidecar from every pod, to follow two pods out of
twenty, or to find out why the view is incomplete.

## How to read it

The first line is the coverage, the same text the viewer's status line
starts with:

```
incomplete: 50 streaming, 20 waiting (limit 50), 2 failed
```

`incomplete:` appears while any selected source waits for a free slot,
waits to reconnect, or failed. Then come the counts of streaming,
reconnecting, waiting, failed, ended and excluded sources.

Below the two fields, one row per source shows its state and name; a
failed source also shows its last error. Rows that need attention come
first: failed, reconnecting, waiting, ended, excluded, then streaming.
When they do not fit, the last row says `... and N more`. The dialog
reads the stream on every draw, so it stays current while the log goes
on behind it.

## Source filter

The Sources field takes space-separated `pod[/container]` terms. Each
part is a glob (`*`, `?`, `[...]`); a missing part matches every name;
a leading `-` excludes. A source is selected when no include term exists
or one matches, and no exclude term matches.

```
-/istio-proxy      every container except the sidecar
api-0 api-1        two pods only
api-*/app          one container of the matching pods
```

The filter matches names, so it also applies to pods created later and
to restarted containers. Changing it touches only the sources it
changes: an excluded stream stops at once and ends with
`[kc] - pod/container (excluded)`, the others go on, and the view keeps
its lines. A source selected again streams from that moment, without
repeating its backlog. A `[kc] source filter: ...` line marks each
change in the log.

## Keys
- `Tab` moves between the fields and the buttons.
- `Enter` or Apply sets the filter and the limit; only what changed is
  applied.
- Retry failed queues every failed source again; each goes on after its
  last delivered line.
- `Esc` or Close closes the dialog without changes.

## Limits
- Streams at once accepts 1 to 500; `viewer.logs.maxRequests` (50) is
  the default. A higher limit starts waiting streams. A lower one stops
  nothing; new streams wait until the count drops below it.
- The filter and the limit last for this viewer, also across `F2`
  changes. They are not saved.
- Lines a source wrote while excluded are not fetched later.
- The kubectl command line shows the selector and the limit, not the
  filter.
- The Logs panel shows the coverage in its header but has no dialog.

## See also
- [Logs viewer](logs-viewer.md)
- [Logs panel mode](mode-logs.md)
