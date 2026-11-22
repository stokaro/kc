# 0136. SIGINT while kubectl owns the terminal

- Status: Accepted
- Date: 2026-11-22
- Issue: #136

## Context

`M-x` and `M-t` run `kubectl exec` and `kubectl attach` through `tea.Exec`. Bubble Tea releases the terminal for the session, so it is in cooked mode until kubectl switches it to raw mode, and for an attach to a container without stdin it stays cooked: the banner says `C-c leaves`. In cooked mode C-c makes the tty send SIGINT to the whole foreground process group. That group holds kubectl and kc. `Run` subscribes to SIGINT and quits the program on it, so C-c ended kc together with kubectl. The same happens for a C-c during a slow `kubectl exec -it` connect, before kubectl turns raw mode on.

## Decision

kc keeps handling SIGINT but ignores it while a kubectl session owns the terminal:

- `interruptGate` (`internal/ui/interrupt_gate.go`) counts the sessions that own the terminal. `execSession.Run` holds it around `cmd.Run`.
- The signal goroutine in `Run` (`forwardQuitSignals`) quits on SIGTERM as before, and on SIGINT only when the gate is not held.
- The gate keeps swallowing SIGINT for one second after the last session ends. The C-c that ended kubectl reaches kc at the same moment, and the signal goroutine may read it only after `cmd.Run` returned.

kubectl still gets the SIGINT and ends, and kc comes back to the panels.

## Alternatives considered

- Start kubectl in its own process group and make that group the foreground group of the tty (`SysProcAttr{Foreground: true}`), then take the terminal back with `tcsetpgrp` after it ends. Only kubectl would receive the signal, with no timing window. It needs Unix-only code, ignoring SIGTTOU around `tcsetpgrp`, and a terminal file descriptor that Bubble Tea does not always hand to `tea.Exec`. C-z would stop only kubectl, whose group the shell does not know, and leave the user without a way back. Not chosen for now.
- `signal.Ignore(SIGINT)` around the session. An ignored signal stays ignored across exec, so kubectl would inherit it and C-c would no longer end the attach.
- Stop quitting on SIGINT at all. In raw mode Bubble Tea reads C-c as a key, so SIGINT only arrives from cooked-mode sessions or from `kill -INT`, but `kill -INT` is a reasonable way to stop kc and should keep working.

## Consequences

- C-c during an attach without stdin, or during a slow exec connect, ends kubectl and returns to kc.
- A `kill -INT` sent to kc while a session runs, or within a second after it ends, is ignored; SIGTERM still quits.
- `kubectl edit` (`tea.ExecProcess` in `app.go`) is not gated yet. Its editor puts the terminal into raw mode, so the window is only the fetch before the editor starts; it can use the same gate if that turns out to matter.
