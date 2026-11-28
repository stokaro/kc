# Log Sources

A merged log (all pods of a workload, all containers of a pod) reads one
stream per container. `M-s` in a merged logs viewer opens the "Log
sources" dialog: it says which sources the view covers, picks the ones
to follow, sets how many streams run at once, and retries failed ones.

Use it to drop a noisy sidecar from every pod, to follow two pods out of
twenty, or to find out why the view is incomplete or empty.

## How to read it

The first line is the coverage, the same text the viewer's status line
starts with:

```
incomplete: 50 streaming, 20 waiting (limit 50), 2 failed
```

`incomplete:` appears while any selected source waits for a free slot,
waits to reconnect, or failed. Then come the counts of streaming,
reconnecting, waiting, failed, ended and excluded sources. Sources
whose container waits are counted by the reason the pod status gives,
for example `0 streaming, 1 waiting (CrashLoopBackOff)`; they do not
make the view incomplete, since their next instance is followed.

Below the two fields, one row per source shows its state and name; a
failed source also shows its last error, for example
`failed  api-1/app: pods/log "api-1" is forbidden: ...`; a long error
wraps onto the next lines. Rows that need
attention come first: failed, waiting containers, reconnecting, waiting
for a slot, ended, excluded, then streaming. When they do not fit, the
last row says `... and N more`. The dialog reads the stream on every
draw, so it stays current while the log goes on behind it.

## Containers that wait

Every container of a matching pod is a source, also one that has not
started. Its row shows `waiting (<reason>)`, such as
`waiting (ContainerCreating)` or `waiting (ImagePullBackOff)`. Such a
container has no log yet, so kc asks for none; its stream starts when the
container runs. In a pod that failed before such a container ran (the
node refused it, or an init container failed), the row shows
`ended ... no log, pod Failed (OutOfcpu)`.

A container that crashed and waits to restart (`CrashLoopBackOff`) still
has the log of its last run. kc reads that previous instance once, as
`kubectl logs -p` does. Its lines come after
`[kc] + pod/container: previous instance (container waiting: CrashLoopBackOff)`
and before `[kc] - pod/container (end of the previous instance)`, and its
row says `previous instance shown`.

When the container starts again, the new instance streams live in the
same view and the row shows `streaming`, then `ended` when it stops.
Once the pod status reports the back-off again, the row shows
`waiting (CrashLoopBackOff)`; that run is not read a second time.

## Why the view is empty

Until the first stream opens, a `[kc]` line says why the view is empty,
and says it again when the reason changes:

- `no pods match selector app=api in shop yet; waiting for them`;
- `no sources selected: the filter excludes all 4`;
- `no log lines yet: 3 waiting (ImagePullBackOff)`, or `1 failed`, when
  no selected source can deliver a line.

Once a stream opened, its `[kc] +` line and the status line
(`2 streaming ... 0 lines`) show that kc follows it, also while the
container writes nothing.

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
- Only the last run of a crash-looping container can be read; the
  kubelet keeps no older ones.
- The kubectl command line shows the selector and the limit, not the
  filter.
- The Logs panel shows the coverage in its header but has no dialog.

## See also
- [Logs viewer](logs-viewer.md)
- [Logs panel mode](mode-logs.md)
