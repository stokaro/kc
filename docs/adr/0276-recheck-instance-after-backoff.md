# 0276. Check the container instance again after the reconnect wait

- Status: Accepted
- Date: 2026-11-23
- Issue: #276

## Context

ADR 0180 resumes a followed container log only while the same instance runs: same pod UID, same restart count, running. `logresume` checked that when a stream stopped, then waited (1s, 2s, ... up to 30s) and reopened without checking again. A restart or a new pod that the shared cache saw during the wait was then streamed as if it continued the old instance, until that stream ended. The issue's static trace is correct on current main; the new test reproduces it.

## Decision

1. After the wait and before the reopen, `reconnect` asks the checker again. When the state is known and the instance is over, Run ends without opening:
   - pod or container gone (zero instance);
   - another pod UID;
   - another restart count;
   - not running.
2. The end is reported in the stream as `[kc] not reconnecting: the container restarted` (or `the pod was replaced`, `the container stopped`, `the pod or container is gone`). The user saw `reconnecting in Ns` just before, so a silent end would read like a hang.
3. An unknown state (cache read failed) keeps the current rule for failures: reconnect.
4. Opening the next instance stays an explicit user action (reopen the logs), as before.

## Deviations from the issue

None in behavior. The issue lists "cancellation" among the cases to test; the existing `TestResumeCancelStopsTheRetry` already covers a cancel during the wait, so no second test was added for it.

## Consequences

- One more cache read per reconnect attempt; no request to the API server.
- A test checker that counts calls (`stopsAfter`) sees one more call per attempt.
- The race after the last check (the container restarts between the check and the kubelet opening the stream) remains, as the issue says; a merged log still also has its follower-driven target cancellation.
- The reconnecting state after this end is left to #277, which clears it for every terminal exit.
