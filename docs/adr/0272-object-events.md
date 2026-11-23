# 0272. List an object's Events in an /events row

- Status: Accepted
- Date: 2026-11-23
- Issue: #272

## Context

To follow the Events of one object, a user opens the namespace's `events` list and filters it by name, which also matches the Events of an earlier object of the same name and of other kinds with that name. The overview shows Warning Events only, and the diagnosis shows a few Events as evidence. The issue asks for a full-screen Events workspace: scopes up to all namespaces, type/namespace/object/reason/reporter filters, message search, per-UID rows, details, a bounded session-updates mode with explicit gaps, a transition to logs at an Event's time, pause and resume, and the shared time model of #255.

The claims hold on current main. Since #278, the Event list's Last Seen and First Seen are computed from the Events' own times and sort by time, and the list loads the full Event objects from the shared cache.

## Decision

1. **An `/events` row inside namespaced objects** (next to `/timeline`, and inside pods), except Secrets (which have no object rows today) and Events. It opens an `ObjectEventsFolder`: the namespace's core/v1 Event list with an object filter.
2. **Match by subject UID.** Each load reads the object (as the owner-based child lists do) and keeps the Events whose `involvedObject.uid` is its UID. Events of a replaced object with the same name and Events without a subject UID are not listed. A gone object shows the read error.
3. **Shared cache only.** The folder is an `ObjectsFolder` for `v1/events` in the object's namespace, resolved with the same list scope as the namespace's `events` list (a namespace cache for a user who may not list Events across namespaces). No other watch, no polling. The filter needs the full objects; when they cannot be read the folder reports an error instead of an empty list. `ObjectsFolder.FilterObject` is the new hook for this.
4. **One row per Event object**, updated in place by the informer. `count` stays the Event's own series count; no occurrence is invented.
5. **Newest first.** The folder starts ordered by Last Seen ascending by age, newest first; `M-o`, `M-r` and the panel options change it as in any list.
6. **One API.** core/v1 only. events.k8s.io/v1 serves the same objects, so listing one never counts an Event twice.
7. The command line shows `kubectl get events -n <ns> --field-selector involvedObject.name=<name>` and the note "kc also matches its UID": the UID is known only after the first load, and the command line is built on the UI goroutine.
8. Help page `object-events` (F1 in the folder), README sentence.

## Deviations from the issue

The issue describes a separate investigation application. kc already has the pieces in its list model: the server's Event table, row filters (`M-f`, which covers type, reason and message text), sorting, `F3` for the whole Event with absolute times and the full message, and full-width panels. What was missing is the subject scope with UID identity and a useful default order. The rest is sized down or deferred:

- **No separate full-screen view, scopes or filter bar.** The namespace and all-namespaces Event lists exist; `/events` adds the object scope. `M-f` filters by column text; a panel goes to full width with `M-w`.
- **No session-updates mode, pause/resume or unseen indicator.** A bounded log of observed Event changes with explicit relist gaps is a second data model (like the timeline recorder, ADR 0191's area) and would need its own retention; the list already updates in place and `C-x f` freezes a panel's display (ADR 0187).
- **No logs transition at an Event's time.** That depends on the investigation window contract (#255, dashboard lane). The pod's `logs` rows are next to `/events` in the pod folder.
- **No entry from the overview Events finding.** The finding lists Warning Events across subjects; its rows already open the subject.
- **Cluster-scoped objects get no `/events`.** Their Events live in another namespace (usually `default`), so the folder would need a read across namespaces, which this issue's "shared caches only" rule and RBAC make a separate decision.
- **Not "all namespaces when permitted"** for one object: an object's Events are in its namespace.

## Consequences

- Every namespaced object folder has one more row.
- Opening `/events` reads the object once per load (the same read the owner-based child lists do) and uses the namespace's Event cache.
- Follow-ups: cluster-scoped subjects (Node Events); a recorded session-updates mode if users need change history beyond the Event's own count; a logs transition once #255 lands.
