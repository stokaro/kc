# Repository Guidelines

## Dependencies Policy (Important)
- Only use Bubble Tea v2: import `charm.land/bubbletea/v2` everywhere (including tests). Do NOT import `charm.land/bubbletea` without `/v2`.
- Keep module paths consistent with Go’s major version semantics. If a module ships a v2+, the import path must include the `/vN` suffix.
- If you see mixed v0/v1 vs v2 imports, fix them immediately and run `go mod tidy`.
- Example:
  - Correct: `tea "charm.land/bubbletea/v2"`
  - Incorrect: `tea "charm.land/bubbletea"` (will break types between v1/v2)

## Architecture & Project Structure

The application is a **Kubernetes TUI** built with **Go 1.27** and **Bubble Tea v2**, structured around a hierarchical navigation model where resources are treated as "folders" and "files".

### Core Components

1.  **Entry Point (`cmd/kc`)**:
    -   Uses `kong` for CLI argument parsing.
    -   Sets up `controller-runtime` logging (zap).
    -   Initializes the UI via `ui.Run`.

2.  **UI Core (`internal/ui`)**:
    -   **App Model**: The central Bubble Tea model (`App` struct in `app.go`). It manages global state: two panels (`leftPanel`, `rightPanel`), an integrated terminal, and modal overlays.
    -   **Panels**: Each `Panel` is a self-contained component that displays either a list of items (a "Folder") or a content viewer (YAML, logs).
    -   **Navigator**: (`internal/navigation`) Manages the navigation stack for each panel. It treats navigation as a stack of `Folder` objects, supporting "enter" (push) and "back" (pop) operations.

3.  **Data Layer (`internal/tablecache`, `internal/cluster`)**:
    -   **Live Data**: Uses **Informers** (via `controller-runtime`) to maintain a live cache of resources.
    -   **Table Cache**: Decorates standard K8s clients to produce "Table" views (similar to `kubectl get`). It watches for changes and updates the UI in real-time.
    -   **Cluster Pool**: Manages connections to Kubernetes clusters.

4.  **Abstraction (`internal/models`)**:
    -   **Folder Interface**: The core abstraction. A `Folder` provides a list of `Item`s.
    -   **Enterable Interface**: Items that can be "entered" (like a Namespace or a Pod) implement this interface to return a new `Folder`.
    -   **Universal Navigation**: Allows uniform navigation through Contexts → Namespaces → Resource Groups → Resources → Objects → Sub-resources.

### Key Characteristics
-   **Server-Side Rendering**: Relies on server-side printing (Table output) from Kubernetes to determine how to display resources, ensuring compatibility with CRDs.

### Directory Layout
-   `cmd/kc/`: Application entrypoint.
-   `internal/ui/`: TUI components (App, Panel, Terminal).
-   `internal/models/`: Core interfaces (Folder, Item, Enterable).
-   `internal/tablecache/`: Live data caching and Table view materialization.
-   `internal/cluster/`: Kubeconfig and client pool management.
-   `internal/navigation/`: Stack-based navigation logic.
-   `pkg/`: Reusable helpers (config, etc.).
-   `examples/`: Minimal runnable samples.

## Build, Test, and Development Commands
- Build binary: `go build -o kc ./cmd/kc`
- Run binary: `./kc`
- Run without building: `go run ./cmd/kc`
- Run examples: `go run examples/handler/main.go`
- Headless TUI driver: `go run ./cmd/bubbleheadless -- go run ./cmd/kc`
- All tests (verbose): `go test ./... -v`
- Envtest suites need control-plane binaries: `export KUBEBUILDER_ASSETS="$(setup-envtest use -p path 1.37.x)"`.
- Run everything as CI does (an envtest startup failure fails the run): `KC_REQUIRE_ENVTEST=1 go test ./...`
- Run only the tests that do not need envtest: `KC_SKIP_ENVTEST=1 go test ./...`
- With neither variable, envtest tests skip when the control plane cannot start. New envtest tests start it through `internal/testing/kcenvtest` (`kcenvtest.Main` in `TestMain` plus `kcenvtest.Require`, or `kcenvtest.Start` per test), never by calling `envtest.Environment.Start` directly (`kcenvtest.StartEnv` takes a configured `envtest.Environment`, for example with CRDs). See `docs/adr/0060-require-envtest-in-ci.md`.
- Prefer the package control plane from `kcenvtest.Main`; start one per test only when the test changes cluster-wide state (CRDs, served kinds) that other tests must not see. On a shared control plane, name namespaces, impersonated users and other cluster-scoped objects with `kctesting.UniqueName`, so tests pass in parallel and with `-count=3`. Start clusters with `kctesting.RunInBackground(t, "cluster", cl.Start)` rather than `go cl.Start(ctx)`. See `docs/adr/0244-envtest-under-load.md`.
- After a test creates a Role, ClusterRole or binding that grants access, wait until the grant is in force with `kctesting.WaitForAccess` before kc reads access. The API server's RBAC authorizer reads grants from its own informers, and kc caches access answers per cluster, so a denial read in that gap sticks for the whole test. Wait for the verbs the test relies on; a grant the test expects to refuse needs no wait.
- With coverage: `go test ./... -cover`
- Static checks: `go vet ./...`
- Lint: `make lint` (golangci-lint and nolintguard, pinned in the `go.mod` tool block). It checks the whole tree and must report 0 issues. `make lint-fix` applies automatic fixes. Config: `.golangci.yml`; decisions: `docs/adr/0118-lint-setup.md`.
- Tidy modules (after dep changes): `go mod tidy`

### Headless Bubbleterm Wrapper
- `cmd/bubbleheadless` is a REPL-style driver around bubbleterm for end-to-end automation in non-interactive environments.
- Launch it with `go run ./cmd/bubbleheadless -- <app command...>`; e.g. `go run ./cmd/bubbleheadless -- go run ./cmd/kc`.
- Once started, send commands over stdin:
  - `key <token>` / `text <string>` to feed keystrokes,
  - `mouse <button> <x> <y> <press|release|motion>` for mouse events,
  - `screen [ansi|plain]` to dump the current framebuffer,
  - `snapshot <file.png>` to write a PNG render of the terminal (plain text; `snapshot <file.png> color` keeps the colors, draws box lines and the Kitty graphics images the child sent, as used by `docs/demo/record.sh` and `screenshots/capture.sh`),
  - `images` to list the Kitty graphics images the child sent, with `shown` for those on the screen and `scaled` for those not sized to the snapshot's cells,
  - `caption <text>` to draw a caption box over the following color snapshots (a bare `caption` clears it) and `caption-at <top|bottom>` to move it (bottom by default, over the shell line and function-key bar),
  - `resize <cols> <rows>` and `wait` / `exit` for session control.
- The child does not see the outer terminal's `TERM_PROGRAM`, `LC_TERMINAL`, `KITTY_WINDOW_ID` or `TMUX`, so kc's `charts.graphics: auto` draws text charts there. With `charts.graphics: kitty` kc sends its chart images, and color snapshots draw them as a terminal with Kitty Unicode placeholders does; the driver answers kc's cell size query with the snapshot's 9x18 pixel cells. See `docs/adr/0348-bubbleheadless-kitty-graphics.md`.
- Designed for CI/e2e test harnesses—script it from shell pipelines or expect-like drivers without allocating a PTY.
- Run it only against an envtest kubeconfig; see "No Live Clusters" below.

## Coding Style & Naming Conventions
- Format: `go fmt ./...` (CI expects gofmt-clean code).
- Lint mindset: prefer small packages, clear interfaces, early returns.
- `make lint` enforces several rules from this file: Bubble Tea and Bubbles v2 imports only, no `io/ioutil`, `ctrllog` as the alias for `sigs.k8s.io/controller-runtime/pkg/log`, no global controller-runtime logger outside `cmd/kc`, no `context.Background()`/`context.TODO()` in tests. Every `//nolint` must name a linter and give a reason (`//nolint:unused // why`); gosec and revive suppressions use `// #nosec G204 -- why` and `//revive:disable-next-line:<rule> why` instead.
- Naming: package names lower-case, no underscores; exported identifiers `CamelCase`, unexported `camelCase`.
- Errors: wrap with `%w` (e.g., `fmt.Errorf("reading config: %w", err)`).
- Files: group closely related types; avoid large god files.

## Logging Guidelines
- Always import controller-runtime logging as `ctrllog` (e.g., `ctrllog "sigs.k8s.io/controller-runtime/pkg/log"`).
- Thread a `context.Context` through the call chain and retrieve loggers with `ctrllog.FromContext(ctx)`; never grab the global logger directly.
- Name the logger variable `log`, and derive new loggers with `log := ctrllog.FromContext(ctx).WithName("component")` before use.
- Treat `Deps.Ctx` as the long-lived root context (e.g., for informers). For request-scoped or short-lived work, derive a child context from it and pass that down. Accessing `Deps.Ctx` directly should be the exception, not the default.
- Never store `context.Context` on structs; pass it explicitly through call chains. The sole allowed long-lived context is `Deps.Ctx` for cluster/informer lifecycle management.
- In Go tests, use `t.Context()` to derive request-scoped contexts instead of `context.Background()` or `context.TODO()`.
- `context.Context` must never be nil; do not check for nil and do not create fallbacks. Always thread a real, non-nil context from the caller.
- Every real client request (non-cache/informer) must log an INFO line before issuing the request (e.g., REST/dynamic/client-go List/Get/Delete/Watch), including GVR/namespace and key parameters, so DEBUG=1 traces outbound calls.

## Abstraction Guidelines
- Prefer composition over wrapping: do not re-invent controller-runtime/client-go abstractions. Embed or compose original types instead of creating near-duplicates.
- Use Kubernetes/client-go types and `controller-runtime` primitives directly (clients, caches, informers, RESTMapper). Add small adapters only where strictly needed.
- Use the controller-runtime shared cache/informers from the Cluster pool; do not start ad-hoc dynamic watches or bespoke informers. All watch/list refreshes must go through the shared cache.

### Identity & Keys (Kubernetes)
- Always use the full GroupVersionResource (GVR) when constructing keys/IDs for folders, items, history, or selection state. The resource name alone is NOT unique across groups and versions.
- Prefer `gvr.GroupVersion().String()` when composing strings; for core resources this yields `v1` and for others `group/version`.
- Examples:
  - Folder keys: `namespaces/<ns>/<group>/<version>/<resource>` (namespaced) or `<group>/<version>/<resource>` (cluster-scoped).
  - Row IDs: `group/version/resource` (not just `resource`).
- Avoid ad-hoc Kind/Version strings for identity; use GVR consistently.

### Config Defaults Consistency
- Keep configuration defaults in sync across:
  - Code: `pkg/appconfig.Default()`
  - Documentation sample: `config-default.yaml` in the repo root
  - README “Configuration” section
- A unit test (`pkg/appconfig/defaults_test.go`) loads `config-default.yaml` and compares it with `Default()`. Update both when changing defaults; do not let them drift.

## Documentation Scope
- Treat `README.md` as purely user-facing; do not direct users to run releases. Release steps are maintainer-only and covered in `docs/RELEASING.md`.

## Testing Guidelines
- Framework: standard `testing` with table-driven tests and `t.Run` subtests.
- Location: `*_test.go` alongside sources (e.g., `pkg/handlers/handler_test.go`).
- Policy: non-trivial logic MUST be covered by unit tests.
- Scope: prioritize `pkg/handlers`, `pkg/kubeconfig`, and `internal/ui` model logic; keep tests deterministic (no live clusters; see "No Live Clusters").
- Commands: `go test ./pkg/handlers/... -v`, `go test ./internal/ui/... -v`.

## Commit & Pull Request Guidelines
- Commit style (observed): short, imperative subjects (e.g., "Add cmd/kc", "Update README").
- Commit in logical pieces; do not mix unrelated changes. Use partial staging (e.g., `git add -p`) to craft focused commits.
- Recommended: optional scope prefixes (e.g., `handlers: add pod logs`) and meaningful bodies when needed.
- PRs must include: concise summary, rationale, test plan/commands, linked issues, and screenshots/GIFs for TUI changes.
- Keep changes focused; include or update tests and docs relevant to your change.

### Build & Test Before Commit
- Build and test when you changed code or tests. At minimum: `go build ./...`, `go test ./...` and `make lint` (or the impacted packages when the full suite is costly). Do not commit if build or tests fail.
- Repeated/fixup commits without new code changes don’t need another build/test cycle.
- Docs-only commits do not require build/test.
- Before committing, run `git status` to ensure there are no unintended or forgotten changes; stage only what belongs in the focused commit.

### Environment Variables (Go builds)
- Never set `GOPATH` or `GOCACHE` in commands or CI. Use the default environment so Go’s module and toolchain behavior works as intended.

See `TODO.md` for the active development plan and current tasks. Keep every TODO entry prefixed with `- [ ]` / `- [x]` and check off items as soon as they are completed.

### Git Hygiene
- If `index.lock` exists or a commit fails due to a lock, stop and escalate to the user. Do not delete or retry until the user explicitly clears or approves a retried command.

### Permission Issues
- Do not override default Go/module cache locations.
- If a command fails due to permissions or sandboxing, immediately rerun it with `with_escalated_permissions: true` and a short justification (do not pause to ask first). Use escalation rather than changing cache locations.
- Apply the same rule to git permission/lock errors (e.g., `index.lock`): rerun with escalation rather than waiting for additional approval.
- Never use `git add -A`; stage only intended files.

## No Live Clusters (Important)
- Never run kc, kubectl, `cmd/bubbleheadless` or any test against a real cluster. This covers every context in `~/.kube/config`, including local ones that look disposable. A real cluster runs real workloads.
- Test with envtest (`internal/testing/kcenvtest`, assets from `setup-envtest`) or fake clients only.
- For a manual or headless TUI check, start an envtest API server, write its kubeconfig to a temporary file, and run kc with `KUBECONFIG=<that file>` and `HOME=<temporary dir>`. Never point kc at the default `~/.kube/config` or a copy of a real kubeconfig, and never pass `--context` with a real context name.
- Read-only checks on a real cluster are not allowed either. kc starts in the kubeconfig's current context, and a scripted key sequence can land on any row: F8 then Enter deletes whatever is under the cursor.
- If something cannot be checked without a real cluster, say so in the PR and leave it unchecked.

## Security & Configuration Tips
- Never commit kubeconfigs, cluster credentials, or secrets.
- Keep tests deterministic and cluster-free; abstract clients to allow fakes/mocks.
