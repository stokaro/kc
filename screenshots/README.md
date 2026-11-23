# Screenshots

One screenshot per kc feature, grouped as the [help index](../internal/help/topics/index.md) groups them. Every image comes from [`capture.sh`](capture.sh), which runs kc against a throwaway kind cluster seeded with [`docs/demo/seed.yaml`](../docs/demo/seed.yaml) and [`seed.yaml`](seed.yaml), so the objects and the problems on screen are the same each time. kc runs at 150x42 in UTC. Each line says what the image shows and which keys lead there; `C-x w` means Ctrl+X, then W, and `M-` is Alt.

- [Panels and navigation](#panels-and-navigation)
- [Viewers and editing](#viewers-and-editing)
- [Info and the cluster overview](#info-and-the-cluster-overview)
- [Dashboard](#dashboard)
- [Investigation window](#investigation-window)
- [Diagnosis](#diagnosis)
- [Rollout, Events and timeline](#rollout-events-and-timeline)
- [NetworkPolicy between pods](#networkpolicy-between-pods)
- [Node drain](#node-drain)
- [Comparing](#comparing)
- [Metric sources](#metric-sources)
- [Menus, dialogs and help](#menus-dialogs-and-help)
- [Terminal](#terminal)
- [How to regenerate](#how-to-regenerate)

## Panels and navigation

The root of a context: resource groups with their API version and object count. Start kc, or `cd /` on the shell line.

![](panels/01-root.png)

The context picker. `F9`, Left, Context... (`t`).

![](panels/02-contexts.png)

The namespace picker. `F9`, Left, Namespace... (`n`), or `F7` in a panel.

![](panels/03-namespaces.png)

Pods with CPU and memory columns, the cursor on a pod and Info about it in the other panel. Enter `/pods`, `C-s` to search a row, `M-w` to widen the panel.

![](panels/04-pods.png)

The row filter dialog with a column term. `M-f`.

![](panels/05-filter.png)

The filtered list; the bottom border shows the filter. Type `status!=Running` in the filter dialog and press Enter.

![](panels/06-filtered.png)

Faults only, across all namespaces: only rows that are not healthy stay. `C-z`.

![](panels/07-faults.png)

Marked rows. `+` with the pattern `*a*`; `C-t` marks one row.

![](panels/08-marks.png)

Inside a Deployment: its ReplicaSets, logs, `/diagnosis`, `/rollout`, `/events`, `/timeline` and the actions. Enter on a Deployment, or `cd deploy/web`.

![](panels/09-deployment.png)

Inside a pod: its containers and the same object rows. Enter on a pod.

![](panels/10-pod.png)

A container's file system. Enter on a container, then on `/files`.

![](panels/11-container-files.png)

Owners of a pod, up to the Deployment. `M-j`, or `F9`, File, Owners.

![](panels/12-owners.png)

What uses a ConfigMap. `M-b` (Used by).

![](panels/13-used-by.png)

What a pod requires: its ConfigMaps, Secrets, claims and ServiceAccount. `M-d` (Requires).

![](panels/14-requires.png)

Field documentation of the kind under the cursor, here a Deployment. `M-e` (Explain).

![](panels/15-explain.png)

What the kubeconfig user may do with the pod under the cursor, and the RBAC rule that allows it. `M-k`.

![](panels/16-access.png)

The hotlist with saved places and views. `C-\`.

![](panels/17-hotlist.png)

A saved view opened from the hotlist: a folder with its filter and sort.

![](panels/18-saved-view.png)

The panel history list. `M-H`; `M-y` and `M-u` step back and forward.

![](panels/19-history.png)

Helm releases of the namespace. `cd /helm`.

![](panels/20-helm.png)

Inside a Helm release: values, manifest, notes, its resources and the stored revisions. Enter on a release.

![](panels/21-helm-release.png)

The port forward dialog on a pod. `M-p`, or `F9`, File, Port forward.

![](panels/22-port-forward.png)

## Viewers and editing

The YAML viewer. `F3` on an object.

![](viewer/01-yaml.png)

Search in the viewer. `F7` in a viewer.

![](viewer/02-search.png)

The Describe panel mode: the other panel describes the object under the cursor. `F9`, Right, Describe (`d`).

![](viewer/03-describe-mode.png)

The Manifest panel mode. `F9`, Right, Manifest (`m`).

![](viewer/04-manifest-mode.png)

The Logs panel mode: the log of the pod under the cursor. `F9`, Right, Logs (`g`).

![](viewer/05-logs-mode.png)

Merged logs of all pods of a Deployment, JSON lines formatted. `F3` on the `logs` row of a Deployment.

![](viewer/06-merged-logs.png)

The log filter dialog. `M-g` in the logs viewer.

![](viewer/07-log-filter.png)

The filtered log: only lines with `level` error or warn (`level=error,warn`).

![](viewer/08-log-filtered.png)

The fields of the JSON line under the cursor, to include or exclude by value. `M-d` in the logs viewer.

![](viewer/09-log-fields.png)

Local files in the other panel. `M-l`.

![](viewer/10-local-panel.png)

A local Markdown file in the viewer. `F3` on a local file.

![](viewer/11-local-file.png)

A large local file in the paged viewer, at its end. `F3` on a large file, then `End`.

![](viewer/12-paged.png)

`F4` on a local file opens `$EDITOR` (vi here) in the terminal.

![](viewer/13-local-edit.png)

The copy dialog. `F5` on a row.

![](viewer/14-copy.png)

## Info and the cluster overview

Info of a Deployment: CPU and memory of its pods against requests and limits, with a history and the top pods. Info mode in the other panel (`F9`, Right, Info), cursor on a Deployment.

![](info/01-workload.png)

Info of a node: allocatable and capacity, pods, disk and the top pods on it. Cursor on a node in `/nodes`.

![](info/02-node.png)

The cluster overview: capacity, health, risks and activity checks with their state. `M-i`, or Enter on `/overview`.

![](info/03-overview.png)

`/attention`: only the failed and warning checks. Enter on `/attention`.

![](info/04-attention.png)

The objects behind one finding. Enter on a finding row.

![](info/05-finding.png)

## Dashboard

The full-screen dashboard of a Deployment: CPU and memory of its pods over the session, Events and the top pods. `C-x g` on a Deployment. The charts here are drawn with blocks (`b`), and `+` zooms in.

![](dashboard/01-live.png)

The cursor moved back in time pauses the live view and shows the values at that point. Left arrow.

![](dashboard/02-cursor.png)

An expanded chart. `Enter` on a chart.

![](dashboard/03-expanded.png)

The focus on the Events of the range. `Tab`.

![](dashboard/04-events.png)

The dashboard of one pod of the Deployment. `Tab` to the top pods, then `Enter`.

![](dashboard/05-pod.png)

## Investigation window

The investigation window dialog: one time window for the Logs and Info panels and the dashboard. `C-x w`, here with `10m`.

![](investigation/01-dialog.png)

A Logs panel limited to the window.

![](investigation/02-logs.png)

The Command menu marks the active window. `F9`, Command.

![](investigation/03-command-menu.png)

## Diagnosis

`/diagnosis` of a Deployment whose pod cannot pull its image: the findings with their evidence and the next step. Enter `/diagnosis` inside a Deployment.

![](diagnosis/01-workload.png)

`/diagnosis` of a Service whose selector matches no running pod.

![](diagnosis/02-service.png)

`/diagnosis` of a pod whose claim names a StorageClass that does not exist.

![](diagnosis/03-claim.png)

`/diagnosis` of a pod no node can take: the scheduler's message and kc's node checks.

![](diagnosis/04-pending-pod.png)

`/diagnosis` of a crash-looping pod, with rows for the current and the previous log.

![](diagnosis/05-crash-loop.png)

## Rollout, Events and timeline

`/rollout` of a Deployment with three revisions and what changed between them. Enter `/rollout` inside a Deployment.

![](rollout/01-revisions.png)

The pod template diff between two revisions. `F3` on a `changes` row.

![](rollout/02-template-diff.png)

`/events` of a pod.

![](rollout/03-events.png)

`/timeline` of a Deployment: the changes kc observed while it records, here a scale from 2 to 3 replicas.

![](rollout/04-timeline.png)

The diff of one timeline entry. `F3` on an entry.

![](rollout/05-timeline-diff.png)

The Scale dialog. Enter on `Scale...` inside a Deployment.

![](rollout/06-scale-dialog.png)

## NetworkPolicy between pods

Whether a web pod (left panel) may reach an api pod (right panel), per port and direction, with the policies that decide it. `C-x n`.

![](networkpolicy/01-result.png)

One of the policies the result cites. `F3` on a policy row.

![](networkpolicy/02-policy.png)

## Node drain

The node actions. `M-n` on a node.

![](drain/01-node-actions.png)

The drain options, with emptyDir data allowed for a pod that keeps data in one. Drain...

![](drain/02-options.png)

The confirmation: what the drain evicts, the PodDisruptionBudgets and the same `kubectl drain` command. Enter.

![](drain/03-confirm.png)

The drain progress once it is done.

![](drain/04-drained.png)

The node stays cordoned afterwards.

![](drain/05-cordoned.png)

## Comparing

Two objects, one in each panel. `C-x C-d`.

![](compare/01-objects.png)

A local manifest against the live object. Local file in one panel, the object in the other, `C-x C-d`.

![](compare/02-local-file.png)

Two lists: Deployments in `shop` against `staging`. `C-x d`.

![](compare/03-lists.png)

One pair of the list compare. Enter on a row.

![](compare/04-list-pair.png)

Comparing a list with another context: the context picker. `C-x c`.

![](compare/05-context-picker.png)

The result against the second context (which points at the same cluster here).

![](compare/06-context-result.png)

## Metric sources

What the context serves for metrics, and the Prometheus backend settings. `C-x m`. The cluster has metrics-server and no Prometheus.

![](metrics/01-sources.png)

## Menus, dialogs and help

The Left menu. `F9`.

![](menus/01-left.png)

The File menu.

![](menus/02-file.png)

The Command menu.

![](menus/03-command.png)

The Options menu.

![](menus/04-options.png)

The user menu from the configuration. `F2`.

![](menus/05-user-menu.png)

The panel options. `M-F1`, or `F9`, Options, Left panel options.

![](menus/06-panel-options.png)

The delete confirmation. `F8`.

![](menus/07-delete-confirm.png)

The create namespace dialog. `F7` in `/namespaces`.

![](menus/08-create-namespace.png)

`F1` on a row opens the help page for it, here `/diagnosis`.

![](menus/09-help-folder.png)

The help index. `F2` in the help.

![](menus/10-help-index.png)

`F1` in a dialog opens its page, here the investigation window.

![](menus/11-help-dialog.png)

## Terminal

The integrated terminal. `C-o`; `C-o` again returns to the panels.

![](terminal/01-terminal.png)

The shell line under the panels: `cd` and kubectl-style commands. Type at the panels.

![](terminal/02-shell-line.png)

A shell in a container. `M-x` on a pod.

![](terminal/03-container-shell.png)

## How to regenerate

`capture.sh` creates a kind cluster named `kc-shots` with two workers, seeds it, captures every scene and deletes the cluster:

```bash
screenshots/capture.sh all
```

It needs what [`docs/demo/record.sh`](../docs/demo/README.md) needs: Go, Docker and kind (here or on a Linux host over ssh), kubectl, Helm and ffmpeg. ffmpeg cuts every PNG to 128 colors, which keeps the folder small. kc and kubectl run with a temporary `HOME` and a kubeconfig that holds only the kind cluster, and every kubectl call first checks the context and the server, so your own clusters are never touched.

The steps also run one by one, which is quicker when you change a scene:

```bash
screenshots/capture.sh up
screenshots/capture.sh seed
screenshots/capture.sh list                      # scenes in capture order
screenshots/capture.sh capture drain             # one group
screenshots/capture.sh capture panels/pods       # one scene
screenshots/capture.sh down
```

The cluster name, the API server port, an ssh host for Docker and the dashboard wait are set through `KC_SHOTS_*` variables; the header of `capture.sh` lists them. Run kind elsewhere with, for example, `KC_SHOTS_SSH=user@docker-host`.

The scenes are shell functions in [`scenes/`](scenes), one file per group. A function `scene_<group>_<name>` becomes `<group>/NN-<name>.png`, numbered in the order of the file. A scene that continues another one calls `after <group>/<name>`. The dashboard scene waits five minutes so the charts have data. The drain scenes cordon and uncordon `kc-shots-worker2`, and the timeline scenes scale `shop/web` and set it back, so a capture can run again on the same cluster.
