# Kubernetes Commander (kc)

[![Build Status](https://github.com/sttts/kc/actions/workflows/build.yml/badge.svg?branch=main)](https://github.com/sttts/kc/actions/workflows/build.yml)

![Kubernetes Commander TUI](docs/screenshot.png)

An experiment in building a two-panel Kubernetes TUI entirely with AI. Inspired by Midnight Commander, powered by Go 1.27, Bubble Tea v2, and controller-runtime informers.

> ⚠️ **Vibe-coded warning:** this project is 100 % AI-authored. No human has reviewed the code, so expect sharp edges, missing affordances, and sudden regressions. See `TODO.md` for the evolving plan.

## Highlights

- **Kubectl-compatible startup intents:** `kc get ...` and `kc logs ...` parse familiar CLI syntax and launch directly into the matching folders, multi-selecting objects and opening manifest/log viewers when requested (`docs/designs/cli-intents.md`).
- **Hierarchy-first navigation:** resource groups → object folders → virtual subfolders (pod containers, ConfigMap/Secret keys, etc.). Every row is keyed by full `group/version/resource`.
- **Child object hierarchies:** drill from parents into their children (e.g., Deployments → ReplicaSets → Pods, Services → EndpointSlices) via per-object child folders so related resources stay discoverable in the tree.
- **Live data via informers:** controller-runtime caches back all folders; add/update/delete events keep selections and scroll offsets stable.
- **CPU and memory usage:** when the cluster serves `metrics.k8s.io` (metrics-server), pod lists show `CPU`, `MEM`, `%CPU/R` (of requests) and `%MEM/L` (of limits), and node lists show `CPU`, `%CPU`, `MEM` and `%MEM` (of allocatable). Wide mode adds `%CPU/L` and `%MEM/R` for pods and `CPU/A` and `MEM/A` for nodes. kc reads the metrics every 15 seconds, only while such a list is on screen and no dialog is open. `n/a` means no metrics for the object yet, no request or limit to compare with, or metrics kc may not read.
- **Two synchronized panels:** each panel can render lists or manifest viewers, supports multi-selection, per-panel options (columns/order/modes), and an integrated function-key bar. A 2-line terminal lives underneath for quick kubectl work.
- **Rich viewers:** YAML/text viewer with syntax highlighting, wrap toggle, and an mc-style search dialog (`F7`: normal, regular expression or wildcard search, case sensitive, backwards, whole words; `n`/`Shift+F7` for the next match). Logs viewer streams `kubectl logs` with follow mode, search, and End-to-follow shortcuts; `F2` sets tail lines, the since window, timestamps and the previous container instance, and restarts the stream. A container that restarted also gets a `logs (previous)` row.
- **Container internals:** each pod container exposes `/logs` plus a `/root` browser so you can inspect container filesystems from kc. When images lack shell tooling, kc can spin up a debug helper automatically.
- **Local exports:** any resource that supports `F3` viewing (YAML, logs, ConfigMap/Secret data, pod filesystem files, etc.) can be saved locally via `F5` with an inline directory+path picker. kc writes the export to a temporary file next to the destination and replaces the destination only when the whole content arrived, so a failed export leaves an existing file as it was. New files are created with mode 0600; a replaced file keeps its owner permissions and loses group and other access. kc refuses to write through a symbolic link. Binary ConfigMap and Secret keys show their size, detected content type and a hex dump in `F3`; `F5` saves the original bytes.
- **Config + persistence:** `~/.kc/config.yaml` controls viewer theme, panel widths, table modes, discovery TTL, mouse preferences, and more. In the option dialogs, `Enter` applies a change to the current session and `C-s` saves it as the default.

## Getting Started

### Build and Run

```bash
go run ./cmd/kc
```

Prefer a compiled binary?

```bash
go build -o kc ./cmd/kc
./kc
```

### Install via Homebrew

```bash
brew tap sttts/homebrew-kc
# macOS (notarized GUI build)
brew install --cask sttts/homebrew-kc/kc

# Linux / CLI-only formula
brew install sttts/homebrew-kc/kc
```

## Releases

Binaries are published by the maintainers via GitHub Releases and the Homebrew tap. Push a `v*` tag to trigger the Goreleaser workflow, which builds/signed binaries, publishes the GitHub Release, notarizes the macOS artifacts, and refreshes the Homebrew tap. See `docs/RELEASING.md` for the Apple signing credentials required for CI.

### Kubectl-style shortcuts

Kubernetes Commander accepts a subset of kubectl syntax so you can jump straight to the desired view:

- `kc get pods` – focuses the Pods list for the current/flagged namespace.
- `kc get deploy myapp -o yaml` – highlights `myapp`, opens the manifest viewer in the opposite panel.
- `kc get pods/nginx pods/api svc/frontend` – multi-selects rows across resources.
- `kc logs payments-0 -c worker --follow` – drills down to the pod → container → logs row and opens the streaming viewer.

All commands honor `-n/--namespace`; when omitted, kc uses the kubeconfig’s default.

## Key Bindings

| Key        | Action                                                                          |
|------------|---------------------------------------------------------------------------------|
| `F1`       | Help – opens the README.md markdown viewer (windowed modal).                    |
| `F2`       | Options – context-aware panel options or viewer theme dialog.                   |
| `F3`       | View – YAML viewer, ConfigMap/Secret key viewer, pod logs viewer, etc. Closes the viewer again. |
| `F4`       | Edit – launches `kubectl edit` for the selected Kubernetes object.              |
| `F5`       | Copy – download the current YAML/log/log/file to the local filesystem.          |
| `F7`       | Create namespace (panels) / Search dialog (viewers/logs).                       |
| `F8`       | Delete – confirms the object and its context, then deletes that instance only (UID precondition); a replaced or already deleted object is reported and left alone. In `/port-forwards` it stops the selected forward. |
| `F9`       | Menu – Built-in object actions and custom commands.                             |
| `F10`      | Quit (`Esc+0` alternative; `C-q` also exits).                                |
| `Tab`      | Switch panels.                                                                  |
| `C-o`      | Toggle the integrated terminal.                                                 |
| `C-s`      | Quick search, as in mc: type to jump to the first row whose name starts with the text (case-insensitive); `C-s` again goes to the next match, `Backspace` removes a character, `Esc` ends the search, and any other key ends it and does its usual job (`Enter` opens the found row). `M-s` and `Esc s` do the same. |
| `M-f`      | Filter the rows of an object list (see [Filtering Rows](#filtering-rows)). `Esc f` does the same. |
| `C-z`      | Show only the rows in a fault state, or all rows again (object lists).         |
| `M-p`      | Port forward the selected pod, container or service (see below). `Esc p` does the same. |
| `C-\`      | Hotlist, as in mc: saved panel locations with their context and namespace. `Enter` goes to the highlighted entry, `Ins` adds the active panel's location, `Del` (or `F8`) removes the entry. Typing filters the list. Saved in `~/.kc/hotlist.yaml`. |
| `M-y`      | Go back in the active panel's history of visited locations (`Esc y` does the same). |
| `M-u`      | Go forward in that history (`Esc u`).                                           |
| `M-H`      | List the active panel's history, newest first; `Enter` goes there (`Esc H`).   |
| `M-o`      | Sort the object list by the next column, ascending. `Esc o` does the same. The sorted column has `↑` (ascending) or `↓` (descending) after its title. |
| `M-r`      | Reverse the sort of the object list. `Esc r` does the same. `F2` sets the column ("Sort by") and the direction ("Reverse") too. |
| `M-a`      | In viewers: copy the whole content.                                             |
| `M-c`      | Copy the kubectl command shown above the shell line (the command that lists the active panel). `Esc c` does the same where Option does not act as Meta. Locally kc uses pbcopy, wl-copy, xclip or xsel; over SSH it sends OSC 52 (allow clipboard access in your terminal); inside tmux it also fills the tmux buffer. |
| `M-j`      | Owners: opens `<object>/owners` with the owner references of the selected object, the controller first with the cursor on it. Owners that no longer exist or whose kind the cluster does not serve are listed dim with the reason. `Esc j` does the same. |
| `M-b`      | Used by: opens `<object>/used-by` with the objects that use the selected one, read from the watch cache: pods using a ConfigMap, Secret, PersistentVolumeClaim or ServiceAccount (volumes, env, envFrom, imagePullSecrets, serviceAccountName), Services selecting a Pod, Ingresses routing to a Service, the claim bound to a PersistentVolume. `Esc b` does the same. |
| `C-x C-d`  | Compare the objects under the cursors of the two panels, as mc's "Compare files". Opens a read-only unified diff of the two YAML manifests. |
| `C-c`      | Quit.                                                                           |

Viewers show the kubectl command that prints the same content on their last line; `M-c` or the clipboard button copies it. Drag with the left mouse button to select text; releasing copies the selection, a click or `Esc` clears it.

Each panel keeps its own history of the last 50 locations. A location is the context, the panel's namespace scope and the rows entered from the panel root, so going back can switch the namespace scope or, for an entry from another context, the context; a context switch puts the other panel at its root, as the context picker does. When a list is still loading, kc waits up to ten seconds for the row it needs.

The compare viewer heads the diff with both identities (context, group/version/resource, namespace, name) and the fields it left out. By default it shows the configuration view: it omits `metadata.uid`, `resourceVersion`, `generation`, `creationTimestamp`, `managedFields`, `selfLink` and `status`, and never reorders lists. `r` switches to the raw view and back, `s` adds or removes `status`, `n`/`Space`/`Enter` go to the next change and `p`/`Backspace` to the previous one (search continues with `Shift-F7`), and `C-r` reads both objects again; until then the diff stays as it was read. The last line shows the matching `diff -u <(kubectl get ...) <(kubectl get ...)` command.

Rows in `/owners` and `/used-by` behave like the same object in its own list: `Enter` opens its child folder (a Deployment's ReplicaSets, a Pod's containers), `F3` shows its YAML. They are read-only there; edit or delete an object from its own list. `/..` returns to the object.

Function keys are also reachable via `Esc+<digit>` (e.g., `Esc+3` for `F3`). In viewers, `F3`, `F10` and `Esc` close the viewer; a lone `Esc` closes it once the Esc timeout expires, so `Esc+<digit>` keeps working. Unimplemented slots (`F6`) are hidden/disabled until their workflows land.

## Port Forwarding

`M-p` (or `Esc p`) on a pod, a container of a pod, or a service opens a dialog with one field, `local:remote`, prefilled with the first declared TCP port. It takes kubectl's syntax: `8080:80`, `8080` (same port on both sides), `:80` (any free local port), and port names such as `8080:http`. The dialog lists the declared ports and shows the equivalent `kubectl port-forward` command. For a service, kc picks a running pod behind it (ready pods first) and maps the service port to its target port, as kubectl does; the forward stays on that pod.

kc forwards in-process (client-go, websocket with SPDY fallback) and listens on `127.0.0.1` unless `portForward.address` says otherwise. While forwards run, the panel root shows `/port-forwards` with their count. It lists every forward of this kc process across contexts: `F3` shows its details and kubectl command, `F8` stops it. Forwards keep running when you switch context or namespace, end with a red message when the pod goes away, and all stop when kc exits.

## Context and Namespace

Each panel has a namespace scope, shown at the left of its bottom border next to the context (`mba : default`). The panel root lists the resource types of that scope, then namespaces and the cluster-scoped types. Click the namespace to pick another one, or "All namespaces" (like `kubectl -A`). Lists across all namespaces get a Namespace column; resource counts are skipped there, because counting would watch every object in the cluster. `/namespaces/<name>` still works for browsing a namespace without changing the scope. Click the context name to switch kc to another kubeconfig context; both panels then show that cluster, starting in the context's namespace. The scope and the context are kc state only; kubeconfig is never changed.

## Filtering Rows

`M-f` opens the Filter dialog for the object list in the active panel. It takes one of:

- `nginx`: keep the rows whose name matches the regular expression (case-insensitive). In lists across all namespaces the pattern also sees `namespace/name`.
- `!nginx`: keep the rows whose name does not match.
- `-l app=web`: keep the rows whose labels match the label selector, in the syntax of `kubectl -l`. The copied kubectl command (`M-c`) gets the same `-l`.

An empty text clears the filter. `C-z`, or "Faults only" in `F2` Options, keeps only the rows in a fault state; it combines with the text filter. Faults are the rows kc colors red: pods that are Pending, Failed or Unknown, not ready, or with a container waiting in CrashLoopBackOff, ImagePullBackOff and the like; Deployments, StatefulSets and ReplicaSets with fewer ready replicas than wanted; DaemonSets with unavailable pods; failed Jobs; nodes that are not Ready; PVCs that are not Bound; failed PVs.

The active filter shows next to the scope in the bottom border (`Filter: !nginx, faults`); a click on it clears it. The filter belongs to the folder: it stays while you look at its objects and come back, and a new listing starts unfiltered. It applies to live updates, and the cursor stays on its row when the row is kept. Marked rows that the filter hides are unmarked.
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

The **F9** menu starts with built-in actions for the selected object, above the custom commands. Each action opens a red confirmation dialog that names the context, namespace and object and shows the equivalent kubectl command.

For CronJobs (`batch/v1`):

- **Run now...** creates a Job from the CronJob's job template, like `kubectl create job --from=cronjob/<name>`. The Job is named `<cronjob>-manual-<suffix>`, carries the `cronjob.kubernetes.io/instantiate: manual` annotation and is owned by the CronJob. The other panel then shows the new Job.
- **Suspend/Resume...** sets `spec.suspend`. The dialog says which of the two it does.

An action is hidden when the resource does not offer the API verb it needs (`patch` for Suspend/Resume).

## Custom Commands

You can define custom shell commands in `~/.kc/config.yaml` and execute them via the **F9** menu. Commands run in a terminal panel and can use templates to reference the selected item.
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
```

### Command Types
- **selector**: Triggered by selection, reruns on change. Good for `describe`, `get -o yaml`, etc.
- **sticky**: Starts with current selection but keeps running.
- **namespace**: Sticky for the current namespace, restarts on namespace change.
- **global**: Independent of selection. Good for cluster-wide commands like `top nodes`.

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

## Configuration

`~/.kc/config.yaml` stores all user preferences. Key sections:

- `viewer.theme`, `viewer.mode` – chroma theme and wrap mode for YAML/text/log viewers.
- `viewer.logs.tail`, `viewer.logs.since`, `viewer.logs.timestamps` – the options a logs viewer opens with. `F2` in the viewer changes them for that viewer; `C-s` there saves them as the defaults.
- `panel.table.mode`, `panel.scrolling.horizontal.step` – object-table rendering and panning behavior.
- `resources.showNonEmptyOnly`, `resources.order`, `resources.favorites` – resource-group listing controls (leave `favorites` empty to follow discovery's `all` category).
- `objects.order`, `objects.columns` – object list sort and wide/normal column display. The order is a column title in lower case with spaces as dashes (`name`, `age`, `status`, `restarts`, `nominated-node`), or `creation`; a leading `-` sorts descending. Columns sort by value: numbers, READY ratios, RESTARTS counts, ages and durations, CPU and memory quantities, and IP addresses; AGE uses the creation time. Empty cells and `<none>` come last. A list without that column sorts by name. Lists that span namespaces (all namespaces, the pods of a node) are grouped by namespace first; the order applies inside each namespace, and `namespace` orders the groups.
- `kubernetes.discovery.refresh`, `kubernetes.clusters.ttl` – discovery invalidation, and how long a cluster cache stays running after the last open folder that uses it is closed.
- `input.mouse.doubleClickTimeout` – double-click threshold for Enter events.
- `nodeShell.enabled`, `nodeShell.contexts`, `nodeShell.image`, `nodeShell.namespace`, `nodeShell.maxDuration` – the node shell (off by default); see below.
- `panel.skin`, `contexts` – panel colors and per-context frame colors (see [Panel colors](#panel-colors)).
- `portForward.address` – local address port forwards listen on (default `127.0.0.1`).

Run `kc` once to materialize defaults or inspect `config-default.yaml`.

`C-s` ("Save as defaults") in an option dialog reads the file again and writes only the settings that dialog shows, so custom commands and keys kc does not know stay as they are. The file is replaced atomically and keeps its permissions; a symbolic link is written through to its target. The command line says whether the settings were saved or apply to this session only.

If the file cannot be read or parsed, kc starts with the defaults and shows the file path and the error on the command line. It never writes over that file: `C-s` is refused until the file loads again. Fix the file (or move it away to start from scratch) and press `C-s` again, or restart kc to load it.

### Full configuration (defaults + inline docs)

Copy/paste into `~/.kc/config.yaml` to start customizing:

```yaml
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

This repo is intentionally vibe-coded. If you want to peek behind the curtain—or help civilize it—see `CONTRIBUTING.md` and `AGENTS.md` for expectations, logging conventions, and build/test commands.
