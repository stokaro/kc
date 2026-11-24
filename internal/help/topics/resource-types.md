# Resource types

A cluster with operators serves well over a hundred resource types. kc
lists the types you read and change day to day, the Work profile, on
the root, in the resources of a namespace and in the cluster resources.
The rest stay one step away under "other types". Your own profiles,
per context or as the default, are set in the
[Resource visibility](resource-visibility.md) editor.

## How to read it

- Work lists the core types (pods, services, configmaps, secrets,
  nodes, namespaces, events and the like), the apps, batch, ingress,
  autoscaling, policy and storage class types, and the types of common
  tools: cert-manager, Argo CD, Flux, Prometheus Operator, Gateway API,
  Cilium and Calico. A type is in Work by its group and resource name;
  its served version does not matter.
- The last row of a type list, `/other types (N)`, counts the
  discovered types the profile leaves out. `Enter` on it lists them.
- "Include empty" still applies inside Work: an empty Work type stays
  hidden while "Include empty" is No. A type you added to Work, or
  checked in an Only selected profile, stays listed and its count says
  `0`, `…` while counting, `denied` or `n/a`.
- A type the profile leaves out costs nothing: kc counts and watches
  only the types it lists.
- Inside a type the profile leaves out, the top border of a List panel
  shows `outside Work` (or the profile's name) at the right, after the
  path. A panel too narrow for both keeps the path.
- Quick search (`C-s`) also finds a type the list does not show and
  opens it with `Enter`; see
  [Resource visibility](resource-visibility.md).

## The list of types

"Other types" and "Browse all types..." in the Left or Right menu
(`F9`) open the same dialog. Each row shows the plural name and the API
group and version; `cluster` marks a cluster-scoped type. In the list
of all types the note says whether the profile lists the type.

- Type to filter by name, kind, short name or API group.
- `Enter` or a click opens the type's objects, as `cd` would.
- `Esc` clears the filter, then closes.

The dialog is a shortcut, not a folder: the path, the history and a
hotlist entry name the type itself, so they open it again directly.

## Limits

- "Types" in the panel options (`M-F1`, `M-F2`) switches one panel
  between Work, All and your saved profiles. `C-s` there saves the
  choice for the context when the context has a profile of its own,
  else as `resources.visibility.defaultProfile`. All lists every
  discovered type, as kc did before Work.
- Work's own list is fixed in kc; a profile adds types to it or
  removes them.
- A type is never hidden from an explicit jump: `cd`, the hotlist, the
  history, links between objects and a namespace change that keeps the
  open list all open types the profile leaves out.

## See also

- [Resource visibility](resource-visibility.md)
- [Panel options](panel-options.md)
