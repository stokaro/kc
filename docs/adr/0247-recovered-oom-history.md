# 0247. Separate recovered OOM kills from current failures

- Status: Accepted
- Date: 2026-11-23
- Issue: #247

## Context

The issue reports that workload diagnosis gives an `error` for any
`OOMKilled` last termination, however old, and always says the container
"used more memory than its limit", even when it has no `limits.memory`.
Both are true on main: `terminatedOOM` returns the last termination,
the OOM branch runs before the restart-age check, and `memoryLimit`
returns an empty string that the sentence then omits. The ordinary
restart check already treats a restart older than `restartWindow` (one
hour) of a running container as history and reports nothing.

## Decision

`oomFinding` decides by the container's current state and the age of the
kill. The finding code stays `oom/<pod>/<container>` in every case.

| State                                             | Severity |
| ------------------------------------------------- | -------- |
| `state.terminated` is `OOMKilled`                 | error    |
| last state `OOMKilled`, now `CrashLoopBackOff`    | error    |
| last state `OOMKilled` within `restartWindow`, or with no finish time | warning |
| last state `OOMKilled` older than `restartWindow` | info     |

The info finding keeps the kill's time (`Observed` is `finishedAt`), the
exit code and reason as evidence, and both log rows. Its title says
"was OOMKilled 7d ago and has run since", and its next step starts with
"History, not a current failure".

The next step names `limits.memory` when the container has one. Without
one it says the kill came from a pod-level memory limit or from memory
pressure on the node, never that the container exceeded its limit.

The owning workload's "pod problems below may explain this" hint now
needs a pod finding of warning or worse, so an info history row does not
offer itself as the explanation of an unavailable Deployment.

## Alternatives considered

- Drop old OOM kills, as old ordinary restarts are dropped. An OOM kill
  says something an exit code does not (the memory sizing was too small
  once), and the issue asks to keep it with its time. An info row is
  cheap and does not count as a current failure.
- A separate age window for OOM kills, or making it configurable. The
  one-hour `restartWindow` already defines "recent" for restarts; one
  policy is easier to explain. A setting can follow if needed.
- Read the node's memory pressure or pod-level resources to say which
  limit applied. Diagnosis reads only the cached objects of the
  namespace; the next step says where to look instead.

## Consequences

- A pod that was OOM killed days ago and runs fine is no longer red in
  diagnosis, and its history is still visible.
- `lastState` keeps only the newest termination, so "info" means the
  newest termination was an OOM kill more than an hour ago and nothing
  terminated since. Earlier kills are not visible to kc.
- A pod with only an info OOM row no longer gets "No problem found";
  the row itself says it is history.
