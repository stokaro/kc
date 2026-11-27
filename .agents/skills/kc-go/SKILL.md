---
name: kc-go
description: Implement or review kc Go API design, error handling, resource ownership, concurrency, or nontrivial pure Go logic. Use when those contracts change; skip prose, dependency-only updates, formatting, mechanical renames, and layout or key wiring already covered by kc-tui.
---

# Go changes in kc

Paths below are relative to the repository root. Read the touched package and
its closest tests first. Take the Go version and dependencies from `go.mod`;
use `go doc` for the installed API before importing examples from another version.

## Design and ownership

- Keep behavior with the package that owns it. Extend an existing component
  before adding another wrapper, registry, options layer, or generic helper.
- Prefer concrete types. Introduce a small interface at its consumer when it
  expresses an actual boundary; do not mirror a dependency merely to mock it.
- Make invalid states difficult to construct. Use early returns and explicit
  transitions; separate responsibilities when they have different lifetimes,
  rather than splitting files to satisfy an arbitrary line limit.
- Identify the owner of every goroutine, stream, lease, timer, and subprocess.
  State how it stops and how completion is observed on success, error, cancel,
  and replacement. Keep cleanup idempotent where multiple exit paths meet.
- Pass request contexts explicitly. Preserve documented lifecycle-owned contexts
  where a framework's callbacks cannot accept one. Never add a detached
  background context or nil-context fallback to bypass cancellation. Capture
  immutable inputs before background work; return UI changes through messages
  (use `kc-tui` for that boundary).
- Do not mutate shared maps, slices, or cached Kubernetes objects without an
  ownership contract. Use `kc-kubernetes` for cache and connection changes.

## Errors and diagnostics

- Return useful operation context. Wrap with `%w` when callers should inspect
  the cause; use `errors.Is`, `errors.As`, or Kubernetes error helpers instead
  of comparing error strings.
- Handle write, flush, close, and subprocess errors where they affect the
  result. Explain intentionally ignored cleanup errors. Do not turn failures
  into empty success, hide them with blanket recovery, or add retries without
  a cancellation and retryability policy.
- Obtain logging from `ctrllog.FromContext(ctx)`, using the `ctrllog` import
  alias. Preserve the existing request logging convention; keep credentials
  and terminal-owned stdout out of diagnostics.
- Fix the cause of a lint finding. A narrowly scoped suppression needs the
  linter name and a concrete reason; do not weaken the shared configuration.

## Verify the changed contract

Use standard `testing`, table cases when they clarify behavior, `t.Context()`,
and `t.Cleanup`; do not introduce testify. Coordinate asynchronous tests with
channels or injected clocks; avoid sleeps used as a guess that work finished.
Assert observable behavior and cleanup, not a copy of the implementation.

Start with `make test-unit PACKAGES='./path/to/package'`. For concurrent paths,
add `make test-race PACKAGES='./path/to/package'`; use the verification skill
for integration or fuzzing. Run `make lint` at the code-change checkpoint.
Do not repeat unchanged successful checks after a prose-only fixup.

## Focused references

- [Pool ownership and deterministic tests](../../../internal/cluster/pool_test.go)
- [Background lifecycle helper](../../../internal/testing/run.go)
- [Lint decisions](../../../docs/adr/0118-lint-setup.md)
- [Go review comments](https://go.dev/wiki/CodeReviewComments): read a relevant
  topic only when the local convention leaves a design question unresolved.
- [JetBrains use-modern-go](https://github.com/JetBrains/go-modern-guidelines/blob/main/plugin/skills/use-modern-go/SKILL.md):
  optional for a specific modernization; match guidance to `go.mod` and check
  the existing `modernize` lint finding before adding a new convention.

Do not preload a Go tutorial or a third-party skill catalog.
