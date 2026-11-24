# 0342. Info behind dialogs, and a young history

- Status: Accepted
- Date: 2026-11-24
- Issue: #323
- Amends: 0051 (section 5, the pause while a dialog is open), 0166 (point 7, "Lifetime"), 0177 (point 2, cells before the first sample), 0233 (the frame)

## Context

#323 reported three Info problems from the screenshots of #322:

- Disk said `n/a loading` for minutes on a node.
- After a drain, the node's top pods still listed the evicted pods.
- The history sparkline was a near-empty baseline in a corner until many samples existed.

The first two had one cause, in two layers. A windowed dialog took one picture of the main view when it opened and drew itself over that picture. Under it, the Info engine skipped its recomputes and held back its first kubelet stats round while any dialog was open (ADR 0051, 0166), and the metrics polls paused. A drain opens node actions, options, confirmation and progress one after another. For all that time the Info panel beside the dialogs showed the state from before the first one: Disk `loading`, the pods about to be evicted.

The third came from ADR 0177 and 0233. The sparkline spans a fixed 15-minute frame with the newest sample at the right, on a 0-100% scale. In the first minutes it is a few `▁` cells at the end of a blank line, and a node at 4% stays at `▁` all session.

## Decision

1. **Dialogs draw the panels live.** A windowed dialog without an explicit background redraws the main view on every render (`App.setBackdrop` via `Modal.SetWindowedBackgroundProvider`). Renders happen when the view is invalidated, as without a dialog.
2. **Info computes behind dialogs.** The Info engine (`NewOverviewInfo`) recomputes from its cache while a dialog is open. A recompute reads the cache only. The first kubelet stats round is not held back, in any engine. Later stats rounds and the metrics polls still wait for the dialog to close. `/overview` keeps the pause of ADR 0051 for its recomputes.
3. **A paused poller keeps its snapshot.** A metrics poller that keeps running while a dialog holds its polls back hands out its last snapshot as usable, instead of "loading". The readers show its age, and the two-minute stale rule still applies. A poller that stopped for lack of readers still waits for a new poll, as before.
4. **Pods being deleted leave the top list.** A pod with a deletion timestamp is on its way out. It still counts under Pods and in the capacity until it ends.
5. **The sparkline starts at its oldest sample.** Cells keep their fixed slice of the window and their alignment to absolute time (ADR 0177). The line starts at the cell of the oldest sample in the window and grows to the right, and the mark after the frame names its span while it is shorter than the window: `▕...▏ 5m of 15m`, then `▕...▏ 15m`. The room for the longest mark is kept from the start, so the cell width does not change as the line grows.
6. **The sparkline scales to its samples.** A full cell is the smallest of 10, 25, 50 and 100% that holds every sample in the window. The mark names it when it is not 100%: `top 10%`. The label keeps its form from ADR 0233.

## Alternatives considered

- **Keep the metrics polls running behind dialogs for Info.** That gives fresh usage, but the pollers are shared with the lists, which would then poll behind every dialog too. The usage age on the header line already says how old the values are.
- **Stretch the cells while the history is young.** A cell would cover a different time as samples arrive, which is what ADR 0177 removed.
- **A free scale (top = peak).** It changes with every new peak, and two bars with different scales look alike. Four fixed tops change rarely and can be named in a few characters.
- **Hide only evicted pods.** The API marks a pod evicted only through its deletion. A deletion timestamp covers evictions, deletes and rollouts the same way.

## Consequences

- Lists and Info behind a dialog now change on screen while it is open. Their state already changed before this decision, and only the drawing was frozen, so no dialog sees other data than before. The node actions and the delete confirmation keep the objects they were opened for.
- Each render with a dialog open renders the main view as well.
- Text and tests that expected the old sparkline mark (` 15m` only) must use the new one.
