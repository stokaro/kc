# 0323. Merge the $KUBECONFIG files as kubectl does

- Status: Accepted
- Date: 2026-11-24
- Issue: #323

## Context

kc loaded every kubeconfig file on its own. A context was tied to the
file that defines it, and every client for it loaded that file alone
(`ClientConfigLoadingRules{ExplicitPath: file}`). With
`KUBECONFIG=a:b`, a context in `b` that names a cluster or user in `a`
failed with "invalid configuration: no configuration has been
provided", although kubectl uses it. A context name defined in both
files was listed twice, and the second entry pointed at a cluster that
kubectl never uses for that name.

No design reason was recorded for this. kc does not write kubeconfig
files (the context picker and `--context` change kc state only), so
writes did not need the per-file model. The overlay that the shell gets
already relied on kubectl's merge: it goes in front of the user's files
in `KUBECONFIG`.

## Decision

The files of `$KUBECONFIG` (and of `--kubeconfig`, which sets it) are one
kubeconfig, merged the way `clientcmd` merges them: each named entry
comes whole from the first file that has it.

- `kubeconfig.Context` keeps `Kubeconfig`, the file that holds the
  context entry. It is the file a change to the entry belongs in, as
  `kubectl config set-context` writes there, and the file the picker
  names.
- `kubeconfig.Context.Source` is what a client loads: for a context from
  `$KUBECONFIG`, the list of files joined with the path list separator
  and the merged config. `kubeconfig.LoadingRules` turns that path into
  `ExplicitPath` for one file and `Precedence` for a list.
- Every client for a named context uses it: the cluster pool (startup,
  the context picker, `C-x c`), the describe loader, kubectl edit, shell
  and attach, and the generated shell kubeconfig. kubectl's
  `--kubeconfig` takes one file, so for a list kc passes `KUBECONFIG`
  in the environment instead.
- A context name defined in several files of `$KUBECONFIG` is listed
  once, from the first file.
- Without `$KUBECONFIG`, kc still reads `~/.kube/config` and the other
  files under `~/.kube`, each on its own, as kubectl would with
  `--kubeconfig` naming that file.

## Consequences

- A context split across files works in kc as in kubectl.
- A context hidden by an earlier file of `$KUBECONFIG` is no longer in
  the picker. kubectl cannot reach it under that list either.
- The `copy` terminal mode writes the merged config, since the file that
  defines the context may not hold its cluster or user.
