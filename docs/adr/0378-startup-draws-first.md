# 0378. kc draws before it connects

- Status: Accepted; in decision 5, the discovery failure message shows only while the cluster still has no successful read, and in decision 2 the screen fits small terminals (amended by [0405](0405-discovery-review-followups.md))
- Date: 2026-11-28
- Issue: #378 (the startup part; the UI loop and API discovery have their
  own 0378 record)

## Context

`Run` read the kubeconfigs, picked the start context, acquired its
cluster for both panels and read its API discovery (`GetResourceInfos`),
all before `tea.NewProgram`. Against a server that accepts connections
and does not answer, the screen stayed empty for over 150 s in the
issue's runs (`--v=3` showed fourteen 10 s TLS handshake timeouts). Only
`C-c` got out: the terminal was not in raw mode yet, and kc had not
installed its signal handler, so the default SIGINT handler ended the
process.

## Decision

1. **Picking the context stays before the UI.** `Run` reads the
   configuration and the kubeconfigs and picks the start context before
   the program starts, as before. This reads local files only, and a
   context that cannot be picked (`--context` names no context, or there
   is no `current-context` and several contexts) still stops kc before
   the UI takes the terminal (`startContextError`, #388).
2. **Connecting runs in a command.** `Init` starts a command that
   acquires the cluster for both panels and reads API discovery once.
   Meanwhile `View` draws the connecting screen, `Connecting to
   <context> (<server>)… 12s · F10 quits`, where the server is the
   kubeconfig's server for the context's cluster. A one-second tick
   redraws the elapsed time.
3. **While connecting, F10 and `C-c` quit; other keys are dropped.**
   There is no panel to act on them, and a key kept until the panels
   open would act on a list the user had not seen. The mouse and pastes
   are dropped too. Other messages (window size, terminal reports) go to
   `Update` as before.
4. **A connection opens the panels.** What `initData` did after
   acquiring runs on the UI goroutine: the panel connections, the
   resource catalog, the shell's kubeconfig, the view options and the
   start namespace. Then the commands `Init` held back run: the panels,
   the shell, the custom hotkeys and the folder event loops. The shell
   starts here, after its kubeconfig is written.
5. **A failed connection stays on the screen with its error**, in the red
   dialog: `Cannot connect to <context> (<server>):`, the error, and
   `Enter opens the panels · F10 quits`. `Enter` opens the panels the
   way kc started before after such a failure: on the cluster when only
   discovery failed, with the "Discovery resources failed" message, and
   with no connection when kc could not build a client for the context.
   The screen does not retry; with the panels open, the cluster's own
   discovery refresh does.
6. **Notices wait for the panels.** The "opened the only context" notice
   is queued when the panels open, and `Init`'s configuration error
   messages run then too, so their display time does not run out behind
   the connecting screen.
7. `initConfigAndData` keeps the synchronous order (pick, connect, open)
   for tests that build an `App` without a program.

## Alternatives

- Draw the panels at once, each in a "connecting" state. Every panel
  mode, the menus, the context picker and the startup intents (`kc get`,
  `kc logs`) would need a state for "no cluster yet"; one screen before
  the panels needs none.
- Keep the keys typed while connecting and replay them when the panels
  open. They would act on a list the user did not see, the problem the
  issue reports for keys queued during a freeze.
- A retry key on the error screen. It would be a second path for what the
  cluster's discovery refresh does once the panels are open.
- Open the panels after a fixed time. A slow server that does answer,
  such as a large cluster's discovery, would get half-loaded panels; the
  elapsed time and F10 leave the choice to the user.

## Consequences

- The first frame shows whatever the server does, and F10 and `C-c` work
  during the whole startup.
- The error shows when discovery fails: at once for a refused port, and
  after client-go's request and handshake timeouts for a server that holds
  connections.
- `make test-headless` starts kc against a listener that never answers
  and checks the connecting screen, the elapsed time and F10.
