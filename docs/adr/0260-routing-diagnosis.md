# 0260. Explain Ingress and HTTPRoute routing paths

- Status: Accepted
- Date: 2026-11-23
- Issue: #260

## Context

The Service diagnosis ([0194](0194-service-diagnosis.md)) checks
selectors, pods, EndpointSlices and target ports, and defers routes. `M-b`
on a Service lists the Ingresses that name it (`related_refs.go`), but
nothing explains how a host and path reach a Service, or why a route is
not attached.

The issue asks for an explicit routing view from an Ingress or HTTPRoute
to its parent listeners and backend Services: host and path matches,
listener attachment, ports and weights, status conditions with reasons,
times and generations, cross-namespace ReferenceGrants and TLS
references, with forbidden, unavailable and unsupported sources reported
as such. Backend rows should enter the existing Service diagnosis.

## Decision

1. **A `/diagnosis` row inside every Ingress (`networking.k8s.io/v1`) and
   every HTTPRoute (`gateway.networking.k8s.io`, any served version).** It
   is the same `RelatedObjectsFolder` in diagnosis mode as for Services
   and workloads, with the same summary, finding and note rows. No new
   key, renderer or cache.
2. **Ingress checks.** The class (`spec.ingressClassName` against the
   cluster's IngressClasses, the deprecated annotation, no class with zero
   or several defaults), every rule path and the default backend (Service
   exists, port number or name is exposed), resource backends as
   unsupported, every TLS entry (Secret exists, TLS host without a rule),
   and the published address.
3. **HTTPRoute checks.** Per parentRef: the Gateway exists, the
   `sectionName`/`port` selects a listener, and some selected listener
   accepts the route (protocol HTTP or HTTPS or explicit `allowedRoutes.kinds`,
   `allowedRoutes.namespaces` `Same`/`All`/`Selector` with the route
   namespace's labels, hostname intersection with suffix matching for
   `*.`), the Gateway's `Accepted` and `Programmed` conditions, and the
   route status the parent's controller wrote (`Accepted`, `ResolvedRefs`).
   A False condition is an error with its reason and message, an Unknown
   or stale one (`observedGeneration` below `metadata.generation`) a
   warning, a missing parent status a warning. Per backendRef: kind
   Service with a port, the Service and port exist, weight, and a
   ReferenceGrant in the target namespace for another namespace.
   Listener certificate references of the selected listeners are checked
   for existence and for a ReferenceGrant when cross-namespace. Extension
   and unknown filters, non-Service backends and non-Gateway parents are
   `info` rows that say they are not checked.
4. **Reads.** Everything goes through the folder's scoped reader (ADR
   0056): shared cache, namespace cache or a Forbidden answer per type and
   namespace. Gateway and ReferenceGrant versions come from the REST
   mapper; a type the cluster does not serve gives `incomplete ... not
   served by the cluster`. A forbidden or failed read is `incomplete` for
   the checks that need it and never reported as a missing object.
5. **TLS Secrets** are checked with a direct metadata-only GET
   (`k8s.io/client-go/metadata`), logged at INFO. Their data never reaches
   kc, and no Secret informer is started.
6. **Watches.** The folder refreshes on the route, the Services of its
   namespace and, for an HTTPRoute, the Gateways of its namespace (in the
   route's API version). Parents, Services and grants in other namespaces
   are read on each refresh but not watched.
7. **Backend Services** are rows whose Enter opens that Service's
   diagnosis directly (one level, not the Service's folder); `F3` shows
   the YAML. Gateways are cited as backend links and open their folder.

## Deviations from the issue

- **No standalone Gateway diagnosis.** A Gateway's routes can live in any
  namespace, so listing them needs a cluster-wide HTTPRoute read; its
  `status.listeners[].attachedRoutes` already counts them. The Gateway's
  conditions and listeners are shown from each route instead.
- **TLS references are existence checks only**, as the issue says; kc does
  not parse certificates, so an expired or mismatched certificate is not
  reported.
- **No GRPCRoute, TLSRoute or TCPRoute.** The issue scopes HTTPRoutes; the
  same parent checks would apply, and they are a follow-up.
- **The Gateway API is decoded into small local structs**, not
  `sigs.k8s.io/gateway-api`. The fields read are stable in v1 and v1beta1,
  and the module would add a large dependency for a dozen fields.
- **Hostname wildcards use suffix matching** (`*.example.com` matches
  `a.b.example.com`), as HTTPRoute hostnames are defined. This errs toward
  "accepted"; the controller's own status remains the authority and is
  shown next to it.

## Alternatives considered

- **Extend `M-b` on a Service with route rows.** It answers "who routes
  here", not "why does this route fail", and would start Gateway API
  informers for every used-by lookup.
- **A routing graph view.** More to build and to read in a terminal; the
  rows of the diagnosis already carry evidence and navigation.
- **Read TLS Secrets from the cache.** That starts an informer holding
  every Secret of the namespace, with data, to answer one existence
  question.

## Consequences

- Opening a route diagnosis starts the Services informer of the namespace
  and, for an HTTPRoute, a Gateways informer (cluster-wide for a user who
  may list Gateways everywhere, else per namespace), plus IngressClass or
  ReferenceGrant and Namespace informers when a check needs them. These
  are the shared informers their lists use.
- One metadata GET per TLS Secret reference per refresh.
- Tests: table tests on fake lookups cover cross-namespace references with
  and without grants, multiple parents and backends, denied and unserved
  sources, unsupported filters, backends and parents, listener
  attachment, condition reasons, times and generations, and hostname
  intersection. An envtest test reads a real Ingress, checks the TLS
  Secret by metadata, follows a new Service, and enters the Service
  diagnosis from a backend row. HTTPRoutes are covered by the fake tests
  only, since envtest has no Gateway API CRDs.
- Follow-ups: GRPCRoute and other route kinds; a Gateway diagnosis that
  lists attached routes for users who may list routes cluster-wide.
