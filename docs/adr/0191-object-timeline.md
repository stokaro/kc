# 0191. Record object changes during an investigation

- Status: Accepted
- Date: 2026-11-23
- Issue: #191

## Context

During an incident an operator wants to know what changed: a new image, a
scale-down, a condition that flipped, a pod that was replaced. kc shows the
current state of an object and keeps no history. The issue asks for a
bounded, in-memory timeline built from shared-cache notifications, with
identity, an opaque resourceVersion, observation times and normalized
content per record, change summaries, managedFields churn suppressed, diffs
through the manifest diff with spec and status apart, and the recording
start, truncation and gaps visible. Retained ReplicaSets and
ControllerRevisions should appear as separate, older evidence.

ADR 0192 declined offline captures for now and names "the revisions #191
recorded, coverage and errors" as an input of a future capture, so the
records need a stable identity and explicit coverage metadata.

Constraints checked against the code:

- The shared controller-runtime cache already runs one informer per type
  and cluster; `GetInformer` returns it and `AddEventHandler` delivers full
  objects. A second watch per object would add API load for nothing.
- Pool entries stop after `kubernetes.clusters.ttl` when nobody holds a
  lease, which would end a recording as soon as the user leaves the folder.
- The informer API does not report watch errors or relists to handlers.
  `SharedIndexInformer.SetWatchErrorHandler` can only be set before the
  informer starts, and the shared cache owns that.

## Decision

1. **`/timeline` row.** Objects in a child folder (every type kc opens with
   child types, plus pods) get a `/timeline` row after `/diagnosis`.
   Secrets and Events do not: a recording of a Secret would keep every
   revision of its data in memory, and an Event is itself a record.
2. **Recording.** Entering `/timeline` starts a recording of that object
   (keyed by cluster key, GVR, namespace and name) unless one runs. It adds
   an event handler to the shared informer of the type, without waiting for
   sync, and holds its own cluster lease so the informer keeps running when
   the user navigates away. Leaving the folder does not stop it; the
   `stop recording` row does, which removes the handlers and releases the
   lease. The entries stay readable. At most `timeline.maxObjects`
   recordings run at once; one more stops the oldest, with a gap entry
   saying why.
3. **Records** (`internal/timeline`). Each entry has a sequence number in
   observation order, a kind (`initial`, `change`, `recreated`, `deleted`,
   `gap`), the UID, the resourceVersion as an opaque string, the observation
   time, the object without `metadata.managedFields`, a summary, and which
   parts changed (spec, metadata, status). The timeline identity is
   context, GVR, namespace and name; each lifetime is its UID.
   - A notification with the resourceVersion already recorded for the UID
     is counted as a duplicate.
   - A revision equal to the last one apart from resourceVersion and
     managedFields is counted as churn and not recorded.
   - A new UID with the same name starts a new lifetime (`recreated`); it is
     never diffed against the old object.
   - resourceVersion is compared for equality only. It is never parsed,
     ordered or compared across objects.
   - Summaries name generation, images per container (pod spec, template,
     CronJob job template), `spec.replicas`, ready, available, updated and
     unavailable counts, phase, conditions with reason, restart counts and a
     requested deletion. Otherwise they name the changed parts.
4. **Diffs.** `F3` on an entry shows the manifest diff (ADR 0067's
   `manifestdiff`, config view) against the previous retained revision of
   the same UID, with a header naming both revisions, their observation
   times and resourceVersions. A status-only change opens the status diff;
   a change of both has a second `status` row. The first revision of a
   lifetime says there is nothing earlier, and a revision whose predecessor
   was evicted says so instead of diffing against nothing silently.
5. **Coverage.** The folder starts with `recording` (start, stop, or why it
   does not run), `coverage` (entries kept with their first and last
   observation, evicted entries up to when, duplicates and churn skipped)
   and `limits` (configured bounds and bytes used), and ends with a note
   that only observed revisions are known.
6. **Gaps.** A gap entry is written when the cache reports a deletion
   without the final state (`DeletedFinalStateUnknown`: changes were
   missed), when the shared informer stops (checked every 5 seconds), when
   kc's root context ends, and when the recording is evicted by
   `maxObjects`.
7. **Retained history.** For Deployments the recording also follows the
   ReplicaSets, for StatefulSets and DaemonSets the ControllerRevisions,
   whose controller owner reference names the object and whose owner UID
   is the current lifetime. They are listed as related rows with their
   revision number and creation time. They are what the controller kept,
   not what kc observed, and are shown apart from the entries.
8. **Bounds.** `timeline.maxEntries` (100) and `timeline.maxBytes` (4 MiB,
   measured as the JSON size of the stored objects) per object, oldest
   evicted first, at least one entry kept; `timeline.maxObjects` (10). With
   the defaults a session holds at most about 40 MiB of revisions.

## Deviations

- **Gaps from watch errors and relists are not detected.** The shared
  informer does not expose them to handlers, and kc cannot set a watch
  error handler on an informer the cache started. A relist after a long
  disconnect delivers the current object as an update, which the timeline
  records as one change covering whatever happened in between; the note on
  every timeline says that changes between two notifications are not
  known. A tombstone, a stopped informer and a lost session are detected.
- **No fake-cache unit test for the recorder.** The record logic is a pure
  package with a fake clock covering status changes, managedFields churn,
  duplicates, gaps, replacement and both bounds. The recorder is proven
  against a real API server in envtest (image change, status-only change,
  retained ReplicaSet, deletion, recreation with a new UID, stop). A fake
  informer would prove only the fake.
- **Observation time, not change time.** Entries carry when kc saw them.
  Server timestamps of a change do not exist in general (managedFields
  times are per manager and are removed), so none is invented.
- **The first entry is the object at recording start**, not a change.
  Earlier history is the retained ReplicaSets or ControllerRevisions.

## Alternatives considered

- **A watch per recorded object** (field selector on the name). Exact and
  able to see watch errors, but one more long-running request per object
  against the API server, which the issue and the project's cache rule both
  exclude.
- **Record every object of a namespace.** Useful for "what changed around
  here", but memory grows with the namespace and most revisions would never
  be looked at. A per-object recording started on purpose is bounded and
  explicit.
- **Store diffs instead of objects.** Smaller, but a diff against an evicted
  predecessor could no longer be shown, and the status and config views
  need the whole objects. The byte bound keeps whole objects affordable.
- **Order by resourceVersion.** It is opaque by API contract; observation
  order is what kc knows.

## Consequences

- Records fit the future capture of ADR 0192: stable identity (context,
  GVR, namespace, name, UID per lifetime), observation order and times,
  explicit coverage (start, stop, evictions, duplicates, churn, gaps with
  reasons). A capture must not serialize the stored objects: they are whole
  objects (ConfigMap data, environment literals) and need the per-kind
  allowlist first. The summaries are derived from fields that allowlist
  would likely keep (images, replicas, conditions, restarts), so a capture
  can start from them.
- Recordings stay in memory until stopped, evicted or kc exits; nothing is
  written to disk.
- The handler runs for every notification of the type on the cluster and
  filters by namespace and name, a cheap comparison next to what the
  informer already does.
