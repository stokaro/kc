# 0403. Logs and Info keep their object; the frame names where it was read

- Status: Accepted
- Date: 2026-11-28
- PR: #403 (follow-up to #399, issue #377)
- Amends: decision 3 and the first consequence of
  [0377](0377-follow-every-selection-change.md)

## Context

0377 shipped with #399. Every move of the other panel's cursor reaches a
follower, and a row that names no object (`..`, a folder, an empty list)
clears it. For Describe and Manifest that is what the issue asked. For
Logs it stopped the stream at every `cd` and every `Enter` into a folder,
and Info dropped the workload it summarized and its history. The review
decided that browsing the other panel must not cut a stream: Logs and
Info keep the object they show until the cursor reaches another object.

A kept object raises the problem of the issue again. The frame of a
follower named the other panel's connection and namespace
(`targetConn`, `targetScope`), so after a `cd -n batch` it would name
`batch` above the log of a pod in `shop`, and after a context switch the
new context above an object of the old one. The old connection could
also be released while the panel still showed its object: the panel
held nothing, and actions that resolve a selection's connection
(`connByID`) found none.

## Decision

1. **A report that names no object does not reach a Logs or Info panel
   that shows one.** `namesObject` says whether the row under the cursor
   names an object: a Kubernetes object, a container or log row of a
   pod, or an `/overview` finding. When it does not, Logs keeps the pod
   it streams and Info the node, workload or finding it summarizes
   (`App.followRule`, installed with `Panel.SetFollowRule`). A Logs panel
   that shows its hint, an Info panel that shows a namespace, and a panel
   that does not know where its object was read keep nothing and follow
   the other panel as before. Describe and Manifest clear as 0377
   decided.
2. **A row that names an object replaces what they show at once.**
   Another pod restarts the stream, also a pod of the same name on
   another connection, since the stream's identity carries the
   connection. Any other object shows the Logs hint or Info's namespace.
3. **A follower records where its object was read** (`followOrigin`):
   the connection, which the follower holds while it shows the object,
   and the namespaces the other panel had then: its scope for the frame,
   the namespace of its list for Info, and the shell's namespace.
   `targetConn` and `targetScope` take the origin after a pin and before
   the other panel, so the frame of a Logs or Info panel that keeps an
   object names the object's context and namespace. `connByID` finds a
   connection a follower holds, so `GetCurrentItem`, the F2 stream
   options, the full-screen log and a pin act on the shown object.
   `termTarget` takes the origin too, so the shell line of an active
   follower runs where its object was read.
4. **The hold ends with the object.** It is given up when the follower
   shows another object or none, leaves the follower modes, or kc exits.
   A pinned panel holds no origin: the pin holds the connection. Unpinning
   hands the pinned object and its connection back to the follower,
   which then keeps it or takes the other panel's row by the rules above.
5. **A mode change decides again.** `followOtherPanel` replays the other
   panel's selection to every follower mode, Info included. A pod that
   Logs kept is not an object Info keeps, so Info follows the other
   panel's namespace.
6. **The frame and the shell line follow an origin change in the same
   Update.** A mode change, an unpin or a thaw can change a follower's
   origin without a selection report; the report hook then syncs the
   frame labels and the shell's context all the same.

## Alternatives considered

- **Clear Logs and Info as Describe and Manifest.** One rule for every
  follower, but each `cd` and each `Enter` into a folder would stop the
  stream and reset Info's history while the cursor crosses `..`.
  Rejected in review.
- **Keep the frame on the other panel's location.** That is the mismatch
  of #377: content of one context or namespace under the label of
  another.
- **Keep Logs until the cursor reaches another pod.** A ConfigMap row is
  a move to another object; Logs shows its hint there as before, rather
  than a stream whose pod the user left.
- **Keep any object in Info.** On a pod Info shows the namespace. Keeping
  the pod would leave Info on the old namespace after a `cd`, though it
  shows no object of its own.

## Consequences

- 0377's first consequence no longer holds for Logs: `Enter` into a
  folder and a `cd` put the cursor on `..`, and Logs goes on streaming
  its pod. Describe and Manifest still show their hint there.
- A follower may hold a connection that neither panel browses any more,
  as a pin does. Its cluster stays leased until the follower shows
  another object or none.
- The shell line of an active Logs or Info panel that keeps an object
  runs in the object's context and namespace.
- Unchanged from 0377: the report reads the row under the cursor of each
  List panel after every message. That read can wait for a list load
  (`liveObjectRowSource.Lines` waits for a pending load until its
  context ends), so it keeps its 250 ms deadline per panel. The contexts
  for `NotifySelection` are created only when a panel's selection moved.
