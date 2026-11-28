# Features

A summary of what kc does. The [F1 help pages](../internal/help/topics/index.md) ship inside the binary and are the reference for each feature.

## Highlights

- **Kubectl-compatible startup intents:** `kc get ...` and `kc logs ...` parse familiar CLI syntax and launch directly into the matching folders, multi-selecting objects and opening manifest/log viewers when requested (`docs/designs/cli-intents.md`).
- **Hierarchy-first navigation:** resource groups → object folders → virtual subfolders (pod containers, ConfigMap/Secret keys, etc.). Every row is keyed by full `group/version/resource`.
- **Child object hierarchies:** drill from parents into their children (e.g., Deployments → ReplicaSets → Pods, Services → EndpointSlices, with a diagnosis of Services, workloads, pods, Ingresses and HTTPRoutes) via per-object child folders so related resources stay discoverable in the tree.
- **Helm releases:** `/helm` lists releases with their stored values, manifest, notes, hooks and revision history, and links each manifest object to the live object (read-only, no `helm` binary needed).
- **Live data via informers:** controller-runtime caches back all folders; add/update/delete events keep selections and scroll offsets stable.
- **CPU and memory usage:** when the cluster serves `metrics.k8s.io` (metrics-server), pod lists show `CPU`, `MEM`, `%CPU/R` (of requests) and `%MEM/L` (of limits), and node lists show `CPU`, `%CPU`, `MEM` and `%MEM` (of allocatable). Wide mode adds `%CPU/L` and `%MEM/R` for pods and `CPU/A` and `MEM/A` for nodes. kc reads the metrics every 15 seconds, only while such a list is on screen and no dialog is open. `n/a` means no metrics for the object yet, no request or limit to compare with, metrics kc may not read, or a sample metrics-server measured more than two minutes before kc read it (the list succeeded, but the value is not current).
- **Two synchronized panels:** each panel can render lists, or follow the other panel's selection in Describe, Manifest, Logs or Info mode (`C-1` / `C-2` cycle the left / right panel's mode; Logs streams the selected pod's or container's log, and `F2` there sets the container, "All containers", tail, since, timestamps and previous; Info shows CPU, memory, pods and disk bars for the other panel's scope), supports multi-selection, per-panel options (columns/order/modes), and an integrated function-key bar. A 2-line terminal lives underneath for quick kubectl work.
- **Rich viewers:** YAML/text viewer with syntax highlighting, wrap toggle, and an mc-style search dialog (`F7`: normal, regular expression or wildcard search, case sensitive, backwards, whole words; `n`/`Shift+F7` for the next match). Logs viewer streams `kubectl logs` with follow mode, search, and End-to-follow shortcuts; `F2` sets tail lines, the since window, timestamps and the previous container instance, and restarts the stream. A container that restarted also gets a `logs (previous)` row. When a followed stream breaks while the container still runs (an API server or kubelet restart, a dropped connection), the viewer and the Logs panel reconnect with backoff and continue after the last line without repeating it: `[kc] reconnecting in 2s: ...` and `[kc] reconnected` mark it, the footer shows `(reconnecting)`, and `[kc] lines after <time> may be missing` says when the kubelet no longer had the lines in between. A container that restarted, stopped or was replaced during the wait ends the stream with `[kc] not reconnecting: ...`. A refused request is not retried, and after eight failed attempts in a row (about two minutes) the stream fails. JSON lines are shown as time, level, message and key=value fields, with display filters and field extraction. `F3` in a Logs panel expands its session full screen without reading the log again, and `M-m` in a merged log shows one source alone ([Logs mode](../internal/help/topics/mode-logs.md)).
- **Workload actions:** entering a Deployment, StatefulSet, DaemonSet or ReplicaSet shows rows next to its child types: `Scale...` (a replica count dialog, prefilled with the current count, written through the scale subresource), `Restart...` (as `kubectl rollout restart`) and, for Deployments, `Roll back...` (pick a revision; as `kubectl rollout undo --to-revision`). Each change asks first in a red dialog that names the context, the object and the equivalent kubectl command. Read-only mode refuses them.
- **Container internals:** each pod container exposes `/logs`, a `shell` row and a `/root` browser so you can inspect container filesystems from kc. When images lack shell tooling, kc can spin up a debug helper automatically. kc lists and copies files through one `sh` per container; it closes that shell after 60 seconds without a file operation (the debug helper's idle timeout) and opens a new one on the next operation. A running copy keeps it open.
- **Merged logs:** Deployments, StatefulSets, DaemonSets, ReplicaSets and Jobs have a `logs` row, and every pod a `logs (all containers)` row. `Enter` or `F3` on it streams the logs of all matching pods and containers into one viewer, each line prefixed with a colored `[pod/container]`, like `kubectl logs -l <selector> --all-containers --prefix -f`. Pods that start or go away are followed; `[kc] +` and `[kc] -` lines mark each stream, and `[kc] ~` lines a stream that reconnects. The status line starts with the coverage, for example `incomplete: 50 streaming, 20 waiting (limit 50), 2 failed`. `M-s` opens the sources dialog: one row per source with its state and last error, a filter of `pod[/container]` globs where `-` excludes (`-/istio-proxy` drops a sidecar in every pod, `api-0 api-1` follows two pods), the number of streams at once for this view, and Retry for failed sources. New pods follow the filter, and changing it leaves the other streams and the shown lines alone.
- **Large files in the viewer:** `F3` reads a local file of up to 2 MiB, or a container file of up to 512 KiB, whole and highlights its syntax. A larger file opens in pages, as in mc's viewer: kc reads it in 256 KiB blocks around the visible lines, keeps at most 16 blocks (4 MiB) in memory, and shows plain text; the status line says so, for example `paged, 1.2 GiB, highlighting off · line 1`. A placeholder line marks a block that is still being read, and the viewer keeps answering keys meanwhile. `Home`, `End`, `PgUp` and `PgDn` work on the whole file; `End` reads the last blocks only, so the lines before it are not numbered and the position shows as a share of the bytes (`~97%`) until kc has counted them (it counts a local file in the background, never a container file). Search (`F7`) streams over the file from the current position with its progress in the footer; `Esc` cancels it, and `n` and `N` find the next and previous match. A line longer than 16 KiB is shown in pieces. A container file is read with one `dd` command per block on the container's filesystem shell, so scrolling costs no new exec request. `M-a` does not copy a paged file; `F5` saves it.
- **Local exports:** any resource that supports `F3` viewing (YAML, logs, ConfigMap/Secret data, pod filesystem files, etc.) can be saved locally via `F5`. The Copy dialog proposes a file in the directory of the other panel when it shows the local filesystem (`M-l`), and in the system temp directory otherwise; edit the path in the `to:` line. An existing file is replaced only with `Overwrite an existing file` checked. kc writes the export to a temporary file next to the destination and replaces the destination only when the whole content arrived, so a failed export leaves an existing file as it was. New files are created with mode 0600; a replaced file keeps its owner permissions and loses group and other access. kc refuses to write through a symbolic link. A pod filesystem file streams from the container whatever its size, and the export fails only when the stream stalls for 30 seconds. Binary ConfigMap and Secret keys show their size, detected content type and a hex dump in `F3`; `F5` saves the original bytes.
- **Copy between local and container folders:** `M-l` opens a local directory in the active panel (the directory of the last copy, or the working directory; `Back` walks up the tree and, from `/`, returns to the cluster). `F3` views a local text file and `F4` edits it; a directory, a binary file (one with a NUL byte), a device or pipe, or a file kc cannot read or write is refused with a message naming the reason. With a container's `/root` folder in one panel and a local directory in the other, `F5` copies the marked files and directories (or the one under the cursor) into the local directory, recursively. The dialog names the pod, container and source first; the `to:` line holds the local directory and can be edited. Entries that exist already are skipped unless `Overwrite existing entries` is checked (files are replaced, directories merged). The copy runs in the background with a file and byte count; `Esc` or `C-c` cancels it. At the end the dialog lists each entry as copied, skipped, failed or canceled. kc streams `tar` from the container like `kubectl cp` (through the debug helper, which it never adds without asking, it streams the helper's `tar` and keeps the helper alive meanwhile), and reads file by file through the filesystem session when there is no `tar`. A directory is assembled in a hidden `.<name>.kc-partial-*` directory and moved into place only when complete, so a failed or canceled entry leaves nothing behind. Names that would leave the destination (`..`, absolute paths) are refused; symbolic links, devices and pipes are listed as left out, not copied. Copies get the owner permission bits of the source only (a 0644 file becomes 0600, 0755 becomes 0700); directories are 0700. The other way round, `F5` in a local directory with a container folder in the other panel copies the marked local entries into the container. A copy into a container changes the cluster, so it asks in a red dialog that names the context, pod, container and target directory and shows the equivalent `kubectl cp` command, with `Cancel` as the default button; read-only mode refuses it. kc packs the entries into a tar stream itself and unpacks it with `tar` in the container, as `kubectl cp` does: each entry goes into a hidden `.<name>.kc-partial-*` directory next to the target first and is moved into place (or merged, for `Overwrite`) only when the whole stream arrived, so a canceled or failed entry leaves nothing that looks finished. kc never reads outside the local directory and never follows a local symbolic link: links, devices and pipes are left out and listed. Files keep their permission bits without setuid, setgid and sticky; owner and group are those of the user `tar` runs as in the container. The image needs a shell and `tar`; without `tar` the copy fails with a message. A container without a shell is written through the debug helper you already agreed to for browsing (busybox `tar`, writing under `/proc/1/root`), and kc keeps the helper alive while the copy runs.
- **Dialogs:** input and copy dialogs look and work like mc's: cyan input lines, check boxes, rules across the frame and bracketed buttons, the default one drawn `[< OK >]`. `Tab` and `S-Tab` (or the arrow keys) move between the widgets, `Space` toggles a check box, `Enter` runs the default button (or the focused one), `Esc` cancels, and `M-<letter>` picks the widget whose letter is highlighted (the letter alone works too while no input line has the focus, except in red dialogs, the `F8` delete dialog included: there `y` alone does nothing and `M-y` deletes). A click focuses an input line, toggles a check box or presses a button.
- **Config + persistence:** `~/.kc/config.yaml` controls viewer theme, panel widths, table modes, discovery TTL, mouse preferences, and more. In the option dialogs, `Enter` applies a change to the current session and `C-s` saves it as the default.

## Help

`F1` opens the help page of what has focus: the dialog on top (for example the investigation window, `C-x w`), the mode of the active panel, or the special folder or row under the cursor. Elsewhere it opens the index. In the help window `Tab` selects a link, `Enter` follows it, `F3` goes back, `F2` opens the index and `F10` or `Esc` closes it, returning to the dialog as you left it.

The pages are in [`internal/help/topics`](../internal/help/topics/index.md) and ship inside the binary. They are the reference for each feature; this page gives a summary and links to them.

## Field documentation

`M-e` browses the field documentation of the selected object or resource type, read from the cluster's OpenAPI v3 schema for that group and version. See [Field documentation](../internal/help/topics/explain.md).

## Menu Bar

`F9` opens the menu bar: Left, File, Command, Options, Right, as in mc. See [Menu bar](../internal/help/topics/menu-bar.md).

## Port Forwarding

`M-p` forwards a local port to the selected pod, container or service; `/port-forwards` lists the forwards of this kc process. See [Port forwards](../internal/help/topics/port-forward.md).

## Context and Namespace

Each panel has its own kubeconfig context and namespace scope, shown in its bottom border: the left panel can browse staging while the right one browses prod, also with namespaces and objects of the same names. "Context..." in a panel's menu switches that panel only; "Context for both panels..." in the Command menu switches both. Every action runs in the context of the row it acts on, under that context's read-only setting, and the shell line follows the active panel. kc never changes the kubeconfig. See [Context and namespace](../internal/help/topics/context-namespace.md).

## Resource types

The root and the other lists of resource types show the Work profile: the types of daily work, from core and apps to cert-manager, Argo CD and Flux. The rest are one `Enter` away under `/other types (N)`, and `cd`, the hotlist, links and quick search still open any type. "Resource visibility..." in the Left or Right menu adds types to Work or removes them, lists only or hides the types you check, and saves the result as a named profile for a kube context or as the default; "Types" in the panel options switches a panel between Work, All and your profiles. See [Resource types](../internal/help/topics/resource-types.md) and [Resource visibility](../internal/help/topics/resource-visibility.md).

## Filtering Rows

`M-f` filters an object list by name, label selector, namespace or column value. See [Row filter](../internal/help/topics/row-filter.md).

## Marks and Delete

`Ins`, `+`, `*` and `C-a` mark rows, as in mc, and the bottom border says how many (`3 marked`). `F8` deletes the marked rows, or the object under the cursor when none is marked, after a red dialog that names the context and the objects: `Delete 3 marked configmaps?` with the first five names. `No` has the focus and `y` alone does nothing; `M-y` deletes. Each object is deleted as the instance its row showed, with its UID as a precondition. One line then reports what was deleted and what was not; a row whose delete failed for another reason than a gone or replaced object stays marked. Deleting a namespace, a CustomResourceDefinition, a PersistentVolume or a node takes other objects with it: the dialog says what (`It holds 5 deployments, 9 pods, 1 PVC and 1 Helm release.`, counted in the background while the dialog is open) and `Yes` stays grayed until you type the object's name. Such an object is deleted alone, not as part of a marked set. `F7` in `/namespaces` creates a namespace from a red dialog that names the context and its read-only mode. See [Delete](../internal/help/topics/delete.md).

## Saved Views

The hotlist (`C-\`) keeps panel locations; a saved view also keeps the columns, sort, grouping, row filter and faults only. See [Hotlist and saved views](../internal/help/topics/hotlist.md). In `~/.kc/hotlist.yaml` a view is an entry with `name` and `view` next to the location fields:

```yaml
entries:
- context: prod
  namespace: payments
  steps: ["payments//v1/pods"]
  path: /pods
  name: payments failures
  view:
    columns: wide
    order: -restarts
    filter: api restarts>3
    faultsOnly: true
```

### cd on the shell line

`cd` on the shell line moves the active panel instead of the shell. See [Shell, terminal and attach](../internal/help/topics/shell.md).

## Node Shell

A node shell runs a privileged pod on the node and opens its shell. See [Shell, terminal and attach](../internal/help/topics/shell.md).

## Object Actions

Workloads have Scale, Restart and Roll back rows; CronJobs have Run now and Suspend/Resume in `F2`. Each change is confirmed in a red dialog. See [Workload actions](../internal/help/topics/workload-actions.md) and [Confirmation dialog](../internal/help/topics/confirm.md).

## Helm Releases

`/helm` lists the Helm releases of the namespace, read from Helm's storage Secrets, with their values, manifest, resources and history. It is read-only. See [Helm releases](../internal/help/topics/helm.md).

## Cluster Overview

The first row of every panel root is `/overview`, a report of checks about the cluster and the namespace (`M-i`, or `kc overview`). See [Cluster overview](../internal/help/topics/overview.md), [Needs attention](../internal/help/topics/attention.md) and [Quiet checks](../internal/help/topics/quiet-checks.md). The Info panel mode shows the load of the other panel's scope; see [Info mode](../internal/help/topics/mode-info.md).

Info and the dashboard (`C-x g`) treat an object of a custom kind that controls pods, such as an Argo Rollout, as a workload: its pods, their usage and its desired replicas from the CRD's scale subresource. kc follows the pods' controller references through any namespaced kind and watches only the kinds it meets there. See [Info mode](../internal/help/topics/mode-info.md) and [Dashboard](../internal/help/topics/dashboard.md#custom-workloads).

### Pin, freeze and the investigation window

`C-x p` pins a Describe, Manifest, Logs or Info panel to what it shows, `C-x f` freezes its display, and `C-x w` sets one time window for the Logs and Info panels and the dashboard. See [Pin and freeze a panel](../internal/help/topics/pin-freeze.md) and [Investigation window](../internal/help/topics/investigation-window.md).

## Read-only mode

In read-only mode kc refuses its own changes to the cluster. Turn it on per context, or with `kc --readonly`; `kc --write` turns it off for a run. A configuration file whose `readOnly` section does not load makes every context read-only, and a red bar names the problem until the file loads. See [Read-only mode](../internal/help/topics/read-only.md).

```yaml
readOnly:
  enabled: false        # true: every context
  contexts:
    - "*prod*"
    - shared-staging
```

## Node Actions

In `/nodes`, `M-n` cordons, uncordons or drains the marked nodes. Before a drain, the confirmation lists the affected workloads and the PodDisruptionBudgets that cover the pods, with their allowance as read then. See [Node actions](../internal/help/topics/node-actions.md).
