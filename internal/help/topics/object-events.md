# Object events

`/events` is a row inside pods and other namespaced objects (not
Secrets and not Events). It lists the Events about this object, newest
first, so you can follow a scheduling failure, a rollout or a readiness
problem without filtering the namespace's whole Event list.

## How it works

- kc reads the object's UID and keeps the core/v1 Events in its
  namespace whose `involvedObject.uid` is that UID. Events of an earlier
  object with the same name, and Events without a subject UID, are not
  listed.
- The Events come from the shared cache of the namespace's Events, the
  one the namespace's `events` list uses; opening `/events` starts no
  other watch. An Event that changes (a higher count, a new last time)
  updates its row in place: one row per Event object.
- The list starts sorted by Last Seen, newest first. `M-o`, `M-r` and
  the panel options change the order as in any list.

## How to read it

The columns are those of `kubectl get events`: Last Seen, Type, Reason,
Object and Message. Last Seen is computed from the Event's own times.
One row is one Event object. Its `count` (in the YAML) covers the whole
series; kc does not split it into occurrences or invent their times.

`F3` on an Event shows it whole, with the full message and the absolute
times. The command line shows
`kubectl get events -n <ns> --field-selector involvedObject.name=<name>`
and notes that kc also matches the UID, which kubectl's command does
not.

## Limits

- Cluster-scoped objects (nodes, for example) have no `/events`: their
  Events live in another namespace, which needs a read across
  namespaces.
- Events expire (the API server keeps them for an hour by default). An
  empty list can mean that nothing happened or that the Events expired.
- If the object is gone, the list shows the error of reading it.
- The Events of the events.k8s.io/v1 API are the same objects; kc lists
  the core/v1 form only, so nothing is counted twice.

## See also

- [Diagnosis](diagnosis.md)
- [Object timeline](timeline.md)
- [Panels](panels.md)
