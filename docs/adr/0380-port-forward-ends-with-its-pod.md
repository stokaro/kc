# 0380. A port forward ends with its pod

- Status: Accepted
- Date: 2026-11-28
- Issue: #380 (port forwards of deleted pods; the lists, the frame of a
  bad connection and the message log are separate changes)

## Context

A forward carries its traffic over one connection to the pod it started
with; for a service, that is the pod chosen at start. When the pod was
deleted, the forward stayed in the list of forwards and its local port
kept accepting connections. Only when something connected did client-go
notice, and kc said `Port forward … ended: lost connection to pod. Try
again.`, which does not say that the pod is gone. A pod created again
under the same name, as a StatefulSet does, is another pod, and the old
connection does not reach it either.

## Decision

1. **Each client forward watches its pod.** `ClientRunner` lists the pod
   by name (`fieldSelector=metadata.name=<pod>`) and watches from that
   list's resource version. When the pod is deleted, or a pod with
   another uid has the name, the forward ends with
   `portforward.PodGoneError`: `pod web-0 was deleted`. The message reads
   `Port forward 127.0.0.1:8080 -> pod/web-0:80 ended: pod web-0 was
   deleted. Try again.`; for a service it names the pod that carried the
   traffic.
2. **The uid comes from the start.** `Spec.PodUID` is the uid of the pod
   kc resolved for the forward. An empty uid takes the uid of the pod
   the first list returns.
3. **The watch is the forward's own request, not the shared cache.** A
   forward outlives the panel and the context that started it and holds
   no cluster lease, so it cannot rely on the pool's informers. One
   field-selected watch per forward costs one stream to the API server.
   The list and the watch are logged before they are sent.
4. **A pod the user may not read keeps the old behavior.** A list or
   watch refused with 403 or 401 ends the check, logged once, and the
   forward goes on as before. Other failures, and a watch the server
   closed, are tried again: after 5 s and 1 s.
5. **A terminating pod still forwards.** The forward ends when the pod is
   gone, not when its deletion starts, so open connections keep working
   during the grace period.

## Alternatives considered

- **Watch through the shared informer of pods.** It needs a lease on the
  cluster for the forward's whole life and an informer for every pod of
  the namespace.
- **End on `deletionTimestamp`.** It cuts connections the pod still
  serves while it shuts down.
- **Follow a service to another pod.** kubectl does not, and it would
  change where the user's traffic goes without asking.

## Consequences

- Every forward holds one more watch on the API server while it runs.
- A forward of a user without list and watch on pods ends, as before,
  only when a connection fails.
- envtest has no kubelet, so the tests run the watch and the registry
  with a forward that only holds its port; the client-go forward itself
  is covered by the existing tests of a refused start.
