# Context and namespace

kc works in one kubeconfig context at a time, and each panel has its
own namespace scope: one namespace, or all namespaces like
`kubectl -A`. The bottom border of each panel shows both, for example
`prod : shop`. The pickers change them without touching your
kubeconfig.

## How it works

Namespace scope:

- Click the namespace in a panel's bottom border, or choose
  "Namespace..." in that panel's Left or Right menu (`F9`).
- The picker lists "All namespaces" first, then every namespace. The
  current one is marked `current`.
- The panel root then lists the types of that scope. A panel on a
  resource list stays on that resource in the new scope; a panel deeper
  down goes back to its root.
- Lists across all namespaces get a Namespace column, and the root
  shows no object counts, since counting would watch every object.
- `/namespaces/<name>` still browses one namespace without changing the
  scope.

Context:

- Click the context name in the bottom border, or choose "Context..."
  in the Left or Right menu.
- The picker lists every context of the kubeconfig files kc loaded. The
  current one is marked; a name defined in several files shows the
  file.
- The files of `$KUBECONFIG` (or `--kubeconfig a:b`) are one kubeconfig,
  as for kubectl: a context may use a cluster or user from another file
  of the list, and a name defined in several of them comes from the
  first. The other files kc finds under `~/.kube` stand alone.
- After a switch both panels show the new cluster, each at its root in
  the context's namespace (else `default`). Pinned panels are released.

At startup, `--context NAME`, `-n NAME` and `-A` set the context and
scope of both panels, as in kubectl; `-A` wins over `-n`.

## Keys

- Typing filters the picker by name; `Backspace` removes a character.
- `Up`, `Down`, `PgUp`, `PgDn`, `Home`, `End` move.
- `Enter` or a click picks the entry.
- `Esc` clears the filter, or closes the picker when there is none.
- `cd -n NAME` and `cd -A` on the shell line change the scope too.

## Limits

- When you may not list namespaces, the picker is titled "Namespace
  (type a name)". It offers the context's namespace, the panels' scopes
  and the namespaces used in this session; type another valid name and
  press `Enter`.
- With access granted only inside a namespace, kc watches a type in that
  namespace only and hides types you cannot read.
- Context and scope are kc state only. The kubeconfig file, and its
  current context, are never changed.
- Read-only mode follows the context: a switch to a protected context
  shows `RO` in the frame and "(read-only)" in the message.

## See also

- [Panels](panels.md)
- [Shell and terminal](shell.md)
- [Read-only mode](read-only.md)
- [Access](access.md)
- [Hotlist and saved views](hotlist.md)
- [Create namespace](namespace-create.md)
