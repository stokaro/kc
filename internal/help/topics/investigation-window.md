# Investigation window

The investigation window is one period of time that you set for the
whole session, for example "the last hour" or "today from 10:00 to
11:30". The panels that can show a period then show that period instead
of their own default. These are the Logs and Info panels and the
dashboard (`C-x g`).

Use it when you investigate an incident. Say an alert fired at 10:12 and
recovered at 10:40. Without a window, a Logs panel shows the last lines
of the log, so the lines from 10:12 are long gone or buried. Press
`C-x w`, type `10:00 .. 10:45` and press `Enter`. Every Logs panel now
reads that pod's log from 10:00, stops at 10:45 and hides what came
later. When you move the cursor to another pod, its Logs panel shows the
same 45 minutes. You set the period once instead of changing the since
and tail options of each log.

## What the field accepts

Times are in your local time zone. The dialog shows the zone, for
example `time zone +01:00`.

- `15m`, `2h`, `1h30m`, `90s`: the last minutes or hours up to now. This
  is a live window: the log keeps following. At most 7 days (`168h`).
- `10:00 .. 11:30`: a fixed period today. Seconds are allowed:
  `10:00:15 .. 10:20`.
- `2026-11-23 10:00 .. 2026-11-23 11:30`: a period with dates. A `T`
  between date and time works too.
- `2026-11-23T09:00:00Z .. 2026-11-23T10:00:00Z`: RFC 3339 times keep
  their own offset.
- `10:00 ..`: from 10:00 until now, live.
- empty, or `off`: turns the window off.

The end must be after the start, and the start cannot be in the future.
A wrong value shows the reason in the dialog; fix it or press `Esc`.

## How it shows on the panels

- Logs: the header shows the window with its zone, for example
  `window 2026-11-23 10:00:00 .. 10:45:00 +01:00`. The panel reads the
  log from the window's start (`--since-time`), all lines within its
  line limits. Its own since and tail options are not used meanwhile.
- A window that has ended does not follow the log. kc turns timestamps
  on, because it needs them to cut lines after the end, and the header
  counts them: `12 after the end hidden`.
- `first line 10:42:07` in the header means the log starts later than
  the window. The kubelet no longer keeps the older lines; kc cannot
  get them.
- Info: a line under the measurement time says how much of the window
  the session usage history covers, for example
  `window last 1h: session usage history from 13:55:00 only
  (incomplete)`. kc only has usage it measured itself while Info was
  open, and the sparklines still show their 15 minutes. "Covered" means
  both CPU and memory have samples from the window's start to its end
  with no gap longer than two and a half polls; otherwise the line names
  what is missing, such as `1 gap`, `stale since 14:02:10` or
  `CPU covered, memory from 14:01:00 only`.
- Info: the Prometheus history of a workload reads the window instead
  of `metricsHistory.window`, and its heading names it.
- Dashboard: the charts and the Events show the window, and the title
  bar names it with its zone, for example `WINDOW 2026-11-23 10:00:00
  .. 10:45:00 +01:00` or `LIVE window 2h`. Zooming or panning leaves it
  (`≠ window`); `w` makes the shown range the window. On an Event, `l`
  sets the window to five minutes either side of it and shows the logs.
  See [Dashboard](dashboard.md).
- The Command menu (`F9`) shows the current window next to
  "Investigation window" with a check mark.

## Keys

- `C-x w` opens the dialog, filled with the current window.
- "Investigation window" in the Command menu (`F9`) does the same.
- `Enter` sets the window, `Esc` leaves it unchanged.
- Clear the text and press `Enter` to turn the window off.

## Limits

- Describe, Manifest, object lists, `/overview` and `/diagnosis` keep
  their own time ranges. Events in Describe are not filtered.
- The window lasts for this session. It is not saved in saved views.

## See also

- [Pin and freeze](pin-freeze.md)
- [Logs mode](mode-logs.md)
- [Info mode](mode-info.md)
- [Dashboard](dashboard.md)
- [Logs viewer](logs-viewer.md)
