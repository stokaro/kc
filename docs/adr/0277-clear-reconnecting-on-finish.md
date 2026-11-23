# 0277. A finished log reader is not reconnecting

- Status: Accepted
- Date: 2026-11-23
- Issue: #277

## Context

`logresume.Reader` keeps a reconnecting flag that `Sink.State` sets. Entering the backoff sets it; only a successful reopen (`StateStreaming`) clears it. A refused reopen, the last failed attempt, a stopped instance (ADR 0276) or a cancel ends Run from the backoff loop without that transition, so the flag stayed true. The full-screen viewer checked `isReconnecting` before its own error and done state, so a stream that had given up kept showing `(reconnecting)`. Both claims in the issue hold on current main.

## Decision

1. The Reader goroutine clears the flag after Run returns and before it closes the pipe. A reader of the pipe therefore sees the end only after `Reconnecting()` is false.
2. `LogsViewer.statusText` checks failed and ended before reconnecting. A stream that stopped for good reports that, whatever a reader says.
3. The state enum stays two-valued (streaming, reconnecting). A third "stopped" state for `Sink.State` was not added: the end of Run is already the end, and the merged-log mux marks its stream ended in `finish`.

## Deviations from the issue

None. The panel (Logs panel mode) reads the same Reader, so it gets the fix without its own change.

## Consequences

- `Reconnecting()` is true only while a retry is pending.
- Tests cover a refusal, running out of attempts and a close during the wait, each checked to report a pending retry first.
