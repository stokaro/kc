# 0180. Resume live logs after a connection failure

- Status: Accepted
- Date: 2026-11-23
- Issue: #180

## Context

Issue #180 asks that live log views recover from a temporary connection
failure while the container keeps running: bounded backoff, no endless
retry of permanent errors, a per-container resume cursor from kubelet
timestamps with a small overlap, bounded duplicate suppression that keeps
distinct records with the same timestamp or text, a visible possible gap,
and finite reads and terminated containers left ended.

What the code on main did: the full-screen viewer and the Logs panel opened
one stream and showed `(ended)` or `(failed)` when it stopped, whatever the
reason. The mux (merged logs) deliberately never reopened a target's
stream, because a new stream would repeat the tail. A restarted container
or a recreated pod is already a new mux target, since the target carries the
pod UID and the restart count.

A stream can stop while the container still runs when the API server or the
kubelet restarts, a proxy or load balancer drops the connection, or the
network fails. The client sees either a read error or a clean end of the
body. A clean end also happens when the container exits, and the stream
alone cannot tell these apart.

## Decision

1. **One package, `internal/logresume`, is used by the full-screen viewer,
   the Logs panel and every mux target.** `logresume.Run` reads a stream
   and reopens it. `logresume.Open` wraps it as an `io.ReadCloser` for the
   single-container viewer and panel. The mux calls `Run` per target.

2. **Only followed streams of the current instance are resumed**
   (`Follow`, not `Previous`). A finite read, a previous-instance read and
   anything kc opens without follow keep their options and end as before.

3. **The resume cursor is the kubelet timestamp of the last delivered
   line.** A resumed stream is always opened with `timestamps=true`. When
   the user did not ask for timestamps, the prefix is removed before the
   line reaches the view, so the view shows the same text as before. A
   reconnect asks for `sinceTime` = the cursor truncated to the second.
   `sinceTime` has second precision, so the overlap is up to one second; it
   also clears the tail and `sinceSeconds`, so the tail is not repeated.

4. **Duplicates are skipped by count, not by content.** After a reconnect,
   lines older than the cursor are dropped. Of the lines with exactly the
   cursor's timestamp, as many are dropped as were already delivered. The
   first newer line ends the check. Identical lines and lines that share a
   timestamp are kept, and the state is two values (a time and a count).
   Outside the window after a reconnect nothing is dropped, so a node clock
   that steps back cannot hide lines.

5. **Gap detection.** The first line of a reopened stream tells whether the
   interruption is covered. If it is at or before the cursor, the new stream
   overlaps the delivered lines and nothing is missing. If it is newer, the
   kubelet no longer has the lines around the cursor (log rotation), and kc
   writes `lines after <time> may be missing`.

6. **Reconnect only while the same instance runs.** The container state
   comes from the shared cache: the mux's `Follower` (already registered on
   the shared pod informer) for merged logs, and the shared cache's pod
   object for single streams. The decision:
   - After a read error, kc reconnects unless the cache says the container
     stopped, restarted (another restart count) or belongs to another pod
     (another UID), or the pod is gone. An unknown state counts as running.
   - After a clean end, kc reconnects only when the cache says the same
     instance still runs. Otherwise the log ended, as before.

7. **Backoff and limits.** The delay is 1 s, then 2 s, 4 s, 8 s, 16 s,
   30 s, 30 s, 30 s: eight attempts, about two minutes. The count starts
   over once a stream delivers a new line or stays up for 30 s, so a
   connection that drops right after every open still gives up. A refusal
   from the server is not retried: unauthorized, forbidden, not found, bad
   request (container waiting, unknown container), invalid, gone, method
   not supported. Run then fails with that error, which the views already
   show as a failed stream.

8. **What the user sees.**
   - Single streams get in-band notices in kc's existing style:
     `[kc] reconnecting in 2s: <error>`, `[kc] reconnected`,
     `[kc] lines after <time> may be missing`.
   - The viewer footer adds `(reconnecting)` while kc waits, and the Logs
     panel header shows `reconnecting`. `(connecting)`, `(ended)` and
     `(failed)` keep their meaning, and giving up is a failure with
     `gave up reconnecting after 8 attempts: <error>`.
   - The merged log uses a third notice sign next to `+` and `-`:
     `[kc] ~ pod/container: reconnecting in 2s: <error>` and
     `[kc] ~ pod/container: reconnected`.

9. **The view's state stays.** A reconnect happens inside the reader, so the
   view never sees a new stream: the records it keeps (the retention window
   from ADR 0179 and its dropped count), the filter, the search and the
   scroll position are not touched. Changing the options, switching the
   target or closing the view cancels the reader's context, which stops any
   wait and closes the stream.

## Alternatives considered

- **Reopen with the user's options (the tail) and drop lines by content.**
  Without timestamps a line cannot be placed, so identical lines (a
  heartbeat, a repeated error) would be dropped or duplicated. The issue
  asks for neither.
- **A content hash window for duplicates.** More memory and still wrong for
  identical lines; the timestamp plus a count is exact for kubelet output,
  where lines are in order.
- **Ask the API for the pod on every decision.** The shared cache already
  holds the pods for both the panel and the mux; a GET per failure would
  add requests exactly when the API server is in trouble.
- **Treat every clean end as a disconnect.** That would reopen the logs of
  every container that exits normally and hit the API server for nothing;
  the cache decides instead.
- **Retry forever.** A permanently broken path (a node gone without its pod
  being deleted) would keep a stream request per container going, in a view
  that may be in the background. Eight attempts and a visible failure are
  easier to understand; the user can reopen the view.
- **Expose a reconnecting count in `Mux.Stats`.** Issue #181 adds per-source
  coverage; the notices are enough until then and keep this change small.

## Consequences

- A resumed stream is always read with timestamps, about 31 bytes more per
  line on the wire; they are removed before display.
- When the cache lags behind a container exit, a clean end may be reopened
  once or a few times. The reopened stream of a terminated container returns
  only the lines kc has already seen, and they are skipped. The attempts
  stop once the cache catches up, and they are bounded either way.
- With a restarted container whose new status has not reached the cache, a
  reopened single stream can show the first lines of the new instance. The
  mux also opens the new instance as its own target, so those lines may
  appear twice. The window is the cache lag.
- Follow-ups: #181 (source coverage) can count `StateReconnecting` per
  target from `logresume.Sink.State`; the mux does not pass it on yet.
