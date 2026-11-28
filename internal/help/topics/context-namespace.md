# Context and namespace

Each panel has its own kubeconfig context and its own namespace scope:
one namespace, or all namespaces like `kubectl -A`. The left panel can
show staging while the right one shows prod, even when both have a
namespace and objects of the same names. The bottom border of each
panel shows both, for example `prod : shop`. The pickers change them
without touching your kubeconfig.

## How it works

Namespace scope:

- Click the namespace in a panel's bottom border, or choose
  "Namespace..." in that panel's Left or Right menu (`F9`).
- The picker lists "All namespaces" first, then every namespace of the
  panel's context. The current one is marked `current`.
- The panel root then lists the types of that scope. A panel on a
  resource list stays on that resource in the new scope, even when the
  root hides the type there as empty; a panel deeper down goes back to
  its root. When the cluster no longer serves the type, kc says so and
  the panel goes to its root.
- Lists across all namespaces get a Namespace column, and the root
  shows no object counts, since counting would watch every object.
- `/namespaces/<name>` still browses one namespace without changing the
  scope.

Context:

- Click the context name in a panel's bottom border, or choose
  "Context..." in the Left or Right menu. Only that panel switches; the
  other one keeps its context, place, cursor, marks, filter, pin and
  running streams.
- "Context for both panels..." in the Command menu switches both
  panels at once.
- The picker lists every context of the kubeconfig files kc loaded,
  sorted by name. The panel's context is marked `current`, the other
  panel's with that panel's name (`left` or `right`); a name defined in
  several files shows the file.
- The files of `$KUBECONFIG` (or `--kubeconfig a:b`) are one kubeconfig,
  as for kubectl: a context may use a cluster or user from another file
  of the list, and a name defined in several of them comes from the
  first. The other files kc finds under `~/.kube` stand alone.
- After a switch the panel shows the new cluster at its root in the
  context's namespace (else `default`). Two panels in one context share
  one connection and one cache.
- A switch that fails leaves the panel where it was. When you pick
  again before a slow switch finishes, the last pick wins.

What follows which context:

- Describe, Manifest, Logs and Info panels show the other panel's row,
  so their border names the other panel's context. A pinned panel keeps
  the context of the pinned object.
- `F4`, `F8`, `F2` actions, port forwards, shells and every other action
  run in the context of the row they act on, with that context's
  read-only setting. Confirmations name the context.
- The shell line follows the active panel's context and namespace (the
  followed panel's, for a Describe, Manifest, Logs or Info panel).
- Panel history and hotlist entries remember the panel's context; going
  to one in another context switches only that panel.
- A link (`Enter` on an owner, a related object, a dashboard row) opens
  the object in the other panel, which switches to the link's context
  when it showed another one.

At startup, `--context NAME`, `-n NAME` and `-A` set the context and
scope of both panels, as in kubectl; `-A` wins over `-n`. Without
`--context`, both panels start in the kubeconfig's `current-context`.
When it names a context the kubeconfig does not define, or when no file
sets one and there are several contexts, kc does not open: it prints
the contexts and asks for `--context`. With one context and no
`current-context`, kc opens that context and says so.

kc draws before it connects. Until the start context's server answers,
the screen shows "Connecting to NAME (SERVER)… 12s · F10 quits", and
`F10` or `C-c` quits. When connecting fails, the screen shows the error:
`Enter` opens the panels anyway, `F10` quits.

A list that cannot be read says why where its rows would be, in red,
with the key that helps below: "Cannot reach 10.0.0.1:6443: connection
refused" (`C-r` retries, Context... picks another), "Not allowed to
list secrets in shop (user dev)", "Namespace shpo does not exist"
(Namespace... picks another). The same plain words replace the raw
client errors in messages: a timeout, a rejected token (401) and a
certificate kc does not trust are named as such. Without a kubeconfig,
the panels name the file kc looked for.

## Keys

- Typing filters the picker by name; `Backspace` removes a character.
- `Up`, `Down`, `PgUp`, `PgDn`, `Home`, `End` move.
- `Enter` or a click picks the entry.
- `Esc` clears the filter, or closes the picker when there is none.
- `cd -n NAME` and `cd -A` on the shell line change the scope of the
  active panel too.

## Limits

- When you may not list namespaces, the picker is titled "Namespace
  (type a name)". It offers the context's namespace, the panels' scopes
  and the namespaces used in this session; type another valid name and
  press `Enter`.
- With access granted only inside a namespace, kc watches a type in that
  namespace only and hides types you cannot read.
- Context and scope are kc state only. The kubeconfig file, and its
  current context, are never changed.
- Read-only mode follows each panel's context: a protected context
  shows `RO` in that panel's frame, and its switch message says
  "(read-only)".
- Operations within one cluster refuse two contexts: `C-x n` needs both
  pods in one cluster. Nothing copies or applies objects from one
  cluster to the other.

## See also

- [Panels](panels.md)
- [Shell and terminal](shell.md)
- [Read-only mode](read-only.md)
- [Access](access.md)
- [Hotlist and saved views](hotlist.md)
- [Create namespace](namespace-create.md)
