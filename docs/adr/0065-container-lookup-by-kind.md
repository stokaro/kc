# 0065. Look up a pod container by its kind and name

- Status: Accepted
- Date: 2026-11-22
- Issue: #65

## Context

F3 and F5 on a container row read the container's entry from the pod and
show or export it. The lookup searched `spec.containers`, then
`spec.initContainers`, then `spec.ephemeralContainers`, and for ephemeral
entries it read `metadata.name`, a field those entries do not have. Every
ephemeral container, kc's own `kc-fs-helper-*` and `kc-fs-ptrace-*` debug
helpers included, was reported as not found.

The row already knows which list it came from: the container browser has one
folder per kind, and the kubectl command copied for a row selects
`.spec.<list>[?(@.name=="<name>")]` in that list. The issue asks that the
lookup be explicit about the kind.

API validation keeps container names unique across all three lists of a pod,
so a name alone would also find the right entry today.

## Decision

The viewer looks up a container in the one list its row belongs to, and
matches the top-level `name` field. `containerViewContent` and
`findContainer` take the row's `containerKind`, and the list name comes from
`containerField`, the same function that builds the copied kubectl command.
A miss names the kind, for example `ephemeral container "x" not found`.

## Alternatives considered

- Keep searching all three lists by name and only fix the ephemeral branch.
  It is a smaller change and works because names are unique, but the viewer
  and the copied kubectl command could then disagree about which entry a row
  means, and the lookup would hide a row whose kind is wrong.
- Decode the pod into `corev1.Pod` and look the container up in the typed
  lists. The typed structs would then be marshaled back to YAML, which adds
  a conversion step and a second shape to keep in step with the unstructured
  object every other viewer reads, for no gain in this lookup.

## Consequences

The viewer, the export and the copied kubectl command now resolve a row to
the same pod spec entry. Other lookups by container name (log target choice,
debug helper status in `EphemeralContainerStatuses`, filesystem readiness)
already read the typed `Name` field and needed no change.
