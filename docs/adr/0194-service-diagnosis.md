# 0194. Service diagnosis

- Status: Accepted
- Date: 2026-11-23
- Issue: #194

## Context

Issue #194 asks for a consistency check of the path Service -> selected
pods -> EndpointSlices -> readiness and target ports, with links to the
supporting objects, explicit handling of ExternalName, selectorless,
headless, dual-stack and `publishNotReadyAddresses` Services, incomplete
results instead of zero-backend claims, and a clear statement that the
check does not prove connectivity.

What the code had, checked on main: a Service's child folder lists
`/endpointslices` (filtered by the standard `kubernetes.io/service-name`
label); `M-b` on a Pod lists the Services selecting it and on a Service the
Ingresses routing to it (ADR 0039); the related folders read through the
per-type scope decision of ADR 0056 (ADR 0193). The overview finding model
the issue points to (`overview_rules.go`) computes namespace-wide counts for
the overview lane; it is not a per-object model.

## Decision

### 1. `/diagnosis` inside a Service, no new key

A Service's child folder lists `/diagnosis` after `/endpointslices`.
Entering it opens `services/<name>/diagnosis`. It is a row, not a key,
because it applies to one type only, and the user who opened the Service
is already looking for its backends. The folder is a `RelatedObjectsFolder`
of kind `RelatedDiagnosis`, so it shares the scoped reads, watches and
object rows of the owners, used-by and requires folders.

Enter runs on the UI thread, so the folder takes nothing when it is built:
the unscoped cluster and the scope decisions (access reviews) are taken on
first use, in the background populate and in a goroutine started by the
watch. `startWatch` of every related folder now decides pairs it finds
undecided in the background and refreshes once they are watched.

### 2. Rows

Columns: Name, Resource, Namespace, Evidence.

1. Summary rows (ID `summary/<label>`): type and IP families with the
   policy, endpoints (`N in M slices, R ready, S serving, T terminating`,
   address types), selector, pods (`N selected, R ready`), ports
   (`http 80 -> http/TCP`).
2. Findings in the order `incomplete`, `error`, `warning`, `info`,
   each followed by the objects it cites as object rows (ID
   `finding/<code>/<object>`), so Enter and F3 work on them.
3. Every backend: the selected pods with readiness and their resolved
   ports, and the EndpointSlices (ID `backend/<object>`).
4. A dim note: configuration only; DNS, CNI, NetworkPolicy, firewalls and the
   application are not tested.

### 3. Rules, as the EndpointSlice controller builds slices

- Selection: pods of the namespace that match the selector and are not
  Succeeded or Failed.
- No selected pod: error. The finding lists up to ten pods that share a
  selector key, with their values for those keys, so a typo shows; if no
  pod has any of the keys, the finding says so.
- Unready selected pods: warning with the Ready condition reason and each
  unready container (waiting reason, terminated reason, or "running,
  readiness failing", restarts). All unready: error. With
  `publishNotReadyAddresses`: info, because they get traffic.
- Named targetPort: resolved on each pod on its own, by name and protocol in
  the pod's container ports (TCP when unset). Pods where it does not resolve
  are listed; error when none resolves. Numeric and unset targetPorts need no
  lookup.
- A selected pod with an IP that no slice lists: warning. An endpoint whose
  targetRef pod does not exist (dim `not found` row), has another UID
  (replaced) or is no longer selected: warning. Endpoints without a
  targetRef are counted only.
- Each IP family of the Service needs a slice of that address type once a
  selected pod has an address of that family: warning otherwise (skipped
  when there is no slice at all; the missing-endpoint finding says it).
- Endpoint conditions as the API defines them: nil ready is ready, nil
  serving follows ready, nil terminating is not terminating.
- ExternalName: one info finding; nothing else is read. Headless: info, and
  the endpoint checks still run. Selectorless: info; a warning only when no
  slice carries the Service's name label.
- Pods or EndpointSlices that cannot be read (forbidden, informer error):
  an `incomplete` finding, and the checks that need the data are left out.
  The summary says `unknown`, never zero.

## Deviations from the issue

- **No Ingress backend row.** `M-b` on the Service already lists the
  Ingresses that route to it; repeating it here would start an Ingress
  informer for every diagnosis.
- **Not the overview finding model.** `overviewFinding` is built for
  namespace-wide counts and is owned by the overview work; the diagnosis has
  its own small finding type with severity, title, detail and links.
  Merging them is a follow-up once both settle.
- **No "the selector and candidate labels open" view.** The selector is in
  the summary and the finding title, and each candidate pod row shows its
  values for the selector keys; Enter or F3 on it opens the pod.
- **Missing endpoints are a warning, not an error.** Without the controller's
  internal state kc cannot tell a slow controller from a broken one.

## Alternatives considered

- **A new `M-<letter>` key.** One more key
  for one type; the row is discoverable where the user already is.
- **Rows inside `/endpointslices`.** That folder is a plain filtered list
  used for edit and delete; mixing findings into it changes its meaning.
- **Reading through the EndpointSlice controller's logic (vendoring it).**
  Heavy, and the checks above cover the cases the issue names.

## Consequences

- Opening a diagnosis starts the pods and EndpointSlices informers of the
  context (or of the namespace for a namespace-only user) if no list of them
  was open, the same informers their lists use.
- Follow-ups: Ingress and Gateway routes as an explicit section; merge the
  finding type with the overview's; readiness gates and container probes in
  the pod evidence.
