# 0181. Choose the sources of a merged log and show its coverage

- Status: Accepted; source rows and coverage amended by 0379 (waiting containers)
- Date: 2026-11-23
- Issue: #181

## Context

Issue #181 asks for a source picker for merged logs: include and exclude
patterns for pods, containers and container kinds, quick per-source
toggles, persistent counts of selected, streaming, waiting, ended and
failed sources with details per source, the stream limit shown and
adjustable, a retry for a failed source, stable source identity across new
pods and restarts, and a view that says it is incomplete when sources are
over the limit. Changing one source must not restart the others or drop
their retained lines.

What the code on main does (checked at 82a3543):

- The mux (`internal/logmux`) runs one stream per container instance, at
  most `viewer.logs.maxRequests` at once (default 50, ADR 0032). It queues
  the rest and writes one `[kc] N streams wait for a free slot` line when
  the queue length changes. That line scrolls away, and nothing else shows
  that the view is partial.
- `Mux.Stats` counted running, waiting and ended streams, but no caller used
  it. A failed stream was counted as ended; its error was only in its `[kc]
  -` line.
- `Mux.SetTargets` already keeps the streams of targets that stay in the
  set, so a narrower set does not restart the others.
- The Follower updates the target set from the shared pod informer; no new
  watch is needed for any of this.
- An F2 change in the viewer starts a new merged stream and clears the
  view, as for a single container.

## Decision

1. **A filter of `pod[/container]` globs, kept in the mux.** One text line
   of space-separated terms; each part is a `path.Match` glob, a missing
   part matches everything, a leading `-` excludes. A target is selected
   when no include term exists or one matches, and no exclude term matches.
   Examples: `-/istio-proxy` drops a sidecar in every pod, `api-0 api-1`
   follows two pods, `api-*/app` one container of matching pods. The filter
   matches names, so it applies to pods created later and to restarted
   containers, and an exclusion stays until the user changes the filter.
   The mux keeps the full target set from the Follower and applies the
   filter itself (`Mux.SetFilter`), so it can tell an excluded source from
   one that waits or failed.
2. **Change only what the filter changes.** An excluded running stream stops
   at once (not after the remove grace, which is for pods that went away).
   The other streams go on, and the viewer keeps its lines: the filter does
   not restart the merged stream. A source selected again streams from that
   moment (`tailLines=0`, like `--tail=0 -f`), so the lines the view already
   has are not repeated and a noisy sidecar's backlog does not push the
   other sources' lines out of the retention window (ADR 0179). A
   `[kc] source filter: ...` line marks the change in the log, and excluded
   streams end with `[kc] - pod/container (excluded)`.
3. **Coverage is always shown.** `Stats` now counts streaming,
   reconnecting, waiting, ended, failed and excluded sources and the limit,
   and prints as `incomplete: 50 streaming, 20 waiting (limit 50), 2 failed`.
   The prefix `incomplete:` appears while any selected source waits for a
   slot, waits to reconnect, or failed. The full-screen viewer puts it first
   in its status line; the Logs panel shows it in the header of an
   all-containers stream.
4. **A sources dialog, `M-s` in a merged viewer.** It shows the coverage,
   the filter, the stream limit and one row per source with its state and,
   for a failed source, its last error. Rows are ordered failed,
   reconnecting, waiting, ended, excluded, streaming, so the ones that need
   attention fit; the rest are summed up. It reads the mux on every draw, so
   it stays current while the log streams behind it. Apply sets the filter
   and the limit; only what changed is applied.
5. **Retry failed.** A failed stream keeps its error. The dialog's Retry
   button queues every failed source again; each continues after its last
   delivered line, with the same duplicate check as a reconnect (ADR 0180).
   For this, `logresume.Config.Position` lets the caller keep the resume
   position across runs.
6. **The limit can be changed for the open view.** The dialog accepts 1 to
   500. A higher limit starts waiting streams; a lower one stops nothing and
   starts no new stream until the count is below it. The value lasts for
   the viewer (including F2 restarts) and shows in the kubectl command line
   under the log; `viewer.logs.maxRequests` stays the default.
7. **The selection outlives F2.** The filter and the limit belong to the
   viewer, so a new merged stream after an F2 change starts with them.

## Deviations from the issue

- **No container kinds.** A Target does not record whether a container is
  an init, main or ephemeral container, and the names already tell them
  apart in practice (`-/*-init`). Adding the kind would mean another field
  in every target and filter term for little gain.
- **No per-row toggles in the dialog.** An exact term (`-api-1/proxy`) is a
  toggle, and a pattern is what survives new pods; a list of check boxes
  would need scrolling for 70 sources and would only cover the pods that
  exist now. The rows show state and errors; the filter line selects.
- **One Retry for all failed sources, not per source.** A failure usually
  has one cause (RBAC, an API server restart), and the dialog stays
  simpler. A source that should not be retried can be excluded.
- **The Logs panel shows coverage but has no dialog.** Its merged mode
  covers the containers of one pod, which rarely exceed the limit, and its
  F2 already picks one container or all.
- **The kubectl command does not show the filter.** `kubectl logs` has no
  way to exclude containers or pick pods by name with a selector; the
  command keeps showing the selector, with the current limit.

## Consequences

- No new requests or watches: the filter and the counts work on the target
  set the Follower already builds from the shared cache. Excluding sources
  lowers the number of open log streams.
- A source excluded and selected again loses the lines it wrote while
  excluded. That is the point of excluding it; the `[kc]` lines show when.
- The filter is per viewer and is not saved. A saved per-workload filter
  could follow if users ask for it.
- Lowering the limit does not stop running streams, so the count can stay
  above the new limit until streams end. Stopping them would drop lines the
  user may be reading.

Tests: `internal/logmux` covers parsing and matching, excluding a sidecar
across pods without touching the other streams, a new pod under an
exclusion, a source selected again (with `tailLines=0`) and one selected
again while its stream still stops, the coverage text over the limit,
raising and lowering the limit, and a failed source with its error and a
retry from its last line. `internal/ui` covers the dialog over a merged
viewer (coverage in the status line, a refused filter and limit, Apply
without clearing the view, the rows), the row order, `M-s` through the app,
and on envtest a merged stream started with a selection on the shared pod
informer, including a pod created later. The panel test covers the header.
