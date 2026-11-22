# 0069. Access permissions for the selected resource

- Status: Accepted
- Date: 2026-11-22
- Issue: #69

## Context

kc enabled F4 and F8 when a client existed and discovery listed the verb. That says what the API offers, not what the current user may do, so users learned about a denial only after the request failed. The issue asks for an access view (allowed, denied, unknown per verb and subresource, with a reason and a `kubectl auth can-i` command) and for action availability that uses those answers together with the read-only preference of #42, which does not exist yet.

Open questions: how to ask the API, how long to trust an answer, how impersonation fits, what "unknown" does to a key, which actions are gated now, which key opens the view, and where #42 plugs in.

## Decision

### 1. One SelfSubjectAccessReview per check

Each check (verb, full GVR, subresource, namespace, name) is one `authorization.k8s.io/v1` SelfSubjectAccessReview. The access view sends one review per row; gating sends the few reviews the selected row needs.

`Allowed` maps to allowed. Not allowed maps to denied, with the authorizer's reason or "no RBAC rule or authorizer allows it" when the reason is empty. A non-empty `evaluationError` without `allowed` maps to unknown, because some rules could not be read. A failed request (Forbidden for the review itself, NotFound, timeout, network error) maps to unknown with the error as reason.

### 2. Cache per identity and check, 30 second TTL

`internal/access.Cache` keys results by identity (kubeconfig path, context, impersonated user, UID and sorted groups) and by the check. The TTL is 30 seconds (`access.DefaultTTL`). An expired answer is marked stale and still gates keys until the refresh lands, so a denied F8 does not flicker on every half minute; the view shows it as "allowed (stale)". Errors are cached for the same TTL so a server that refuses reviews is not asked on every keystroke. Reviews in flight are tracked, so the same check is never sent twice at once. A context or impersonation change produces another identity key: old answers are not reused, and new ones are asked on the next update. `r` in the view drops the view's answers and asks again. The TTL is a constant, not a config key; one can be added if somebody needs it.

### 3. Impersonation comes from the client

Reviews go through a clientset built from the current cluster's `rest.Config`, the same config the pool builds with the impersonation overrides. The API server therefore evaluates the review for the impersonated user and groups, which is what every other kc request is authorized as. The can-i command carries `--as`, `--as-uid` and `--as-group`, and `--context` when the context differs from the one plain kubectl uses.

### 4. Unknown keeps the action enabled

Gating only ever disables on an explicit denial. Unknown (no answer yet, failed or unsupported review, evaluation error) keeps the key enabled. The API server stays authoritative, and the original operation keeps its own server-side check. Gating reads the cache only and never blocks the update goroutine; reviews run in a `tea.Cmd` that ends with `accessResultMsg`, which redraws the function bar and an open access view. `App.Update` starts the reviews for the active panel's selected row after each message, which is a few map lookups when everything is cached.

### 5. Gated actions

- F4 Edit: `get` and `patch` on the object, because `kubectl edit` reads the object and sends a patch. Gating on `update` would allow F4 for a user who cannot patch.
- F8 Delete: `delete` on the object.
- F3 and F5 on a container logs row: `get` on `pods/log` for that pod.

The helpers for the rest exist in `internal/access` so the open work can use them without its own review code: `ExecChecks` (`create pods/exec`) for #96 and the pod filesystem, `PortForwardChecks` (`create pods/portforward`) for #91, and `Target.Check`/`SubCheck` for the F9 object actions of #80 (for example `patch cronjobs` for suspend, `create jobs` for "run now"). F9 custom commands are not gated: kc cannot know what a shell command does.

### 6. Type-level answer first for gating

For a named object, gating first asks the type-level question (same verb, namespace and resource, no name). If that is allowed, every object of that type is allowed and no named review is sent; moving the cursor across 200 pods costs three reviews, not 600. If it is denied, a named review follows, so `resourceNames` rules still enable the one object they name. The access view always asks the named question for the selected object and shows type-level rows (list, watch, create) as "(any)".

An authorizer that allows a whole type but denies one name would be shown as allowed by the gate. RBAC cannot express that; a webhook authorizer can. The view's named rows and the server's own check still show the truth.

### 7. M-k opens the view

`M-k` (and `Esc k`) opens the access view for the selected row of the active panel: an object, a resource type row (as "any object" in the panel's namespace, or cluster-wide when the namespace is empty), or a container logs row (its pod). M-k was free on main and in the open pull requests; the mnemonic is `kubectl auth can-i`. A function key was not used because F1 to F10 are taken, and an F9 menu entry waits for #97's menu bar.

### 8. Read-only mode plugs into `access.Policy`

`access.Policy{ReadOnly bool}.Permits(decide, checks...)` is the single gate: read-only refuses every mutating check (anything other than get, list, watch, including exec and port-forward, which are `create`), and a denied answer refuses the action. `App.accessPolicy()` returns an empty policy today; #42 sets `ReadOnly` from its flag and config there, and every gated key follows. The view shows a "Read-only mode" line when the policy is read-only.

## Alternatives considered

- SelfSubjectRulesReview per namespace: one request lists the rules, and the API documents it for UIs that show or hide actions. kc would then match the rules itself, reimplementing wildcard, `resourceNames` and subresource matching, and the list is `incomplete` under webhook and other non-RBAC authorizers, which is exactly where a UI hint is most likely wrong. Answers could also differ from `kubectl auth can-i`, which the view offers as the way to check. Per-check reviews cost more requests, which the cache and the type-level shortcut (decision 6) keep small.
- No cache, review on every render: blocks or floods the API server.
- A longer TTL (minutes): RBAC changes would take too long to show; 30 seconds matches the time a user waits before retrying.
- Unknown as disabled: a cluster that does not answer reviews would lose F4 and F8 entirely, while the actual request might succeed.
- Named review for every row: exact, but a request burst per cursor move. The type-level shortcut answers the common case with one review per verb and type.
- Reading the user name with SelfSubjectReview to show "who am I": useful, but another request and not needed for the decision. Left as a follow-up; the view shows the context and impersonation instead.

## Consequences

- F4, F8 and logs F3/F5 reflect RBAC within one review round trip after the selection settles; the first frame after a selection change still shows them enabled.
- Every review logs an INFO line with verb, GVR, subresource, namespace and name.
- Exec, port-forward and F9 object actions are not gated here because they are not on main yet (#91, #96, #80); they should call the `access` helpers when they land. The pod filesystem (Enter on a container) is navigation, not a function key, and is not gated.
- #42 adds its flag and config and sets `ReadOnly` in `App.accessPolicy()`; no other gating code changes.
