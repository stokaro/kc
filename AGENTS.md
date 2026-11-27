# Working on kc

kc is a Go Kubernetes TUI with independent panels and MC/FAR-style interaction. Use the versions in `go.mod`; Bubble Tea, Bubbles and Lipgloss use v2 APIs.

## Load only what the task needs

Skills live in `.agents/skills/`; `.claude/skills` points to the same files. Read the matching skill before nontrivial work, then only necessary references. Skip domain skills for documentation, mechanical renames and formatting.

| Work | Skill |
| --- | --- |
| Go design, errors, ownership or concurrency | [kc-go](.agents/skills/kc-go/SKILL.md) |
| Panels, widgets, keys, layout, streams or terminal | [kc-tui](.agents/skills/kc-tui/SKILL.md) |
| Kubernetes clients, discovery, cache, scope or actions | [kc-kubernetes](.agents/skills/kc-kubernetes/SKILL.md) |
| Verifying code changes or investigating unclear failures | [kc-verification](.agents/skills/kc-verification/SKILL.md) |

Read [internal/table/AGENTS.md](internal/table/AGENTS.md) when editing the table. Search `docs/adr` for the relevant decision and follow its replacement when superseded. Locate symbols and representative tests before reading large files; do not load all ADRs.

## Invariants

- Keep blocking I/O, discovery and RESTMapper calls off `View` and the update loop. Change UI-owned state through messages; reject stale results after selection, mode or connection changes.
- Each panel owns a `panelConn`. Deferred actions retain the originating connection, regardless of the active panel. Use connection identity plus full GVR and namespace/name; preserve UID preconditions for confirmed instances.
- Reuse the cluster pool, shared caches/informers and existing refresh/stream components. Give leases, goroutines and streams explicit owners and teardown paths.
- Pass non-nil contexts through request calls. Use `t.Context()` in tests and `ctrllog.FromContext(ctx)` for logging; alias controller-runtime logging as `ctrllog`. Preserve documented framework/lifecycle ownership; do not add fallback contexts or global loggers.
- Log direct Kubernetes requests before issuing them, with GVR/scope and useful parameters, without credentials or object secrets.
- Keep defaults aligned in `pkg/appconfig.Default()`, `config-default.yaml` and `docs/configuration.md`. Keep README user-facing; maintainer release steps belong in `docs/RELEASING.md`.
- Test nontrivial behavior. A regression test must detect the original failure. Use standard `testing`; pinned lint enforces formatting, imports and suppression rules.

## Checks

Use `make check-agents` for instruction/skill changes. Select affected packages with `python3 scripts/test-changed.py --plan` or set `PACKAGES` explicitly.

- `make test-unit`: tests without envtest; `PACKAGES` defaults to `./...`.
- `make test-integration`: requires envtest and fails if it is unavailable.
- `make test-race`: focused concurrency checks without envtest; override `PACKAGES` for other affected packages.
- `make test-tui`: UI model/rendering and headless-driver unit tests, not live-cluster E2E.
- `make test-headless`: isolated envtest plus the real kc and terminal-driver binaries.
- `make test-fuzz`: bounded log formatter fuzzing.
- `make lint` and `go build ./...`: full-tree pinned lint and build before handing off code.

See [CONTRIBUTING.md](CONTRIBUTING.md) for options and [docs/agent-workflow.md](docs/agent-workflow.md) for verification, headless checks, debugging and review. Reuse results for unchanged code; report what ran and what it proves.

## Workspace and clusters

- Never run kc, kubectl, headless drivers or tests against a real cluster: any context of the user's kubeconfig, including local ones that look disposable. Tests use fakes or envtest, with child configuration isolated from the user's kubeconfig and home.
- Check live behavior envtest cannot show (running pods, logs, exec, port forwards, metrics, controllers, screenshots) on a throwaway kind cluster made by `screenshots/capture.sh`: its kubeconfig is a temporary file, every command checks its context and server, and `down` deletes it. See [throwaway kind clusters](docs/agent-workflow.md#throwaway-kind-clusters).
- Never commit kubeconfigs, credentials, secrets or generated test artifacts. Do not override `GOPATH` or `GOCACHE`.
- Preserve unrelated changes and stage intended paths explicitly; never use `git add -A`. Do not remove active Git locks or race another writer. Inspect lock ownership and use an isolated worktree when appropriate.
- Respect actual environment permissions. Do not prescribe nonexistent escalation flags or bypass a denied action.
- Keep one implementation context for ordinary tasks. Delegate independent work or substantial review with a short brief and exact paths. Avoid duplicate reviewers and broad re-review loops.
- PRs explain the problem, resulting behavior and verification. Include screenshots for visible TUI changes and a small before/after diagram for architecture or workflow changes. Keep commits focused and update relevant completed TODO entries.
