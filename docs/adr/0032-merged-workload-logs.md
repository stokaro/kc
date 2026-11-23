# 0032. Merged logs for workloads and for all containers of a pod

- Status: Accepted; "a stream is not reopened" superseded by 0180 (streams resume after a failure)
- Date: 2026-11-22
- Issue: #32

## Context

kc streams the log of one container (`pod/container/logs`). k9s opens one
view that merges the logs of every pod and container of a Deployment,
StatefulSet, DaemonSet or Job, with a prefix per line. Issue #32 asks for the
same, and leaves open whether the number of concurrent streams is limited and
whether pods that come and go are followed.

Constraints: all watching goes through the shared controller-runtime cache
(no ad-hoc watches); API work stays off the Bubble Tea update goroutine; the
kubectl command under the viewer must reproduce what it shows. PR #76
(issue #31) rewrites `internal/ui/logs_viewer.go`, moves `LogsSpec` to
`internal/models/logs_spec.go`, opens streams through an opener in a
`tea.Cmd` and numbers them with a stream generation. It is not merged, and
this change must not stack on it.

## Decision

1. **The core is a new package, `internal/logmux`.** It knows nothing about
   the UI:
   - `Mux` takes a set of `Target`s (namespace, pod, container, plus pod UID
     and restart count) and `corev1.PodLogOptions`, opens one stream per
     target through an injected `Opener`, and merges whole lines into one
     channel. `SetTargets` replaces the set at any time; `Close` cancels every
     stream.
   - `Follower` is a client-go event handler. It is registered on the shared
     pod informer (`Cluster.GetCache().GetInformer`), sees the initial list
     and every add, update and delete, and keeps the mux's target set in step.
     Pods that do not match are rejected on metadata, before any conversion.
   - `Reader` exposes the merged lines as an `io.ReadCloser`, and `Format`
     renders a line. So the existing logs viewer reads a merged stream exactly
     as it reads one container log.
2. **Which containers.** Init, main and ephemeral containers, as
   `kubectl logs --all-containers` does, but only once a container has started
   (running or terminated). A waiting container has no log, and the API
   refuses the request; it joins the set when its status changes.
3. **Following pods: yes.** New pods start streams, and deleted or relabeled
   pods leave the set. A target that leaves keeps its running stream for a
   grace of 5 seconds, so the last lines arrive; a pod leaves the cache only
   after its containers stopped, so the stream has usually ended by then. A
   stream that ends while its target stays in the set is not reopened, since a
   new stream would repeat the tail. A restarted container or a recreated pod
   has a new restart count or UID, so it is a new target with a new stream.
4. **Concurrency limit: yes, `viewer.logs.maxRequests`, default 50**, the
   stern default for following logs (kubectl's `--max-log-requests` default is
   5, too low for a Deployment with sidecars). Unlike kubectl, which refuses
   to start above the limit, kc starts the first targets in name order and
   queues the rest; a queued target starts when a stream ends. A `[kc]` line
   says how many streams wait.
5. **Line format.** `[pod/container] text`, the prefix colored. The color is
   an FNV hash of `pod/container` over ten basic ANSI colors (red and white
   left out), so a target keeps its color across restarts and viewers and the
   colors follow the terminal scheme. kc's own lines start with `[kc]`:
   `+ pod/container` when a stream opens, `- pod/container[: error]` when it
   ends, the waiting notice, and "no pods match ... yet" when the initial list
   has nothing to stream.
6. **Selecting pods.** A workload's pods are the pods in its namespace that
   match its `spec.selector`, read from the shared cache. This is what
   `kubectl logs -l` does, so the command line reproduces it exactly. An empty
   or missing selector is refused. The pod row selects its pod by name.
7. **Entry points.** The child folder of Deployments, StatefulSets,
   DaemonSets, ReplicaSets and Jobs gets a `logs` row after the child types;
   a pod's folder gets a `logs (all containers)` row after the container
   sections (the label follows #76's `logs (previous)`). `F3` opens them, as
   for a container's `logs` row. No new key.
8. **Opening.** `F3` returns a `tea.Cmd` that reads the workload from the
   cache, gets the pod informer and starts the mux; a
   `workloadLogsReadyMsg` brings the started stream back, and the update
   goroutine opens the viewer. A failure shows as a toast, like a failed
   container log today.
9. **The viewer hook is small.** `LogsViewer` gets `SetStyledLines(true)`:
   each line is appended with `viewer.Widget.AppendStyledLines(stripped,
   styled)`, so search, selection, copy and width use the plain text while the
   view shows the colors. A theme change keeps the styled lines. Nothing else
   in the viewer changes.
10. **The kubectl command.** `kubectl logs -l <selector> -n <ns>
    --all-containers --prefix --tail=200 --max-log-requests=<N> -f`, or
    `kubectl logs <pod> -n <ns> --all-containers ...` for the pod row.
    `--tail` is always printed, because kubectl's default with a selector is
    10, not all lines.

## Alternatives considered

- **A dedicated merged viewer that receives one Bubble Tea message per line
  from the mux.** It would duplicate the logs viewer (search, follow, copy,
  F2) and conflict with every line #76 changes. Reading the mux as a reader
  keeps one viewer.
- **A watch per workload with the label selector.** AGENTS.md forbids
  ad-hoc watches; the shared pod informer already exists, and the handler
  filters on metadata.
- **Refuse above the limit, as kubectl does.** In a TUI an error with no
  logs at all is worse than the first 50 streams and a line saying the rest
  wait.
- **Owner references instead of the selector.** Exact for ReplicaSets and
  Jobs, but a Deployment's pods are owned by its ReplicaSets, so it needs two
  hops, and it would make the command line (`-l`) show something else. The
  selector matches what kubectl and k9s do.
- **Reopen a stream that ended while its pod is still there.** It repeats the
  tail on every reopen; restarts already get a new stream through the
  restart count.
- **Escape codes in the raw text.** Copy strips them already, but search
  would match on shifted columns and line widths would count the escapes.
- **kubectl's prefix `[pod/<pod>/<container>]`.** Longer on every line for
  no information in a view that only shows pods.
- **One color per pod rather than per container.** The issue asks for a
  prefix per `pod/container`; a color per target keeps a pod's sidecar apart
  from its main container.

## Consequences

- `internal/logmux` is tested on its own with fake readers (merging and
  prefixes, the limit and its queue, target set changes and the grace,
  cancellation) and with fake informer events; an envtest test runs the
  follower on the real shared cache with a fake opener.
- Each merged viewer holds an event handler on the shared pod informer
  until it closes. Conversion runs only for matching pods.
- Lines of different targets interleave in arrival order; the tails of the
  first streams arrive in blocks, not sorted by time.
- The merged viewer has the stall of the current logs viewer on main: with
  the F2 dialog open on top, the next line reaches the dialog and the read
  loop stops. #76 fixes this for both.

### Follow-up after #76 merges

Done when this change was merged onto main, with one difference from step
2: `openWorkloadLogs` still resolves the workload's selector in a `tea.Cmd`
first and `workloadLogsReadyMsg` opens the viewer, because the command line
shows the selector before the stream opens. The viewer's opener then calls
`startWorkloadLogs` with the F2 options, so each change starts a new merged
stream. The selectors of the supported workloads are immutable, so the
selector does not go stale.

#76 replaces `NewLogsViewer(title, reader, cancel, theme)` with
`NewLogsViewer(title, spec models.LogsSpec, open logStreamOpener, theme)`,
where the opener runs in a `tea.Cmd` and every F2 change closes the old
stream and opens a new one under a new generation. The merged viewer plugs
into that as follows:

1. In `showWorkloadLogs` (and `openWorkloadLogs`), build the viewer with an
   opener instead of a reader:
   `open := func(ls models.LogsSpec) (io.ReadCloser, context.CancelFunc, error)`
   calls `startWorkloadLogs` with `logmux.Options.LogOptions = *ls.PodLogOptions()`
   (the mux sets `Container` per target) and returns the source and
   `func() { _ = src.Close() }`. A restart closes the old source, which removes
   its informer handler and cancels its streams, and starts a new mux, as #76
   does for one container. The spec passed to the viewer carries the
   namespace and `Follow: true`, and `logsSpecWithDefaults` applies
   `viewer.logs.tail/since/timestamps` as for a container log.
2. Since #76's opener already runs off the update goroutine, the workload
   read moves into the opener, and `workloadLogsReadyMsg`, its `App.Update`
   case and the toast go away; an open failure then shows as a line in the
   viewer, as #76 does.
3. Replace `SetCommand` with `SetCommandBuilder`, and make
   `workloadLogsCommand` take the `LogsSpec` options: print
   `--since=FormatLogsSince(...)`, `--timestamps` and `-p` the way
   `logsCommand` does, with `EffectiveTailLines` for `--tail`. Share one test
   table with `LogsSpec.PodLogOptions`, as #76 does for a container.
4. In #76's `handleChunk`, call `v.appendLines(m.lines)` instead of
   `v.inner.AppendLines(m.lines)`, so styled lines keep working.
5. Config: `LogsViewerConfig` gets #76's `Tail`, `Since` and `Timestamps`
   next to `MaxRequests`; `Default()`, `config-default.yaml` and the README
   list all four. Both PRs add the same `viewer.logs` comparison to
   `defaults_test.go`; keep one.
6. With `previous` set in F2, every stream asks for the previous instance;
   containers without one end at once with a `[kc] - pod/container: ...`
   line.

### Other follow-ups

- A pod/container picker in F2 Options (the issue's proposal), built on #76's
  Logs section: it filters the target set the follower publishes.
- `kc logs deploy/x` and `kc logs -l app=x` startup intents.
- Services (a `map` selector) and CronJobs (pods through their Jobs).
- Running/waiting counts from `Mux.Stats` in the footer next to #76's status.
- Reading several lines per message in the viewer; a merged stream can be
  busier than one container.
- `F5` save of a merged log (a snapshot without follow).
