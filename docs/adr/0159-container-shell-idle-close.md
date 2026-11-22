# 0159. Close the idle container filesystem shell

- Status: Accepted
- Date: 2026-11-22
- Issue: #159

## Context

kc lists, reads and copies container files through one `sh` per container,
run over an exec stream (`internal/podfs`). Since #141 (the fix for #133) that
stream outlives the request that opened it, and `containerSessionHandle` in
`internal/models` caches it. Folders have no close event, so the shell stayed
open until kc exited: browsing 20 pods left 20 `sh` processes and 20 exec
connections through the API server, visible in audit logs.

The issue asks to close the shell after an idle period and reopen it on the
next operation, like the debug helper, without breaking two cases: a copy
into the container that runs longer than the idle period, and an operation
that starts at the moment the idle timer fires.

Open questions: what counts as "in use", where the timer lives, which
timeout, whether it is configurable, and what happens on kc exit.

## Decision

1. **Leases on the handle.** `containerSessionHandle.acquire` returns the
   session and a `release` func. Every user holds a lease for the whole
   operation: a listing (`loadRows`), a file view (`readFileContent`, until
   the content is read) and a copy (`TransferSession`, which now returns
   `release` too; `runTransfer` defers it). The handle counts the holders.
   A held session never closes for being idle, however long the copy runs.
   `release` is idempotent.
2. **Idle timer with a generation.** When the last holder releases, the
   handle arms `time.AfterFunc(idle)`. Every `acquire` stops the timer and
   moves a generation counter under the handle's mutex; the timer callback
   closes the session only when the generation it was armed with is still
   current. An `acquire` that takes the mutex after the timer fired but
   before its callback ran therefore keeps the session open for itself, and
   an `acquire` that comes after the close opens a new shell. No operation
   can get a session that the timer is about to close.
3. **Timeout: 60 s, the helper's.** `podfs.SessionIdleTimeout` is defined from
   `helperIdleTimeout`, the time the debug helper waits for a heartbeat. A
   shell through the helper is dead after that anyway, and a direct shell
   goes idle on the same schedule. It is a constant, not a config key: no
   user asked to tune it, and the helper's timeout is not configurable
   either. There is therefore no new default in `pkg/appconfig`.
4. **Close on exit.** When a session opens, the handle registers
   `context.AfterFunc(Deps.Ctx, Close)` and removes it when the session
   closes, so hooks do not pile up while the user browses many containers.
   kc cancels `Deps.Ctx` on exit, which closes every open shell and disarms
   its timer. `acquire` after that fails instead of opening a new shell.

## Alternatives considered

- **Close when the folder is left.** Folders have no close event, the same
  handle is shared by every folder level of one container, and history
  (`M-y`) can return to a folder; adding a lifecycle hook to the navigator
  is a much larger change.
- **Timer inside `podfs` (a wrapping `ExecSession`).** `podfs` would have to
  guess when an operation ends: `ReadFile` returns a reader used after the
  call, and a copy runs many calls plus extra tar streams. The handle knows
  the operation boundaries, and it already owns caching and invalidation.
- **Keep the old `Session` and close on the next use after the timeout
  ("lazy expiry").** It needs no timer, but the shell of a container the
  user never comes back to stays open until kc exits, which is the problem
  the issue describes.
- **Close at the timer and let the next operation retry on the transient
  error.** The copy path (`TransferSession`) has no retry, as #133 showed,
  and a copy in progress would be cut.
- **A config key for the timeout.** Possible later; one value tied to the
  helper keeps both idle periods aligned and adds nothing to keep in sync.

## Consequences

- A container visited more than 60 s ago has no open `sh`; the next listing
  pays one exec round trip to open a new shell.
- Through the debug helper, the helper exits about when the shell closes, as
  before. A new listing after that asks for consent again if the helper has
  exited (unchanged behavior).
- `Invalidate` (after a transient error or a canceled copy) still closes the
  shell at once, even if another operation holds it; that path is unchanged.
- Code that takes the filesystem session must use `acquire` and release it;
  `TransferSession` callers must call the returned `release`.
