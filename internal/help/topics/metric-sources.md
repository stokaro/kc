# Metric sources

The Metric sources dialog tells you which data kc can read in the
current context, and lets you set the Prometheus-compatible server that
the history views read. Open it with `C-x m` or "Metric sources" in the
Command menu (`F9`).

kc reads two kinds of usage data:

- **Current usage** comes from the Kubernetes metrics API
  (`metrics.k8s.io`, served by metrics-server). Info and the CPU and
  memory columns use it.
- **History** comes from a Prometheus-compatible server in the cluster
  (Prometheus, Thanos Query, Mimir, VictoriaMetrics). kc asks it through
  the API server's service proxy with your own kubeconfig credentials.
  It needs the series that kube-state-metrics and the kubelet's cAdvisor
  export.

## The report

When the dialog opens, kc checks each source once for the namespace of
the active panel and lists one line per check: the state, what was
checked and what it means. A check that is not `ok` is red. States:

- `ok`: the source answers.
- `forbidden`: your user may not read it. The line names the
  `kubectl auth can-i` command to confirm. A namespace-only user sees
  `nodes` forbidden: node capacity is not available, but the workloads
  and pods of the namespace still are.
- `missing`: the metrics API is not served, or the server has no series
  of that family. The line names the exporter that provides it, for
  example kube-state-metrics.
- `stale`: the family had series in the last hour but has none now. Its
  exporter stopped, or the server no longer scrapes it.
- `labels match nothing`: the series exist, but none carry the labels
  you set. Fix the labels.
- `warning`: the server holds several clusters (several values of the
  `cluster` label) and no labels are set. Without labels, objects with
  the same name in another cluster would be summed in.
- `no backend`: no backend is set for this context.
- `failed`: the request failed, for example because the Service has no
  endpoints. When the server itself cannot be reached or is refused, the
  remaining checks are skipped.

The dashboard's request, limit and allocatable lines need
kube-state-metrics 2 or later: `kube_pod_container_resource_requests`,
`kube_pod_container_resource_limits` (with a `resource` label),
`kube_pod_container_info`, `kube_pod_status_phase` and
`kube_node_status_allocatable`.

A missing family disables only the views that need it. Without
kube-state-metrics kc cannot tie pods to workloads in the past, but
current usage from the metrics API still works.

## Setting the backend

- **Prometheus Service**: `namespace/name` or `namespace/name:port`, for
  example `monitoring/prometheus-k8s:9090`.
- **Path prefix**: the server's route prefix, for example `/prometheus`
  for Mimir. Empty for a plain Prometheus.
- **Labels**: `name=value` pairs added to every series kc asks for, for
  a server that holds several clusters: `cluster=prod-eu`. kc never
  derives them from the context name.
- **https**: the proxy reaches the Service over https.

Buttons:

- `Test` (`Enter`) checks the fields as they are, without applying them.
- `Find` lists the Services of all namespaces once and fills in the
  first one that looks like a metrics server. Press it again for the
  next one. If you may not list Services, enter the Service yourself;
  nothing else needs that permission.
- `Use` applies the backend to this session.
- `Save` (`C-s`) applies it and writes it to `~/.kc/config.yaml` as the
  backend of exactly this context. An empty Service field removes that
  entry.
- `Close` (`Esc`) leaves everything as it was.

## Limits

- kc checks only when you open the dialog or press Test. The checks
  are a few access reviews and up to 20 small count queries.
- kc never installs monitoring components.
- The report says whether series exist now. It does not prove that a
  past time range has data; the history views say so per chart.

## See also

- [Info mode](mode-info.md)
- [Investigation window](investigation-window.md)
- [Overview](overview.md)
