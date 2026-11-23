# 0257. Show the metric sources of a context and set its backend

- Status: Accepted
- Date: 2026-11-23
- Issue: #257

## Context

The issue asks for a setup and status flow: which data sources can answer
each dashboard question, a configure and test action for the Prometheus
Service, a bounded capability result per context and backend, states
that tell "no backend", "forbidden", "missing metric family", "no data"
and "partial" apart, optional discovery of candidate Services, and Info
and Dashboard that work for a user with namespace-only access.

What the code has on main:

- `metricsHistory.backends` (ADR 0197) names a Service per context glob.
  kc reaches it through the API server's service proxy with the
  kubeconfig credentials. A backend can only be set by editing
  `~/.kc/config.yaml`, and a wrong one shows up only as an error line in
  the Info panel of a workload.
- The history depends on fixed metric families: `kube_pod_owner` and
  friends from kube-state-metrics, `container_cpu_usage_seconds_total`
  and `container_memory_working_set_bytes` from cAdvisor. When nothing
  comes back, Info only says that owner series are missing.
- The Info engine (`gatedInfo`) refuses to start unless the user may list
  and watch nodes and pods in all namespaces. ADR 0182 kept that gate,
  because its bars are node capacity. The overview engine itself has
  namespace caches since ADR 0182, and its workload summaries need only
  the namespace.
- The dashboard (#252) does not exist yet, so there is no dashboard
  question to answer today beyond what Info and #253 to #255 will read.

## Decision

1. **A report of checks, `internal/metricsources`.** Each check names its
   source, what was checked, what kc shows from it, a state and one line
   of detail. States: `ok`, `forbidden`, `missing`, `stale`,
   `labels match nothing`, `warning`, `no backend`, `failed`.
   - Kubernetes: one discovery request for `metrics.k8s.io/v1beta1`, then
     `list` access reviews for `pods.metrics.k8s.io` in the scope,
     `nodes.metrics.k8s.io`, pods in the scope and nodes. A denied review
     names the `kubectl auth can-i` command that confirms it and what it
     disables (for nodes: cluster and node capacity).
   - Prometheus: for each family kc reads or will read (`kube_pod_owner`,
     the two cAdvisor usage families,
     `kube_pod_container_resource_requests`,
     `container_cpu_cfs_throttled_periods_total`, `node_cpu_seconds_total`)
     an instant `count(family{labels, namespace})`. Zero is followed by
     `count(last_over_time(family[1h]))` to tell a stopped exporter
     (`stale`) from one that never ran (`missing`), and, when labels are
     configured, by the same count without them to tell a wrong label
     mapping from missing data. A forbidden or failed request stops the
     report: every further query would fail the same way.
   - Without configured labels, `count(count by (cluster) (kube_pod_owner))`
     above one is a warning: the server holds several clusters, and
     same-name objects of another cluster would be summed in.
2. **The Metric sources dialog, `C-x m`** ("Metric sources" in the Command
   menu). It runs the report for the current context and the namespace
   scope of the active panel when it opens, and on `Test`. Its fields
   edit the backend: `namespace/name[:port]`, path prefix, labels as
   `name=value, ...` and an https check box. `Test` checks the fields
   without applying them, `Use` applies them for the session, `Save`
   (`C-s`) also writes them to the config file as the entry of exactly
   this context. That entry goes first, so it wins over a glob entry; an
   empty Service removes it. Field validation reuses
   `promhistory.NewServiceProxyClient`, so the dialog accepts exactly what
   a read accepts.
3. **Discovery only on request.** `Find` lists Services in all namespaces
   once (at most 2000) and fills in the first whose name looks like
   Prometheus, Thanos Query, Mimir, Cortex or VictoriaMetrics and that has
   a known query port. `Find` again fills in the next. A user who may not
   list Services gets "not permitted to list Services in all namespaces;
   enter the Service". Nothing else needs that permission.
4. **Info in a namespace scope needs only the namespace.** `gatedInfo`
   still asks for nodes and pods in all namespaces first. When they are
   denied and the scope is one namespace, it asks for pods in that
   namespace; when allowed, the engine starts with namespace caches only
   (ADR 0182). Workload bars and top pods then work. The capacity bars say
   why they are empty: "capacity needs every node and pod: nodes: not
   permitted to list and watch". In an all-namespaces scope, or when pods
   in the namespace are denied as well, nothing starts, as before. The
   cluster-wide pod metrics poll that the namespace engine also reads
   already backs off to five minutes on forbidden (`usageForbiddenInterval`).
5. **`promhistory` additions.** A refused proxy request (401 or 403)
   matches `promhistory.ErrForbidden`; its message is unchanged.
   `Client.Scalar`, `Client.Selector`, `UnlabeledSelector` and `Eq` let
   the checks build quoted selectors with the configured labels.

## Deviations from the issue

- **No standing capability cache.** The issue asks for a bounded result
  kept per context and backend. The report is a dozen small requests that
  run when the user opens the dialog. A kept result would go stale as
  exporters stop and start, and a periodic refresh would be cluster load
  nobody looks at. The history views (#253) report missing data per
  chart from their own reads.
- **"No data in this time range" and "partial coverage" are not report
  states.** They depend on a chosen range and scope, which a source check
  does not have. The report has `stale` for "series existed in the last
  hour but not now"; ranges and partial contributor coverage belong to
  the range queries of #253.
- **Application metrics are not checked.** kc has no mapping of
  application metrics yet; checking an arbitrary family would only say
  that it exists. #254 adds the families its presets need to
  `metricsources.Families`.
- **The cluster-mixing check knows one label name, `cluster`.** It is the
  common external label of Thanos, Mimir and kube-prometheus. Other names
  would be guesses; configured labels are checked directly instead.
- **The dashboard part waits for #252.** The same report will be shown
  there; nothing in this change is dashboard-specific.

## Alternatives considered

- **A `/sources` special folder.** It fits the "lists everything" style of
  kc, but a folder cannot hold the form that edits the backend, and the
  report is short enough for a dialog.
- **Probe with series listing (`/api/v1/series`) or metadata
  (`/api/v1/metadata`).** Metadata does not say whether series exist now
  or carry the labels; `count()` answers both in one small response.
- **Guess a backend automatically.** Rejected as in ADR 0197: a guess can
  point at the wrong cluster's data. `Find` only fills in the field; the
  user tests and saves.
- **Drop the Info gate entirely.** In an all-namespaces scope a
  namespace-only user would start cluster-wide informers that retry
  forbidden lists; ADR 0182's reason stands for that case.

## Consequences

- A namespace-only user sees workload usage in Info. The capacity bars of
  that panel are empty with the reason, as `/overview` already shows.
- Setting up history no longer needs a hand-edited config file.
- New key `C-x m`, registered among the built-in chords.
- Follow-ups: the dashboard shows the report (#252); presets add their
  families (#254).
