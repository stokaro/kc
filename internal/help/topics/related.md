# Owners, Used By, Requires

Three keys show how the selected object is linked to others. Each opens
a folder in the same panel, under the object, so the breadcrumb shows
the path you took (`pods/web/requires/cfg/used-by`). Use them to answer
"who runs this pod", "what breaks if I change this ConfigMap" and "what
does this Deployment need to start".

## How it works

- `M-j` opens `<object>/owners`: the owner references of the object,
  the controller first, with the cursor on it.
- `M-b` opens `<object>/used-by`: the objects that use it, read from
  the watch cache. Pods that use a ConfigMap, Secret, claim or
  ServiceAccount, with the controllers that run them (ReplicaSet,
  Deployment, Job and so on, once each), and the Deployments,
  StatefulSets, DaemonSets, Jobs and CronJobs whose pod template names
  it, also when they run no pod. Also the Services that select
  a Pod, the Ingresses that route to a Service, and the claim bound to
  a PersistentVolume.
- `M-d` opens `<object>/requires`: what the object names in its spec.
  For a Pod its ConfigMaps, Secrets, claims, ServiceAccount and Node;
  for a workload or CronJob the same from its pod template; for a
  claim its PersistentVolume.

## How to read it

The Relation column names the field that links the two objects, for
example `volumes[cfg]`, `containers[app].env[A,B]`, `spec.nodeName` or
`controller of pods/web-1-abc`. A relation that starts with
`pod template:` comes from the workload's spec, not from a running pod;
a workload that has both says both on one row. In a narrow panel the other columns are
cut first; the bottom status line shows the object and the relation in
full.

A dim row cannot be opened; the reason says why:

- `not found`, or `not found (an object with another UID has the
  name)` for an owner that was deleted and created again,
- `unknown kind` when the cluster does not serve the type,
- `forbidden` when you may not read that type there.

A `(not listed)` row in used-by means kc may not list a type it needs,
so the result is incomplete. It is not an empty answer.

The folders follow changes: a target that appears or goes away shows
at once.

## Keys

- `M-j` (or `Esc j`) opens owners.
- `M-b` (or `Esc b`) opens used by.
- `M-d` (or `Esc d`) opens requires. The File menu calls it Requires.
- `Enter` on a row opens its child folder, as in its own list.
- `F3` on a row shows its YAML.
- `M-j`, `M-b` and `M-d` work on the rows too, one step at a time.
- `/..` returns to the object.
- `C-u` swaps the panels if you want the result on the other side.

## Limits

- Rows are read-only here; edit or delete an object from its own list.
- Pod templates are scanned for Deployments, StatefulSets, DaemonSets,
  Jobs and CronJobs. A bare ReplicaSet or a custom resource with its
  own template is found only through its pods.
- Only built-in relations are known; no links are guessed from CRDs.
- A pod names a ConfigMap by name only, so a replaced ConfigMap with
  the same name is not detected.
- kc asks the API server before it reads a type, and reads only where
  you have access. A type you may not read starts no watch.

## See also

- [Diagnosis](diagnosis.md)
- [Timeline](timeline.md)
- [Access](access.md)
- [Workload actions](workload-actions.md)
