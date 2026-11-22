# 0038. "Faults only" toggle for object lists

- Status: Accepted
- Date: 2026-11-22
- Issue: #38

## Context

k9s shows only rows in a fault state on `C-z`. kc colors unhealthy rows (`internal/models/row_style.go`) but cannot show them alone. The issue asks for a toggle (shortcut and F2 Options) that keeps the rows the row styler marks unhealthy, with "faults" in the panel frame, sharing code with the row filter of #30.

Open questions: the key; what counts as a fault per kind; how the toggle and the stylers stay in agreement; whether the toggle is saved as a default.

## Decision

1. Key: `C-z`, as in k9s. It only acts when panels have the keyboard (no terminal, command or typed shell input). F2 Options shows "Faults only: Yes/No" in the Objects section for lists that can filter. `C-s` "Save as defaults" does not store it; it is view state of the folder, like the row filter.
2. The toggle is the `FaultsOnly` field of the row filter (ADR 0030). It combines with the text filter (AND), lives on the folder, survives live updates, and shows in the frame label as `faults` (`Filter: faults`, or `Filter: !web, faults`). A click on the label clears both.
3. One classification for colors and the filter. `internal/models/row_health.go` maps a GVR to a rule that returns a `RowHealth` (OK, Succeeded, NotReady, Failed, Unknown). The row stylers color NotReady and Failed red and Succeeded green from it, and the filter keeps NotReady and Failed. A row that is red is a row the toggle keeps.
4. What a fault is:
   - Pods: phase Pending, Failed or Unknown; or not Ready; or a container (init or regular) waiting for a reason other than `ContainerCreating`/`PodInitializing` (CrashLoopBackOff, ImagePullBackOff, ErrImagePull, CreateContainerConfigError, ...). Succeeded pods are not faults, although their Ready condition is False.
   - Deployments: `Available=False`, or fewer ready or available replicas than `spec.replicas`.
   - StatefulSets and ReplicaSets: fewer ready (or available) replicas than `spec.replicas` (1 when unset). Scaled to zero is fine.
   - DaemonSets: fewer ready or available pods than `desiredNumberScheduled`.
   - Jobs: `Failed=True`, or failures reached `backoffLimit`. A Job that retries below the limit is not a fault.
   - Nodes: the `Ready` condition is not True (False or Unknown). Cordoned nodes are not faults.
   - PVCs: not Bound (Pending is NotReady, Lost is Failed).
   - PVs: Failed. Available and Released are normal.
   - Other kinds, including CRDs: no rule, never a fault. Being deleted is not a fault by itself.

## Alternatives considered

- `M-z` or another Meta key in mc style: mc has no such feature to copy, and k9s users already know `C-z`. In raw mode `C-z` arrives as a key, so it does not suspend kc; in the full-screen terminal (`C-o`) it still goes to the shell.
- Compare the style a row got with `NotReadyStyle()`: works today, but ties the filter to colors and breaks when a styler changes or a user theme arrives.
- A separate fault classifier next to the stylers: two sources of truth would drift.
- Count recent restarts as a fault (k9s marks pods with restarts): an old restart says little about the current state; a crash loop is caught by the waiting reason.
- Generic `Ready`/`Available` condition rule for every kind (CRDs): many CRDs use these names with other meanings; left for a later, opt-in change.

## Consequences

- Row colors change a little: completed (Succeeded) pods are no longer red, failed pods are bold red, Pending pods, StatefulSets and ReplicaSets below their replica count, unbound PVCs and failed PVs are now colored.
- New kinds need one rule in `healthRules`; the color and the toggle follow.
- Health is read from the object in the cached Table row, so it is as fresh as the list.
- Follow-up: rules for CRDs (for example Cluster API, cert-manager `Ready`), maybe configurable.
