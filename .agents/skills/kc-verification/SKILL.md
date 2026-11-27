---
name: kc-verification
description: Select focused checks for non-trivial kc code or test changes and verify completion claims. Also use for unclear, recurring or nondeterministic failures. Skip prose-only and mechanical edits unless they change validation tooling.
---

# Verify kc changes

## Choose evidence before expanding work

Read the diff, the affected package's existing tests and the relevant contract.
Name the behavior that could break and choose the smallest check that observes
it. Start with a regression test for a behavior fix; confirm it fails for the
original defect, then passes with the fix. Test behavior, not a mock's setup or
the implementation's private arrangement. Do not add tests for prose edits.

| Change | First check | Expand when needed |
| --- | --- | --- |
| Pure Go logic | `make test-unit PACKAGES=./path/to/package/...` | Reverse importers when an API or shared behavior changes |
| UI state, focus, layout | Focused `internal/ui` tests; `make test-tui` | Assert small sizes, resize, Unicode and stale responses relevant to the change |
| Kubernetes API, cache, RBAC | `make test-integration PACKAGES=./path/to/package/...` | The affected integration packages and lifecycle/race tests |
| Goroutines or shared mutable state | `make test-race PACKAGES=./path/to/package/...` | Stress/repeat only to reproduce a concrete remaining race |
| Parser or text boundary | Existing seeds; `make test-fuzz` when applicable | A bounded fuzz run for the changed parser |
| Agent instructions or validation tooling | `make check-agents` and the changed tool's checks | [Routing evaluations](../../../docs/agent-workflow.md#evaluate-agent-changes) |

Substitute the affected package; read the Makefile for flags. `test-tui` covers
models/rendering and driver units, not application E2E. Skipped envtest is not
integration evidence. Use only fake clients or envtest, never a real kubeconfig.
Read [envtest setup](../../../docs/agent-workflow.md#envtest-constraints) or
[headless limits](../../../docs/agent-workflow.md#headless-tui-checks) as needed.

## Investigate failures conditionally

If the cause is unclear, the defect is nondeterministic, or a fix failed, read
[references/debugging.md](references/debugging.md). Correct a clear mechanical
error directly. Preserve useful failure output; do not rerun a whole suite
merely to get a shorter log or a lucky pass.

## Bound the work

Use one implementer by default. Plan in a few bullets; write a design only when
the change introduces a consequential interface, ownership or behavior decision.
For substantial or risky changes, request one independent review of the diff,
the affected contracts and recorded test evidence. Give reviewers a focused
task; do not spawn a new implementer/reviewer pair per small edit.

Keep full command logs outside the conversation and report the command, exit
status, scope and relevant failures. Run focused tests while iterating, then
the required build/lint and broader checks once on the final code. Reuse valid
evidence for unchanged code and inputs; repeat only when an edit or environment
change invalidates it, a specific risk remains, or a required gate demands it.
Do not run expensive suites concurrently on the same workspace.

## Completion contract

Before claiming completion, inspect the diff and command results. Record:

- What behavior changed and which regression or assertion proves it.
- Commands actually run, their results, and the code revision they cover.
- Checks skipped or blocked, with the concrete reason and remaining coverage.
- Material review findings fixed or still open.

A lint pass is not a build or integration pass. A timeout is not a test pass.
Do not weaken assertions, hide failures or silently switch envtest off.
Follow the repository PR requirements; no separate approval ritual is added.

Adapted process concepts: [source and license notes](../../../docs/agent-workflow.md#sources-and-licenses).
