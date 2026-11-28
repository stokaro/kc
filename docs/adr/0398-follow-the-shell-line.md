# 0398. kc follows the shell line closely

- Status: Accepted
- Date: 2026-11-28
- PR: #398 (follow-up to #397, issue #376)
- Extends: decision 3 of [0376](0376-keys-reach-the-shell.md)

## Context

0376 shipped with #397. Its decision 3 gives a typed shell line every key
but a short list. kc counts the line as typed (`Terminal.HasInput`) while
it knows the line holds text, and also while it cannot follow the line
since the last `Enter` or `C-c`. Before 0376 the second case moved only
`Enter`, `Left`, `Right`, `C-a`, `C-e` and `+ - *` to the shell; now it
moves almost every key. The review of #397 found two common paths to that
state while the prompt looks empty:

- On an empty line, a key that is neither a panel key nor plain text, such
  as `C-l` or `C-e`, made the line unknown. `Tab`, `Insert`, the Esc
  prefix and the Meta keys then went to the shell until `Enter` or `C-c`.
  A lone `Space` did the same, with only a blank on the line.
- Text typed under the panels, then erased with `C-u` or canceled with
  `C-c` in `C-o`. In `C-o` kc marked the line unknown on every key but
  `Enter` and kept the typed flag, so back at the panels `Tab` went to the
  shell. Running a program in `C-o` (`Enter`, `less`, `q`) did the same.

## Decision

1. **Keys that type nothing keep an empty line empty.** On an empty line
   `C-a`, `C-b`, `C-d`, `C-e`, `C-f`, `C-g`, `C-k` and `C-l` keep it known
   and empty (`leavesLineEmpty` in `internal/ui/shell_line.go`). Measured
   on a pty with bash 3.2 and zsh 5.9 with no configuration, none of them
   types anything. `Delete`, `S-Tab` and the modified arrows still make the
   line unknown: both shells type `;5C` for `C-Right` and `;2B` for
   `S-Down`, zsh types `~` for `Delete`, and many zsh setups complete on
   `S-Tab`. After those keys the line can really hold text.
2. **A line of spaces counts as empty.** It runs nothing, so it does not
   take the keys.
3. **kc follows the line in `C-o`.** Text typed under the panels stays on
   the line in `C-o`, so kc follows the keys there as it does under the
   panels: `C-u` empties the line, more text or a paste adds to it, `Tab`
   makes it unknown. `Enter` and `C-c` end the line in `C-o` too and clear
   the typed flag. Keys typed in `C-o` after `Enter` may go to another
   program, so they make the line unknown and do not count as typed text
   when the panels return.

## Alternatives considered

- **Count a line kc cannot follow as empty unless it held text before.**
  `C-p` or `C-y` on an empty line puts text there, and `Enter` must run
  it.

## Consequences

- After a key kc cannot follow (`Tab` completion, `M-b`, a cursor move)
  the line still counts as typed until `Enter` or `C-c`, as 0376 says.
- A shell in vi mode, or one with its own bindings for the keys of
  decision 1, may type text for them. kc then counts the line as empty
  until more is typed; the text stays visible on the shell line.
- Text typed in `C-o` after `Enter` does not hold the keys at the panels:
  `Enter` there opens the row even if that text is still on the shell
  line.
