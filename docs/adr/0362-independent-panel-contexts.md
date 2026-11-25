# 0362. Independent kubeconfig contexts in the two panels

- Status: Accepted
- Date: 2026-11-25
- Issue: #362
- Supersedes: decision 1 of [0196](0196-compare-resource-sets.md) (one
  context per app), decision 3 of [0062](0062-cache-ownership.md) (the
  app holds the current context's cluster), in
  [0187](0187-pin-freeze-window.md) decision 1 that a context switch drops
  pins, in [0041](0041-hotlist-and-history.md) that a jump to another
  context moves both panels, and in [0055](0055-execution-context.md)
  decision 5 that a watching command moves to the new context on its next
  run

## Context

The App held one cluster (`App.cl`, `App.clLease`, `App.currentCtx`).
The context picker replaced it, dropped the pins and put both panels at
the root of the new context's namespace. Every read and action read the
App's cluster: describe, logs, Info, the dashboard, the metric sources,
F4, F7, creating a namespace, the read-only checks, the access reviews,
the terminal's kubeconfig, the confirmations, the hotlist and history,
the visibility profiles. The folders already carried their own
`models.Deps` (cluster, pool key, context name, read-only flag) and the
object rows their origin cluster, so deletes and the built-in object
actions used the row's cluster; everything that read `App.cl` did not.

ADR 0196 kept one context per app because an action that still read the
App's cluster would act on the other panel's cluster. Issue #362 asks for
the change anyway: each List panel owns its context and namespace, the
viewers follow the context of what they show, every operation and
confirmation uses the target's cluster, identity and read-only policy,
the terminal follows the active panel, and late answers never reach a
panel that has switched.

## Decision

1. **Each panel owns a connection.** `panelConn` is one panel's
   connection to a context: the kubeconfig context, the pool key (with
   kc's impersonation), the cluster, its pool lease, the pod filesystem
   factory and the discovery listener. A connection never changes; a
   switch makes a new one with a new identity. Two panels in one context
   hold two leases on the same pool entry, so they share one informer
   cache. The App removes `cl`, `clLease`, `currentCtx`, the App-wide pod
   filesystem factory and the App-wide discovery listener; the compiler
   found every reader, and each now names its target.

2. **Holders release the connection.** A connection counts its holders:
   the panel that browses it, a pinned panel that shows one of its
   objects, an open dashboard, an open logs viewer and a running Logs
   panel stream. The lease and the discovery listener go when the last
   holder lets go; after that the pool keeps the cluster for its TTL, as
   before. A switch therefore never stops a cache that a pin, a stream or
   the other panel still reads.

3. **Switching.** The context picker of a panel (its Left or Right menu,
   or a click on the context in its bottom border) switches that panel
   only. "Context for both panels..." in the Command menu switches both,
   with one connect. The startup flags (`--context`, `-n`, `-A`) still
   set both panels. The cluster is acquired in a command; its result
   names the panel (a pointer, so a swap with `C-u` meanwhile does not
   misroute it) and the panel's switch sequence. Only the newest request
   of a panel applies; an older one, or a failed one, releases its lease
   and changes nothing, so a failed connection leaves the panel where it
   was. A switch puts that panel at the root of the new context's
   namespace (else `default`), drops that panel's own visibility choice
   (ADR 0357) and its pending jump, and leaves the other panel's
   location, selection, filter, marks, pins and sessions alone. The
   switch result passes an open dialog, so a picker or viewer opened
   meanwhile does not swallow it and leak the lease. The picker marks
   the panel's context `current` and the other panel's `left` or
   `right`.

4. **The effective target of a panel.** A List or Command panel's target
   is its own connection and namespace scope. Describe, Manifest, Logs
   and Info follow the other panel: their target is the other panel's
   connection and scope. A pinned panel keeps the connection it was
   pinned on, holding it, until `C-x p` unpins it or the panel leaves the
   follower modes; switching the other panel's context does not drop the
   pin. Selections carry the identity of the connection they come from
   (`panelcontent.Selection.Source`); describe, manifest and logs key
   their content by it, so an object of the same name in another context
   is another target, and a read started for an old connection is
   dropped when it returns. The reads resolve the identity to a live
   connection on the UI goroutine; a released one reads as "the context
   of this panel changed", and an action on such a row does nothing. A
   List or Command panel acts only on its own list's row: on `..` or in
   an empty list it has none, although it hears about the other panel's
   selection.

5. **What the frame shows.** The bottom border shows the effective
   target: context, namespace scope, the context's color and the RO
   marker of that context. A follower panel shows the context and scope
   of the panel it follows, or of its pin; clicks on those labels do
   nothing, since they name the other panel's scope. A Command panel
   names the context its command runs in, which for a namespace command
   is the other panel's. The Left and Right
   menu entries "Context..." and "Namespace..." always change the panel's
   own list. The list stays where it was while the panel follows the
   other one, and List mode shows it again: the navigator is never
   replaced by a mode change.

6. **Every action resolves its target where it starts.** F3, F4, F5, F7,
   F8, the user menu, the built-in workload, CronJob and node actions,
   the node shell, `M-x`, `M-t`, port forwards, the pod filesystem helper,
   copies into a container, explain, owners and used-by, quick search,
   the type catalog and the path walk use the connection of the panel the
   key was pressed in. Requests that a row sends back to the App as a
   message (a confirmation, a shell or attach session, a link, a workload
   action, the type catalog) are stamped with their panel by
   `Panel.Update`, so a `Tab` typed before the message arrives does not
   retarget it. Deletes keep the UID precondition on the row's origin
   cluster (ADR 0058); F4 runs `kubectl edit` with the panel's
   kubeconfig, context and impersonation and refuses a row whose origin
   context is not the panel's. Asynchronous results (namespace lists,
   related objects, NetworkPolicy pairs, workload logs) carry the
   connection they started with and are dropped or reported when the
   panel no longer has it. F7 keeps the connection its panel had when
   the dialog opened; when the panel switched before the name is
   confirmed, no namespace is created. A logs viewer holds its
   connection until it closes, whether by `F3`, `F10`, `Esc Esc` or
   another viewer taking its place.

7. **Read-only is per target.** `refuseReadOnly`, the confirmation
   dialog, the user menu's dangerous commands, the function bar and the
   access reviews take the target's context. A confirmation shows a
   `Context` row with the target's context; the red dialog checks
   read-only again for that context when it is answered. With a
   protected context in one panel and a writable one in the other, F8 is
   refused in the first and works in the second whichever panel is
   active.

8. **Terminal and custom commands.** The integrated shell's kubeconfig
   follows the active panel's effective target: its context and
   namespace. It is rewritten when the active panel, its context, its
   namespace or its mode changes, and only when the result differs. A
   program that already runs keeps the environment it started with
   (ADR 0055). A custom command keeps the connection it started with for
   its watch restarts. A namespace command runs in the connection of the
   panel whose selection named the namespace, the one it was approved
   for, and restarts there when that selection moves to another
   namespace. When that panel switches its context, the command does not
   follow it into the new cluster: the panel says the context changed
   and the command has to be run again.

9. **Comparisons and cross-panel actions.** `C-x C-d` reads each side
   from its own row and names each side's context. `C-x d` and `C-x a`
   read each list from its panel's connection; a side that cannot be
   read stays unavailable, as ADR 0196 decided. `C-x c` compares the
   active panel's list with another context, as before. The
   NetworkPolicy pair (`C-x n`) needs both pods in one cluster and is
   refused with both contexts named when the panels' connections differ.
   Opening a linked object in the other panel (a Helm manifest link, an
   Info finding, the dashboard) moves that panel to the link's connection
   first when it differs, sharing the connection, and says so. kc does
   not copy, apply or synchronize objects between clusters.

10. **History, hotlist, saved views, visibility, metrics.** A panel's
    history and the hotlist record the panel's context; a jump to
    another context switches only that panel. Visibility profiles
    resolve the binding of the panel's context. Info and the dashboard
    read the metric backend of their target's context; the metric
    sources dialog the active panel's.

## Alternatives considered

- **Keep one context per app (ADR 0196).** The comparison features
  exist, but #362 asks for live panels in two contexts.
- **Connection state on `Panel`.** The panel is a UI widget; the App owns
  navigation, scopes and histories per panel index and swaps them with
  `C-u`. The connections follow the same rule.
- **One shared connection per context, reference counted by key.**
  Fewer objects, but a switch could not tell its own connection from the
  other panel's, and a late result keyed by context name would match a
  panel that switched away and back. A connection per panel per switch
  gives every async result an identity to compare.
- **Resolve viewers from the row's origin cluster only.** Object rows
  know their cluster, but container, log and Helm rows do not, and a
  scoped cache (a node's pods) is not the panel's connection. The
  selection's source identity covers every row.
- **Refuse links into a panel of another context.** Safe, but the user
  asked to see the object; moving the panel to the link's context is
  what Enter on a link means, and the frame shows the change.
- **Clickable follower labels that open the other panel's pickers.** A
  click in the right panel would change the left one.

## Consequences

- Two contexts on screen put F8 one `Tab` away from another cluster. The
  frame color, the RO marker and the `Context` row of every confirmation
  are the guard; read-only contexts refuse per target.
- Each panel's own namespace and context make the startup flags the only
  place that sets both; "Context for both panels..." is the explicit way
  to do it later.
- A connection held by a pin keeps its informers until the pin goes,
  even when no panel browses that context.
- Tests start a second control plane per test (`kcenvtest.Start`) for
  the two-context cases: identical namespaces and names in both,
  delete with the UID precondition in the right cluster, read-only per
  target, stale and failed switches and lease cleanup.
