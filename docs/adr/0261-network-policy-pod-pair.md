# 0261. Explain NetworkPolicies between two pods

- Status: Accepted
- Date: 2026-11-23
- Issue: #261

## Context

Service diagnosis says a consistent Service does not prove the network
works, and stops there. The question an operator asks next is narrower:
do the NetworkPolicies let this client pod reach this server pod? The
two panels already give a source and a destination.

The issue asks for one pod pair, a protocol and a port, the standard
`networking.k8s.io/v1` semantics in each direction, links to the policies
and rules responsible, and unknown results where data is missing. It
suggests reusing netpol-analyzer.

## Decision

1. `C-x n` takes the pod under the active panel's cursor as the source and
   the pod under the other panel's cursor as the destination. Both must
   be pod rows; anything else gives a toast. The result is a snapshot
   folder pushed onto the active panel, like `M-e` (explain). It fixes
   both pods by namespace, name and UID, and the read time, at the key
   press. Moving a cursor or replacing a pod does not retarget it. If the
   pod read at load time has a different UID from the row, the result
   says the pod was replaced and gives no verdict.

2. No port dialog. The result evaluates every port the destination
   declares, every port a selecting rule names, and "TCP other ports"
   (any port no rule names). This answers the issue's protocol-and-port
   question for all relevant ports at once, and costs no extra keys.
   For a port that is not declared and not named, "other ports" gives
   the answer.

3. A small evaluator in `internal/netpol`, with no Kubernetes client: pod
   and namespace selectors and their combination, `ipBlock` with `except`,
   protocols, numeric ports, `endPort` and named ports resolved on the
   destination pod. `policyTypes` follows the API defaults. Each side
   gives allowed, denied or unknown. The pair is allowed when both sides
   allow, denied when either denies, unknown otherwise. Each verdict lists
   the policies and rule indexes (`spec.ingress[1]`) behind it, or the
   isolating policies when nothing allows.

4. Unknown instead of a guess:
   - an `ipBlock` that contains a pod IP: whether ipBlock applies to pod
     traffic is up to the plugin;
   - pods on the host network;
   - policies or namespace labels that could not be read. A denied read
     makes the rules that need it unknown, never allowed.

5. Reads go through `scopedReader` with the per-type scope decision of
   [0056](0056-namespace-scoped-access.md): pods and NetworkPolicies in
   the two namespaces, Namespace objects for labels. A namespace-only user
   can check a pair inside their namespace. The read happens once in a
   `tea.Cmd`; no watch stays open, and the leases are released when the
   folder closes.

## Deviations from the issue

- netpol-analyzer is not used. It analyzes a whole set of resources,
  which kc would have to list across the cluster first, and it brings a
  large dependency tree. The pair question needs the policies of two
  namespaces and the labels of two namespaces. The evaluator is about 500
  lines with table tests for each rule of the API semantics. If kc later
  needs whole-namespace connectivity maps, that is the time to adopt it.
- No "explicit picker" for pods outside the panels. The panels are the
  picker, and a second picker would duplicate navigation.
- Only the panel's context. Pods in two contexts cannot talk through
  NetworkPolicy in any meaningful way.

## Limits

- Policy configuration only. kc does not probe a connection, and an
  allowed result does not prove that traffic flows.
- Plugin-specific resources (Calico, Cilium, AdminNetworkPolicy, and
  others) are not read, and kc cannot know whether the plugin enforces
  NetworkPolicy at all. The help page and a note row say so.
- Services are not modeled: policies apply to pod IPs after the Service
  translation, so the pair is pod to pod.

## Consequences

- A new `internal/netpol` package with no cluster dependency, and a new
  folder type `NetworkPolicyPairFolder` with its own F1 page.
- `C-x n` is reserved for kc; custom commands may not bind it.
