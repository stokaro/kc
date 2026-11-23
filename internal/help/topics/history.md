# Panel history

Each panel remembers the places it visited, as mc's directory history
does. Go back to the pod list you left a minute ago, or forward again,
without walking the folders.

## How it works

- A place is the context, the panel's namespace scope and the rows
  entered from the panel root.
- Every move is recorded: entering or leaving a folder, a scope change,
  a context switch, a hotlist jump, a `cd` on the shell line, the place
  kc started in.
- A place is kept once. Visiting it again moves it to the newest end.
- Recording after going back drops the entries ahead, like a browser.
- Each panel keeps the last 50 places, in memory for this session.
- `C-u` (swap panels) swaps the histories with the panels.

## How to read it

`M-H` opens the "History" list of the active panel, newest first. Each
entry shows its path and, in gray, `context : namespace`. The place the
panel is at is marked `current`.

## Keys

- `M-y` (or `Esc y`) goes back.
- `M-u` (or `Esc u`) goes forward.
- `M-H` (or `Esc H`) opens the list. In it, `Enter` or a click goes to
  the entry, typing filters, `Esc` clears the filter, then closes.
- The Command menu of `F9` has History back, forward and list.

## Limits

- Going to an entry of another context switches kc to that context, and
  the other panel goes to its root, as the context picker does.
- When a list is still loading, kc waits up to ten seconds for the rows
  it needs. A place whose objects are gone stops at the deepest folder
  that still exists, with a message.
- `M-y`, `M-u` and the list move within the history and record nothing,
  so the forward entries stay.
- With the full-screen terminal shown (`C-o`), these keys go to the
  shell.

## See also

- [Hotlist and saved views](hotlist.md)
- [Panels](panels.md)
- [Shell and terminal](shell.md)
