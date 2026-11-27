# 0392. Scoped agent skills and executable quality checks

- Status: Proposed
- Date: 2026-11-28
- PR: #392

## Context

The root agent instructions contained 15,343 bytes, obsolete table examples and
conflicting workflow advice. Most tasks need only one part of that context.
Go, terminal UI and Kubernetes correctness also depend on concrete repository
contracts and test fixtures, which generic skill collections cannot establish.

The project needs idiomatic changes, observable regression checks and controlled
agent cost. More instructions do not establish correctness or demonstrate token
savings. The existing lint setup did not enable errcheck, and its exclusions
would hide several meaningful write/close errors once enabled.

## Decision

### 1. One canonical, selectively loaded instruction set

Keep the root `AGENTS.md` below 5 KiB and route nontrivial work to `kc-go`,
`kc-tui`, `kc-kubernetes` and `kc-verification` under `.agents/skills`.
Each skill also has a 5 KiB budget; larger process references load on demand.
`CLAUDE.md` imports the root file and `.claude/skills` is a relative symlink to
the canonical directory. Prose and mechanical edits do not trigger domain
skills. Conditional debugging loads only for unclear, recurring or
nondeterministic failures.

Adapt useful debugging, verification and focused-review concepts from the
pinned Superpowers revision documented in [the workflow guide](../agent-workflow.md#sources-and-licenses),
with its MIT notice retained. Do not install upstream hooks, approval cascades
or automatic updates. JetBrains Go guidance and the reviewed TUI collection
remain optional references; `go.mod` and existing project contracts govern.

### 2. Focused local evidence with complete required gates

The changed-package selector includes the Git merge-base diff, working/index
changes and untracked files. It adds transitive normal and test importers and
accounts for embedded inputs and testdata. Unknown inputs, build changes and
missing metadata conservatively select the full tree. The selector covers the
current Go build configuration and does not replace full lint, build or CI.

Make unit, required-envtest, race, TUI, headless and bounded fuzz modes explicit.
`make test` requires envtest; unavailable assets cannot silently count as an
integration pass. CI adds agent-file checks, a focused race job and an actual
headless application check. Reuse valid evidence for unchanged inputs and
delegate only independent work or a focused review.

`make check-agents` validates frontmatter, discovery paths, local links and
byte budgets and runs the verification-tool tests. A fresh-context routing
review is recorded separately from implementation effectiveness. Compare
accepted implementations before claiming token or cost savings.

### 3. Error handling is checked and exceptions are local

Enable errcheck and remove the broad `std-error-handling` exclusion preset.
Keep `check-blank: false` so explicit discards remain a reviewed choice, and
retain errcheck's built-in known-infallible exclusions. An explicit
`nolint:errcheck` must explain the local cleanup or diagnostic tradeoff. This
supersedes the errcheck explanation exemption in decision 2 of
[ADR 0118](0118-lint-setup.md); its other decisions retain their existing status.

Handle the initial 37 findings and fix related lost errors in version output,
process-identity reads and background workers. Regression tests must detect the
original failure. Cancellation does not retroactively excuse an error that
returned before the worker's context was canceled.

### 4. Isolated, API-backed terminal evidence

The headless fixture builds the real kc and driver, starts envtest, supplies its
own temporary kubeconfig and home, and requires a seeded ConfigMap row before
testing resize/help/exit. Bounded observable polling replaces fixed readiness
sleeps. A child wrapper checks kc's actual exit status because the driver's
`wait` reports termination without preserving that status.

The shared envtest helper defaults to loopback advertisement and the no-op
bootstrap endpoint reconciler, preserving explicit caller flags. This removes
the host-default-route requirement without opening a real cluster connection.
Automatic `default/kubernetes` endpoint maintenance is omitted; the Service
and endpoint APIs remain. Tests seed their endpoint fixtures. This extends the
lifecycle/load tuning in [ADR 0244](0244-envtest-under-load.md).

## Alternatives considered

- Installing an entire external skill bundle adds overlapping triggers and
  process requirements without establishing kc's specific invariants.
- Instruction-only changes cannot enforce error handling, validate references
  or demonstrate that test selection handles deleted and embedded inputs.
- Running every expensive check after every edit duplicates work. Scoped
  development checks plus complete required gates preserve meaningful evidence.
- Static footer-only headless readiness can pass when API loading fails; the
  fixture must observe data produced by its isolated server.

## Consequences

Developer verification tools require Python 3.9 or later and Git; Go runtime
dependencies and tool versions do not change. The CI additions consume runner
time, so they target concurrency and a small terminal journey rather than
repeated full E2E sessions. Envtest cannot prove real pod execution, streaming
logs, port forwarding or metrics. Narrow instruction triggers and smaller
startup text are design choices; actual token/cost benefit remains measurable
work, not an asserted result.
