# Kubernetes Commander (kc)

[![Build Status](https://github.com/stokaro/kc/actions/workflows/build.yml/badge.svg?branch=main)](https://github.com/stokaro/kc/actions/workflows/build.yml)

> **Origin:** kc started as a fork of [Kubernetes Commander by Stefan Schimanski](https://github.com/sttts/kc) (upstream). This repository is developed independently and has diverged a lot from it: key bindings, features and internals differ. Report issues for this version at [stokaro/kc](https://github.com/stokaro/kc/issues), not upstream. See [NOTICE](NOTICE) for attribution.

![kc demo: two panels, a live pod list while scaling a Deployment, YAML and logs, copying a file out of a container, draining a node, the terminal](docs/demo.gif)

The demo was recorded before the menu bar stayed on the top row (see [Menu Bar](#menu-bar)).

A two-panel Kubernetes TUI inspired by Midnight Commander, built with Go 1.27, Bubble Tea v2 and controller-runtime informers. See `TODO.md` for the plan.

## Highlights

- **Kubectl-compatible startup intents:** `kc get ...` and `kc logs ...` parse familiar CLI syntax and launch directly into the matching folders, multi-selecting objects and opening manifest/log viewers when requested (`docs/designs/cli-intents.md`).
- **Hierarchy-first navigation:** resource groups → object folders → virtual subfolders (pod containers, ConfigMap/Secret keys, etc.). Every row is keyed by full `group/version/resource`.
- **Child object hierarchies:** drill from parents into their children (e.g., Deployments → ReplicaSets → Pods, Services → EndpointSlices, with a Service diagnosis) via per-object child folders so related resources stay discoverable in the tree.
- **Helm releases:** `/helm` lists releases with their stored values, manifest, notes, hooks and revision history, and links each manifest object to the live object (read-only, no `helm` binary needed).
- **Live data via informers:** controller-runtime caches back all folders; add/update/delete events keep selections and scroll offsets stable.
- **CPU and memory usage:** when the cluster serves `metrics.k8s.io` (metrics-server), pod lists show `CPU`, `MEM`, `%CPU/R` (of requests) and `%MEM/L` (of limits), and node lists show `CPU`, `%CPU`, `MEM` and `%MEM` (of allocatable). Wide mode adds `%CPU/L` and `%MEM/R` for pods and `CPU/A` and `MEM/A` for nodes. kc reads the metrics every 15 seconds, only while such a list is on screen and no dialog is open. `n/a` means no metrics for the object yet, no request or limit to compare with, or metrics kc may not read.
- **Two synchronized panels:** each panel can render lists, or follow the other panel's selection in Describe, Manifest, Logs or Info mode (`C-1` / `C-2` cycle the left / right panel's mode; Logs streams the selected pod's or container's log, and `F2` there sets the container, "All containers", tail, since, timestamps and previous; Info shows CPU, memory, pods and disk bars for the other panel's scope), supports multi-selection, per-panel options (columns/order/modes), and an integrated function-key bar. A 2-line terminal lives underneath for quick kubectl work.
- **Rich viewers:** YAML/text viewer with syntax highlighting, wrap toggle, and an mc-style search dialog (`F7`: normal, regular expression or wildcard search, case sensitive, backwards, whole words; `n`/`Shift+F7` for the next match). Logs viewer streams `kubectl logs` with follow mode, search, and End-to-follow shortcuts; `F2` sets tail lines, the since window, timestamps and the previous container instance, and restarts the stream. A container that restarted also gets a `logs (previous)` row. When a followed stream breaks while the container still runs (an API server or kubelet restart, a dropped connection), the viewer and the Logs panel reconnect with backoff and continue after the last line without repeating it: `[kc] reconnecting in 2s: ...` and `[kc] reconnected` mark it, the footer shows `(reconnecting)`, and `[kc] lines after <time> may be missing` says when the kubelet no longer had the lines in between. A refused request is not retried, and after eight failed attempts in a row (about two minutes) the stream fails. JSON lines are shown as time, level, message and key=value fields, with display filters and field extraction.
- **Workload actions:** entering a Deployment, StatefulSet, DaemonSet or ReplicaSet shows rows next to its child types: `Scale...` (a replica count dialog, prefilled with the current count, written through the scale subresource), `Restart...` (as `kubectl rollout restart`) and, for Deployments, `Roll back...` (pick a revision; as `kubectl rollout undo --to-revision`). Each change asks first in a red dialog that names the context, the object and the equivalent kubectl command. Read-only mode refuses them.
- **Container internals:** each pod container exposes `/logs`, a `shell` row and a `/root` browser so you can inspect container filesystems from kc. When images lack shell tooling, kc can spin up a debug helper automatically. kc lists and copies files through one `sh` per container; it closes that shell after 60 seconds without a file operation (the debug helper's idle timeout) and opens a new one on the next operation. A running copy keeps it open.
- **Merged logs:** Deployments, StatefulSets, DaemonSets, ReplicaSets and Jobs have a `logs` row, and every pod a `logs (all containers)` row. `F3` on it streams the logs of all matching pods and containers into one viewer, each line prefixed with a colored `[pod/container]`, like `kubectl logs -l <selector> --all-containers --prefix -f`. Pods that start or go away are followed; `[kc] +` and `[kc] -` lines mark each stream, and `[kc] ~` lines a stream that reconnects.
- **Local exports:** any resource that supports `F3` viewing (YAML, logs, ConfigMap/Secret data, pod filesystem files, etc.) can be saved locally via `F5`. The Copy dialog proposes a file in the directory of the other panel when it shows the local filesystem (`M-l`), and in the system temp directory otherwise; edit the path in the `to:` line. An existing file is replaced only with `Overwrite an existing file` checked. kc writes the export to a temporary file next to the destination and replaces the destination only when the whole content arrived, so a failed export leaves an existing file as it was. New files are created with mode 0600; a replaced file keeps its owner permissions and loses group and other access. kc refuses to write through a symbolic link. Binary ConfigMap and Secret keys show their size, detected content type and a hex dump in `F3`; `F5` saves the original bytes.
- **Copy between local and container folders:** `M-l` opens a local directory in the active panel (the directory of the last copy, or the working directory; `Back` walks up the tree and, from `/`, returns to the cluster). With a container's `/root` folder in one panel and a local directory in the other, `F5` copies the marked files and directories (or the one under the cursor) into the local directory, recursively. The dialog names the pod, container and source first; the `to:` line holds the local directory and can be edited. Entries that exist already are skipped unless `Overwrite existing entries` is checked (files are replaced, directories merged). The copy runs in the background with a file and byte count; `Esc` or `C-c` cancels it. At the end the dialog lists each entry as copied, skipped, failed or canceled. kc streams `tar` from the container like `kubectl cp` (through the debug helper, which it never adds without asking, it streams the helper's `tar` and keeps the helper alive meanwhile), and reads file by file through the filesystem session when there is no `tar`. A directory is assembled in a hidden `.<name>.kc-partial-*` directory and moved into place only when complete, so a failed or canceled entry leaves nothing behind. Names that would leave the destination (`..`, absolute paths) are refused; symbolic links, devices and pipes are listed as left out, not copied. Copies get the owner permission bits of the source only (a 0644 file becomes 0600, 0755 becomes 0700); directories are 0700. The other way round, `F5` in a local directory with a container folder in the other panel copies the marked local entries into the container. A copy into a container changes the cluster, so it asks in a red dialog that names the context, pod, container and target directory and shows the equivalent `kubectl cp` command, with `Cancel` as the default button; read-only mode refuses it. kc packs the entries into a tar stream itself and unpacks it with `tar` in the container, as `kubectl cp` does: each entry goes into a hidden `.<name>.kc-partial-*` directory next to the target first and is moved into place (or merged, for `Overwrite`) only when the whole stream arrived, so a canceled or failed entry leaves nothing that looks finished. kc never reads outside the local directory and never follows a local symbolic link: links, devices and pipes are left out and listed. Files keep their permission bits without setuid, setgid and sticky; owner and group are those of the user `tar` runs as in the container. The image needs a shell and `tar`; without `tar` the copy fails with a message. A container without a shell is written through the debug helper you already agreed to for browsing (busybox `tar`, writing under `/proc/1/root`), and kc keeps the helper alive while the copy runs.
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

## Key Bindings

| Key        | Action                                                                          |
|------------|---------------------------------------------------------------------------------|
| `F1`       | Help – opens the README.md markdown viewer (windowed modal).                    |
| `F2`       | User menu – the custom commands menu, as in mc. In viewers and in a Logs panel: the options dialog. |
| `F3`       | View – YAML viewer, ConfigMap/Secret key viewer, pod logs viewer, etc. Closes the viewer again. |
| `F4`       | Edit – launches `kubectl edit` for the selected Kubernetes object.              |
| `F5`       | Copy – download the current YAML/log/file to the local filesystem. Between a container folder and a local panel (`M-l`), copy the marked files and directories in either direction. |
| `F7`       | Create namespace (panels) / Search dialog (viewers/logs).                       |
| `F8`       | Delete – confirms the object and its context, then deletes that instance only (UID precondition); a replaced or already deleted object is reported and left alone. In `/port-forwards` it stops the selected forward. |
| `F9`       | Menu bar – Left, File, Command, Options, Right, as in mc (see below).            |
| `F10`      | Quit (`Esc+0` alternative; `C-q` also exits).                                |
| `M-F1`, `M-F2` | Panel options of the left or right panel: sort order, columns, non-empty only, width. |
| `Tab`      | Switch panels.                                                                  |
| `C-o`      | Toggle the integrated terminal.                                                 |
| `C-s`      | Quick search, as in mc: type to jump to the first row whose name starts with the text (case-insensitive); `C-s` again goes to the next match, `Backspace` removes a character, `Esc` ends the search, and any other key ends it and does its usual job (`Enter` opens the found row). `M-s` and `Esc s` do the same. |
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
| `M-l`      | Open a local directory in the active panel, or leave it again. `Esc l` does the same. |
| `M-c`      | Copy the kubectl command shown above the shell line (the command that lists the active panel). `Esc c` does the same where Option does not act as Meta. Locally kc uses pbcopy, wl-copy, xclip or xsel; over SSH it sends OSC 52 (allow clipboard access in your terminal); inside tmux it also fills the tmux buffer. |
| `M-n`      | In the node list: cordon, uncordon or drain the marked nodes (see [Node Actions](#node-actions)). `Esc n` does the same. |
| `M-j`      | Owners: opens `<object>/owners` with the owner references of the selected object, the controller first with the cursor on it. Owners that no longer exist or whose kind the cluster does not serve are listed dim with the reason. `Esc j` does the same. |
| `M-b`      | Used by: opens `<object>/used-by` with the objects that use the selected one, read from the watch cache: pods using a ConfigMap, Secret, PersistentVolumeClaim or ServiceAccount (volumes, env, envFrom, imagePullSecrets, serviceAccountName) with the controllers that run them (ReplicaSet, Deployment, Job and so on, once each), Services selecting a Pod, Ingresses routing to a Service, the claim bound to a PersistentVolume. `Esc b` does the same. |
| `M-d`      | Requires: opens `<object>/requires` with the objects the selected one names in its spec: for a Pod its ConfigMaps, Secrets, PersistentVolumeClaims, ServiceAccount and the Node it runs on; for a Deployment, StatefulSet, DaemonSet, ReplicaSet, Job or CronJob the same from its pod template; for a PersistentVolumeClaim its volume. Targets that do not exist are listed dim with the reason. `Esc d` does the same; the File menu calls it Requires. |
| `C-x C-d`  | Compare the objects under the cursors of the two panels, as mc's "Compare files". Opens a read-only unified diff of the two YAML manifests. |
| `M-e`      | Explain: browse the field documentation of the selected object or resource type, read from the cluster's OpenAPI v3 schema for exactly that group and version (CRDs included). See [Field documentation](#field-documentation). `Esc e` does the same. |
| `M-i`      | Open the [cluster overview](#cluster-overview) in the active panel for the namespace of the list it shows (all namespaces for a list across namespaces). `Esc` and `/..` return to the panel root. `Esc i` does the same. |
| `M-k`      | Access – show what the current identity may do with the selected object or resource type: allowed, denied or unknown for get, list, watch, create, update, patch, delete and the subresources kc uses (pod logs, exec, port-forward, attach, eviction; scale for workloads), with the reason and the matching `kubectl auth can-i` command (`Enter` copies it, `r` checks again). `Esc k` does the same. |
| `M-x`      | Shell in the container of the selected container or pod row: `kubectl exec -it` runs `bash`, or `sh` when the image has no bash. On a pod row kc takes the container named by the `kubectl.kubernetes.io/default-container` annotation, else the first one; open the pod's `/containers` folder to pick another. kc hands the terminal to kubectl and comes back when the shell ends. `Esc x` does the same. Enter on a container's `shell` row does the same. |
| `M-t`      | Attach to the main process of the selected container or pod (`kubectl attach`, with `-i` and `-t` when the container has stdin and a TTY). Keys go to that process, so ending it ends the container. `Esc t` does the same. |
| `C-c`      | Quit.                                                                           |

Shell and attach run kubectl with the panel's kubeconfig, context and impersonation (`--as`, `--as-group`, `--as-uid`), and print the equivalent kubectl command before the session starts. Ephemeral containers work the same way. When the image has no shell, kc says so; the `/root` row can still browse its files through the podfs debug helper.

Viewers show the kubectl command that prints the same content on their last line; `M-c` or the clipboard button copies it. Drag with the left mouse button to select text; releasing copies the selection, a click or `Esc` clears it.

In every menu and list dialog (the user menu `F2`, node actions, the hotlist and panel history, the context and namespace pickers, the panel and view options, the access view), the mouse wheel moves the selection up or down while the pointer is over the dialog, as in mc. A list longer than its window scrolls to keep the selection in view. The wheel never runs the selected entry or changes an option, and a wheel turned outside the dialog is ignored.

Each panel keeps its own history of the last 50 locations. A location is the context, the panel's namespace scope and the rows entered from the panel root, so going back can switch the namespace scope or, for an entry from another context, the context; a context switch puts the other panel at its root, as the context picker does. When a list is still loading, kc waits up to ten seconds for the row it needs.

The compare viewer heads the diff with both identities (context, group/version/resource, namespace, name) and the fields it left out. By default it shows the configuration view: it omits `metadata.uid`, `resourceVersion`, `generation`, `creationTimestamp`, `managedFields`, `selfLink` and `status`, and never reorders lists. `r` switches to the raw view and back, `s` adds or removes `status`, `n`/`Space`/`Enter` go to the next change and `p`/`Backspace` to the previous one (search continues with `Shift-F7`), and `C-r` reads both objects again; until then the diff stays as it was read. The last line shows the matching `diff -u <(kubectl get ...) <(kubectl get ...)` command.

Rows in `/owners`, `/used-by` and `/requires` behave like the same object in its own list: `Enter` opens its child folder (a Deployment's ReplicaSets, a Pod's containers), `F3` shows its YAML, and `M-j`, `M-b` and `M-d` work on them, so a chain can be followed one step at a time. They are read-only there; edit or delete an object from its own list. `/..` returns to the object.

The Relation column says which field links the two objects: `volumes[cfg]`, `containers[app].envFrom`, `containers[app].env[A,B]`, `spec.nodeName`, `controller of pods/web-1-abc`. A related object that cannot be opened is dim with the reason: `not found`, `not found (an object with another UID has the name)` for an owner that was deleted and created again, `unknown kind`, or `forbidden` when you may not read that type there. When you may not list a type a used-by lookup reads, a `(not listed)` row says the result is incomplete, instead of an empty folder. kc asks the API server (SelfSubjectAccessReview) before it reads a type, so a type you may read only in some namespaces is read from that namespace, and a type you may not read starts no watch.

Inside a Service, next to `/endpointslices`, `/diagnosis` checks the path from the Service to its backends. It lists a summary (type and IP families, selector, selected and ready pods, endpoint counts by ready, serving and terminating, address types, each port with its targetPort), then the findings, each followed by the objects it cites, then every backend with its evidence. Findings: no pod matches the selector (with the pods that share a selector key and their values), selected pods that are not ready (with the Ready condition and the waiting or failing containers), a named targetPort that does not resolve on some pods (checked per pod, with the protocol), selected pods with an IP that no EndpointSlice lists, endpoints whose pod is gone, replaced or no longer selected, and an IP family without a slice. ExternalName, headless, selectorless and `publishNotReadyAddresses` Services get an explanation instead of a failure. When pods or EndpointSlices cannot be read, the finding says the result is incomplete rather than reporting zero backends. The check reads only the API objects: a consistent Service does not prove that DNS, the network or the application work.

`F4`, `F8` and `F3` on container logs are grayed out when a SelfSubjectAccessReview for the current context, impersonation and namespace says the action is denied (`F4` needs get and patch, as `kubectl edit` sends a patch). Answers are cached for 30 seconds. An unknown or failed review keeps the key enabled; the API server still decides every request.

Function keys are also reachable via `Esc+<digit>` (e.g., `Esc+3` for `F3`). In viewers, `F3`, `F10` and `Esc` close the viewer; a lone `Esc` closes it once the Esc timeout expires, so `Esc+<digit>` keeps working. Unimplemented slots (`F6`) are hidden/disabled until their workflows land.

### JSON logs

The logs viewer and the Logs panel keep the most recent lines they read: up to `viewer.logs.maxLines` lines (default 100000) and about `viewer.logs.maxMemoryMiB` MiB (default 64) each. When either limit is passed, the oldest tenth goes; the footer then counts the dropped lines (`90000 lines (10000 older dropped)`), and a copy with `M-a` or `M-A` says so too. If you were reading lines that were dropped, the view moves to the oldest kept line and the footer says so until you scroll. A line longer than 256 KiB keeps its first 256 KiB and ends with `[kc] line truncated: <n> bytes, the first 262144 kept`. Lines that hold one JSON object (optionally after the `--timestamps` time) are shown formatted: time, level (colored by severity), message, then the other fields as `key=value`, with nested objects as dotted keys. Other lines, including broken JSON, are shown as they are. `M-v` switches to the raw lines and back; the filter, the search and follow mode stay.

`M-g` opens the filter. It takes space-separated terms; a line is shown when it matches every term and no `!` term. The footer shows how many lines pass.

| Term                | Matches                                                                 |
|---------------------|-------------------------------------------------------------------------|
| `timeout`           | lines that contain the text, in any case                                |
| `"conn refused"`    | lines that contain the quoted text                                      |
| `/5\d\d/`           | lines that match the regular expression (RE2)                           |
| `level=error,warn`  | JSON lines whose field equals one of the values, in any case            |
| `msg~^GET`          | JSON lines whose field matches the regular expression                   |
| `!term`             | drops the lines that match `term`                                       |
| `.msg .http.status` | shows only these fields of JSON lines (a line without them is shown whole) |

Field names are paths: `http.status`, `items[0].name`, `["key with spaces"]`; `http.status` also finds a literal `http.status` key. Field terms never match a text line. Filtering only changes what is shown: the stream is not reopened, and clearing the filter brings every line back. An invalid term is reported in the dialog. `M-a` copies the shown lines, `M-A` the original records.

In a merged log (all pods of a workload, all containers of a pod) the colored `[pod/container]` prefix is the source of the line: it stays in front in both views, the JSON after it is formatted, and `M-A` copies the records without it.

## Field documentation

`M-e` on an object row or a resource type row opens the type's schema in the same panel, at `/explain/<group>/<version>/<kind>`. The rows are the fields with their type (`string`, `[]Container`, `map[string]string`, ...), whether they are required, and the first sentence of their description. `Enter` on a `/field` row lists its nested fields; arrays and maps are crossed to their items, as in `kubectl explain`. `F3` on a field shows its full description with enum values, default, format and list type. `..` goes back, and at the top returns to the row you started from. The command line shows the matching `kubectl explain <resource>.<field> --api-version=<group>/<version>`; `M-c` copies it.

The schema comes from the connected cluster (`/openapi/v3`, the source `kubectl explain` uses) and matches the version the panel lists, so a kind that exists in two API groups shows the right one. kc keeps the last 16 group-version documents in memory; every `M-e` reads the small `/openapi/v3` index, whose hashes change with the schema, so CRD updates and other contexts show up without a restart. When the cluster publishes no schema for the type, the panel says "No documentation"; when the request fails, it says "Schema request failed" in red with the error.

## Menu Bar

The menu bar titles stay on the top row, as in mc, and the panels start under them. `F9` (or `Esc 9`, or a click on `9 PullDn`) opens the bar; a click on a title opens that menu at any time. Every entry runs an action that also has a key, and shows that key on the right, so the menus double as a key reference.

Options > Menu bar visible (or `layout.menuBarVisible: false` in the config) hides the bar and gives the row to the panels; `F9` then draws the bar over the top row only while it is open. The entry saves the setting to `~/.kc/config.yaml`. The fullscreen terminal (`C-o`) and fullscreen viewers cover the bar, as in mc.

- **Left** / **Right**: listing mode (List, Describe, Manifest, Logs, Info, and Local files, which lists the local filesystem like `M-l`; the current one is marked `*`), panel options, columns, reread, sort and reverse, group by namespace, row filter and faults only, namespace and context pickers.
- **File**: view, edit, copy, create namespace, delete, port forward, owners, used by and requires, explain, marking and selecting rows, quick search, exit.
- **Command**: the user menu (object actions and custom commands, `F2`), swap panels, switch panels on/off, copy the kubectl command, compare objects, hotlist and panel history, help.
- **Options**: panel width, the options of each panel, and whether the menu bar stays visible.

`Left`/`Right` move between menus, `Up`/`Down` between entries, `Enter` runs the entry and `Esc` closes the bar. The highlighted letter of a title opens that menu; the highlighted letter of an entry runs it (`M-<letter>` always picks a title). Entries that do not apply to the current selection are dim. With the mouse, click a title to open its menu, click an entry to run it, click anywhere else to close the bar. The mouse wheel over an open menu moves the highlight up or down, skipping separators and dim entries; it stops at the first and last entry and never runs one.

The colors are mc's default skin. While no menu is open, the titles are black on cyan with no hotkey letters. An open bar has white titles and entries on cyan with yellow hotkey letters, a white frame and separators, white on black for the open title and the selected entry, and gray for entries that do not apply.

## Port Forwarding

`M-p` (or `Esc p`) on a pod, a container of a pod, or a service opens a dialog with one field, `local:remote`, prefilled with the first declared TCP port. It takes kubectl's syntax: `8080:80`, `8080` (same port on both sides), `:80` (any free local port), and port names such as `8080:http`. The dialog lists the declared ports and shows the equivalent `kubectl port-forward` command. For a service, kc picks a running pod behind it (ready pods first) and maps the service port to its target port, as kubectl does; the forward stays on that pod.

kc forwards in-process (client-go, websocket with SPDY fallback) and listens on `127.0.0.1` unless `portForward.address` says otherwise. While forwards run, the panel root shows `/port-forwards` with their count. It lists every forward of this kc process across contexts: `F3` shows its details and kubectl command, `F8` stops it. Forwards keep running when you switch context or namespace, end with a red message when the pod goes away, and all stop when kc exits.

## Context and Namespace

Each panel has a namespace scope, shown at the left of its bottom border next to the context (`mba : default`). The panel root lists the resource types of that scope, then namespaces and the cluster-scoped types. Click the namespace to pick another one, or "All namespaces" (like `kubectl -A`). Lists across all namespaces get a Namespace column; resource counts are skipped there, because counting would watch every object in the cluster. `/namespaces/<name>` still works for browsing a namespace without changing the scope. Click the context name to switch kc to another kubeconfig context; both panels then show that cluster, starting in the context's namespace. The scope and the context are kc state only; kubeconfig is never changed.

kc works with access granted only inside namespaces (a Role and RoleBinding, no ClusterRole). Before it watches a resource type, kc asks the API server (SelfSubjectAccessReview) whether you may list and watch it in all namespaces. If not, a panel scoped to a namespace watches that type in that namespace only, and types you cannot read are hidden instead of showing errors. If you may not list namespaces, the namespace picker shows the context's namespace and the ones used in this session, and you can type a namespace name and press Enter. "All namespaces" stays available for users who may list across namespaces.

## Filtering Rows

`M-f` opens the Filter dialog for the object list in the active panel. The filter is a list of terms separated by spaces, and a row must match every term:

- `nginx`: the name matches the regular expression (case-insensitive). In lists across all namespaces the pattern also sees `namespace/name`. Spaces inside parentheses stay in the pattern: `^(web| api)`.
- `!nginx`: the name does not match.
- `-l app=web`: the labels match the label selector, in the syntax of `kubectl -l`. The selector runs as far as it parses, so `-l env in (a, b) web` is a selector and a name term. Several `-l` terms combine.
- `-n web,api`: the namespace is one of the listed ones. In a list of all namespaces this narrows the view to those namespaces and updates live; `namespace!=kube-system` leaves namespaces out.
- `<column><op><value>`: a column compares with the value. The column is named by its sort key (the title in lower case, spaces as dashes): `cpu>500m`, `mem>=1Gi`, `restarts>3`, `status=Running`, `node=worker-1`, `age<10m`. Operators are `=`, `!=`, `>`, `>=`, `<`, `<=`. `=` and `!=` take a list: `status=Pending,Failed` keeps either one, `status!=Running,Completed` keeps neither. Values compare the way the column sorts: CPU and memory as quantities, RESTARTS as counts, ages and durations as time, numbers as numbers, the rest as text in any case. Wide columns filter in normal mode too, so hiding a column does not drop its term.

A row whose cell is empty, `<none>` or `n/a` (no metrics) matches no column term, `!=` included, and a cluster-scoped row matches no namespace term: a term never makes the view wider. The dialog refuses a column the list does not have and a value the column cannot hold (`cpu>fast`). An empty text clears the filter. `C-z`, or "Faults only" in the panel options (`M-F1`, `M-F2`), keeps only the rows in a fault state; it combines with the text filter. Faults are the rows kc colors red: pods that are Pending, Failed or Unknown, not ready, or with a container waiting in CrashLoopBackOff, ImagePullBackOff and the like; Deployments, StatefulSets and ReplicaSets with fewer ready replicas than wanted; DaemonSets with unavailable pods; failed Jobs; nodes that are not Ready; PVCs that are not Bound; failed PVs.

The copied kubectl command (`M-c`) gets the label selector as `-l`, and a single `-n ns` in a list of all namespaces as `-n ns`. Terms kubectl has no flag for are listed after it, for example `kubectl get pods -A -l app=api  # kc also filters: -n prod,stage, cpu>500m, faults`.

The active filter shows next to the scope in the bottom border (`Filter: !nginx, faults`); a click on it clears it. The filter belongs to the folder: it stays while you look at its objects and come back, and a new listing starts unfiltered. It applies to live updates, and the cursor stays on its row when the row is kept. Marked rows that the filter hides are unmarked.
## Saved Views

A saved view is a named hotlist entry that also restores how the list looks. `F2` in the hotlist (`C-\`) asks for a name, such as "payments failures", and saves the active panel's location with its settings:

- the columns mode (normal or wide),
- the sort (`objects.order` syntax, e.g. `-mem`),
- "Group by namespace", for lists that span namespaces,
- the row filter text and faults only.

`Enter` on a view goes to its context, namespace scope and path, then applies the settings to that panel. Columns, sort and grouping become the panel's settings, as if set in the panel options. A view stores settings only, never listed objects or their state, so it always shows the cluster as it is now. Nothing changes a view while you browse: `F4` on a view replaces its location and settings with the active panel's, and `F6` renames it. Names are unique, in any case.

kc does not guess when a view cannot be restored. A missing context is reported and the panel stays where it is; no context with a similar name is used. When the path stops early (a resource type or namespace that is gone), the panel stays at the last step it reached and the settings are not applied. A saved filter is parsed again: an invalid one is left out with a warning, and a term on a column the list does not have is kept, so it hides every row, and reported.

In `~/.kc/hotlist.yaml` a view is an entry with `name` and `view` next to the location fields. Bookmarks have neither, so files from older kc versions load unchanged, and fields kc does not know are ignored:

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

As in mc, `cd` typed on the shell line below the panels moves the active panel instead of running in the shell:

- `cd pods`, `cd po`, `cd deploy/myapp` – a resource type (any name kubectl accepts, such as `po` or `deployments.apps`) and optionally an object. A relative path that starts with a type goes from the root when the panel is not on a type list.
- `cd /namespaces/kube-system/pods` – an absolute path, as the panel title shows it; `cd ..` goes up, `cd ./name` stays relative.
- `cd -n kube-system svc`, `cd -A pods` – change the panel's namespace scope first.
- `cd -` – back to where the panel was before the last jump; `cd` or `cd ~` – the root in the namespace kc started in.

kc waits up to 10 seconds for each part of the path to appear (lists fill from the cluster) and shows an error on the command line when it does not. Types the root hides because they are empty can still be entered this way. Only plain `cd` lines are taken over; after Tab, a cursor key or with shell syntax such as `&&` or `$`, the line goes to the shell. `builtin cd` always reaches the shell. Details: `docs/adr/0040-cd-from-shell-line.md`.
Everything kc starts runs against the context and identity the panels show: the integrated shell, `F4` (`kubectl edit`) and custom commands. `--as`, `--as-uid` and `--as-group` are applied on top of the context's own credentials (token, client certificate or exec plugin). `F4` passes them as kubectl flags. The shell and custom commands get `KUBECONFIG` pointing at a kubeconfig that kc generates in a private temp directory (mode 0600) and removes on exit. With `terminal.mode: overlay` (the default) that file only sets the current context, the namespace and, when you impersonate, the user entry, and your own kubeconfig files follow it in `KUBECONFIG`; with `terminal.mode: copy` it is a full copy of the file that defines the context. The shell keeps the same `KUBECONFIG` path for its whole life, so it follows context and namespace changes from its next command on. `terminal.follow: false` leaves the shell's environment alone.

## Node Shell

With `nodeShell.enabled: true` (optionally limited to `nodeShell.contexts`), entering a node shows `Start a node shell...` under `/pods`. `Enter` opens a red confirmation that names the context, node, namespace, pod and image and shows a similar `kubectl debug node/...` command. After `Start`, kc creates a privileged pod on the node (`nodeName`, host PID, network and IPC namespaces, tolerating every taint) and opens a root shell in the host namespaces (`nsenter -t 1 -m -u -i -n -p`) full screen. `exit` or `C-]` ends the shell; kc then deletes the pod, and also when kc quits. While the shell is open kc keeps a second exec stream as a heartbeat; if kc dies, the pod stops about a minute later, and after `nodeShell.maxDuration` in any case. A stopped pod stays as a `Completed` object; leftovers carry the label `app.kubernetes.io/component=kc-node-shell`. Pod security admission must allow privileged pods in `nodeShell.namespace` (`pod-security.kubernetes.io/enforce=privileged`); otherwise the create fails and kc shows the error.

## Object Actions

The **F2** user menu starts with built-in actions for the selected object, above the custom commands. Each action opens a red confirmation dialog that names the context, namespace and object and shows the equivalent kubectl command.

For CronJobs (`batch/v1`):

- **Run now...** creates a Job from the CronJob's job template, like `kubectl create job --from=cronjob/<name>`. The Job is named `<cronjob>-manual-<suffix>`, carries the `cronjob.kubernetes.io/instantiate: manual` annotation and is owned by the CronJob. The other panel then shows the new Job.
- **Suspend/Resume...** sets `spec.suspend`. The dialog says which of the two it does.

An action is hidden when the resource does not offer the API verb it needs (`patch` for Suspend/Resume).

## Helm Releases

`/helm`, a row at the top of the panel root (after `/overview`) and the first row of `/namespaces/<name>`, lists the Helm releases of that scope: name, namespace, newest revision, status, chart, app version and last update. kc reads the release records Helm stores as Secrets (type `helm.sh/release.v1`) from its shared cache; it does not need the `helm` binary and never changes a release. `F3` on a release shows its stored status, like `helm status`.

Enter a release for its newest revision:

- `values`, `manifest`, `notes` and `hooks` open with `F3` and save with `F5`. They are the content stored in the release record, not the live state; the viewer shows the matching `helm get` command.
- `/resources` lists the objects of the stored manifest with a Live column (`yes`, `missing`, `denied`, `type not served`). `Enter` on an object (marked `@`, like a link in mc) opens the live object in the other panel; `F3` shows the object as the release stored it.
- `/history` lists every stored revision; enter one to see its values, manifest and objects.

kc needs get, list and watch on Secrets in all namespaces for this, because its cache watches Secrets cluster-wide. Without that access `/helm` shows a row that says what is missing and the `helm list` command to use instead. Releases stored with `HELM_DRIVER=configmap` or `sql` are not shown.

## Cluster Overview

The first row of every panel root is `/overview`: one row per finding about the panel's context and namespace scope, in four sections. `M-i` opens it from any list for that list's namespace, and `kc overview` starts there. Each section header names its scope: Capacity always covers the whole cluster (`Capacity · cluster`), the other sections the namespace scope (`Health · namespace shop`), and the node and PersistentVolume checks inside them say `(cluster)`. `F3` shows the scope of each finding and how to open another namespace. Capacity shows CPU and memory per node with the same bases as the Info panel mode, for example `pods 22% req 25% lim 116% of alloc, node 40% of cap`: the measured usage of the pods on the node (PodMetrics, every namespace), their requests and their limits against allocatable, and the node's own usage (NodeMetrics, system daemons included) against capacity. Percentages are truncated, as `kubectl describe node` prints them. A node is `under-req` when its pods use well above what they request, so the scheduler thinks it is emptier than it is; usage outside pods does not count for it. A row's state is its worst node, with the line that causes it and how many nodes share it (`node full, 1 of 2 nodes`), so a healthy sum can carry one node's state; Enter lists the nodes, the worst first. F3 lists the formulas and the kubectl commands for each input. It also shows pods per node against allocatable pods, and node and image filesystem use from the kubelet stats summary. Health lists nodes that are not ready, under pressure or cordoned; pods that are not ready, pending, crash looping, failing to pull images or to start, OOMKilled or restarting; workloads that are unavailable or rolling out; failed Jobs and CronJobs; Pending or Lost claims and Released or Failed volumes. Risks lists PodDisruptionBudgets that allow no eviction, pods without requests or without a memory limit, containers near their memory limit or at their CPU limit (measured), PersistentVolumeClaims near full (kubelet stats), single-replica workloads without a PDB, ResourceQuotas near their limit, HPAs at their maximum or without metrics, and cert-manager Certificates that are not Ready or expire within `overview.certificateExpiryDays` (14 days; the row is dim when cert-manager is not installed). Activity lists warning events and pod restarts inside the window.

Each row has a value and a state, colored like rows of object lists: normal is ok, the not-ready color a warning, the failed color failed, and dim when kc cannot read the source (the reason is in `F3`, for example "metrics.k8s.io is not served by this cluster" or "forbidden: nodes/proxy"). `Enter` lists exactly the objects behind a row, with the reason for each, and `/..` returns. `Enter` on a warning event opens the object it is about in the other panel. `F3` explains how the value is computed and shows the kubectl commands that show the same inputs. Capacity rows always cover every node and every pod on it; the other rows follow the namespace scope.

The overview reads its own cluster-wide informer cache. It starts when `/overview` is opened, recomputes at most once a second after a change, stops reading once nothing has shown it for `kubernetes.clusters.ttl`, and the cache is released one TTL later. While it is open it lists NodeMetrics and PodMetrics every 15 seconds and reads `/api/v1/nodes/<node>/proxy/stats/summary` once a minute, at most five nodes at a time; that needs get on `nodes/proxy`, is skipped on clusters with more than `overview.statsMaxNodes` nodes, and stops after a round in which every node answers forbidden. A metrics list that is forbidden is asked again after five minutes instead of every 15 seconds. Thresholds are set under `overview:` in the configuration.

The Info panel mode (`C-1` / `C-2`, or Info in the `F9` panel menu) is mc's Info panel for kc: it shows bars for CPU, memory, pods and disk of the other panel's namespace scope, or of one node when the other panel's cursor is on a node, a sparkline of the measured CPU and memory usage while it is open, and the five pods that use the most memory. A label above the bars and the top-pods heading name what each covers, for example `Cluster capacity (all nodes, all namespaces)` and `Top pods by memory in namespace shop`, so an empty namespace next to busy cluster capacity is not a contradiction. Under the CPU and memory bars, each line names its base: `pods 2.3/10.4 Gi allocatable, 22%` is the usage of the pods on the nodes (PodMetrics, every namespace) against allocatable, and fills the bar; `node 6.0/14.9 Gi capacity, 40%` is the node's own usage (NodeMetrics, system daemons included) against capacity; `req 25%, lim 116% (overcommitted)` are the pods' requests and limits against allocatable. A state word follows the line it belongs to, so `overcommitted` marks the limits, not the usage. The bar and each line are colored by their own state; when another line needs attention, a mark after the percentage names it (`! lim`, `!! node`: one `!` for a warning, two for a failure), so the cause is readable without color. Without pod usage the bar shows the requests. Percentages are truncated, as `kubectl describe node` prints them. Under the top pods, `no pods in <ns>` means the scope has no pods, and `metrics unavailable: <reason>` means they are not measured. When the other panel's cursor is on a Deployment, StatefulSet, DaemonSet, ReplicaSet, Job or CronJob, the bars are about that workload's pods instead: CPU and memory usage of their limits (of their requests when a pod has no limit, `near-limit` and `under-req` flag the extremes), ready pods of the desired replicas, and its pods by memory. Pods are grouped by their controller owner references (a Deployment's pods through its ReplicaSets), not by label selectors. It reads the same data as `/overview` and stops when the panel leaves the mode. The right panel starts in Info mode; `layout.rightPanelMode: list` restores two lists (see [Configuration](#configuration)). Info needs list and watch on nodes and pods in all namespaces; kc asks the API server first (SelfSubjectAccessReview), and a user without that grant sees "not permitted to list and watch nodes and pods in all namespaces" while Info starts no informers and no metrics polls.

## Read-only mode

In read-only mode kc does not change the cluster: `F4` (edit), `F7` (create namespace) and `F8` (delete) are grayed out, the pod filesystem offers no debug helper for a container without a shell, custom commands marked `dangerous: true` are hidden from the `F9` menu, and port forwards (`M-p`), copies into a container (`F5` from a local panel), shell and attach in a container (`M-x`, `M-t`), the node shell and the CronJob actions of the `F2` menu (Run now, Suspend/Resume) are refused. Pressing a grayed key shows "Read-only mode: ... is disabled". The panel frame shows a red `RO` before the context name.

Turn it on per context in `~/.kc/config.yaml`, with exact names or globs (`*` matches any run of characters, including `/` in EKS ARNs; `?` one character):

```yaml
readOnly:
  enabled: false        # true: every context
  contexts:
    - "*prod*"
    - shared-staging
```

`kc --readonly` makes every context read-only for this run; `kc --write` ignores the config and allows writes everywhere. Without either flag the config decides. Switching the context with a click on its name applies the setting of the new context.

Read-only mode guards against accidental changes from kc's own keys. It is not a security boundary: the integrated terminal (`C-o`) and commands not marked dangerous still run whatever they run, and RBAC on the cluster stays the real limit.

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
    dangerous: true # Hidden and refused in read-only mode
```

Mark every command that changes the cluster with `dangerous: true`. kc cannot tell what a shell command does, so a command without the flag runs in read-only mode too.

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

- `hotkey` runs the command without the menu, in mc notation: `M-<key>` (Meta/Alt; `Esc <letter>` works as well), `C-M-<key>`, `F11`–`F24`, or a chord `C-x <key>` / `C-x C-<letter>`. The command must apply where the cursor is, as in the menu. kc refuses keys without a modifier, single `C-<letter>` keys, `F1`–`F10`, and keys kc uses itself (for example `M-c`, `M-s`, `M-w`, `C-x C-d`); bad and duplicate hotkeys are reported on the command line at startup and ignored. The menu shows each hotkey next to the command.
- `prompts` opens one dialog before the command runs (up to 8 fields). `default` may use the template variables (`"{{.Name}}-copy"`); `choices` turns a field into a list that `Left`/`Right`/`Space` cycle through. The answers are inserted as typed, so quote them in the command where the shell needs it. Commands with prompts end in `...` in the menu.
- `confirm: true`, or a message, shows the rendered command in the red confirmation dialog first. `Cancel` has the focus. A confirmation approves one run, so it cannot be combined with `type: namespace` or `watchInterval`, which rerun the command on their own; kc refuses to run such a command.

### Templating
Available variables: `{{.Name}}`, `{{.Namespace}}`, `{{.Kind}}`, `{{.Group}}`, `{{.Version}}`, `{{.Resource}}`.
The execution context is available as `{{.Context}}`, `{{.As}}`, `{{.AsUID}}` and `{{.AsGroups}}` (a list), for tools that do not read `KUBECONFIG`. Commands already run with `KUBECONFIG` set to kc's selected context and the namespace of the selected item (else the panel's), so `kubectl` needs no extra flags. Commands without a selected item are run as written, without template expansion.

A command acts on the marked rows when any are marked, otherwise on the highlighted row. `.Items` lists them in panel order, each with its own `Name`, `Namespace`, `Group`, `Version` and `Resource`; the top-level variables repeat the first item. Commands with `supportsMultiSelection: true` are offered for several marked rows; other commands are hidden from the F9 menu then.

```yaml
  - name: "Describe marked pods"
    command: "{{range .Items}}kubectl describe {{.Resource}} {{.Name}} -n {{.Namespace}}; {{end}}"
    type: "sticky"
    supportsMultiSelection: true
    showFor:
      resources: ["pods"]
```

## Node Actions

In the node list (`/nodes`), `M-n` (or `Esc n`) opens a menu for the marked nodes, or the node under the cursor when none is marked:

- **Cordon...** and **Uncordon...** set or clear `spec.unschedulable`, like `kubectl cordon` and `kubectl uncordon`. Nodes that are already in that state are left alone.
- **Drain...** asks for the grace period (`-1` uses each pod's own), a timeout per node (`0` waits as long as it takes), and whether to skip DaemonSet pods, delete emptyDir data and evict pods without a controller (`--force`). kc then reads the nodes' pods and refuses at once, changing nothing, when kubectl would refuse them. Otherwise the red dialog shows what the drain does and the equivalent `kubectl drain` command.

A drain cordons all the nodes first and then evicts their pods node by node through the Eviction API, which respects PodDisruptionBudgets. Mirror pods and, when chosen, DaemonSet pods stay. A dialog shows per node the pods evicted and remaining and the pods a budget refuses; those are asked again every five seconds. `Esc` cancels the drain; the nodes stay cordoned. Cordoned nodes are shown in red, like nodes that are not ready.

Read-only mode refuses `M-n` before the menu opens.

## Configuration

`~/.kc/config.yaml` stores all user preferences. Key sections:

- `layout.menuBarVisible` – keep the menu bar titles on the top row (default `true`, as in mc); see [Menu Bar](#menu-bar).
- `layout.leftPanelMode`, `layout.rightPanelMode` – the mode each panel starts in: `list`, `describe`, `manifest`, `logs` or `info` (default left `list`, right `info`). One panel must start in `list`. kc does not save panel modes between runs, so these apply on every start; start targets such as `kc overview` or `kc get pods` open in the left panel, and `kc get TYPE NAME -o yaml` still puts the right panel in Manifest mode.
- `viewer.theme`, `viewer.mode` – chroma theme and wrap mode for YAML/text/log viewers.
- `viewer.logs.tail`, `viewer.logs.since`, `viewer.logs.timestamps` – the options a logs viewer and a Logs panel open with. `F2` in the viewer or the panel changes them there; `C-s` saves them as the defaults.
- `viewer.logs.maxRequests` – how many log streams a merged log (all pods of a workload, all containers of a pod, in a viewer or a Logs panel) runs at once (default 50, like stern); further streams wait for a free slot.
- `viewer.logs.maxLines`, `viewer.logs.maxMemoryMiB` – how many lines, and about how much memory, one logs viewer or Logs panel keeps (defaults 100000 and 64); older lines are dropped, see [JSON logs](#json-logs).
- `panel.table.mode`, `panel.scrolling.horizontal.step` – object-table rendering and panning behavior.
- `resources.showNonEmptyOnly`, `resources.order`, `resources.favorites` – resource-group listing controls (leave `favorites` empty to follow discovery's `all` category).
- `objects.order`, `objects.columns`, `objects.groupByNamespace` – object list sort, wide/normal column display, and namespace grouping. The order is a column title in lower case with spaces as dashes (`name`, `age`, `status`, `restarts`, `nominated-node`), or `creation`; a leading `-` sorts descending. Columns sort by value: numbers, READY ratios, RESTARTS counts, ages and durations, CPU and memory quantities, and IP addresses; AGE uses the creation time. Empty cells, `<none>` and `n/a` (no metrics) come last in both directions. Ties sort by name, then by namespace. A list without that column sorts by name. Lists that span namespaces (all namespaces, the pods of a node) are grouped by namespace first while `groupByNamespace` is true (the default); the order applies inside each namespace, and `namespace` orders the groups. With `groupByNamespace: false` the order applies to all rows at once, so `-mem` puts the pod that uses the most memory first whatever its namespace. "Group by namespace" in the Left/Right menu and in the panel options (`M-F1`, `M-F2`) switches one panel; it is checked while the list is grouped, and the sort, the filter and the namespace scope stay as they are.
- `columns` – your own columns per resource type, see [Custom Columns](#custom-columns). Empty by default.
- `kubernetes.discovery.refresh`, `kubernetes.clusters.ttl` – discovery invalidation, and how long a cluster cache stays running after the last open folder that uses it is closed.
- `input.mouse.doubleClickTimeout` – double-click threshold for Enter events.
- `nodeShell.enabled`, `nodeShell.contexts`, `nodeShell.image`, `nodeShell.namespace`, `nodeShell.maxDuration` – the node shell (off by default); see below.
- `panel.skin`, `contexts` – panel colors and per-context frame colors (see [Panel colors](#panel-colors)).
- `portForward.address` – local address port forwards listen on (default `127.0.0.1`).
- `overview.*` – thresholds of the `/overview` folder: the window for events and restarts, the restart count, requests and limits against allocatable, measured pod and node usage, the under-requested factor, containers near their memory or CPU limit, node disk and volume use, pods per node, ResourceQuota use, and the node count above which the kubelet stats are not read.
- `readOnly.enabled`, `readOnly.contexts` – contexts that open in read-only mode (see [Read-only mode](#read-only-mode)).

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

panel:
  scrolling:
    horizontal:
      step: 4
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

# Read-only mode: kc refuses edits (F4), deletes (F8), namespace creation
# (F7), the pod filesystem debug helper and commands marked dangerous.
# --readonly turns it on for every context, --write turns it off.
readOnly:
  # true makes every context read-only.
  enabled: false
  # Context names or globs ("*" any run of characters, "?" one), for
  # example "*prod*". A pattern must match the whole name.
  contexts: []
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
