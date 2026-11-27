# Contributing to kc

kc is developed independently from its original [sttts/kc](https://github.com/sttts/kc) prototype. Open issues and pull requests in [stokaro/kc](https://github.com/stokaro/kc).

## Environment

Use the Go version declared in `go.mod`. Python 3.9 or later is needed only for agent-file checks and the changed-package selector. Lint tools are pinned in the module's `tool` block and run through `go tool`; do not substitute a globally installed linter.

Read [AGENTS.md](AGENTS.md) for repository invariants. The four skills in `.agents/skills` load by task; `.claude/skills` links to the same directory, and `CLAUDE.md` imports the shared instructions. No global plugin installation or Superpowers hook is required. Without Git symlink support, read the canonical paths from `AGENTS.md`.

## Select checks by the change

| Command | Coverage |
| --- | --- |
| `make check-agents` | Skill metadata, local links, discovery paths, context budgets and verification-tool tests |
| `make test-unit` | All unit tests; envtest suites explicitly skip |
| `make test-integration` | All tests, with envtest required |
| `make test-race` | Concurrency-sensitive packages under the race detector; envtest skipped |
| `make test-tui` | UI state/render tests, the table and headless-driver unit tests |
| `make test-headless` | Real kc/terminal-driver startup, resize, help and clean exit against isolated envtest |
| `make test-fuzz` | Log formatter fuzz target, bounded by `FUZZ_TIME` (default `10s`) |
| `make lint` | Pinned golangci-lint and nolintguard over the whole tree; zero issues |
| `make lint-fix` | Apply available fixes, then check the whole tree |
| `make build` | Compile all packages |

`make test` aliases `test-integration`; missing envtest assets fail instead of silently skipping integration coverage. `test-unit`, `test-integration`, `test-tui` and `test-race` accept `PACKAGES`, `GO_TEST_FLAGS` and `TEST_TIMEOUT` (default `5m` per package):

```sh
make test-unit PACKAGES='./internal/ui/panelcontent/reload' GO_TEST_FLAGS='-run TestOneLoadInFlightAndStaleResultDropped'
make test-race PACKAGES='./internal/cluster ./internal/logmux'
make test-integration PACKAGES='./internal/ui' GO_TEST_FLAGS='-run TestPanelContexts'
```

Select changed packages and their transitive in-repository importers:

```sh
python3 scripts/test-changed.py --plan
python3 scripts/test-changed.py --mode unit
python3 scripts/test-changed.py --mode race --base origin/main
```

The selector includes staged, unstaged and untracked files relative to the merge base. Module/build changes and unmapped inputs conservatively select the full tree. Documentation-only changes need no Go tests. This is a development shortcut; full lint, build and the required CI suite remain mandatory. See [the workflow guide](docs/agent-workflow.md) for boundaries.

## Integration tests

Install `setup-envtest` and acquire binaries matching CI:

```sh
go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
export KUBEBUILDER_ASSETS="$(setup-envtest use -p path 1.37.x)"
make test-integration
make test-headless
```

Use `internal/testing/kcenvtest`, not `envtest.Environment.Start` directly. Prefer the package control plane from `kcenvtest.Main`; use a separate server only when cluster-wide changes must be isolated. Use `kctesting.UniqueName`, `RunInBackground` and `WaitForAccess` for shared names, lifecycle and RBAC propagation. See ADRs [0060](docs/adr/0060-require-envtest-in-ci.md) and [0244](docs/adr/0244-envtest-under-load.md).

Never use a real kubeconfig or cluster. Envtest has an API server and etcd, not a kubelet or controller manager: it cannot validate real container exec, log streaming, scheduling or metrics. Use controlled fake transports for those boundaries and report what remains unverified.

`make test-headless` builds kc and its driver into a temporary directory, creates an isolated home and kubeconfig, and polls for observable terminal states. It checks 120x30 and 100x24 layouts, contextual help and kc's successful exit. Ordinary `go test` skips this opt-in process test; the Linux CI job runs it explicitly after integration tests.

## Changes and review

Test nontrivial behavior. Check that regression tests detect the original failure, then run affected checks. Run full lint and build before handing off code; CI requires the full envtest suite. Reuse valid verification results for unchanged code.

PRs describe the problem, implementation and resulting behavior, with commands and outcomes. Include screenshots for visible TUI changes and a compact before/after diagram for architecture or workflow changes. Keep unrelated refactors out of fixes. Process references and pinned upstream attribution are in [docs/agent-workflow.md](docs/agent-workflow.md).
