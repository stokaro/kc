# Access

`M-k` shows what you may do with the selected object or resource type:
allowed, denied or unknown for each verb, with the reason and the
matching `kubectl auth can-i` command. Use it when a key is grayed out,
when a request fails with "forbidden", or before you start a change on
a cluster where your rights are narrow.

## How to read it

- `Context:` names the kubeconfig context and the identity kc acts as:
  the kubeconfig user, or the user, UID and groups kc impersonates.
- `Target:` is the object, or `(any object)` for a resource type row.
  A type without a namespace is checked cluster-wide.
- `Read-only mode: writes stay disabled whatever RBAC allows` appears
  when read-only mode is on for the context.
- One line per check: the verb, the resource and the result. The verbs
  are get, list, watch, create, update, patch and delete. Pods add the
  subresources kc uses: log, exec, portforward, attach, eviction and
  ephemeralcontainers. Deployments, StatefulSets, ReplicaSets and
  ReplicationControllers add scale. `(any)` after a resource means the
  check is for the type, not this one object (list, watch, create).
- The result is `allowed`, `denied` or `unknown`. `checking` means the
  answer has not arrived. `(stale)` means the answer is older than 30
  seconds and a new one is on its way.
- Under the list: the reason for the selected line, as the API server
  gave it, and `$ kubectl auth can-i ...` with `--context` and the
  impersonation flags.

Each line is one SelfSubjectAccessReview, so the answer comes from the
API server and matches what kubectl says. Unknown means the review
failed or could not be evaluated.

## How kc uses the answers

`F4`, `F8`, and `F3` on container logs are grayed out when the review
says denied (`F4` needs get and patch). Unknown keeps the key enabled:
the API server still decides each request. With marked rows, `F8` is
grayed out when every marked object is denied, and names the denied
ones when only some are.

## Keys

- `M-k` or `Esc k` opens the view for the row under the cursor.
- `Up`, `Down`, `Home`, `End` and the mouse wheel move the selection.
- `Enter` copies the `kubectl auth can-i` command of the selected line.
- `r` asks the API server again.
- `Esc` or `F10` closes the view.

## Limits

- The view says what RBAC and the other authorizers allow. It does not
  check admission rules, quotas or policy engines.
- Custom commands are not checked: kc cannot know what a shell command
  does.
- With rights only inside some namespaces, kc reads resources from
  those namespaces and hides the types you cannot read.

## See also

- [Read-only mode](read-only.md)
- [Context and namespace](context-namespace.md)
- [Delete](delete.md)
