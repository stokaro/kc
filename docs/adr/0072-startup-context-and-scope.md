# 0072. Startup context and all-namespaces scope

- Status: Accepted
- Date: 2026-11-22
- Issue: #72

## Context

kc has a context picker and a per-panel namespace scope that includes
"all namespaces", but the command line only offered `--kubeconfig`,
`-n/--namespace` and the impersonation flags. Commands copied from kubectl,
such as `kubectl --context staging -A get pods`, could not be replayed with
kc. The issue leaves open which panels the flags affect, how `-n` and `-A`
combine, which flag combinations fail, and how an unknown context is
reported.

Constraints: kubeconfig must not be rewritten; the existing scope model
(`models.AllNamespaces`, `goToNamespace`, the context the cluster pool is
keyed by) is reused instead of a second navigation path; `cmd/kc/main.go`
stays thin.

## Decision

1. Flags. `--context NAME` and `-A/--all-namespaces` are global kong flags,
   like `-n`, so they work before or after `get` and `logs`. `--kubeconfig`,
   `--as`, `--as-group` and `--as-uid` already exist and are unchanged. They
   reach the UI as `ui.RunConfig.Context` and `ui.RunConfig.AllNamespaces`.
2. Both panels. The flags set the whole application, as the context picker
   does and as kubectl users expect: both panels connect to the requested
   context, both start in the requested namespace scope, and the panel
   labels, the terminal's kubeconfig overlay and copied kubectl commands
   follow from that state.
3. Context lookup. kc looks the name up in the kubeconfig files it loads
   (`--kubeconfig`, else `$KUBECONFIG`, else `~/.kube`). When several files
   define it, the first one wins, as when kubectl merges `$KUBECONFIG`. The
   selection is kc state only; `current-context` in the file stays as it is,
   so copied commands get `--context NAME` when it differs from plain
   kubectl's context.
4. Unknown context. `ui.Run` returns `ui.ErrUnknownContext` before it
   connects and before the Bubble Tea program takes the terminal. The
   message names the context and the kubeconfig files searched; kc exits
   with status 1. Other startup problems keep today's behavior (a warning,
   then the UI).
5. `-n` and `-A`. `-A` wins and `-n` is ignored, as in kubectl
   ("Namespace in current context is ignored even if specified with
   --namespace"). Copied commands that carry both therefore behave the same.
   An omitted namespace still means the context's namespace (else
   `default`); only `-A` selects the all-namespaces scope, because
   `models.AllNamespaces` is the empty string and could not be told apart
   from "not set" otherwise.
6. Unsupported combinations fail before the UI starts:
   - `kc get -A` with an object name (`kc get pods web -A`,
     `kc get pod/web -A`): kubectl rejects this too, and the all-namespaces
     list cannot select a unique row by name.
   - `kc logs -A`: kubectl logs has no `-A`, and a single pod must not be
     picked from an arbitrary namespace.

## Alternatives considered

- Only the active panel follows the flags. Rejected: a context is
  app-wide in kc (one cluster pool entry, one terminal overlay), and the
  context picker already switches both panels.
- Reject `-n` together with `-A`. Rejected: kubectl accepts it, and the
  point of the issue is replaying kubectl commands.
- Validate the context in `cmd/kc` with `clientcmd` loading rules.
  Rejected: kc discovers more files than kubectl when `$KUBECONFIG` is not
  set (every file under `~/.kube`), so the check belongs next to kc's own
  discovery, in `ui.Run`.
- Switch `current-context` in kubeconfig (`kubeconfig.Manager.SetCurrentContext`).
  Rejected: the issue requires kubeconfig to stay unchanged.
- `kc get -A NAME` selecting every row with that name across namespaces.
  Possible later, but kubectl does not do it and it needs row IDs that
  include the namespace.

## Consequences

- `kc --context x`, `kc -A`, `kc --context staging -A get pods` start in the
  requested state without touching kubeconfig.
- `resolveGetResources` and `goToResourceFolder` no longer default an empty
  namespace to `default`; the callers do, so the empty string can carry the
  all-namespaces scope. PR #92 moves these startup flows onto
  `NavigatePanel`; its `startupTypePath` must keep `res.namespace` empty for
  `-A` instead of defaulting it, or `-A get` lands in `default`.
- Switching context in the UI afterwards keeps today's behavior: both panels
  start in the new context's namespace, not in the `-A` scope.
