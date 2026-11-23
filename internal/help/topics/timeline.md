# Timeline

`/timeline` is a row inside pods and every object that has a child
folder, except Secrets and Events. Entering it starts recording that
object: every revision kc sees from now on, with what changed. Use it
during a rollout or an incident to see the order of changes and diff
any two neighboring revisions.

## How it works

The recording reads the shared cache kc already keeps; it adds no
watch. It keeps running when you leave the folder, until you stop it,
until kc exits, or until more than `timeline.maxObjects` (10) objects
are recorded, when the oldest one stops. Recordings live in memory only.

## How to read it

The first three rows describe the recording:

- `recording`: when it started, and whether it runs.
- `coverage`: the entries kept, how many older ones were evicted, and
  how many notifications were skipped.
- `limits`: `timeline.maxEntries` (100) and `timeline.maxBytes` (4 MiB)
  per object, and the memory used.

Then the entries, newest first. Columns: the observation time, the
kind, the resourceVersion and a summary of the change. Kinds:

- `initial`: the object as kc first saw it.
- `spec`, `metadata`, `status` or a mix such as `spec+status`: a change.
- `deleted` and `recreated`: a deletion, and a new object with a new
  UID under the same name. The new one is never diffed with the old.
- `gap`: changes may be missing here (the cache lost the final state of
  a deletion, or the watch stopped).

The summary names images, replicas, ready and available counts, phase,
conditions, restarts, generation and a requested deletion.

For Deployments, StatefulSets and DaemonSets the folder also lists the
ReplicaSets or ControllerRevisions their controller kept. They show
older templates from before the recording.

## Keys

- `F3` on an entry opens the diff against the previous recorded
  revision in the diff viewer. A status-only change shows the status
  diff; a change of both has an extra `status` row below it.
- `Enter` on `stop recording` stops the recording. The entries stay
  readable until `/timeline` is opened again.
- `Enter` on a kept ReplicaSet or ControllerRevision opens it.
- `M-c` copies `kubectl get ... --watch --output-watch-events`.

## Limits

- Only observed revisions are known. Changes between two notifications,
  or before the recording started, are not.
- Notifications that repeat a resourceVersion or change only
  `managedFields` are counted, not recorded.

## See also

- [Diff viewer](diff-viewer.md)
- [Diagnosis](diagnosis.md)
- [Compare objects](compare-objects.md)
