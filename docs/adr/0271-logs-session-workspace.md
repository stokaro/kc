# 0271. Expand a Logs panel session full screen, and Solo a source

- Status: Accepted
- Date: 2026-11-23
- Issue: #271

## Context

A Logs panel (ADR 0049) streams the log of the other panel's selection and keeps its lines within `viewer.logs.maxLines` and `viewer.logs.maxMemoryMiB`. JSON formatting, the filter, the fields view and the sources dialog are only in the full-screen logs viewer, which a `logs` row opens with `Enter` or `F3`. That viewer opens its own stream and reads the configured tail again: the lines the panel collected are not carried over, an older line the panel kept may be gone from the kubelet, and two streams of the same container run while both are open. In a Logs panel `F3` did nothing.

The issue's claims hold on current main (`openLogsViewer` builds a new viewer and stream; the panel widget has its own stream and window).

## Decision

1. **`F3` in a Logs panel expands its session.** The function bar shows `F3 Expand` and the File menu `Expand logs`; both are disabled while the panel shows the hint. The full-screen viewer starts with the lines the panel keeps and then gets every line the panel's stream reads. No stream is opened: no tail refetch, no gap and no repeat between the two.
2. **The panel keeps its stream.** The viewer reads through a `Follower` that the panel feeds on the UI goroutine. Closing the viewer closes only the follower; the panel's stream, lines and position stay. The panel never waits for the viewer: a follower holds at most 8192 unread lines (`FollowerQueue`), and past that the oldest unread lines go and the viewer reads `[kc] N lines skipped: the view fell behind the stream`.
3. **Cancellation follows the panel.** When the panel stops the stream (the other panel moves to another pod, the options change, the panel leaves Logs mode), the viewer reads `[kc] the Logs panel stopped this stream; reopen the logs to follow it again` and ends. When the stream ends or fails, the viewer ends with it. Its `(reconnecting)` comes from the panel's stream.
4. **The options stay with the panel.** The expanded viewer has no `F2` options (the stream is the panel's; `F2` in the panel changes it). The command line shows the panel's `kubectl logs` command, with `--all-containers --prefix` for an all-containers stream.
5. **Solo (`M-m`) in a merged log** shows only the source of the focused record; `M-m` again shows every source. It is a display filter in `logview.Log`, next to the query: every stream keeps reading, the hidden records stay kept within the same limits, and clearing Solo shows them again. The status line names it (`solo: pod/container`) and counts `N of M lines`. The sources dialog (`M-s`, ADR 0181) still is what stops streams. Solo works in every merged viewer, including an expanded all-containers panel session. `M-m` was free in `internal/ui` and in the open pull requests. It joins the keys kc refuses for custom commands: a custom command bound to `M-m` is now reported at startup and keeps no hotkey.

## Deviations from the issue

- **Record focus and detail panes are deferred.** The viewer already has a record identity for the focused line (`focusRecord`, made exact for the context view in #275), the fields view (`M-d`), the context view and the sources dialog. A side pane that keeps the raw record, the parsed fields and the source status in view needs a split layout inside the full-screen modal, which the viewer does not have; at 80x24 it would leave about 40 columns for the log. That is a layout project of its own, not part of making the session continuous.
- **One direction only.** The session moves from the panel to the full-screen viewer, not back: the panel keeps its own session the whole time, so there is nothing to hand back. The search, the filter and Solo of the expanded viewer stay there; the panel is a plain text view (ADR 0188) and has none of them.
- **The reading position is not carried over.** The viewer starts at the newest line and follows, as every logs viewer does. A paused panel stays paused at its line.
- **The merged coverage is not shown in the expanded viewer.** The follower reads the panel's merged stream as text, so the viewer has no mux for its status or for `M-s`. The panel header still shows the coverage. Exposing the panel's mux to the viewer would let `M-s` stop streams the panel owns, which this issue keeps apart.
- **Opening "directly from Pod/container/workload targets"** already exists (`Enter`/`F3` on a `logs` row) and is unchanged. Integration with the shared time model (#255) is the dashboard lane's; a Logs panel already reads from the investigation window (`C-x w`, ADR 0187), and the expanded viewer shows what the panel reads.
- **No Close-session action.** Closing the viewer (`F3`, `F10`, `Esc`) ends the follower; the panel's session ends as before, by leaving Logs mode.

## Consequences

- One stream per Logs panel, however often it is expanded. The extra memory is the viewer's own retention (the same limits) and at most 8192 unread lines per follower.
- `viewer.Widget.RawLines` returns the shown text lines, for the handover.
- `logview.Log` has a second display filter (Solo). Code that asked `Query().Filters()` to decide whether lines are hidden now also asks `Solo()`.
- Follow-ups: the record and source panes; showing the coverage of an expanded merged panel session without giving the viewer control of the panel's streams.
