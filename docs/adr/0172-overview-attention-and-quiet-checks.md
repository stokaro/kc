# 0172. Attention summary, quiet checks and not-applicable rows

- Status: Accepted
- Date: 2026-11-23
- Issue: #172

## Context

`/overview` gives every rule a row, in a fixed section order. A healthy
cluster shows about thirty rows, most of them `0 ok`, and one warning
looks like any other row. The issue's claims hold on current main:

- `populate` renders every finding in rule order.
- A finding has a value, a state, a severity and an unavailable reason,
  but nothing says whether there was anything to check. `/jobs failed` in
  a namespace without Jobs reads `0 ok`, as if Jobs had been checked and
  passed.
- A cluster without cert-manager showed the certificate row as unavailable
  (`n/a`, dim, with "not served by this cluster"), in the same class as a
  forbidden read.

The user decided earlier (#163, declined) that findings stay folders:
Enter lists the objects, F3 explains the value.

## Decision

- **Not applicable is its own result.** `countFinding` marks a finding
  `notApplicable` when no object of the kinds it checks is in scope. Its
  value is `none` and its state `n/a`, and F3 says that nothing was
  checked. The kinds a rule only reads to judge others (PDBs, the Jobs of a
  CronJob, ReplicaSets, the nodes whose stats hold a volume) do not count
  as subjects. Rules over events in a window always apply: their zero
  means a quiet window.
- **An optional add-on that is not installed is not applicable.**
  `overviewOptionalKinds` names cert-manager's Certificates. When the
  cluster does not serve the kind, the row is not applicable, with the
  reason. Any other read error, such as forbidden, still makes it
  unavailable. A NoMatch error from a List now gets the same reason as the
  one from the informer.
- **An attention row comes first.** `/attention` shows the failed, warning
  and unavailable counts, then the ok and n/a counts. Enter lists the rows
  that need attention: failed, then warning, then unavailable, each in rule
  order. Unavailable checks are counted apart and never as passed. In a
  namespace where every namespace check is not applicable, the state says
  `no workloads in namespace <name>`.
- **Quiet checks fold.** Within each section, a finding that is not
  applicable, or ok with no objects behind it, moves into one
  `/N quiet checks` row at the end of the section. Enter on it lists those
  findings as the same finding rows, so Enter and F3 work there as well.
  Rows that are ok but carry figures (the capacity rows, which list nodes)
  stay visible. Section headers show the section's counts.

## Deviations

- No in-place collapse or toggle key. The issue asks for "collapsing
  healthy groups or showing healthy checks on demand" with "predictable
  focus behavior". A quiet row that opens a folder fits the folder model,
  needs no new key, and keeps focus simple: `/..` returns to the quiet row,
  and the main list never changes length under the cursor because of a
  toggle.
- No separate attention ordering of the main list. Sections keep rule
  order, so rows do not move when severities change. The attention row
  gives the priority order in one place.
- A finding that turns quiet moves into the quiet list, even while the
  cursor is on it; the cursor is then on another row. Other rows keep their
  IDs and order, which a test checks. Holding a recovered finding in place for a while is
  the lifecycle work of #190 (recovered findings kept for a bounded
  window), not this change.
- The PVC-near-full rule now needs the PersistentVolumeClaim list as well
  as the nodes. Its subjects are the claims in scope, so a namespace without
  claims is not applicable. It is unavailable when claims cannot be read,
  which matches the `kubectl get persistentvolumeclaims` input it already
  listed.

## Consequences

- One warning among many quiet checks is visible in the first row and in
  its section without scrolling past `0 ok` rows.
- Counts that used to read `0 ok` read `none n/a` where nothing was
  checked. Scripts do not read these rows; tests that expected `0 ok` were
  updated to expect the set of not-applicable rules by name.
- A new optional integration adds its kind to `overviewOptionalKinds`.
- The change does not add any cluster reads. It changes the presentation
  and one check in `countFinding`.
