# Configuration

`~/.kc/config.yaml` stores all user preferences. In the option dialogs, `Enter` applies a change to the current session and `C-s` saves it as the default. Key sections:

- `layout.menuBarVisible` – keep the menu bar titles on the top row (default `true`, as in mc); see [Menu Bar](features.md#menu-bar).
- `layout.leftPanelMode`, `layout.rightPanelMode` – the mode each panel starts in: `list`, `describe`, `manifest`, `logs` or `info` (default left `list`, right `info`). One panel must start in `list`. kc does not save panel modes between runs, so these apply on every start; start targets such as `kc overview` or `kc get pods` open in the left panel, and `kc get TYPE NAME -o yaml` still puts the right panel in Manifest mode.
- `viewer.theme`, `viewer.mode` – chroma theme and wrap mode for YAML/text/log viewers.
- `viewer.logs.tail`, `viewer.logs.since`, `viewer.logs.timestamps` – the options a logs viewer and a Logs panel open with. `F2` in the viewer or the panel changes them there; `C-s` saves them as the defaults.
- `viewer.logs.maxRequests` – how many log streams a merged log (all pods of a workload, all containers of a pod, in a viewer or a Logs panel) runs at once (default 50, like stern); further streams wait for a free slot. `M-s` in a merged viewer changes it for that view.
- `viewer.logs.correlationFields` – the JSON fields `M-d` starts on and follows a request by, first one found (default `trace_id`, `traceId`, `traceID`, `request_id`, `requestId`, `requestID`); a name is a top-level key or a dotted path to a nested one.
- `viewer.logs.traceURL` – URL template for `M-d` `t`, for example `https://tempo.example/trace/{trace}`; empty (the default) means no trace link.
- `viewer.logs.maxLines`, `viewer.logs.maxMemoryMiB` – how many lines, and about how much memory, one logs viewer or Logs panel keeps (defaults 100000 and 64); older lines are dropped, see [JSON logs](keys.md#json-logs).
- `panel.table.mode`, `panel.scrolling.horizontal.step` – object-table rendering (`scroll`, the default, or `fit`) and panning behavior.
- `terminal.follow`, `terminal.mode` – whether the shell line follows the active panel's context and namespace (default `true`), and how its `KUBECONFIG` points there: `overlay` (default, a small file before your own kubeconfig files) or `copy` (a full copy of the kubeconfig kc loaded for the context: the file that defines it, or the merged `$KUBECONFIG` files).
- `resources.showNonEmptyOnly`, `resources.order`, `resources.favorites` – resource-group listing controls (leave `favorites` empty to follow discovery's `all` category).
- `resources.visibility` – which resource types the root and the other type lists show. `defaultProfile` is the profile of a context without one of its own: `work` (the default: kc's list of daily types, see [Resource types](features.md#resource-types)), `all` or a name from `profiles`. `profiles` holds your own, each a `mode` (`work`: Work plus `show` minus `hide`; `hide`; `only`; `all`) with `show` and `hide` lists of API group and plural resource names, no versions or wildcards. `contexts` maps an exact kube context name to a profile. A profile that is missing or invalid lists Work, never every type, and kc says so at start. The editor ("Resource visibility..." in the Left or Right menu) writes these keys; "Types" in the panel options (`M-F1`, `M-F2`) switches one panel. See [Resource visibility](../internal/help/topics/resource-visibility.md).
- `objects.order`, `objects.columns`, `objects.groupByNamespace` – object list sort, wide/normal column display, and namespace grouping. The order is a column title in lower case with spaces as dashes (`name`, `age`, `status`, `restarts`, `nominated-node`), or `creation`; a leading `-` sorts descending. Columns sort by value: numbers, READY ratios, RESTARTS counts, ages and durations, CPU and memory quantities, and IP addresses; AGE uses the creation time. In an Events list, `last-seen` and `first-seen` use the Event's own times (`series.lastObservedTime`, `lastTimestamp`, `firstTimestamp`, `eventTime`), most recent first, and these cells count on with the clock like AGE; `name` sorts by the Event name, the wide Name column. Empty cells, `<none>` and `n/a` (no metrics) come last in both directions. Ties sort by name, then by namespace. A list without that column sorts by name. Lists that span namespaces (all namespaces, the pods of a node) are grouped by namespace first while `groupByNamespace` is true (the default); the order applies inside each namespace, and `namespace` orders the groups. With `groupByNamespace: false` the order applies to all rows at once, so `-mem` puts the pod that uses the most memory first whatever its namespace. "Group by namespace" in the Left/Right menu and in the panel options (`M-F1`, `M-F2`) switches one panel; it is checked while the list is grouped, and the sort, the filter and the namespace scope stay as they are.
- `columns` – your own columns per resource type, see [Custom Columns](#custom-columns). Empty by default.
- `health` – row health of custom resource types from their status, see [Custom Resource Health](#custom-resource-health). Empty by default.
- `kubernetes.discovery.refresh`, `kubernetes.clusters.ttl` – how often kc reads API discovery again in the background (a failed read is retried sooner), and how long a cluster cache stays running after the last open folder that uses it is closed.
- `input.mouse.doubleClickTimeout` – double-click threshold for Enter events.
- `nodeShell.enabled`, `nodeShell.contexts`, `nodeShell.image`, `nodeShell.namespace`, `nodeShell.maxDuration` – the node shell (off by default); see [Node Shell](features.md#node-shell).
- `panel.skin`, `contexts` – panel colors and per-context frame colors (see [Panel colors](#panel-colors)).
- `portForward.address` – local address port forwards listen on (default `127.0.0.1`).
- `overview.*` – thresholds of the `/overview` folder: the window for events and restarts, the restart count, requests and limits against allocatable, measured pod and node usage, the under-requested factor, containers near their memory or CPU limit, node disk and volume use, pods per node, ResourceQuota use, and the node count above which the kubelet stats are not read.
- `readOnly.enabled`, `readOnly.contexts` – contexts that open in read-only mode (see [Read-only mode](features.md#read-only-mode)). A file whose `readOnly` section does not load makes every context read-only; see [When the file does not load](#when-the-file-does-not-load).
- `metricsHistory.window`, `metricsHistory.timeout`, `metricsHistory.backends` – past workload usage from Prometheus in the Info panel mode (off by default; see [Cluster Overview](features.md#cluster-overview)). `C-x m` tests a backend and saves it for the current context; see [Metric sources](../internal/help/topics/metric-sources.md). The dashboard (`C-x g`, `kc dashboard`) reads the same backend; see [Dashboard](../internal/help/topics/dashboard.md).
- `charts.graphics` – how the dashboard draws its charts and Info mode its bars and history: `auto` (the default) draws images in iTerm2 3.7 or later, kitty and Ghostty, and under tmux with `allow-passthrough on`; `text` always draws text (braille or blocks in the charts); `kitty` always draws images. See [Dashboard](../internal/help/topics/dashboard.md) and [Info mode](../internal/help/topics/mode-info.md).

Run `kc` once to materialize defaults or inspect `config-default.yaml`.

## Custom Columns

Object lists show the columns the API server sends (`kubectl get`). The `columns` section adds columns computed from the object and hides server columns, per resource type:

```yaml
columns:
  apps/v1/deployments:
    - name: IMAGE
      jsonPath: .spec.template.spec.containers[*].image
    - name: Up-to-date
      hide: true
  v1/pods:
    - name: NODE-IP
      jsonPath: .status.hostIP
      wide: true
```

- Keys are `group/version/resource`; the core group has no group part (`v1/pods`, `v1/configmaps`). A key for one version does not apply to another.
- `jsonPath` takes the same expressions as `kubectl get -o custom-columns` (`.spec.x`, `{.spec.x}`, `[*]`, filters). Several matches are joined with commas; no match prints `<none>`. Added columns come after the server columns.
- `wide: true` shows an added column only in wide mode (`objects.columns: wide`).
- `hide: true` hides the server column of that name (case does not matter). `name: "*"` with `hide: true` hides every server column except Name, so the list shows only your columns. Name is never hidden.
- A bad entry is reported once on the command line when the first list opens; its column stays empty.
- When added columns are shown, the command line shows the matching `kubectl get ... -o custom-columns=...` command.

## Custom Resource Health

kc colors the rows of pods, nodes, claims, volumes and workloads by their health, and the faults filter (`C-z`) keeps the unhealthy ones. The `health` section does the same for your own resource types, from their status:

```yaml
health:
  # Ready, Reconciling and Stalled conditions (Flux, Cluster API, kstatus).
  kustomize.toolkit.fluxcd.io/v1/kustomizations:
    - condition: Stalled
      health: failed
    - condition: Reconciling
      health: notReady
    - condition: Ready
      health: ok
    - condition: Ready
      status: "False"
      health: notReady
  # Synced and Ready conditions (Crossplane).
  database.example.org/v1alpha1/postgresinstances:
    - condition: Synced
      status: "False"
      health: failed
    - condition: Ready
      health: ok
  # A status field instead of conditions (Argo CD).
  argoproj.io/v1alpha1/applications:
    - jsonPath: .status.health.status
      value: Healthy
      health: ok
    - jsonPath: .status.health.status
      value: Progressing
      health: notReady
    - jsonPath: .status.health.status
      value: Degraded
      health: failed
  # An operator that runs pods itself (CloudNativePG).
  postgresql.cnpg.io/v1/clusters:
    - condition: Ready
      health: ok
    - condition: Ready
      status: "False"
      health: notReady
```

- Keys are `group/version/resource`, as for `columns`. A key for one version does not apply to another.
- Rules are tried in order; the first that matches sets the health: `ok`, `succeeded` (finished work, like a completed Job), `notReady` (yellow, a fault) or `failed` (red, a fault). An object no rule matches stays unknown and keeps its color.
- A `condition` rule matches the entry of `status.conditions` with that type (case-sensitive, as in Kubernetes) and `status` (`True` by default, or `False` or `Unknown`).
- A `jsonPath` rule matches when the field, printed as text, equals `value`. It takes the expressions of `kubectl get -o custom-columns`; a missing field matches nothing. Quote values YAML would read as a boolean (`"yes"`, `"on"`).
- A status written for an older generation is not current: when `status.observedGeneration`, or the `observedGeneration` of the matched condition, is below `metadata.generation`, an `ok` or `succeeded` row shows as `notReady`.
- A field of the wrong type (conditions that are not a list, an object where the rule wants a value) makes the health unknown, and `/diagnosis` names the problem.
- Rows, the faults filter and `/diagnosis` read the same result. Inside a configured namespaced object, `/diagnosis` shows the health, the rule and the condition's reason and message, a status that lags the spec, the object's warning events, and the problems of the pods whose controller owner is the object (by UID, never by name). Pods of a StatefulSet or Deployment the object creates are not followed.
- kc checks the section when it starts and reports problems on the command line: a bad key, an unknown health, a bad JSONPath, a rule that sets both or neither of `condition` and `jsonPath`, and a rule that matches what an earlier rule already matches. An entry with a problem is not used at all. Types with a built-in rule (pods, nodes, claims, volumes, Deployments, StatefulSets, ReplicaSets, DaemonSets, Jobs) ignore their entry; the built-in rule wins.

`C-s` ("Save as defaults") in an option dialog reads the file again and writes only the settings that dialog shows, so custom commands and keys kc does not know stay as they are. The file is replaced atomically and keeps its permissions; a symbolic link is written through to its target. The command line says whether the settings were saved or apply to this session only.

## When the file does not load

kc reads the file once, when it starts. When the file does not load in full, a red bar between the panels and the command line says so for the whole session, with the file and the field:

```
Config layout not loaded, using its defaults: ~/.kc/config.yaml: layout.rightPanelMode: unknown panel mode "describ"; use list, describe, manifest, logs or info
```

- Each top-level section (`layout`, `readOnly`, `commands`, ...) loads on its own. A section with a bad value keeps all of its defaults, and the other sections load. A list such as `commands` counts as one value: one bad command drops all of them, so no command runs without its `dangerous` flag.
- When the `readOnly` section does not load, or nothing of the file does (it cannot be read, or it is not valid YAML), every context is read-only: `Config not loaded, every context read-only: ...`. kc cannot tell which contexts the file protects, so it protects all of them. `kc --write` still allows writes for one run.
- kc never writes over such a file: `C-s` is refused while any part of it does not load. Fix the file (or move it away to start from scratch) and press `C-s` again; restart kc to load the fixed file and remove the bar.

## All settings with their defaults

[`config-default.yaml`](../config-default.yaml) lists every setting with its default and a comment. Copy the parts you want to change into `~/.kc/config.yaml`; a test keeps that file equal to the defaults in the code.

## Panel colors

Set `contexts` to color the panel frame and the context label for some clusters, for example red for every context with `prod` in its name. A context without a matching entry keeps the normal look.

`panel.skin` picks the panel colors. kc ships `mc` (blue panels, the default) and `dark` (black panels). To make your own, put a YAML file in `~/.kc/skins/<name>.yaml` and set `panel.skin: <name>`; a file with the name of a built-in skin replaces it. Every color is optional and falls back to the `mc` value:

```yaml
panel:
  background: blue
  foreground: white
  frame: white
  header: yellow        # table header
  marked: yellow        # rows marked with Insert
  cursor:
    foreground: black
    background: cyan
  title:                # path label of the active panel
    foreground: black
    background: white
  context: bright-yellow   # context name in the bottom border
  namespace: bright-cyan  # namespace name; also the "outside Work" mark
```

A skin covers the two panels only; dialogs, the function key bar and the viewer keep their colors (the viewer has `viewer.theme`). An unknown skin, an unknown key or a bad color is shown as an error at startup, and kc then uses `mc`; a bad `contexts` entry is reported and skipped.
