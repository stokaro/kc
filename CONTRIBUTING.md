# Contributing to kc

kc started as a fork of [Kubernetes Commander](https://github.com/sttts/kc) (upstream) and is now developed independently in [stokaro/kc](https://github.com/stokaro/kc). It has diverged a lot from upstream, so open issues and pull requests for this version here, not upstream.

## Dev Environment

- Go 1.27+ (module toolchain is pinned in `go.mod`).
- Control-plane binaries for the envtest suites: `export KUBEBUILDER_ASSETS="$(setup-envtest use -p path 1.37.x)"` (install with `go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest`). `KC_REQUIRE_ENVTEST=1 go test ./...` runs them as CI does and fails if envtest cannot start. If you cannot run envtest, `KC_SKIP_ENVTEST=1 go test ./...` runs only the other tests.

Handy commands:

```bash
go fmt ./...
go test ./... -v
go vet ./...
go build ./cmd/kc
```

## Workflow & Expectations

- Imports for Bubble Tea/Bubbles must use the v2 paths (e.g. `charm.land/bubbletea/v2`).
- Read `AGENTS.md` for logging rules, naming conventions, module boundaries, and UI guidelines. Contributions should follow those instructions unless you propose an intentional refactor.
- The UI is driven by controller-runtime caches; avoid touching live clusters in tests. Envtests cover navigation and resource hierarchy behavior.
- Always add or update tests when you change non-trivial logic. The envtest suites live under `internal/cluster`, `internal/models`, `internal/navigation`, `internal/tablecache` and `internal/ui`.

## Opening Changes

1. Fork [stokaro/kc](https://github.com/stokaro/kc) (or create a feature branch if you have write access).
2. Make your edits, keeping commits focused.
3. Run the relevant `go test` / `go vet` / `go build` commands above.
4. Describe what changed, why, and how you tested it in the PR description.
