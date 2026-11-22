# 0042. Read-only mode

- Status: Accepted
- Date: 2026-11-22
- Issue: #42

## Context

kc enabled F4, F7 and F8 whenever a client existed. Users who browse production clusters want kc to refuse changes, like k9s does with `--readonly` and `readOnly: true`. The issue asks for a flag, a per-context setting, disabled write keys and debug helper, hidden dangerous custom commands, and an "RO" marker in the panel frame.

Open questions: the config shape and how contexts are matched; how the flag and the config combine; where the decision lives so that the access review work of #69 (PR #102) and the open write features (#80 object actions, #91 port-forward, #96 node shell, #105 copy, #88 exec context) share one gate; how a custom command says whether it writes; what happens to a key pressed while it is grayed.

## Decision

### 1. One gate: `access.Policy{ReadOnly}.Permits`

`internal/access` holds `Policy{ReadOnly bool}` and `Permits(decide, checks...)` with the same names and semantics as PR #102: an action is described by `access.Check` values (verb, GVR, subresource, namespace, name), and read-only refuses every check whose verb is not get, list or watch. `App.accessPolicy()` returns the policy for the current context and is the only place that reads the read-only setting; `App.permitAccess` feeds it to the panel capabilities through `PanelEnvironment.Permit`. When #102 merges, its `accessPolicy()` body becomes `access.Policy{ReadOnly: a.readOnly()}` and its RBAC answers go in through `decide`; nothing else changes.

The shared part of `internal/access/access.go` and `internal/ui/access_gate.go` is copied from PR #102 so that the merge keeps #102's files. Read-only only helpers (`CreateNamespaceChecks`, `DebugHelperChecks`, `PermitsCommand`, `ReadOnlyRefusal`) live in `internal/access/readonly.go`.

### 2. Config: `readOnly.enabled` and `readOnly.contexts`

```yaml
readOnly:
  enabled: false
  contexts: ["*prod*", "shared-staging"]
```

`enabled: true` makes every context read-only. `contexts` lists exact names or globs. The glob follows the context color rules of #43 (PR #89): `*` matches any run of characters including `/` (EKS ARNs, OpenShift user contexts), `?` one character, everything else is literal, the whole name must match, and matching is case-sensitive like kubeconfig names. Defaults are off and empty, so existing configs behave as before.

### 3. Flags: `--readonly` and `--write`, flag beats config

`--readonly` forces read-only for every context of the run. `--write` forces writes on and ignores the config, for the one time a user needs to change a cluster their config protects. Kong refuses both together (`xor`). Without a flag the config decides. The names follow k9s.

### 4. What read-only refuses

- F4 (`get` and `patch`, as kubectl edit sends a patch), F7 (`create namespaces`) and F8 (`delete`).
- Every `models.ConfirmedAction`: such a row changes the cluster by definition. Today that is the pod filesystem debug helper. The pod filesystem folder also gets `Deps.ReadOnly` and shows "Debug helper disabled (read-only mode)" instead of the consent row, and the helper's `Confirm` refuses on its own.
- Custom commands with `dangerous: true` (decision 5).

Browsing the pod filesystem (exec running ls and cat) stays allowed: it reads, and it is the way to look into containers. The integrated terminal and the shell command stay available. Read-only mode is a guard against accidental changes from kc's own keys, not a security boundary; RBAC is.

### 5. Custom commands declare `dangerous: true`; unknown commands run

kc cannot tell what a shell command does. A command that changes the cluster sets `dangerous: true` (the k9s plugin name, and the issue's). In read-only mode dangerous commands are hidden from the F9 menu, F9 is grayed when only dangerous commands apply, and starting one (from the menu or a namespace restart) is refused. A command without the flag runs. The default commands (top nodes, top pods, shell) are not dangerous.

### 6. Blocked at the handler, explained on the key

The function bar grays F4, F7 and F8. Each handler and each request function behind it (`editSelectionForPanel`, `runKubectlEdit`, `deleteResourceForPanel`, `performDelete`, `createNamespaceForPanel`, `createNamespaceWithName`, `showConfirm`, `handleConfirmResult`, the command menu callback, `startNamespaceCommand`) checks the policy first and returns the toast "Read-only mode: <action> is disabled". A grayed write key pressed in read-only mode (F-key, `Esc <digit>`, a click on the bar) reaches its handler on purpose, so the user sees why nothing happens.

### 7. "RO" in the frame

The bottom border shows a red ` RO ` before the context label (`─ RO  prod : default`). It follows the context: a context switch redraws it and the function bar, and the toast says "Context: X (read-only)".

## Alternatives considered

- **A `readOnly` flag on the `contexts` entries of PR #89.** One list for color and read-only reads well, but it ties this PR to an unmerged one and mixes looks with safety. A separate `readOnly` section with the same glob rules can be folded into #89's list later if wanted.
- **Regex patterns, as #89 also allows.** Globs cover the use case ("*prod*"); a regex can be added later without breaking configs.
- **`path.Match` globs.** `*` stops at `/`, so `*prod*` would miss EKS ARNs.
- **A `readOnlySafe: true` opt-in per command, refusing unknown commands.** Safer by default, but it would disable every existing user command and the defaults the moment read-only is on, and the shell (C-o) can do the same anyway. Opt-out with `dangerous` matches k9s and the issue.
- **Gray dangerous commands in the menu instead of hiding them.** The command selector has no disabled entries, and the issue asks for hiding; the RO marker tells why they are missing.
- **Only gray the keys.** A key path that skips the function bar (Esc digits, mouse, a future binding) would still write. The handler gate closes those paths.
- **A boolean `--readonly` only, or `--readonly=false`.** There would be no way to override a config that protects a context. `--write` is explicit.
- **Its own read-only package.** Two gates for one question (may kc do this?) would drift; #102 already proposes `access.Policy` for exactly this.

## Consequences

- Gates added when the features merged: #80's Run now and Suspend/Resume are cluster actions behind the same confirmation, which refuses them; #91 port-forward (`access.PortForwardChecks`) in `portForwardForPanel`; #96 node shell through its confirmation and a `create pods` check in `startNodeShell`; #88 keeps `refuseCommand` on the command start path. #105 copies only out of a container (exec, a read), so it needs no gate until a copy into a container exists. Open: whether `M-x`/`M-t` (#106, `kubectl exec -it`/`attach`) should be refused; `App.execGate` is the place, but this ADR keeps exec for reading allowed, so it needs a decision.
- With #102 merged, a denied RBAC answer and read-only mode share `Permits`; the handler refusal keeps calling `Permits(nil, ...)` so its message stays about read-only.
- A command that already runs in a panel keeps running after a switch to a read-only context; only new starts and namespace restarts are refused.
- Users must mark their own writing commands `dangerous: true`; the README says so.
