# 0178. Input commands on the overview command line

- Status: Accepted
- Date: 2026-11-23
- Issue: #178

## Context

The command line above the shell line shows the folder's kubectl command,
and `M-c` copies it. In `/overview` that was always
`kubectl get nodes,pods,deployments,statefulsets,daemonsets,jobs -n <ns>`,
whatever row the cursor was on. With `/nodes cpu` selected, the command
read no metrics, no limits and, in a namespace overview, not even the
cluster-wide pods the row adds up. The issue's other claims hold on
current main as well:

- `OverviewTargetsFolder.KubectlCommand` returned the first of the
  finding's commands only (the capacity rows have five).
- The disk and PVC findings held `/api/v1/nodes/<node>/proxy/stats/summary`
  with the placeholder left in, and the rollout finding held
  `deployment/<name>`.
- Commands carried `--context` but not kc's impersonation (`kc --as`), so a
  copied command ran as a different identity than the overview read with.

F3 already listed every input command of a finding.

## Decision

- A folder can implement `SelectionCommander`: its command line follows
  the selected row. `/overview` and the list behind a finding (Enter)
  implement it. A section header keeps the folder's command, now with the
  note `# objects the overview reads; a finding row shows its inputs`.
- A finding's line is labeled `Input:` or `Inputs (N):` instead of
  `Command:`. kc computes every finding, so the line never calls its
  command the command that produces the value.
- With one runnable command, `M-c` copies that command. With more, it copies
  all of them as one block: a first comment saying that kc computes the
  value from these inputs and that no single command reproduces it, then
  each input after a comment that says what it supplies. The line shows the
  first input and `# +K more, M-c copies all`, so the count is visible.
- Each finding with more than one input declares what each supplies
  (`supplies`, parallel to `kubectl`). A test runs every rule and fails
  when a finding has more inputs than descriptions.
- Placeholders are explicit (`<node>`, `<workload>`). On the finding's row
  an input with a placeholder is expanded once per target that resolves it
  (at most 20, then a count); on a target row it is resolved for that
  object. When nothing resolves it, it is copied only as a comment that
  says how to fill it in. A runnable line never contains a placeholder,
  and a test checks that for every rule.
- `kubectlCommand` adds `--as`, `--as-uid` and `--as-group` from the
  panel's cluster key, the same flags the exec commands already showed.
  This applies to every copied kubectl command, not only the overview's.

## Deviations

- No chooser dialog. The issue asks for "a compact multi-command chooser
  or copy-all action". Copy-all needs no new key or modal, and F3 already
  shows the inputs one per line for anyone who wants a single one. A
  chooser can be added later if copy-all turns out to be too coarse.
- The PVC finding's stats input stays a described template on its PVC
  rows. Its targets are claims, and the node is that of the pod that
  mounts the claim, which the target does not carry. The comment says how
  to find the node (`kubectl get pods -o wide`).
- Commands from the measured findings (`kubectl top`) are labeled as
  inputs like any other; the capacity row's comment says that the usage is
  a recent window. The issue's "time-windowed calculation" concern is met
  by the label and the copied header, not by a different command.
- Helm commands get no impersonation flags: `helm` has none.

## Consequences

- The command line of an overview row now matches what F3 lists, and a
  copied block can be pasted into a shell as it is: comments do nothing.
- Every copied kubectl command in an impersonating session changes, by
  adding the identity flags. That is what makes it read what kc read.
- Each new rule with several inputs must describe them, or the test fails.
- The line asks the folder for the selected row on each render; for
  `/overview` that is a lookup in the latest report snapshot, with no new
  cluster reads.
