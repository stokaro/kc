# 0031. Logs viewer options: previous, timestamps, since, tail

- Status: Accepted
- Date: 2026-11-22
- Issue: #31

## Context

The logs viewer always streamed with `Follow: true` and a fixed tail of 200.
Issue #31 asks for the options k9s offers (previous instance, timestamps, a
since window, tail lines), a `/logs (previous)` row for restarted containers,
and config keys. It leaves open how the options are reached, which values to
offer, what the config looks like, how a restart behaves, and whether the
buffer limit (`viewer.logs.buffer`) is part of this change. Multi-pod and
multi-container streaming is issue #32 and stays out.

The viewer's last line shows a kubectl command that must reproduce what the
viewer shows, and API work must not run on the Bubble Tea update goroutine.

## Decision

1. **Options live in the F2 dialog, in a new "Logs:" section.** The rows are
   Tail lines (50, 100, 200, 500, 1000, 5000, All), Since (Off, 1m, 5m, 15m,
   30m, 1h), Timestamps (On/Off) and Previous (On/Off). Wrap stays in the
   existing Viewer section. Enter applies to this viewer; `C-s` also saves
   tail, since and timestamps as defaults. A configured value that is not in
   the list (for example `tail: 300`) is added to the list, so it shows and
   survives a commit.
2. **One spec drives both the request and the command.** `models.LogsSpec`
   gains `SinceSeconds`, `Timestamps` and `Previous`. `LogsSpec.PodLogOptions`
   builds the API options and `logsCommand` prints the kubectl command from
   the same spec, for example
   `kubectl logs p -n web -c app --tail=200 --since=1h --timestamps -p -f`.
   A test table checks both for each case. `TailLines` 0 means the default
   (200) and a negative value the whole log, as in kubectl, where `--tail` is
   then left out. Log rows implement `models.LogsCommander`, so the viewer
   rebuilds the command (with `--context` when needed) whenever the options
   change.
3. **The footer shows the active options and the stream state**, such as
   `tail 200, since 1h, timestamps, previous (ended)`. A search summary takes
   the place while a search is active.
4. **A change restarts the stream from an empty view.** The old stream is
   closed, the content is cleared and a new stream opens in a `tea.Cmd`.
   Streams are numbered per viewer; open results and lines of an older
   stream, or of another viewer, are dropped, and a late open result is
   closed. The first open also runs in a `tea.Cmd` now; a failure (such as
   "previous terminated container not found") is shown as a line in the
   viewer instead of a toast with no viewer.
5. **Stream messages go to their viewer directly**, not to the top modal. The
   F2 dialog sits on top of the viewer; routed the old way, the next line
   reached the dialog, was dropped, and the stream stopped for good.
6. **`logs (previous)` row when `restartCount > 0`.** The container detail
   folder reads the pod from the shared cache and follows it with the same
   live pod row source as the container list, so the row appears when the
   container restarts while the folder is open. The label matches the
   existing `logs` row (no leading slash). If the pod cannot be read, the row
   is left out and the other rows stay.
7. **Config keys:** `viewer.logs.tail` (default 200, -1 for the whole log),
   `viewer.logs.since` (duration, default `0s` for no limit) and
   `viewer.logs.timestamps` (default false), kept in sync across
   `appconfig.Default()`, `config-default.yaml` and the README. Previous is
   not a default: it describes one container's history, and as a default it
   would make every viewer fail on containers that never restarted.
8. **The viewer always follows.** The streamed spec sets `Follow: true`, as
   before, and the command shows `-f`.

## Alternatives considered

- **Direct keys as in k9s** (`p`, `t`, `0`-`5`). Letters in the viewer go to
  search (`n`) and may go to more viewer actions later; digits collide with
  the `Esc+<digit>` function-key input. F2 already holds the viewer options,
  so the new options sit with theme and wrap. Direct keys can be added later
  on top of `SetLogOptions`.
- **Append the new stream to the old content** with a separator. Lines from
  two option sets in one buffer make the shown text disagree with the command
  line, which then no longer reproduces it.
- **Since and tail as one choice**, as k9s keys 0-5 do. kubectl combines
  them, and keeping them separate lets the command show exactly what is sent.
- **Fill the previous row once from the container list** when the folder is
  entered. Simpler, but the row would not appear for a container that
  restarts while the folder is open.
- **`viewer.logs.buffer`** (bounded line buffer). Trimming lines needs the
  viewer widget to drop lines from the front and reindex search matches and
  the selection. That is a separate change; it is left for a follow-up.

## Consequences

- The command line and the stream cannot drift apart for these four options:
  both come from one spec and one test table covers both.
- An open failure no longer shows a toast; it shows inside the viewer, where
  a previous-log failure is easy to read.
- The container detail folder now holds a pod informer like the container
  list, at the cost of a cache read per rebuild.
- Follow-ups: the bounded buffer (`viewer.logs.buffer`); `kc logs -p`,
  `--timestamps`, `--since` and `--tail` startup flags; optional direct keys.
