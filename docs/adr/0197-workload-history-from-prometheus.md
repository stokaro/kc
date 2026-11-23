# 0197. Workload history from Prometheus

- Status: Accepted
- Date: 2026-11-23
- Issue: #197

## Context

The Info panel mode shows the CPU and memory of the workload under the
other panel's cursor, and a sparkline of the usage samples it took itself
(metrics.k8s.io, every 15 seconds, at most 15 minutes, only while kc runs).
A spike from before kc started is invisible.

Issue #197 asks for an optional historical view from a Prometheus-compatible
backend: a provider interface, per-context endpoint, authentication
reference, cluster-label mapping and query limits, CPU and memory per
container, pod and workload, requests and limits beside the samples, a
stable time window shared with logs and events, owner-aware handling of
replaced pods, and honest gaps, coverage and errors.

What the code really has: one metrics path (`internal/models/usage_metrics.go`,
metrics.k8s.io through the API server) and no outbound connection other than
to the API server. kc has no secret store and no way to keep a credential
for a second service. The Info widget shows one workload at a time and is
not a time-series viewer.

The real risks of the feature are credentials (a token in kc's config file
or in memory, redaction in every error), endpoint discovery (guessing a
Prometheus service per cluster), a wrong pod set (pods of today's selector
instead of the pods that belonged to the workload at the time), and query
cost on a shared Prometheus.

## Decision

1. **Opt-in per context, through the API server's service proxy.**
   `metricsHistory.backends` names a Service (`namespace`, `service`,
   optional `port`, `scheme`, `pathPrefix`) for context globs, as
   `readOnly.contexts` does. kc reaches it at
   `/api/v1/namespaces/<ns>/services/[<scheme>:]<svc>[:<port>]/proxy/api/v1/...`
   with the kubeconfig credentials, TLS and impersonation of the context.
   There is no separate endpoint, token or authentication reference, and
   no discovery: nothing is shown until a backend is configured. Access is
   `get` on `services/proxy`, which RBAC already controls; a refusal shows
   as "unavailable: ... (needs get on services/proxy)".
2. **Cluster labels are configured, never derived.** `labels` (a map of
   exact label matches) is added to every vector selector, the owner joins
   included, for a server that holds several clusters. Names are checked
   against the Prometheus label syntax and values are quoted, so a value
   cannot break out of the selector. kc never maps a context name to a
   cluster label.
3. **Pods are tied to the workload by kube-state-metrics owner series at
   each point in time.** `kube_pod_owner` gives pod to owner;
   `kube_replicaset_owner` (Deployment) and `kube_job_owner` (CronJob) give
   the middle level. The join is evaluated per step, so a pod of an old
   ReplicaSet counts while it existed and a pod of another workload never
   counts. CPU is `rate(container_cpu_usage_seconds_total[5m])` and memory
   `container_memory_working_set_bytes`, summed over the pods, with
   `container!=""` and `container!="POD"`. The queries were checked against
   Prometheus 3.5 with backfilled series: a rollout from one ReplicaSet to
   another, a pod of another Deployment and the same names in a second
   cluster.
4. **No guessing when owner series are missing.** When CPU and memory
   return nothing, kc asks whether the namespace has any `kube_pod_owner`
   series. Without them the block says that pods cannot be tied to the
   workload; it does not fall back to pod name prefixes or to today's pod
   list.
5. **Shown in the Info panel, under the workload's bars.** "History, last
   1h (Prometheus monitoring/prometheus-k8s:9090)", a CPU and a memory
   sparkline scaled from zero to the peak with the peak and end values, the
   time span and step, the number of pods the owner series tie to the
   workload in the window, the steps without samples and the age of the last
   sample. A step without a sample is a blank cell; a missing series says
   "no series"; neither is drawn as zero. Backend warnings are shown.
6. **Bounded.** One read per workload: two range queries and one instant
   query at a time, a fourth only when nothing came back. The window
   (`metricsHistory.window`, default 1h, 5m to 168h) is cut into 60 steps
   of at least 15 seconds, aligned to the step. A read is repeated at most
   once a minute (or once a step), two minutes after a failure, not while
   a dialog is open, and only while the panel is in Info mode on that
   workload. `metricsHistory.timeout` (default 15s) bounds a read; a body
   over 4 MiB or more than 8 series is refused. A read that a cursor move
   made obsolete is canceled, and its result is dropped by sequence number.
7. **Failures stay inside the block.** A failed read keeps the last good
   history and adds the error line; the bars from metrics.k8s.io and
   browsing are untouched. The history is also shown when the overview is
   refused (a namespace-only user may still read Prometheus). Errors drop
   the request URL and never carry a credential.

## Deviations from the issue

- **No provider interface.** There is one implementation; an interface with
  one caller only adds indirection. `internal/promhistory` is the seam if a
  second backend appears.
- **No authentication reference or direct URL.** The service proxy covers
  in-cluster Prometheus, Thanos Query, Mimir and VictoriaMetrics behind a
  Service without kc holding any credential. A Prometheus outside the
  cluster would need a token kc stores and redacts; that is a follow-up if
  users ask for it, not part of this change.
- **No per-container or per-pod history, no requests and limits history.**
  The Info panel shows workloads; per-pod series would need a list view, and
  requests and limits history needs `kube_pod_container_resource_*` joins
  for a line the bars above already give for now.
- **No shared time window with logs and events.** The logs viewer has its
  own `--since` and the events have no time cursor; sharing one window is a
  larger UX change (see #187 for pinning targets).
- **No time range picker.** The window is a config value. A key to cycle
  windows can come later without changing the data path.

## Alternatives considered

- **Decline the issue.** Grafana already answers "what happened before"; a
  sparkline is a coarse copy. Not chosen because during an incident the
  question "was this workload already high an hour ago" comes up on the
  same screen as the workload, and the service proxy made a version without
  credentials and without new watches possible.
- **Pod name prefixes instead of owner series.** Works without
  kube-state-metrics but merges `web` with `web-api`, and cannot tell a
  StatefulSet pod `db-0` from a pod of another workload with that name.
  Rejected: a wrong history is worse than none.
- **A direct URL with a token from a file.** Simple to write, but makes kc
  responsible for a credential, and the token would be easy to leak in an
  error. Rejected for now.
- **The Prometheus HTTP client library.** Adds a dependency for two
  endpoints whose JSON is small and stable.

## Consequences

- The feature does nothing until a backend is configured; the default
  config has `backends: []`.
- It depends on kube-state-metrics and cAdvisor metric names as the
  kube-prometheus stack ships them. Other names need a follow-up (per
  backend metric names), not a guess.
- Queries go through the API server, so they count against its rate limits
  and appear in its audit log as `get services/proxy`.
- Follow-ups: a key to change the window; per-pod series in a list; history
  of requests and limits; a direct endpoint with a stored token, if asked
  for.
