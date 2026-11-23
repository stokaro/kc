# Kubernetes Commander (kc)

[![Build Status](https://github.com/stokaro/kc/actions/workflows/build.yml/badge.svg?branch=main)](https://github.com/stokaro/kc/actions/workflows/build.yml)

> **Origin:** kc started as a fork of [Kubernetes Commander by Stefan Schimanski](https://github.com/sttts/kc) (upstream). This repository is developed independently and has diverged a lot from it: key bindings, features and internals differ. Report issues for this version at [stokaro/kc](https://github.com/stokaro/kc/issues), not upstream. See [NOTICE](NOTICE) for attribution.

![kc demo with a caption per scene: two panels and quick search, the cluster overview and diagnosis of broken objects, the menu bar, draining a node, scaling a Deployment and its timeline, YAML, comparing two objects, filtered JSON logs, copying a file out of a container, the hotlist, the terminal](docs/demo.gif)

The demo was recorded before the menu bar stayed on the top row (see [Menu Bar](#menu-bar)).

A two-panel Kubernetes TUI inspired by Midnight Commander, built with Go 1.27, Bubble Tea v2 and controller-runtime informers. See `TODO.md` for the plan.

## Highlights

- **Kubectl-compatible startup intents:** `kc get ...` and `kc logs ...` parse familiar CLI syntax and launch directly into the matching folders, multi-selecting objects and opening manifest/log viewers when requested (`docs/designs/cli-intents.md`).
- **Hierarchy-first navigation:** resource groups → object folders → virtual subfolders (pod containers, ConfigMap/Secret keys, etc.). Every row is keyed by full `group/version/resource`.
- **Child object hierarchies:** drill from parents into their children (e.g., Deployments → ReplicaSets → Pods, Services → EndpointSlices, with a diagnosis of Services, workloads, pods, Ingresses and HTTPRoutes) via per-object child folders so related resources stay discoverable in the tree.
- **Helm releases:** `/helm` lists releases with their stored values, manifest, notes, hooks and revision history, and links each manifest object to the live object (read-only, no `helm` binary needed).
- **Live data via informers:** controller-runtime caches back all folders; add/update/delete events keep selections and scroll offsets stable.
- **CPU and memory usage:** when the cluster serves `metrics.k8s.io` (metrics-server), pod lists show `CPU`, `MEM`, `%CPU/R` (of requests) and `%MEM/L` (of limits), and node lists show `CPU`, `%CPU`, `MEM` and `%MEM` (of allocatable). Wide mode adds `%CPU/L` and `%MEM/R` for pods and `CPU/A` and `MEM/A` for nodes. kc reads the metrics every 15 seconds, only while such a list is on screen and no dialog is open. `n/a` means no metrics for the object yet, no request or limit to compare with, metrics kc may not read, or a sample metrics-server measured more than two minutes before kc read it (the list succeeded, but the value is not current).
- **Two synchronized panels:** each panel can render lists, or follow the other panel's selection in Describe, Manifest, Logs or Info mode (`C-1` / `C-2` cycle the left / right panel's mode; Logs streams the selected pod's or container's log, and `F2` there sets the container, "All containers", tail, since, timestamps and previous; Info shows CPU, memory, pods and disk bars for the other panel's scope), supports multi-selection, per-panel options (columns/order/modes), and an integrated function-key bar. A 2-line terminal lives underneath for quick kubectl work.
- **Rich viewers:** YAML/text viewer with syntax highlighting, wrap toggle, and an mc-style search dialog (`F7`: normal, regular expression or wildcard search, case sensitive, backwards, whole words; `n`/`Shift+F7` for the next match). Logs viewer streams `kubectl logs` with follow mode, search, and End-to-follow shortcuts; `F2` sets tail lines, the since window, timestamps and the previous container instance, and restarts the stream. A container that restarted also gets a `logs (previous)` row. When a followed stream breaks while the container still runs (an API server or kubelet restart, a dropped connection), the viewer and the Logs panel reconnect with backoff and continue after the last line without repeating it: `[kc] reconnecting in 2s: ...` and `[kc] reconnected` mark it, the footer shows `(reconnecting)`, and `[kc] lines after <time> may be missing` says when the kubelet no longer had the lines in between. A container that restarted, stopped or was replaced during the wait ends the stream with `[kc] not reconnecting: ...`. A refused request is not retried, and after eight failed attempts in a row (about two minutes) the stream fails. JSON lines are shown as time, level, message and key=value fields, with display filters and field extraction. `F3` in a Logs panel expands its session full screen without reading the log again, and `M-m` in a merged log shows one source alone ([Logs mode](internal/help/topics/mode-logs.md)).
- **Workload actions:** entering a Deployment, StatefulSet, DaemonSet or ReplicaSet shows rows next to its child types: `Scale...` (a replica count dialog, prefilled with the current count, written through the scale subresource), `Restart...` (as `kubectl rollout restart`) and, for Deployments, `Roll back...` (pick a revision; as `kubectl rollout undo --to-revision`). Each change asks first in a red dialog that names the context, the object and the equivalent kubectl command. Read-only mode refuses them.
- **Container internals:** each pod container exposes `/logs`, a `shell` row and a `/root` browser so you can inspect container filesystems from kc. When images lack shell tooling, kc can spin up a debug helper automatically. kc lists and copies files through one `sh` per container; it closes that shell after 60 seconds without a file operation (the debug helper's idle timeout) and opens a new one on the next operation. A running copy keeps it open.
- **Merged logs:** Deployments, StatefulSets, DaemonSets, ReplicaSets and Jobs have a `logs` row, and every pod a `logs (all containers)` row. `Enter` or `F3` on it streams the logs of all matching pods and containers into one viewer, each line prefixed with a colored `[pod/container]`, like `kubectl logs -l <selector> --all-containers --prefix -f`. Pods that start or go away are followed; `[kc] +` and `[kc] -` lines mark each stream, and `[kc] ~` lines a stream that reconnects. The status line starts with the coverage, for example `incomplete: 50 streaming, 20 waiting (limit 50), 2 failed`. `M-s` opens the sources dialog: one row per source with its state and last error, a filter of `pod[/container]` globs where `-` excludes (`-/istio-proxy` drops a sidecar in every pod, `api-0 api-1` follows two pods), the number of streams at once for this view, and Retry for failed sources. New pods follow the filter, and changing it leaves the other streams and the shown lines alone.
- **Large files in the viewer:** `F3` reads a local file of up to 2 MiB, or a container file of up to 512 KiB, whole and highlights its syntax. A larger file opens in pages, as in mc's viewer: kc reads it in 256 KiB blocks around the visible lines, keeps at most 16 blocks (4 MiB) in memory, and shows plain text; the status line says so, for example `paged, 1.2 GiB, highlighting off · line 1`. A placeholder line marks a block that is still being read, and the viewer keeps answering keys meanwhile. `Home`, `End`, `PgUp` and `PgDn` work on the whole file; `End` reads the last blocks only, so the lines before it are not numbered and the position shows as a share of the bytes (`~97%`) until kc has counted them (it counts a local file in the background, never a container file). Search (`F7`) streams over the file from the current position with its progress in the footer; `Esc` cancels it, and `n` and `N` find the next and previous match. A line longer than 16 KiB is shown in pieces. A container file is read with one `dd` command per block on the container's filesystem shell, so scrolling costs no new exec request. `M-a` does not copy a paged file; `F5` saves it.
- **Local exports:** any resource that supports `F3` viewing (YAML, logs, ConfigMap/Secret data, pod filesystem files, etc.) can be saved locally via `F5`. The Copy dialog proposes a file in the directory of the other panel when it shows the local filesystem (`M-l`), and in the system temp directory otherwise; edit the path in the `to:` line. An existing file is replaced only with `Overwrite an existing file` checked. kc writes the export to a temporary file next to the destination and replaces the destination only when the whole content arrived, so a failed export leaves an existing file as it was. New files are created with mode 0600; a replaced file keeps its owner permissions and loses group and other access. kc refuses to write through a symbolic link. A pod filesystem file streams from the container whatever its size, and the export fails only when the stream stalls for 30 seconds. Binary ConfigMap and Secret keys show their size, detected content type and a hex dump in `F3`; `F5` saves the original bytes.
- **Copy between local and container folders:** `M-l` opens a local directory in the active panel (the directory of the last copy, or the working directory; `Back` walks up the tree and, from `/`, returns to the cluster). `F3` views a local text file and `F4` edits it; a directory, a binary file (one with a NUL byte), a device or pipe, or a file kc cannot read or write is refused with a message naming the reason. With a container's `/root` folder in one panel and a local directory in the other, `F5` copies the marked files and directories (or the one under the cursor) into the local directory, recursively. The dialog names the pod, container and source first; the `to:` line holds the local directory and can be edited. Entries that exist already are skipped unless `Overwrite existing entries` is checked (files are replaced, directories merged). The copy runs in the background with a file and byte count; `Esc` or `C-c` cancels it. At the end the dialog lists each entry as copied, skipped, failed or canceled. kc streams `tar` from the container like `kubectl cp` (through the debug helper, which it never adds without asking, it streams the helper's `tar` and keeps the helper alive meanwhile), and reads file by file through the filesystem session when there is no `tar`. A directory is assembled in a hidden `.<name>.kc-partial-*` directory and moved into place only when complete, so a failed or canceled entry leaves nothing behind. Names that would leave the destination (`..`, absolute paths) are refused; symbolic links, devices and pipes are listed as left out, not copied. Copies get the owner permission bits of the source only (a 0644 file becomes 0600, 0755 becomes 0700); directories are 0700. The other way round, `F5` in a local directory with a container folder in the other panel copies the marked local entries into the container. A copy into a container changes the cluster, so it asks in a red dialog that names the context, pod, container and target directory and shows the equivalent `kubectl cp` command, with `Cancel` as the default button; read-only mode refuses it. kc packs the entries into a tar stream itself and unpacks it with `tar` in the container, as `kubectl cp` does: each entry goes into a hidden `.<name>.kc-partial-*` directory next to the target first and is moved into place (or merged, for `Overwrite`) only when the whole stream arrived, so a canceled or failed entry leaves nothing that looks finished. kc never reads outside the local directory and never follows a local symbolic link: links, devices and pipes are left out and listed. Files keep their permission bits without setuid, setgid and sticky; owner and group are those of the user `tar` runs as in the container. The image needs a shell and `tar`; without `tar` the copy fails with a message. A container without a shell is written through the debug helper you already agreed to for browsing (busybox `tar`, writing under `/proc/1/root`), and kc keeps the helper alive while the copy runs.
- **Dialogs:** input and copy dialogs look and work like mc's: cyan input lines, check boxes, rules across the frame and bracketed buttons, the default one drawn `[< OK >]`. `Tab` and `S-Tab` (or the arrow keys) move between the widgets, `Space` toggles a check box, `Enter` runs the default button (or the focused one), `Esc` cancels, and `M-<letter>` picks the widget whose letter is highlighted (the letter alone works too while no input line has the focus, except in red dialogs). A click focuses an input line, toggles a check box or presses a button.
- **Config + persistence:** `~/.kc/config.yaml` controls viewer theme, panel widths, table modes, discovery TTL, mouse preferences, and more. In the option dialogs, `Enter` applies a change to the current session and `C-s` saves it as the default.

## Getting Started

### Install with Homebrew

The tap is [`stokaro/homebrew-kc`](https://github.com/stokaro/homebrew-kc). It works on macOS and Linux:

```bash
brew install --cask stokaro/kc/kc
```

The tap starts with the first release of this fork. Until then, use a source build.

### Download a release binary

Each release on the [GitHub Releases page](https://github.com/stokaro/kc/releases) has `.tar.gz` archives for Linux and macOS (amd64 and arm64) and a `kc_checksums.txt` file:

```bash
VERSION=X.Y.Z   # without the leading "v"
curl -LO "https://github.com/stokaro/kc/releases/download/v${VERSION}/kc_${VERSION}_linux_amd64.tar.gz"
curl -LO "https://github.com/stokaro/kc/releases/download/v${VERSION}/kc_checksums.txt"
shasum -a 256 --check --ignore-missing kc_checksums.txt
tar -xzf "kc_${VERSION}_linux_amd64.tar.gz" kc
./kc --version
```

The checksum file is signed with [cosign](https://docs.sigstore.dev/) by the release workflow. To check the signature, also download `kc_checksums.txt.sigstore.json` and run:

```bash
cosign verify-blob --bundle kc_checksums.txt.sigstore.json kc_checksums.txt \
  --certificate-identity-regexp '^https://github.com/stokaro/kc/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

### Build from source

kc needs Go 1.27 or newer.

```bash
git clone https://github.com/stokaro/kc.git
cd kc
go run ./cmd/kc            # run without building
go build -o kc ./cmd/kc    # or build a binary
./kc
```

Or install with Go: `go install github.com/stokaro/kc/cmd/kc@main`. The tags up to `v0.8.0` come from upstream and use its module path, so `@latest` works only once a release above `v0.8.0` exists.

### Check the installed version

`kc --version` and `kc version --client` print the kc version, the client-go version, the commit and the build date. They do not read a kubeconfig or contact a cluster. `kc version` also asks the current cluster for its version, as `kubectl version` does, and fails when it cannot reach one.

### Kubectl-style shortcuts

Kubernetes Commander accepts a subset of kubectl syntax so you can jump straight to the desired view:

- `kc get pods` – focuses the Pods list for the current/flagged namespace.
- `kc get deploy myapp -o yaml` – highlights `myapp`, opens the manifest viewer in the opposite panel.
- `kc get pods/nginx pods/api svc/frontend` – multi-selects rows across resources.
- `kc logs payments-0 -c worker --follow` – drills down to the pod → container → logs row and opens the streaming viewer.
- `kc overview`, `kc -n shop overview`, `kc -A overview` – start in the [cluster overview](#cluster-overview) of the context's namespace, of `shop`, or of all namespaces.

All commands honor `-n/--namespace`; when omitted, kc uses the kubeconfig’s default.

Startup scope flags work as in kubectl, before or after the subcommand:

- `kc --context staging` – start in the `staging` context instead of the current one. kc reads the context from the kubeconfig files it loads (`--kubeconfig`, `$KUBECONFIG` or `~/.kube`); an unknown name stops kc with an error before the UI opens. kubeconfig is not changed.
- `kc -A`, `kc --context staging -A get pods` – start both panels in the all-namespaces scope, with the Namespace column. `-A` wins over `-n`, as in kubectl.
- `kc get -A` takes types only: `kc get pods -A web` fails, like kubectl, because a name is only unique in one namespace. `kc logs` does not take `-A`.
- `--as`, `--as-group` and `--as-uid` keep working with all of these.

## Help

`F1` opens the help page of what has focus: the dialog on top (for example the investigation window, `C-x w`), the mode of the active panel, or the special folder or row under the cursor. Elsewhere it opens the index. In the help window `Tab` selects a link, `Enter` follows it, `F3` goes back, `F2` opens the index and `F10` or `Esc` closes it, returning to the dialog as you left it.

The pages are in [`internal/help/topics`](internal/help/topics/index.md) and ship inside the binary. They are the reference for each feature; this README gives a summary and links to them.

## Key Bindings

| Key        | Action                                                                          |
|------------|---------------------------------------------------------------------------------|
| `F1`       | Help for what has focus: the dialog on top, the panel mode, or the folder or row under the cursor. See [Help](#help). |
| `F2`       | User menu – the custom commands menu, as in mc. In viewers and in a Logs panel: the options dialog. |
| `F3`       | View – YAML viewer, ConfigMap/Secret key viewer, pod logs viewer, a file in a container or local panel, etc. In a Logs panel: show its stream full screen, from the lines it has. Closes the viewer again. `Enter` on a row that cannot be entered (a `logs` row, a key, a file) does the same. |
| `F4`       | Edit – launches `kubectl edit` for the selected Kubernetes object. In a local panel (`M-l`) it opens the file in `$KUBE_EDITOR`, `$EDITOR` or `vi`, the editor `kubectl edit` uses, and the editor saves it in place. |
| `F5`       | Copy – download the current YAML/log/file to the local filesystem. Between a container folder and a local panel (`M-l`), copy the marked files and directories in either direction. |
| `F7`       | Create namespace (panels) / Search dialog (viewers/logs).                       |
| `F8`       | Delete – confirms the object and its context, then deletes that instance only (UID precondition); a replaced or already deleted object is reported and left alone. In `/port-forwards` it stops the selected forward, also in read-only mode (the forward runs in kc). |
| `F9`       | Menu bar – Left, File, Command, Options, Right, as in mc (see below).            |
| `F10`      | Quit (`Esc+0` alternative; `C-q` also exits).                                |
| `M-F1`, `M-F2` | Panel options of the left or right panel: sort order, columns, non-empty only, width. |
| `Tab`      | Switch panels.                                                                  |
| `C-o`      | Toggle the integrated terminal.                                                 |
| `Ins`, `C-t` | Mark the row under the cursor and move down, as in mc. `C-a` marks every row. |
| `+`, `-`   | Select group and Unselect group, as in mc: a dialog asks for a shell pattern (`*`, `?`, `[a-z]`, matched against the whole name, case ignored) and marks or unmarks the rows whose name matches. |
| `*`        | Invert the marks of the list. `+`, `-` and `*` go to the shell line while it holds typed text. |
| `C-s`      | Quick search, as in mc: type to jump to the first row whose name starts with the text (case-insensitive); `C-s` again goes to the next match, `Backspace` removes a character, `Esc` ends the search, and any other key ends it and does its usual job (`Enter` opens the found row). Text typed while the list is still loading is kept and applied when the rows arrive. `M-s` and `Esc s` do the same. |
| `C-r`      | Reread the active panel, as in mc (Left/Right menu: Reread). A list reads its folder again: a local or container directory lists again, a list of resource types runs API discovery again (a new CRD shows at once), and an object list reads the shared cache at once, starting no new watch; the cursor stays on its row (or moves to the next one when the row is gone), marks stay on the rows still listed, and the filter and sort stay. Describe and Manifest read the object again, Info reads now. A Logs panel keeps its stream and its lines. In the `F3` viewer `C-r` reads the file or object again and keeps the scroll position; a large file shown in pages is opened again (so a file an editor replaced shows its new content), its cached blocks are dropped and the position stays, moved up when the file got shorter. In the full-screen terminal (`C-o`) `C-r` stays with the shell. |
| `M-f`      | Filter the rows of an object list (see [Filtering Rows](#filtering-rows)). `Esc f` does the same. |
| `C-z`      | Show only the rows in a fault state, or all rows again (object lists).         |
| `M-p`      | Port forward the selected pod, container or service (see below). `Esc p` does the same. |
| `C-\`      | Hotlist, as in mc: saved panel locations with their context and namespace. `Enter` goes to the highlighted entry, `Ins` adds the active panel's location, `F2` saves it as a named view (see [Saved Views](#saved-views)), `F4` stores the current settings in the highlighted view, `F6` renames, `Del` (or `F8`) removes the entry. Typing filters the list. Saved in `~/.kc/hotlist.yaml`. |
| `M-y`      | Go back in the active panel's history of visited locations (`Esc y` does the same). |
| `M-u`      | Go forward in that history (`Esc u`).                                           |
| `M-H`      | List the active panel's history, newest first; `Enter` goes there (`Esc H`).   |
| `M-o`      | Sort the object list by the next column, ascending. `Esc o` does the same. The sorted column has `↑` (ascending) or `↓` (descending) after its title. |
| `M-r`      | Reverse the sort of the object list. `Esc r` does the same. The panel options (`M-F1`, `M-F2`) set the column ("Sort by") and the direction ("Reverse") too. |
| `M-a`      | In viewers: copy the whole content. In the logs viewer: copy the shown lines.   |
| `M-A`      | In the logs viewer: copy the original records that pass the filter, unchanged.  |
| `M-v`      | In the logs viewer: switch between the formatted and the raw view.              |
| `M-g`      | In the logs viewer: open the filter dialog.                                     |
| `M-d`      | In the logs viewer: the fields of the line in focus (exact filters, follow a request, copy, trace URL, context). |
| `M-z`      | In the logs viewer: return to the filter and the line before the last `M-d` action. |
| `e`, `E`, `m`, `u`, `U`, `c` | In the logs viewer: next and previous warning or error (JSON level), mark a line, next and previous mark, copy the marked lines. See [Logs viewer](internal/help/topics/logs-viewer.md). |
| `M-s`      | In a merged logs viewer: open the sources dialog (states, source filter, stream limit, retry). |
| `M-m`      | In a merged logs viewer: show only the source of the line in focus; again to show every source. The other sources keep streaming. |
| `M-l`      | Open a local directory in the active panel, or leave it again. `Esc l` does the same. |
| `M-c`      | Copy the kubectl command shown above the shell line (the command that lists the active panel; on an overview finding, all of its input commands). `Esc c` does the same where Option does not act as Meta. Locally kc uses pbcopy, wl-copy, xclip or xsel; over SSH it sends OSC 52 (allow clipboard access in your terminal); inside tmux it also fills the tmux buffer. |
| `M-n`      | In the node list: cordon, uncordon or drain the marked nodes (see [Node Actions](#node-actions)). `Esc n` does the same. |
| `M-j`      | Owners: opens `<object>/owners` with the owner references of the selected object, the controller first with the cursor on it. Owners that no longer exist or whose kind the cluster does not serve are listed dim with the reason. `Esc j` does the same. |
| `M-b`      | Used by: opens `<object>/used-by` with the objects that use the selected one, read from the watch cache: pods using a ConfigMap, Secret, PersistentVolumeClaim or ServiceAccount (volumes, env, envFrom, imagePullSecrets, serviceAccountName) with the controllers that run them (ReplicaSet, Deployment, Job and so on, once each) and the workloads whose pod template names it, also scaled to zero, Services selecting a Pod, Ingresses routing to a Service, the claim bound to a PersistentVolume. `Esc b` does the same. |
| `M-d`      | Requires: opens `<object>/requires` with the objects the selected one names in its spec: for a Pod its ConfigMaps, Secrets, PersistentVolumeClaims, ServiceAccount and the Node it runs on; for a Deployment, StatefulSet, DaemonSet, ReplicaSet, Job or CronJob the same from its pod template; for a PersistentVolumeClaim its volume. Targets that do not exist are listed dim with the reason. `Esc d` does the same; the File menu calls it Requires. |
| `C-x C-d`  | Compare the objects under the cursors of the two panels, as mc's "Compare files". Opens a read-only unified diff of the two YAML manifests. |
| `C-x d`    | Compare what the two panels show, as mc's "Compare directories": the marked rows, else the rows a filter keeps, else the whole lists (a panel without marks or filter contributes the counterparts of the other side's rows). The same resource and API version on both sides, matched by name (by namespace and name when both list all namespaces; two different namespaces pair with each other). Lists each object as changed, only left, only right, unavailable or equal, and names what each side evaluated; Enter on a pair opens its diff. Read-only. See [Compare lists](internal/help/topics/compare-lists.md). |
| `C-x a`    | Compare the whole lists of the two panels, ignoring filters and marks. |
| `C-x c`    | Compare what the active panel shows (marked rows, else filtered rows, else the whole list) with their counterparts in another kubeconfig context, picked from a list. kc connects to that context for the read and does not switch to it; neither panel shows it. The Command menu has the whole-list variant. |
| `C-x p`    | Pin the Describe, Manifest, Logs or Info panel (the active one, else the other) to what it shows, or unpin it. See [Pin, freeze and the investigation window](#pin-freeze-and-the-investigation-window). |
| `C-x f`    | Freeze the display of that panel at the current time, or return it to live data. |
| `C-x w`    | Set the investigation window the Logs and Info panels use. |
| `C-x m`    | Show which metric sources the context has and set its Prometheus backend. |
| `M-e`      | Explain: browse the field documentation of the selected object or resource type, read from the cluster's OpenAPI v3 schema for exactly that group and version (CRDs included). See [Field documentation](#field-documentation). `Esc e` does the same. |
| `M-i`      | Open the [cluster overview](#cluster-overview) in the active panel for the namespace of the list it shows (all namespaces for a list across namespaces). `Esc` and `/..` return to the panel root. `Esc i` does the same. |
| `M-k`      | Access – show what the current identity may do with the selected object or resource type: allowed, denied or unknown for get, list, watch, create, update, patch, delete and the subresources kc uses (pod logs, exec, port-forward, attach, eviction; scale for workloads), with the reason and the matching `kubectl auth can-i` command (`Enter` copies it, `r` checks again). `Esc k` does the same. |
| `M-x`      | Shell in the container of the selected container or pod row: `kubectl exec -it` runs `bash`, or `sh` when the image has no bash. On a pod row kc takes the container named by the `kubectl.kubernetes.io/default-container` annotation, else the first one; open the pod's `/containers` folder to pick another. kc hands the terminal to kubectl and comes back when the shell ends. `Esc x` does the same. Enter on a container's `shell` row does the same. |
| `M-t`      | Attach to the main process of the selected container or pod (`kubectl attach`, with `-i` and `-t` when the container has stdin and a TTY). Keys go to that process, so ending it ends the container. `Esc t` does the same. |
| `C-c`      | Quit.                                                                           |

Shell and attach run kubectl with the panel's kubeconfig, context and impersonation (`--as`, `--as-group`, `--as-uid`), and print the equivalent kubectl command before the session starts. Ephemeral containers work the same way. When the image has no shell, kc says so; the `/root` row can still browse its files through the podfs debug helper.

Viewers show the kubectl command that prints the same content on their last line; `M-c` or the clipboard button copies it. Drag with the left mouse button to select text; releasing copies the selection, a click or `Esc` clears it.

In every menu and list dialog (the user menu `F2`, node actions, the hotlist and panel history, the context and namespace pickers, the panel and view options, the access view), the mouse wheel moves the selection up or down while the pointer is over the dialog, as in mc. A list longer than its window scrolls to keep the selection in view. The wheel never runs the selected entry or changes an option, and a wheel turned outside the dialog is ignored.

Each panel keeps a history of the last 50 locations ([Panel history](internal/help/topics/history.md)). `/owners`, `/used-by` and `/requires` list related objects ([Owners, used by and requires](internal/help/topics/related.md)). Services, pods, workloads, PersistentVolumeClaims, Ingresses and HTTPRoutes have a `/diagnosis` row ([Diagnosis](internal/help/topics/diagnosis.md)), and objects a `/timeline` row ([Object timeline](internal/help/topics/timeline.md)) and, in a namespace, an `/events` row with the Events about that object, newest first ([Object events](internal/help/topics/object-events.md)). The compare viewer is described in [Diff viewer](internal/help/topics/diff-viewer.md).

`F4`, `F8` and `F3` on container logs are grayed out when a SelfSubjectAccessReview for the current context, impersonation and namespace says the action is denied (`F4` needs get and patch, as `kubectl edit` sends a patch). Answers are cached for 30 seconds. An unknown or failed review keeps the key enabled; the API server still decides every request.

Function keys are also reachable via `Esc+<digit>` (e.g., `Esc+3` for `F3`). In viewers, `F3`, `F10` and `Esc` close the viewer; a lone `Esc` closes it once the Esc timeout expires, so `Esc+<digit>` keeps working. Unimplemented slots (`F6`) are hidden/disabled until their workflows land.

### JSON logs

The logs viewer detects JSON lines and shows them formatted. `M-g` filters by text, regular expression or field, `M-d` acts on the fields of the line in focus, and `M-v` switches to the raw view. See [Log filter and JSON logs](internal/help/topics/logs-filter.md) and [Log fields](internal/help/topics/logs-fields.md).

## Field documentation

`M-e` browses the field documentation of the selected object or resource type, read from the cluster's OpenAPI v3 schema for that group and version. See [Field documentation](internal/help/topics/explain.md).

## Menu Bar

`F9` opens the menu bar: Left, File, Command, Options, Right, as in mc. See [Menu bar](internal/help/topics/menu-bar.md).

## Port Forwarding

`M-p` forwards a local port to the selected pod, container or service; `/port-forwards` lists the forwards of this kc process. See [Port forwards](internal/help/topics/port-forward.md).

## Context and Namespace

kc works in one context at a time, shown in both panels; each panel has its own namespace scope. kc never changes the kubeconfig. See [Context and namespace](internal/help/topics/context-namespace.md).

## Filtering Rows

`M-f` filters an object list by name, label selector, namespace or column value. See [Row filter](internal/help/topics/row-filter.md).

## Saved Views

The hotlist (`C-\`) keeps panel locations; a saved view also keeps the columns, sort, grouping, row filter and faults only. See [Hotlist and saved views](internal/help/topics/hotlist.md). In `~/.kc/hotlist.yaml` a view is an entry with `name` and `view` next to the location fields:

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

`cd` on the shell line moves the active panel instead of the shell. See [Shell, terminal and attach](internal/help/topics/shell.md).

## Node Shell

A node shell runs a privileged pod on the node and opens its shell. See [Shell, terminal and attach](internal/help/topics/shell.md).

## Object Actions

Workloads have Scale, Restart and Roll back rows; CronJobs have Run now and Suspend/Resume in `F2`. Each change is confirmed in a red dialog. See [Workload actions](internal/help/topics/workload-actions.md) and [Confirmation dialog](internal/help/topics/confirm.md).

## Helm Releases

`/helm` lists the Helm releases of the namespace, read from Helm's storage Secrets, with their values, manifest, resources and history. It is read-only. See [Helm releases](internal/help/topics/helm.md).

## Cluster Overview

The first row of every panel root is `/overview`, a report of checks about the cluster and the namespace (`M-i`, or `kc overview`). See [Cluster overview](internal/help/topics/overview.md), [Needs attention](internal/help/topics/attention.md) and [Quiet checks](internal/help/topics/quiet-checks.md). The Info panel mode shows the load of the other panel's scope; see [Info mode](internal/help/topics/mode-info.md).

### Pin, freeze and the investigation window

`C-x p` pins a Describe, Manifest, Logs or Info panel to what it shows, `C-x f` freezes its display, and `C-x w` sets one time window for the Logs and Info panels. See [Pin and freeze a panel](internal/help/topics/pin-freeze.md) and [Investigation window](internal/help/topics/investigation-window.md).

## Read-only mode

In read-only mode kc refuses its own changes to the cluster. Turn it on per context, or with `kc --readonly`; `kc --write` turns it off for a run. See [Read-only mode](internal/help/topics/read-only.md).

```yaml
readOnly:
  enabled: false        # true: every context
  contexts:
    - "*prod*"
    - shared-staging
```

## Custom Commands

You can define custom shell commands in `~/.kc/config.yaml` and execute them via the **F2** user menu (also under **Command** in the `F9` menu bar). Commands run in a terminal panel and can use templates to reference the selected item.
By default, kc ships with `Top Nodes` (global) and `Top Pods` (namespace-scoped) until you define your own commands.

### Configuration Example

```yaml
commands:
  - name: "Top Pods"
    command: "kubectl top pods -n {{.Namespace}}"
    type: "namespace" # Sticky for the current namespace
    watchInterval: 5s
    showFor:
      resources: ["pods"]

  - name: "Top Nodes"
    command: "kubectl top nodes"
    type: "global" # Independent of selection
    watchInterval: 5s

  - name: "Shell ($SHELL)"
    command: "$SHELL"
    type: "global"
    interactive: true
    onExit: "restore" # Return to previous panel mode when the shell exits

  - name: "Describe Resource"
    command: "kubectl describe {{.Resource}} {{.Name}} -n {{.Namespace}}"
    type: "selector" # Reruns when selection changes
    location: "panel"
    onExit: "keep-open"

  - name: "Restart Deployment"
    command: "kubectl rollout restart deployment {{.Name}} -n {{.Namespace}}"
    type: "selector"
    showFor:
      resources: ["deployments"]
    dangerous: true # Hidden from the F2 user menu and refused in read-only mode
```

Mark every command that changes the cluster with `dangerous: true`. In read-only mode such a command is missing from the `F2` user menu, `F2` and Command > User menu are dim when nothing else applies, and its hotkey is refused. kc cannot tell what a shell command does, so a command without the flag runs in read-only mode too.

### Command Types
- **selector**: Triggered by selection, reruns on change. Good for `describe`, `get -o yaml`, etc.
- **sticky**: Starts with current selection but keeps running.
- **namespace**: Sticky for the current namespace, restarts on namespace change.
- **global**: Independent of selection. Good for cluster-wide commands like `top nodes`.

### Hotkeys, prompts and confirmation

```yaml
commands:
  - name: "Scale"
    command: "kubectl scale {{.Resource}} {{.Name}} -n {{.Namespace}} --replicas={{.Input.replicas}}"
    type: "sticky"
    showFor:
      resources: ["deployments", "statefulsets"]
    hotkey: "M-1"            # or a C-x chord: "C-x 1"
    prompts:
      - name: replicas       # read as {{.Input.replicas}}
        label: "Replicas"
        default: "1"
      - name: wait
        choices: ["no", "yes"]
    confirm: "Scale {{.Name}} to {{.Input.replicas}}?"   # or: confirm: true
```

- `hotkey` runs the command without the menu, in mc notation: `M-<key>` (Meta/Alt; `Esc <letter>` works as well), `C-M-<key>`, `F11`–`F24`, or a chord `C-x <key>` / `C-x C-<letter>`. The command must apply where the cursor is, as in the menu. kc refuses keys without a modifier, single `C-<letter>` keys, `F1`–`F10`, and keys kc uses itself (for example `M-c`, `M-d`, `M-s`, `M-w`, `M-z`, `C-x C-d`; [Custom commands](internal/help/topics/custom-commands.md#hotkeys) lists them all); bad and duplicate hotkeys are reported on the command line at startup and ignored. The menu shows each hotkey next to the command.
- `prompts` opens one dialog before the command runs (up to 8 fields). `default` may use the template variables (`"{{.Name}}-copy"`); `choices` turns a field into a list that `Left`/`Right`/`Space` cycle through. The answers are inserted as typed, so quote them in the command where the shell needs it. Commands with prompts end in `...` in the menu.
- `confirm: true`, or a message, shows the rendered command in the red confirmation dialog first. `Cancel` has the focus. A confirmation approves one run, so it cannot be combined with `type: namespace` or `watchInterval`, which rerun the command on their own; kc refuses to run such a command.

### Templating
Available variables: `{{.Name}}`, `{{.Namespace}}`, `{{.Kind}}`, `{{.Group}}`, `{{.Version}}`, `{{.Resource}}`.
The execution context is available as `{{.Context}}`, `{{.As}}`, `{{.AsUID}}` and `{{.AsGroups}}` (a list), for tools that do not read `KUBECONFIG`. Commands already run with `KUBECONFIG` set to kc's selected context and the namespace of the selected item (else the panel's), so `kubectl` needs no extra flags. Commands without a selected item are run as written, without template expansion.

A command acts on the marked rows when any are marked, otherwise on the highlighted row. `.Items` lists them in panel order, each with its own `Name`, `Namespace`, `Group`, `Version` and `Resource`; the top-level variables repeat the first item. Commands with `supportsMultiSelection: true` are offered for several marked rows; other commands are hidden from the F2 user menu then.

```yaml
  - name: "Describe marked pods"
    command: "{{range .Items}}kubectl describe {{.Resource}} {{.Name}} -n {{.Namespace}}; {{end}}"
    type: "sticky"
    supportsMultiSelection: true
    showFor:
      resources: ["pods"]
```

### Diagnostic commands

A command with a `diagnostic:` block runs a local tool once and shows its parsed output in a report. See [Diagnostic commands](internal/help/topics/diagnostics.md).

```yaml
commands:
  - name: "KRR recommendations"
    type: "sticky"
    showFor:
      resources: ["deployments", "statefulsets", "daemonsets", "namespaces"]
    hotkey: "C-x k"
    diagnostic:
      adapter: krr
      args: ["krr", "simple", "-f", "json", "--logtostderr", "-n", "{{.Namespace}}"]
      timeout: 10m      # default 5m
```

## Node Actions

In `/nodes`, `M-n` cordons, uncordons or drains the marked nodes. Before a drain, the confirmation lists the affected workloads and the PodDisruptionBudgets that cover the pods, with their allowance as read then. See [Node actions](internal/help/topics/node-actions.md).

## Configuration

`~/.kc/config.yaml` stores all user preferences. Key sections:

- `layout.menuBarVisible` – keep the menu bar titles on the top row (default `true`, as in mc); see [Menu Bar](#menu-bar).
- `layout.leftPanelMode`, `layout.rightPanelMode` – the mode each panel starts in: `list`, `describe`, `manifest`, `logs` or `info` (default left `list`, right `info`). One panel must start in `list`. kc does not save panel modes between runs, so these apply on every start; start targets such as `kc overview` or `kc get pods` open in the left panel, and `kc get TYPE NAME -o yaml` still puts the right panel in Manifest mode.
- `viewer.theme`, `viewer.mode` – chroma theme and wrap mode for YAML/text/log viewers.
- `viewer.logs.tail`, `viewer.logs.since`, `viewer.logs.timestamps` – the options a logs viewer and a Logs panel open with. `F2` in the viewer or the panel changes them there; `C-s` saves them as the defaults.
- `viewer.logs.maxRequests` – how many log streams a merged log (all pods of a workload, all containers of a pod, in a viewer or a Logs panel) runs at once (default 50, like stern); further streams wait for a free slot. `M-s` in a merged viewer changes it for that view.
- `viewer.logs.correlationFields` – the JSON fields `M-d` starts on and follows a request by, first one found (default `trace_id`, `traceId`, `traceID`, `request_id`, `requestId`, `requestID`); a name is a top-level key or a dotted path to a nested one.
- `viewer.logs.traceURL` – URL template for `M-d` `t`, for example `https://tempo.example/trace/{trace}`; empty (the default) means no trace link.
- `viewer.logs.maxLines`, `viewer.logs.maxMemoryMiB` – how many lines, and about how much memory, one logs viewer or Logs panel keeps (defaults 100000 and 64); older lines are dropped, see [JSON logs](#json-logs).
- `panel.table.mode`, `panel.scrolling.horizontal.step` – object-table rendering (`scroll`, the default, or `fit`) and panning behavior.
- `terminal.follow`, `terminal.mode` – whether the shell line follows the active panel's context and namespace (default `true`), and how its `KUBECONFIG` points there: `overlay` (default, a small file before your own kubeconfig files) or `copy` (a full copy of the file that defines the context).
- `resources.showNonEmptyOnly`, `resources.order`, `resources.favorites` – resource-group listing controls (leave `favorites` empty to follow discovery's `all` category).
- `objects.order`, `objects.columns`, `objects.groupByNamespace` – object list sort, wide/normal column display, and namespace grouping. The order is a column title in lower case with spaces as dashes (`name`, `age`, `status`, `restarts`, `nominated-node`), or `creation`; a leading `-` sorts descending. Columns sort by value: numbers, READY ratios, RESTARTS counts, ages and durations, CPU and memory quantities, and IP addresses; AGE uses the creation time. In an Events list, `last-seen` and `first-seen` use the Event's own times (`series.lastObservedTime`, `lastTimestamp`, `firstTimestamp`, `eventTime`), most recent first, and these cells count on with the clock like AGE; `name` sorts by the Event name, the wide Name column. Empty cells, `<none>` and `n/a` (no metrics) come last in both directions. Ties sort by name, then by namespace. A list without that column sorts by name. Lists that span namespaces (all namespaces, the pods of a node) are grouped by namespace first while `groupByNamespace` is true (the default); the order applies inside each namespace, and `namespace` orders the groups. With `groupByNamespace: false` the order applies to all rows at once, so `-mem` puts the pod that uses the most memory first whatever its namespace. "Group by namespace" in the Left/Right menu and in the panel options (`M-F1`, `M-F2`) switches one panel; it is checked while the list is grouped, and the sort, the filter and the namespace scope stay as they are.
- `columns` – your own columns per resource type, see [Custom Columns](#custom-columns). Empty by default.
- `health` – row health of custom resource types from their status, see [Custom Resource Health](#custom-resource-health). Empty by default.
- `kubernetes.discovery.refresh`, `kubernetes.clusters.ttl` – discovery invalidation, and how long a cluster cache stays running after the last open folder that uses it is closed.
- `input.mouse.doubleClickTimeout` – double-click threshold for Enter events.
- `nodeShell.enabled`, `nodeShell.contexts`, `nodeShell.image`, `nodeShell.namespace`, `nodeShell.maxDuration` – the node shell (off by default); see below.
- `panel.skin`, `contexts` – panel colors and per-context frame colors (see [Panel colors](#panel-colors)).
- `portForward.address` – local address port forwards listen on (default `127.0.0.1`).
- `overview.*` – thresholds of the `/overview` folder: the window for events and restarts, the restart count, requests and limits against allocatable, measured pod and node usage, the under-requested factor, containers near their memory or CPU limit, node disk and volume use, pods per node, ResourceQuota use, and the node count above which the kubelet stats are not read.
- `readOnly.enabled`, `readOnly.contexts` – contexts that open in read-only mode (see [Read-only mode](#read-only-mode)).
- `metricsHistory.window`, `metricsHistory.timeout`, `metricsHistory.backends` – past workload usage from Prometheus in the Info panel mode (off by default; see [Cluster Overview](#cluster-overview)). `C-x m` tests a backend and saves it for the current context; see [Metric sources](internal/help/topics/metric-sources.md).

Run `kc` once to materialize defaults or inspect `config-default.yaml`.

### Custom Columns

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

### Custom Resource Health

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

If the file cannot be read or parsed, kc starts with the defaults and shows the file path and the error on the command line. It never writes over that file: `C-s` is refused until the file loads again. Fix the file (or move it away to start from scratch) and press `C-s` again, or restart kc to load it.

### Full configuration (defaults + inline docs)

Copy/paste into `~/.kc/config.yaml` to start customizing:

```yaml
layout:
  # Keep the menu bar titles on the top row, as in mc. false gives the row
  # to the panels; F9 still opens the bar.
  menuBarVisible: true
  # Modes the panels start in: list, describe, manifest, logs or info.
  # One panel must start in list mode. Panel modes are not saved, so these
  # apply on every start; C-1 / C-2 switch them as usual.
  leftPanelMode: list
  rightPanelMode: info

viewer:
  theme: dracula
  mode: scroll
  logs:
    # Recent lines fetched before following; -1 fetches the whole log.
    tail: 200
    # Only lines newer than this; 0s means no limit.
    since: 0s
    # Prefix every line with its timestamp.
    timestamps: false
    # Log streams a merged logs viewer (all pods of a workload, all
    # containers of a pod) runs at once; more wait for a free slot.
    maxRequests: 50
    # Lines one logs viewer or Logs panel keeps, and their estimated memory
    # in MiB; older lines are dropped and the footer counts them.
    maxLines: 100000
    maxMemoryMiB: 64
    # JSON fields that M-d "Follow request" filters by, first one found.
    correlationFields: [trace_id, traceId, traceID, request_id, requestId, requestID]
    # URL template for the trace of a line; {trace} is the field value.
    # Empty: no trace link. Example: https://tempo.example/trace/{trace}
    traceURL: ""

panel:
  scrolling:
    horizontal:
      step: 4
  table:
    # Object tables: scroll pans across all columns, fit squeezes them into
    # the panel width.
    mode: scroll
  width:
    leftPercent: 50
    rightPercent: 50
  # Panel colors: a built-in skin (mc, dark) or ~/.kc/skins/<name>.yaml.
  skin: mc

input:
  # How long kc waits for the key after Esc (Esc c, Esc 3, Esc s). A lone
  # Esc in a viewer closes it after this time.
  escTimeout: 1s
  mouse:
    doubleClickTimeout: 300ms

kubernetes:
  clusters:
    ttl: 2m
  discovery:
    refresh: 30s

resources:
  # Default behavior: hide resources with zero objects
  showNonEmptyOnly: true
  # How often to re-peek hidden resources for changes when showNonEmptyOnly is true
  peekInterval: 10s
  # Default ordering: alphabetic by resource plural
  order: alpha
  # Favorites are used when order=favorites; leave empty to use discovery's "all" category.
  favorites: []

objects:
  # Object list sort: a column key (name, age, status, restarts, ...) or creation; "-" sorts descending
  order: name
  # Columns mode for server-side Tables:
  # - normal: show priority 0 columns (kubectl default)
  # - wide: show all server-provided columns (like `kubectl get -o wide`)
  columns: normal
  # Lists across namespaces: group the rows by namespace and sort inside each
  # one (true), or sort all rows by the sort column as one list (false)
  groupByNamespace: true

terminal:
  # The shell line (and C-o) follows the context and namespace of the active
  # panel from its next command on; false leaves its environment alone.
  follow: true
  # How the shell's KUBECONFIG reaches that context: overlay puts a small
  # file before your own kubeconfig files, copy writes a full copy of the
  # file that defines the context.
  mode: overlay

# Tint the panel frame and the context label per kubeconfig context. The
# first matching entry wins. match is a name or a glob ("*" and "?"); regex is
# a Go regular expression. Colors: ANSI names (red, bright-cyan), 0-255 or
# #rrggbb. Example:
#   contexts:
#     - match: "*prod*"
#       color: red
#     - regex: "^kind-"
#       color: green
contexts: []

nodeShell:
  # Off by default. "Start a node shell..." inside a node creates a
  # privileged pod with the host PID, network and IPC namespaces on that
  # node and opens a root shell in the host namespaces.
  enabled: false
  # Limit the node shell to these kube contexts; empty means all contexts.
  # contexts: [kind-dev]
  # The image needs sh and nsenter.
  image: docker.io/library/busybox:1.36.1
  # Namespace of the shell pod; pod security admission must allow
  # privileged pods there.
  namespace: default
  # activeDeadlineSeconds of the pod: it stops after this time even in use.
  maxDuration: 8h

portForward:
  # Local address port forwards listen on (M-p). 127.0.0.1 keeps them
  # reachable from this machine only; "localhost" listens on both loopback
  # addresses like kubectl; 0.0.0.0 exposes them to the network.
  address: 127.0.0.1

# Columns per resource type, keyed by group/version/resource (v1/pods for
# the core group). See "Custom Columns" above.
columns: {}

# Health of custom resource types, keyed by group/version/resource like
# columns. Rules are tried in order; the first that matches sets the row
# health (ok, succeeded, notReady or failed), and an object no rule matches
# stays unknown. A rule matches a status condition (condition, with status
# True by default) or a field (jsonPath, value). A status written for an
# older generation (status.observedGeneration, or the observedGeneration of
# the matched condition, below metadata.generation) turns ok into notReady.
# Types with a built-in rule (pods, nodes, claims, volumes, workloads)
# ignore their entry. Example:
#
# health:
#   kustomize.toolkit.fluxcd.io/v1/kustomizations:
#     - condition: Stalled
#       health: failed
#     - condition: Ready
#       health: ok
#     - condition: Ready
#       status: "False"
#       health: notReady
#   argoproj.io/v1alpha1/applications:
#     - jsonPath: .status.health.status
#       value: Healthy
#       health: ok
#     - jsonPath: .status.health.status
#       value: Degraded
#       health: failed
health: {}

# Object timelines (/timeline inside an object): kc records the revisions
# it observes from the shared cache while a recording runs.
timeline:
  # Revisions kept per object; the oldest are evicted first.
  maxEntries: 100
  # Bytes of revisions (as JSON) kept per object.
  maxBytes: 4194304
  # Objects recorded at once; one more stops the oldest recording.
  maxObjects: 10

overview:
  # How far back warning events and container restarts count.
  window: 1h
  # Restarts (the last one inside window) that make a pod "restarting".
  restartThreshold: 3
  # CPU and memory requests on a node, in percent of allocatable.
  requestsWarnPercent: 80
  requestsFailPercent: 95
  # Limits above this percent of allocatable mark a node overcommitted.
  limitsWarnPercent: 100
  # Pods on a node, in percent of allocatable pods.
  podsWarnPercent: 90
  # A ResourceQuota is flagged when any used value reaches this percent.
  quotaWarnPercent: 80
  # Measured CPU and memory usage (metrics.k8s.io): of the pods on a node,
  # in percent of allocatable, and of the node itself, of capacity.
  usageWarnPercent: 80
  usageFailPercent: 90
  # A node is "under-req" when its pods use at least this many times what
  # they request and reach underRequestedMinPercent of allocatable.
  underRequestedFactor: 2
  underRequestedMinPercent: 25
  # A container near its memory limit (OOM soon) or at its CPU limit
  # (throttled), in percent of the limit.
  memoryLimitWarnPercent: 90
  cpuLimitWarnPercent: 90
  # Node and image filesystem use (kubelet stats summary), in percent.
  diskWarnPercent: 80
  diskFailPercent: 90
  # A mounted PersistentVolumeClaim is flagged at this percent full.
  volumeWarnPercent: 80
  # The kubelet stats summary (one request per node) is skipped on
  # clusters with more nodes than this.
  statsMaxNodes: 50
  # A cert-manager Certificate is flagged when it expires within this many
  # days.
  certificateExpiryDays: 14
  # A pod that is not ready or pending, or a Deployment rolling out or not
  # yet available, is "starting" for this long after the API server dated
  # its state. Explicit failures never wait.
  startupGrace: 5m
  # A container flagged near its memory or CPU limit stays flagged until its
  # usage is this many points below the warning percent.
  usageHysteresisPercent: 5

# Read-only mode: kc refuses edits (F4), deletes (F8), namespace creation
# (F7), the pod filesystem debug helper and commands marked dangerous.
# --readonly turns it on for every context, --write turns it off.
readOnly:
  # true makes every context read-only.
  enabled: false
  # Context names or globs ("*" any run of characters, "?" one), for
  # example "*prod*". A pattern must match the whole name.
  contexts: []

# Past CPU and memory usage of a workload in the Info panel mode, read from
# a Prometheus-compatible server through the API server's service proxy
# with the context's own credentials (get on services/proxy). Off until a
# backend matches the context. It needs the cAdvisor container metrics and
# the kube-state-metrics owner series (kube_pod_owner and friends). Example:
#   backends:
#     - contexts: ["*prod*"]
#       namespace: monitoring
#       service: prometheus-k8s
#       port: "9090"       # number or name; empty: the only port
#       scheme: http       # or https
#       pathPrefix: ""     # e.g. /prometheus
#       labels:            # exact matchers added to every series
#         cluster: prod-eu
metricsHistory:
  # How far back the history goes (5m to 168h).
  window: 1h
  # Bound on one read of the history, all queries together.
  timeout: 15s
  backends: []
```

### Panel colors

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
  namespace: bright-cyan
```

A skin covers the two panels only; dialogs, the function key bar and the viewer keep their colors (the viewer has `viewer.theme`). An unknown skin, an unknown key or a bad color is shown as an error at startup, and kc then uses `mc`; a bad `contexts` entry is reported and skipped.

## Contributing / Source

See `CONTRIBUTING.md` and `AGENTS.md` for expectations, logging conventions, and build and test commands.
