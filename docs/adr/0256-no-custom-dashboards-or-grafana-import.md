# 0256. No custom dashboards or Grafana import for now

- Status: Declined
- Date: 2026-11-23
- Issue: #256

## Context

#256 asks for user-defined dashboards: a versioned schema for layout,
queries, units, thresholds and variables bound to Kubernetes identity,
and a selective import of Grafana Classic JSON with a report of what
could not be carried over.

The full-screen dashboard (#252, ADR 0252) shows built-in CPU and
memory views for a scope, node, workload or pod. Its queries come from
`promhistory` (#253), which builds them from typed scopes with escaped
label values, owner joins through kube-state-metrics, coverage series
and object lifetime clipping. Each chart shows one series and its
reference lines. Series selection, events and the shared time window
come with #254 and #255.

## Decision

Do not build custom dashboards or Grafana import now. Close the issue;
open a new one when the conditions below hold.

## Why

- **The renderer is not ready for it.** Custom panels need several
  series per chart, a legend with selection, stat and table widgets and
  annotations. None of these exist yet, and #254 and #255 will shape
  them. A schema written now would freeze a chart model that is about to
  change, and stored dashboards are a contract users keep.
- **Arbitrary PromQL loses what the built-in views guarantee.** The
  built-in queries know which pods were expected and which were
  measured, and clip at the object's creation. A user-written query has
  no coverage and no identity, so its chart cannot say "partial" or
  "created 09:41". It would look as trustworthy as the built-in ones
  while promising less. The issue asks not to show misleading charts,
  and this is the most common way to show one.
- **Grafana import is mostly diagnostics.** The dashboards people would
  import, such as kubernetes-mixin's workload views, depend on
  `joinByField`, `organize`, field overrides, library panels and data
  links. A faithful subset imports few real dashboards; a loose one
  imports wrong tables. Either way the work is mostly the report of what
  was skipped, and no user request beyond this issue names a dashboard
  to import.
- **Variable substitution is a security surface.** Escaping context,
  namespace and workload values into regex and literal matchers, and
  validating expressions before sending them through the API server
  proxy with the user's credentials, needs its own review. It should be
  designed once there is a schema worth that review.
- **Grafana exists.** A user who needs application dashboards almost
  always has Grafana next to Prometheus. kc's value is the Kubernetes
  identity the built-in views add, not a second dashboard engine.

## When to revisit

Open a new issue when all of these hold:

1. #254 and #255 have merged, so the chart model has several series,
   selection, events and one time window.
2. Users ask for application metrics that the built-in views cannot
   show, with concrete queries.
3. The first step is a native schema of a few panel types with
   server-side validation of each expression before it is sent. Grafana
   import would come after it, if at all, and would refuse what it does
   not understand.

## Consequences

- The dashboard has built-in views only. Its queries stay in
  `promhistory`, where coverage and identity are guaranteed.
- Nothing in `pkg/appconfig` gains a dashboard schema, so there is no
  stored format to keep compatible.
- Users with custom application metrics keep using Grafana or the
  Prometheus UI.
