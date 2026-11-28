# 0375. Ask before quitting with live sessions, keep kc when its shell exits, ask before attach

- Status: Proposed
- Date: 2026-11-28
- Issue: #375
- Follows: [0373](0373-delete-marked-rows.md) for the dialog keys, [0376](0376-keys-reach-the-shell.md) and [0398](0398-follow-the-shell-line.md) for the shell line

## Context

Three ways to lose work by accident:

- `F10`, `Esc 0` and File > Exit quit at once. Port forwards, a node shell
  and a running copy stop with kc, with no word about them.
- When the embedded shell exits (`exit`, or `C-d` on an empty line), kc
  quits. The shell is the line under the panels; ending it to start a
  clean one is common in a terminal, and nobody expects it to end kc.
- `M-t` runs `kubectl attach` at once. The keys then go to the container's
  main process, and `C-c`, `C-d` or `exit` there can end that process and
  the container with it.

The issue left open which sessions count, the `C-c` behavior, the
connecting screen, and how to keep a shell that cannot start from looping.

## Decision

1. **Quitting asks while something would stop.** `F10`, `Esc 0`, File >
   Exit and a click on Quit open a red Yes/No dialog while port forwards
   run, a node shell is open or its pod starts, or a copy runs. The title
   names what stops, for example `2 port forwards will stop. Quit?`. The
   list below names each session (up to five, then `and N more`), and a
   note says that kc deletes the pod of an open node shell. With nothing
   running, quitting takes one key as before.
2. **Not counted: what holds the terminal, and programs in the shell.**
   kubectl edit, the editor of a local file, and `M-x` and `M-t` sessions
   take the terminal while they run, so kc gets no key to quit with. A
   program running in the embedded shell, such as a `kubectl port-forward`
   typed on the shell line, is not counted: bubbleterm keeps the pty to
   itself, so kc cannot ask for its foreground process group.
3. **`confirm.quit` in `~/.kc/config.yaml`.** `sessions` (the default)
   asks as in decision 1; `always` asks every time, with the title
   `Quit kc?`. Another value keeps the default, and kc reports the section
   at start like any section that did not load (0372).
4. **The quit and attach questions keep the red dialog keys of 0373.**
   `No` has the focus, `M-y` and `M-n` answer, a letter alone does nothing,
   `Enter` presses the focused button. `Esc`, `C-c`, `C-g` and `F10`
   answer No: `F10` closes a dialog or a viewer everywhere else in kc, and
   a second `F10` pressed out of habit must not stop the forwards.
5. **`C-c` does not quit, and signals do not ask.** At the panels `C-c`
   goes to the shell, as before (0136). `SIGINT` from outside, `SIGTERM`
   and `SIGHUP` quit at once: nobody is at the keyboard to answer, and a
   closed terminal cannot show a dialog.
6. **The connecting screen does not ask** (0378). No forward, node shell
   or copy exists before the panels open, so `F10` and `C-c` quit there at
   once.
7. **When the shell exits, kc starts a new one.** Each shell gets a new
   emulator and pty: macOS revokes a pty when its session leader exits,
   so writes to it fail and a new process cannot start on it (measured on
   macOS 27: `EIO` on write, `fork/exec` fails). kc says
   `The shell exited; kc started a new one.`, with the exit status when
   it is not 0. An exit in the full-screen terminal (`C-o`) returns to the
   panels, where the notice shows and the new shell line waits. The new
   shell gets the `KUBECONFIG` of the active panel at that time.
8. **A shell that exits at once backs off, then stops.** A shell that ran
   less than 2 seconds, or did not start at all (a `SHELL` that does not
   exist, no pty), exited at once. The first such exit starts a new shell
   at once; the next ones wait 1, 2 and 4 seconds, and the shell line and
   a red notice say how long. After 5 in a row kc stops starting shells
   and the shell line says why, for example
   `The shell /bin/nosuch did not start (...), 5 times in a row; kc stopped starting it. C-o starts it again.`
   `C-o` while no shell runs starts one; after a stop it gets one try.
   A shell that ran 2 seconds or more resets the count.
9. **`M-t` and `Esc t` ask before they attach.** The red Yes/No dialog
   opens over the active panel and names the context, namespace, pod and
   container. Its notes follow what the container offers: with stdin and
   a TTY, keys go to the main process and `C-c`, `C-d` or `exit` can end
   it and the container; with stdin only, typed lines go there and `C-d`
   closes its input; without stdin the session only shows output. They
   also say that kubectl attach has no detach key and that `M-x` opens a
   shell instead. The dialog keeps the cluster of the row it was opened
   from. `Yes` checks read-only mode again before kubectl starts. `M-x`
   does not ask: it starts a new process and ends nothing.

## Alternatives considered

- **Ask on every quit by default.** Quitting with nothing running must
  stay one key; `confirm.quit: always` is there for those who want it.
- **Keep the dead shell's emulator and start the new shell in its pty.**
  Not possible on macOS (decision 7); on Linux the pty survives, but one
  code path for both is simpler and the screen of a shell that ended has
  no use.
- **Show the dead shell's last screen, or quit as before, and start a
  shell on a key.** Without its shell line kc loses the mc model, and the
  notice already tells what happened.
- **Retry forever with a capped delay.** bubbleterm v0.3.5 leaves the
  goroutine that drains terminal responses blocked on its emulator after
  `Close`, so every new shell keeps the old emulator in memory. A `SHELL`
  that cannot start would grow kc for hours. Stopping after 5 bounds that
  to a few emulators per `C-o`.
- **A typed `y` to confirm.** 0373 removed letter answers from red
  dialogs; a stray key must not stop forwards or attach.
- **Attach without asking and rely on the banner.** kc prints the banner
  after kubectl started; by then the keys already go to the main process.

## Consequences

- Each new shell leaves one blocked goroutine and the old emulator behind
  (bubbleterm v0.3.5). Normal exits are rare and user driven, and the
  back-off and stop bound a broken shell. A fix belongs in bubbleterm:
  close the emulator's response pipe in `Close` once the read loop ended.
- The screen of the ended shell is gone; its scrollback lived in the old
  emulator.
- Programs started in the embedded shell still stop silently with kc
  (decision 2). Counting them needs the pty's foreground process group
  from bubbleterm.
- Tests replace `tea.Exec` through `App.execCommand` to see whether
  kubectl would run.
