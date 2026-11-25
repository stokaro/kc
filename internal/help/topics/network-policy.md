# NetworkPolicy between two pods

`C-x n` explains whether the NetworkPolicies of the cluster let the pod
under the cursor of the active panel (the source) open a connection to
the pod under the cursor of the other panel (the destination). The
result opens in the active panel; `/..` goes back.

## How to read it

The first rows fix what was evaluated: the source and destination pod
with namespace, name, UID and IPs, and the time of the read. The result
is a snapshot: moving a cursor, or a pod being replaced, does not change
it. Press `C-x n` again to read again.

Then one row per destination port: every declared container port, every
port a policy names, and `TCP other ports` for the ports no rule names.
Each port row says `allowed`, `denied` or `unknown`, and is followed by
two rows:

- `egress`: the policies of the source namespace that select the source
  pod for egress, and whether one of their egress rules admits the
  destination on that port,
- `ingress`: the policies of the destination namespace that select the
  destination pod for ingress, and whether one of their ingress rules
  admits the source.

Under each side are the NetworkPolicies behind the decision, with the
rule (`spec.ingress[1]`) and why it matches: `allows` for a rule that
matches, `may allow` for one kc cannot decide, and `isolates` for the
policies that select the pod when nothing allows the connection.
`Enter` or `F3` on a policy opens it.

A connection is allowed when both sides allow it, denied when either
side denies it, and unknown otherwise. A pod no policy selects for a
direction is not isolated for it, so that side allows everything.
Allowances of several policies add up; there is no deny rule.

## What is modeled

Pod selectors, namespace selectors (with the namespace labels), their
combination, `ipBlock` with `except`, protocols, numeric ports, port
ranges (`endPort`) and named ports, resolved on the destination pod.
`policyTypes` as the API defines them: without it, a policy isolates
for ingress always and for egress when it has egress rules.

## Limits

- This is policy configuration only. kc does not probe a connection,
  and an allowed result does not prove that traffic flows.
- Plugin-specific policies (Calico, Cilium, AdminNetworkPolicy and
  others) are not read. Whether the network plugin enforces
  NetworkPolicy at all is not known.
- An `ipBlock` that contains a pod IP gives `unknown`: whether ipBlock
  applies to pod traffic depends on the plugin.
- Pods on the host network give `unknown`.
- Policies or namespaces you may not read give `unknown` for the rules
  that need them, never `allowed`.
- Both pods must be in one cluster: both panels in the same context.
  With two contexts kc refuses and names them; the policies of one
  cluster say nothing about a pod of another.

## See also

- [Diagnosis](diagnosis.md)
- [Owners, used by and requires](related.md)
