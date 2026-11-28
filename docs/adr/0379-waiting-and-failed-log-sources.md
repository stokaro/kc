# 0379. Waiting containers and failed sources in merged logs

- Status: Accepted
- Date: 2026-11-28
- Issue: #379
- Supersedes: decision 2 of [0032](0032-merged-workload-logs.md) (a
  waiting container is left out)
- Amends: the source rows and the coverage of
  [0181](0181-log-sources-and-coverage.md)

## Context

The first thing to open on a failing Deployment is its logs. For pods in
CrashLoopBackOff, `logs (all containers)` and the workload's `logs` row
showed an empty viewer (`0 streaming; tail 200, formatted, 0 lines`) for
as long as it stayed open, and M-s said `no sources yet`, while the
container's own `logs` row showed the lines that explain the crash.

The cause is decision 2 of 0032: only a running or terminated container
is a source. A container in back-off spends nearly all its time waiting
(`state.waiting.reason: CrashLoopBackOff`), so the follower almost never
saw it start, and the mux never opened its log. A user who may not read
pod logs saw the same empty viewer, since no request was made that could
fail; nothing said why the view was empty.

What the API offers: the kubelet keeps the log of the last terminated
instance of a container, and `previous=true` reads it by the container ID
in `lastState.terminated`. The pod status counts a container's instances
in `restartCount`; a container in back-off reports the count of the
instance that just exited, and the next instance starts with the next
count. Both the waiting state and the last state reach kc through the
shared pod informer the follower is already registered on.

## Decision

1. **Every container of a matching pod is a source**, started or not. The
   follower publishes each target (pod UID, container, restart count, as
   before) with the state of its container: the reason it waits, and
   whether its last instance has a log (`lastState.terminated` with a
   container ID). The state is not part of the target's identity, so a
   container that starts or crashes keeps its target. A container the
   status does not list yet waits with the reason the pod is unschedulable
   (`PodScheduled=False`), or `Pending`.

2. **A waiting container that ran before is read with `previous`.** Its
   last terminated instance is the instance the target names, and it is
   read once, without follow, with the viewer's tail and since options.
   `[kc] + pod/container: previous instance (container waiting:
   CrashLoopBackOff)` marks the start and `[kc] - pod/container (end of
   the previous instance)` the end. The same rule covers other waits after
   a run, such as `CreateContainerError` on a restart.

3. **A waiting container without an earlier instance gets no request**
   (the API refuses it: "waiting to start"). It is held, takes no slot
   under the stream limit, and is queued when the pod status says the
   container runs or terminated. In a pod that is over (phase Failed or
   Succeeded) such a container never runs: the kubelet rejected the pod
   (`OutOfcpu`, no container statuses), or the pod failed before its init
   containers finished (`PodInitializing`). Its source is `ended` at once,
   and its M-s row says `no log, pod Failed (OutOfcpu)`. A container of
   such a pod whose last instance has a log is still read.

4. **When the container starts again, the source moves to the live
   stream.** The new instance has the next restart count, so it is a new
   target and gets a followed stream with `[kc] + pod/container`; the
   target of the old instance leaves the set, and its finite read has
   normally ended already (otherwise the remove grace of 0032 applies).
   The viewer keeps its lines, and M-s shows one row for the container,
   `streaming`. When the instance crashes, its stream ends (`ended` while
   the kubelet still reports the container terminated), the row shows
   `waiting (CrashLoopBackOff)` once the back-off is reported, and the
   instance is not read a second time. If the informer never saw the
   instance run (it crashed between two status updates), the waiting
   target with the new count reads it with `previous`. Either way each
   instance is read once.

   Two paths keep this when a source comes back. Retry of a source that
   failed while it followed an instance, which has since crashed, resumes
   that instance after the last line shown (the kubelet serves a waiting
   container's last instance as current), instead of reading the whole
   previous log again; a failed source whose container now waits without
   a log is held. A source selected again after the filter excluded it
   reads from that moment on (0181), so an instance that is over is not
   read again.

5. **State shown.** A source whose container waits is `waiting (<reason>)`
   in M-s, whether its previous instance is being read, was read, or does
   not exist; a row whose previous instance is shown says
   `previous instance shown`. A failure, a reconnect and a wait for a slot
   take precedence. The coverage counts these sources per reason, for
   example `0 streaming, 1 waiting (CrashLoopBackOff)`. They do not make
   the view `incomplete:`: their next instance is followed.

6. **A refused source is failed**, as in 0181: its row in M-s shows the
   refusal (`failed  pod/container: pods/log "x" is forbidden: ...`), the
   coverage counts it, and `[kc] - pod/container: <error>` marks it in the
   log. Decision 1 makes a request for a crash-looping container, so a
   user without `pods/log` now sees the refusal instead of an empty view.

7. **An empty merged view says why.** Once the informer's initial list
   was delivered, and until the first stream opens, the mux writes a
   `[kc]` line whenever the reason changes:
   - `no pods match <selector or pod> in <namespace> yet; waiting for
     them` (the notice from 0032);
   - `no sources selected: the filter excludes all N`;
   - `no log lines yet: 2 failed, 1 waiting (ImagePullBackOff)` when no
     selected source can deliver a line (each failed, waits without a
     log, or ended).

   While a source streams, reconnects, waits for a slot or reads a
   previous instance, lines can still come: the `[kc] +` line and the
   status line (`2 streaming ... 0 lines`) say so, and no further line is
   written. Once a stream opened, its `[kc] +` and `[kc] -` lines are in
   the view, and the mux explains nothing more: a crash loop that writes
   nothing would otherwise add `no log lines yet: 1 ended`, then
   `... 1 waiting (CrashLoopBackOff)`, on every restart. A notice is a
   line, so the viewer redraws and its status line catches up even when
   no container writes anything.

8. **M-s rows** come in this order: failed, waiting containers,
   reconnecting, waiting for a slot, ended, excluded, streaming. The state
   column widens to the longest state. The dialog widens to its longest
   row, within the screen, and a row that still does not fit wraps under
   the name, up to three lines, so a refusal is read in full rather than
   cut at the dialog edge. Widths are terminal cells, as the dialog cuts
   by cells.

## Alternatives considered

- **Request the current instance of a waiting container.** The kubelet
  answers a current-instance request of a container in back-off with its
  last terminated instance, so this would have worked without
  `previous`. But which instance it returns depends on the kubelet's
  state at the moment of the request: if the next instance started in
  between, it would be streamed under the old target and again under the
  new one. `previous` names the last terminated instance explicitly.
- **Put the waiting state in the target's identity.** A container that
  terminated (read as the current instance) and then waits in back-off
  would become a new target and be read again with `previous`,
  duplicating every crash.
- **One stream per container across restarts.** The stream would have to
  switch instances inside logresume, which deliberately stops at a
  restart (0180), and the restart count would stop telling instances
  apart.
- **Keep waiting containers out and only explain the empty view.** The
  view would still not show the lines that explain the crash, which is
  what the user opened it for.
- **Explain the empty view in the viewer body or status line only.** The
  viewer redraws on messages; with no line arriving it would not redraw
  when the reason changes. A `[kc]` line is a message and stays in the
  log as a record of what happened.
- **Say "no lines yet" while sources stream.** The `[kc] +` lines and the
  status line already say it; an extra line would appear in nearly every
  merged view before the tails arrive.

## Consequences

- Sidecars that are still being created, containers waiting for init
  containers (`PodInitializing`) and unscheduled pods now appear in M-s
  and in the coverage as `waiting (<reason>)`. The coverage of a starting
  workload is longer, and it is accurate.
- A crash-looping container adds `[kc] +` and `[kc] -` lines on every
  restart, at the back-off's pace (at most every five minutes once the
  back-off is at its maximum).
- If the next instance starts and crashes between the pod event and the
  kubelet's answer to a `previous` request, the request returns that next
  instance, which is then read again under its own target. The window is
  a whole run of the container.
- No new requests or watches beyond the one log request per instance: the
  states come from the shared pod informer.
- Without list and watch on pods the informer never syncs, so the view
  stays empty with no explanation, as before. Saying why needs the
  reflector's error from the shared cache; that is left for later.

Tests: `internal/logmux` covers the candidates of a pod (back-off with and
without a previous container ID, waiting to start, unscheduled, a pod
that is over), the previous read and its notices, the switch to the live
stream on restart, the terminated window and no second read after the
next crash, an instance the informer never saw run, a container that
waits and then runs, a source without a log in a failed pod, a refused
previous read as a failed source, Retry after a crash (resumed, or held
without a log), a source selected again, and the explanations (no pods,
all waiting, all failed, filter excludes all, a silent stream, a changed
reason, a silent crash loop). `internal/ui` covers the merged viewer and
M-s over a crash-looping pod, a forbidden source in M-s (also wrapped in
a narrow dialog), row wrapping by cells, the row of a source without a
log, and the empty viewer explanations; on envtest, the merged logs of a
Deployment whose pod is in back-off read through the shared pod
informer.
