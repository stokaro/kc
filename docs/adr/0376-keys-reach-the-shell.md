# 0376. Keys reach the shell in C-o and on a typed shell line

- Status: Accepted
- Date: 2026-11-28
- Issue: #376

## Context

Two places promise that keys go to the shell. In the full-screen terminal
(`C-o`) help said "every key goes to the shell, F9 and F10 included". On
the shell line under the panels, once the user typed, `Left`, `Right` and
`Tab` were to go to the shell line.

`App.handleKeyMsg` checked the full-screen terminal last. Before that, the
global keys took `Tab` (switch panels), `C-u` (swap panels), `M-w`, `M-l`,
`M-k`, `M-F1` and `M-F2`; `C-1` and `C-2` changed panel modes; and `Esc`
armed kc's Esc prefix and was never sent. So in `C-o` a lone `Esc` never
reached the shell, `Esc :` arrived as `:`, and in vim `Esc 4j` ran
`kubectl edit` on the row hidden behind the terminal and `Esc x` opened a
container shell.

With typed text on the shell line, kc kept taking its own keys; only
`M-i`, `M-f`, `C-z` and `C-x` checked for typed text. `Tab` and `C-u`
still switched and swapped the panels, `C-w`, `C-t`, `C-r`, `M-b` and
`M-d` ran kc actions, and `M-t` attached to the container under the
cursor at once.

Open questions: how a lone `Esc` reaches the shell without a delay, what
kc's Esc prefix (`Esc <digit>` for a function key, `Esc <letter>` for a
Meta key) does where the shell has the keyboard, and which keys kc keeps
on a typed shell line. ADR 0236 decided that `C-r` rereads the panel
"even with text on the shell line, as in mc".

## Decision

1. **`C-o` takes every key but `C-o`.** `handleKeyMsg` checks the
   full-screen terminal first, before the menu bar, the custom command
   hotkeys and the global keys. `C-o` returns to the panels; every other
   key press goes to the terminal as it arrived, the function keys
   included.
2. **No Esc prefix in `C-o`.** `Esc` goes to the shell at once, with no
   timer in kc. Each key is written to the PTY before the next one is
   handled (`writeNow`), so `Esc` and the key after it arrive in order.
   Whether Bubble Tea delivered a fast `Esc :` as one `alt+:` key or as two
   keys, bubbleterm writes the same bytes (`\x1b:`). The shell and the
   programs in it apply their own Esc timeouts (readline's
   `keyseq-timeout`, zsh's `KEYTIMEOUT`, vim's `ttimeoutlen`), as in a
   plain terminal. An Esc armed at the panels ends when `C-o` switches.
3. **A typed shell line takes every key but a short list.** While the
   shell line holds typed text (`Terminal.HasInput`), kc keeps only:
   - the function keys, with or without a modifier (`F1`-`F12`, `M-F1`,
     `M-F2`, and custom `F11`-`F24` hotkeys);
   - `C-o`;
   - `Up`, `Down`, `PgUp` and `PgDn`, which move the panel cursor as in mc;
   - `C-1` and `C-2`, which change a panel's mode.

   `Enter` and `C-c` reach the shell and give the keys back to the panels,
   as before; `Enter` on a plain `cd` line still moves the panel (ADR
   0040). Every other key goes to the shell: `Tab`, `C-u`, `C-w`, `C-t`,
   `C-r`, `C-s`, `C-x`, `C-\`, `Home`, `End`, `Left`, `Right`, `Insert`,
   `+`, `-`, `*` and every Meta key.
4. **`Esc` on a typed shell line goes to the shell at once**, and so does
   the key after it. kc's Esc prefix works while the shell line is empty.
5. **`C-r` with typed text is the shell's history search.** This replaces
   the last point of ADR 0236 for the shell line; in panels with an empty
   shell line `C-r` still rereads.
6. **kc follows the shell line closely.** `Terminal.HasInput` is true
   while kc knows the line holds text, and also while it cannot follow
   the line since the last `Enter` or `C-c`. Such a line now takes almost
   every key, so a line that looks empty must not end up there:
   - On an empty line `C-a`, `C-b`, `C-d`, `C-e`, `C-f`, `C-g`, `C-k` and
     `C-l` keep it known and empty. `Delete`, `S-Tab` and the modified
     arrows do not: bash and zsh with no configuration type `;5C` for
     `C-Right`, zsh types `~` for `Delete`, and many zsh setups complete
     on `S-Tab`.
   - A line of spaces counts as empty.
   - Text typed under the panels stays on the line in `C-o`, so kc follows
     the keys there as under the panels. `Enter` and `C-c` end the line in
     `C-o` too. Keys typed after `Enter` there may go to another program,
     so they do not count as typed text when the panels return.

The check sits in `shellLineKey` (`internal/ui/app_input.go`), before the
custom command hotkeys and the global keys, so a new kc key does not take
a shell key by default.

## Alternatives considered

- **Keep the Esc prefix in `C-o` and send `Esc` when the prefix times
  out.** Every lone `Esc` (leaving insert mode in vim) would arrive
  `input.escTimeout` late, one second by default, and `Esc 4` or `Esc x`
  would still run panel actions behind the terminal.
- **Send `Esc` in `C-o` and also arm the prefix.** The shell and kc would
  both act on `Esc 4`.
- **Keep the Esc prefix on a typed shell line, with `Esc <digit>` as the
  function key and other keys sent after the `Esc`.** This is mc's command
  line. But a lone `Esc` would reach the shell a second late, and in a
  shell in vi mode `Esc 4` or `Esc 8` (a count) would edit or delete the
  row under the cursor: the same accident as in `C-o`. The function keys
  still work.
- **Extend the list of keys that go to the shell** (add `Tab`, `C-u`,
  ...). Every new kc key would take a shell key again by default; the bug
  came from such a list.
- **Send `Up` and `Down` to the shell (history).** mc keeps them with the
  panel, and moving the cursor while composing a command is common. `C-p`
  and `C-n` reach the shell's history. `PgUp` and `PgDn` have no default
  binding in bash or zsh, and zsh types a `~` for them.
- **Keep `Home` and `End` with the panel.** They are line start and end in
  every shell, like `C-a` and `C-e`, which went to the shell already.
- **Count a line kc cannot follow as empty unless it held text before.**
  `C-p` or `C-y` on an empty line puts text there, and `Enter` must run
  it.

## Consequences

- In `C-o` only `C-o` or a click on the bottom line returns to the panels.
  `F10` does not quit there (it did not before either).
- While the shell line holds text, kc's Meta keys, `C-r`, `C-\`, `C-u`,
  `Tab` and the custom `M-` hotkeys wait until the line is run (`Enter`),
  canceled (`C-c`) or erased with `Backspace` or `C-u`. After a key kc
  cannot follow (`Tab` completion, `M-b`, a cursor move) the line counts
  as typed until `Enter` or `C-c`, as before (see 6).
- `Esc <digit>` for a function key needs an empty shell line; the function
  keys themselves work.
- ADR 0041 (hotlist and history keys) and ADR 0046 (custom command
  hotkeys) said what happens in `C-o`; with typed text those keys now go
  to the shell too. The Status line of ADR 0236 points here.
- Attach asking first is #375.
