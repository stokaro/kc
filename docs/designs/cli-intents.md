# CLI Startup Intents for Kubectl-Compatible Flows

## Goals
- Treat `kc` as a drop-in replacement for common `kubectl` invocations while keeping the TUI as the primary UX.
- Reuse the existing navigation stack and viewers instead of hand-coding shortcuts per command.
- Support the first batch of CLI flows:
  - `kc get <resource>` → focus the resource list in the left panel.
  - `kc get <resource> <name>` → drill into that object (e.g. pods → containers).
  - `kc get <resource> <name> -o yaml` → mirror the object in the left panel but open the right panel’s manifest preview so users immediately see the YAML.
  - `kc get <resource>/<name>` → treat the combined syntax the same as separate arguments.
  - `kc get <resource> <name1> <name2 …>` → land on the object list and multi-select each requested object.
  - `kc get <resource1>,<resource2>` or `kc get <resource1> <resource2>` → land on the namespace resources folder and multi-select each resource group.
  - Mixed pairs like `kc get pod/foo svc/bar` → navigate to the shallowest shared folder and highlight each requested row.
  - `kc logs <pod> [-c <container>] [--follow]` → open the pod/container logs viewer.
  - `kc overview` → open `/overview` in the left panel (`docs/adr/0051-cluster-overview.md`). kubectl has no such verb; it takes `-n` and `-A` like `get`.
- Honor `-n/--namespace` everywhere a kubectl command would, falling back to the context/default namespace when omitted.
- Honor `--context` and `-A/--all-namespaces` at startup (`docs/adr/0072-startup-context-and-scope.md`). `RunConfig.Context` picks the context before kc connects; `RunConfig.AllNamespaces` starts both panels in `models.AllNamespaces`, and `kc get` resolves its type rows in that scope.

## High-Level Design
### CLI Parsing
- Keep the current global flags (`--version`, `--kubeconfig`, `--context`, `-n/--namespace`, `-A/--all-namespaces`) and introduce subcommands implemented with kong:
  - `kc get [flags] <resource> [name]`
  - `kc logs [flags] <pod>`
- Each subcommand owns the kubectl-compatible flags it needs. Unknown flags are collected for future compatibility but ignored for now.
- Parsing yields a `StartupIntent` struct:
  ```go
  type StartupIntent struct {
      Verb       KubectlVerb // e.g. VerbGet, VerbLogs
      Resource   string      // user input (pods, po, nodes,…)
      Name       string      // optional object name
      Namespace  string
      Container  string
      Follow     bool
      Output     string      // captures -o values such as "yaml"
  }
  ```
- `RunConfig` gains an optional `StartupIntent`. `cmd/kc/main.go` fills it after parsing.

### Applying the Intent
1. `ui.Run` creates the `App`, sets `app.namespaceOverride`, and copies the `StartupIntent`.
2. After `initData` finishes and `goToNamespace` wires both panels, call `app.applyStartupIntent()` on the UI goroutine using `enqueueCmd`.
3. `applyStartupIntent` performs:
   - Resource resolution: use the cluster’s RESTMapper (`ResourceFor`) so short forms like `po` work.
   - Path building: translate the intent into a `PanelPath` (the type row, then the object) and call `App.NavigatePanel`, the routine `cd` on the shell line also uses (`docs/adr/0040-cd-from-shell-line.md`). It waits up to 10 seconds for each row, because on a fresh start the root shows a type only once its emptiness is known and object rows arrive from informers (#84). Empty types the root hides are still found. The navigator stack and breadcrumbs stay consistent.
   - Panel sync: reuse the existing helper that pushes navigator state into `Panel.SetFolder`, `SetCurrentPath`, `UseFolder`, and `SelectByRowID`.
   - Verb-specific hooks (below).

### `get` Verb
- **Resource list (single target)**: if no name is provided, stop after selecting the resource group row in the namespace folder; the left panel now shows the object list.
- **Object drill-in (single target)**: when a name is present, add the object as the last path element and select it (`SelectLast`), so the cursor is on the object and F3 works.
- **Multiple names for one resource**: navigate to the object list once and multi-select all named rows. We do not auto-enter a single object because there is no unique target.
- **Multiple resources**: determine the closest shared ancestor (usually the namespace resources folder). Navigate there and multi-select the resource group rows referenced either via commas or individual arguments. Mixed `TYPE/NAME` pairs (e.g., `pod/foo svc/bar`) likewise stop at the shared level; each row is selected so function keys operate on the visible set.
- **`-o yaml`**:
  - Always drive the left panel as above so navigation stays intuitive.
  - After the navigation succeeds, set `a.activePanel = 1`, ensure the right panel is in manifest mode, and call the same helper we use today for the describe/manifest viewer (selects the object on the right and refreshes `PanelModeManifest`).
  - No modal: the manifest view auto-opens in the right panel so users can scroll immediately. When multiple objects are requested, show their selection on the left but only render the first target’s manifest on the right.

### `logs` Verb
- Validate the namespace (use default when omitted).
- Resolve the pod via RESTMapper (`pods` resource).
- Determine the target container:
  - If `-c/--container` is set, use it.
  - Otherwise fetch the Pod (`Cluster.GetByGVR`); if only one regular container exists, pick it; if multiple regular containers or only init/ephemeral containers exist, emit a toast asking for `-c`.
- Build the path:
  1. `namespaces/<ns>/pods` resource row.
  2. Pod row (`Enter=true`).
  3. Container section row (`containers` / `init` / `ephemeral`).
  4. Container row.
  5. Select the `latest` logs row (no Enter).
- After the navigation completes, enqueue `a.openViewerForPanel(leftPanel)` so the logs modal opens using the existing `LogsProvider` code. Pass `Follow` from the intent into the `LogsSpec`.

### `overview` Verb
- `KubectlVerbOverview` walks the left panel with `NavigatePanel` to the root of the intent's namespace scope (`-A`, else `-n`, else the context's namespace) and enters the `__overview__` row (`models.OverviewRowID`). `M-i` uses the same path for the active panel.

### Error Handling
- If resolution fails (unknown resource, missing pod/container, namespace not found), show a toast and keep both panels at whatever state `goToNamespace` left them in.
- On a failed or timed-out navigation the panel stays at the deepest folder reached (always a valid folder) and a toast names the missing element.
- When some targets resolve and others fail, still navigate/select the valid set and show a toast summarizing the failures.

### Testing
- CLI parsing tests under `cmd/kc` to verify flags map to `StartupIntent`.
- UI/envtest coverage:
  - `get` without name selects resource list.
  - `get` with name positions the navigator at the object.
  - `get -o yaml` switches the right panel to manifest mode.
  - `logs` opens the logs modal and chooses the default container heuristics.

### Follow-Ups
- Extend the intent system to future verbs (`describe`, `exec`, `port-forward`, etc.).
- Surface unsupported flags (`-o json`, label selectors) with clear guidance.
- Allow `kc logs pod/container` syntax akin to `kubectl logs pod container`.
