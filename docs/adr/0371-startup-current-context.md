# 0371. Start only in a context the user or the kubeconfig names

- Status: Accepted
- Date: 2026-11-27
- Issue: #371
- Supersedes: in decision 4 of [0072](0072-startup-context-and-scope.md),
  the message of an unknown `--context` and the rule that other startup
  problems open the UI

## Context

Without `--context`, kc picked its start context in three steps: the
first file of `$KUBECONFIG` whose current-context named a context, else
any loaded file whose current-context did, else the first entry of
`kubeconfig.Manager.GetContexts`. That list was built by ranging over
each file's context map, so its order changed from run to run. The files
under `~/.kube` were loaded by parallel workers and appended in the order
the workers finished. A current-context that named no context (a typo, a
context deleted with `kubectl config delete-context`) therefore opened a
random other context, possibly a production one, and nothing on screen
said so. kubectl refuses the same kubeconfig.

Two more cases came from the lookup by name over all files: a
current-context in `~/.kube/config` that named a context of another file
under `~/.kube` opened that context, although ADR 0323 treats those files
as standalone and `kubectl --kubeconfig ~/.kube/config` refuses it; and a
file whose current-context named one of its own contexts got an earlier
file's entry of the same name, which may point at another cluster.

The issue leaves open what "no current context" means when kc loads
several files, and the setup that must keep working: one file with a
valid current-context next to files without one.

## Decision

1. **Which current-context.** kc reads the loaded files in load order:
   the `$KUBECONFIG` list in its order, else `~/.kube/config` and then
   the other files under `~/.kube` in lexical order. The first file that
   sets `current-context` decides; later files are not consulted. For
   `$KUBECONFIG` this is kubectl's merge rule, and the name resolves in
   the merged list. A file under `~/.kube` stands alone, so its
   current-context resolves to a context of that file only.
   `kubeconfig.Manager.CurrentContext` returns the name, the file that
   sets it and the context it selects.
2. **Not defined.** A current-context that selects no context stops kc
   before the UI takes the terminal (`ui.ErrUndefinedCurrentContext`):
   `current-context "typo-context" in /home/me/.kube/config is not
   defined; contexts: badtoken, good, limited, refused; pick one with
   --context`. A later file's current-context does not replace it. When
   another standalone file defines the name, the message says so (`is
   not defined there, only in …`); kc does not open it.
3. **None set.** When no file sets a current-context:
   - several contexts stop kc the same way (`ui.ErrNoCurrentContext`,
     `no current-context is set in …`);
   - exactly one context opens, with an info message at the bottom of
     the screen ("No current-context set; opened the only context, X")
     and a log line. One name defined in two standalone files is two
     contexts, since the entries can point at different clusters;
   - no context at all keeps the behavior of 0072: a warning, then the
     UI.
4. **Unknown `--context`.** The message lists the contexts:
   `unknown context "nope" in /home/me/.kube/config; contexts: …`.
   `--context` still finds a name in the first file that defines it.
5. **Output.** `ui.Run` returns these errors (all `*startContextError`)
   before it creates the Bubble Tea program, and `cmd/kc` prints them on
   stderr with exit status 1. The sequence that turns the kitty keyboard
   protocol off after the UI moves from `cmd/kc` into `ui.Run`, after the
   point where these errors return, so it is written only when the UI
   ran. A context name with a space, a comma, a quote or a control
   character is quoted in the list, so the message stays one plain line.
6. **Order.** `GetContexts` is sorted by name. The sort is stable over
   file order, so the entries of one name keep the order of their files
   and `GetContextByName` returns the first file's. The files under
   `~/.kube` load in parallel and are kept in walk order. Clusters are
   sorted by name within each file. The context picker and the compare
   picker show this order.

## Alternatives considered

- Skip a file whose current-context selects nothing and use the next
  file's, as before. Rejected: it is the silent fallback the issue
  reports, only across files.
- Resolve a standalone file's current-context by name in every file, as
  `--context` does. It keeps a kubeconfig written by
  `kubectl config use-context` under a `$KUBECONFIG` list working when kc
  runs without that list. Rejected: kubectl refuses it, it contradicts
  0323, and a context removed from `~/.kube/config` would come back from
  a backup copy under `~/.kube`. Running kc with the same `$KUBECONFIG`,
  or `--context`, opens it.
- Let only `~/.kube/config` set the start context. Rejected: it stops
  users who have no `~/.kube/config` and one file under `~/.kube` with a
  current-context, which starts today.
- With no current-context, open the first context in sorted order.
  Rejected: stable, but still a choice the user did not make.
- Print the one-context notice on stderr. Rejected: the UI's alternate
  screen hides it at once.

## Consequences

- A kubeconfig that only worked because kc opened some context now stops
  with a message that says what to fix or which `--context` to pass.
- A current-context in a standalone file under `~/.kube` that names a
  context of another file stops kc; before, it opened that context.
- `ui.Run` owns the terminal reset; `cmd/kc` no longer writes escape
  sequences for a run that stopped before the UI.
- The picker order no longer depends on map iteration.
