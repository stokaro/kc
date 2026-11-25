# Panels and navigation

[All screenshots](../README.md)

The root of a context: resource groups with their API version and object count. Start kc, or `cd /` on the shell line.

![](01-root.png)

The context picker of the left panel; only that panel switches. `F9`, Left, Context... (`t`).

![](02-contexts.png)

The namespace picker. `F9`, Left, Namespace... (`n`), or `F7` in a panel.

![](03-namespaces.png)

Pods with CPU and memory columns, the cursor on a pod and Info about it in the other panel. Enter `/pods`, `C-s` to search a row, `M-w` to widen the panel.

![](04-pods.png)

The row filter dialog with a column term. `M-f`.

![](05-filter.png)

The filtered list; the bottom border shows the filter. Type `status!=Running` in the filter dialog and press Enter.

![](06-filtered.png)

Faults only, across all namespaces: only rows that are not healthy stay. `C-z`.

![](07-faults.png)

Marked rows. `+` with the pattern `*a*`; `C-t` marks one row.

![](08-marks.png)

Inside a Deployment: its ReplicaSets, logs, `/diagnosis`, `/rollout`, `/events`, `/timeline` and the actions. Enter on a Deployment, or `cd deploy/web`.

![](09-deployment.png)

Inside a pod: its containers and the same object rows. Enter on a pod.

![](10-pod.png)

A container's file system. Enter on a container, then on `/files`.

![](11-container-files.png)

Owners of a pod, up to the Deployment. `M-j`, or `F9`, File, Owners.

![](12-owners.png)

What uses a ConfigMap. `M-b` (Used by).

![](13-used-by.png)

What a pod requires: its ConfigMaps, Secrets, claims and ServiceAccount. `M-d` (Requires).

![](14-requires.png)

Field documentation of the kind under the cursor, here a Deployment. `M-e` (Explain).

![](15-explain.png)

What the kubeconfig user may do with the pod under the cursor, and the RBAC rule that allows it. `M-k`.

![](16-access.png)

The hotlist with saved places and views. `C-\`.

![](17-hotlist.png)

A saved view opened from the hotlist: a folder with its filter and sort.

![](18-saved-view.png)

The panel history list. `M-H`; `M-y` and `M-u` step back and forward.

![](19-history.png)

Helm releases of the namespace. `cd /helm`.

![](20-helm.png)

Inside a Helm release: values, manifest, notes, its resources and the stored revisions. Enter on a release.

![](21-helm-release.png)

The port forward dialog on a pod. `M-p`, or `F9`, File, Port forward.

![](22-port-forward.png)

Two contexts side by side: the left panel in the kind context and `shop`, the right one in the mirror context, read-only by the config (`RO` in its frame), and `batch`. `F9`, Right, Context... (`t`) switches only the right panel; Namespace... (`n`) then picks its namespace.

![](23-two-contexts.png)
