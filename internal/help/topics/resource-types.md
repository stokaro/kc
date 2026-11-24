# Resource types

A cluster with operators serves well over a hundred resource types. kc
lists the types you read and change day to day, the Work profile, on
the root, in the resources of a namespace and in the cluster resources.
The rest stay one step away under "other types".

## How to read it

- Work lists the core types (pods, services, configmaps, secrets,
  nodes, namespaces, events and the like), the apps, batch, ingress,
  autoscaling, policy and storage class types, and the types of common
  tools: cert-manager, Argo CD, Flux, Prometheus Operator, Gateway API,
  Cilium and Calico. A type is in Work by its group and resource name;
  its served version does not matter.
- The last row of a type list, `/other types (N)`, counts the
  discovered types Work leaves out. `Enter` on it lists them.
- "Include empty" still applies inside Work: an empty Work type stays
  hidden while "Include empty" is No.
- A type Work leaves out costs nothing: kc counts and watches only the
  types it lists.
- Inside a type Work leaves out, the top border of a List panel shows
  `outside Work` at the right, after the path. A panel too narrow for
  both keeps the path.

## The list of types

"Other types" and "Browse all types..." in the Left or Right menu
(`F9`) open the same dialog. Each row shows the plural name and the API
group and version; `cluster` marks a cluster-scoped type. In the list
of all types the note says whether Work lists the type.

- Type to filter by name, kind, short name or API group.
- `Enter` or a click opens the type's objects, as `cd` would.
- `Esc` clears the filter, then closes.

The dialog is a shortcut, not a folder: the path, the history and a
hotlist entry name the type itself, so they open it again directly.

## Limits

- "Types" in the panel options (`M-F1`, `M-F2`) switches one panel
  between Work and All; `C-s` there saves it as
  `resources.visibility.defaultProfile`. All lists every discovered
  type, as kc did before Work.
- Work is a fixed list. Adding or removing your own types is not
  possible yet.
- A type is never hidden from an explicit jump: `cd`, the hotlist, the
  history, links between objects and a namespace change that keeps the
  open list all open types Work leaves out.
