# 0118. Lint setup adapted from ptah

- Status: Accepted; decision 4 (baseline) superseded by 0128
- Date: 2026-11-22
- Issue: #118

## Context

kc had no linter configuration. Style and correctness rules from `AGENTS.md` were checked only in review. stokaro/ptah has a golangci-lint v2 setup with a tuned linter list, nolintguard for suppression comments, qtlint for quicktest, and a `Go Lint` workflow.

Constraints:

- The first full run on main reports 1735 findings (counted with `--uniq-by-line=false`; the default de-duplication shows 1626).
- Six pull requests (#122 to #127) are open and edit many Go files. The lint setup must not edit existing Go source, so those pull requests still merge without conflicts.
- Adding tools to `go.mod` must not change the versions kc builds with.

## Decision

### 1. Linters and thresholds

`.golangci.yml` keeps ptah's explicit list (`default: none`) without `rowserrcheck`, because kc has no `database/sql` code. Thresholds stay at ptah's values: funlen 240 lines / 160 statements, gocognit 30, gocyclo 20, nestif 6, lll 240. The issue allows lowering a threshold where kc's code already fits, but every one of them already has findings on main (funlen 2, gocognit 33, gocyclo 38, nestif 22, lll 1), so none can go lower without adding to the baseline.

The revive rule list is ptah's, with two changes:

- `redundant-import-alias` is off. `AGENTS.md` gives `tea "charm.land/bubbletea/v2"` as the correct import, and the alias names the package where the `/v2` path does not. The rule would report each of the 148 such imports and every new one.
- `unused-parameter` and `unused-receiver` stay off, as in ptah. Bubble Tea and controller-runtime interfaces fix many signatures.

staticcheck also turns off ST1000, and the `comments` exclusion preset stays on. ptah requires a package comment on every package; kc does not.

### 2. AGENTS.md rules as lint rules

- depguard refuses `charm.land/bubbletea` and `charm.land/bubbles` (any path under them) and allows `charm.land/bubbletea/v2` and `charm.land/bubbles/v2`; the longer match wins. It also refuses the old `github.com/charmbracelet/bubbletea` and `github.com/charmbracelet/bubbles` paths and `io/ioutil`.
- importas requires `ctrllog` for `sigs.k8s.io/controller-runtime/pkg/log`, with `no-unaliased`. There is no `tea` entry: the package is already named `tea`, so an unaliased import reads the same as the aliased one.
- forbidigo with `analyze-types` refuses `context.Background` and `context.TODO` in `_test.go` files (a `path-except` exclusion keeps them legal in production code, where `cmd/kc` and `Deps.Ctx` create root contexts). It also refuses `ctrllog.Log` and `ctrl.Log` (`sigs.k8s.io/controller-runtime`'s `Log`) everywhere except `cmd/kc`, which installs the logger and hands it to klog. Because the patterns match by type, an alias such as `crlog.Log` does not hide a call.
- nolintlint requires a specific linter and an explanation (`lll` and `errcheck` may skip the explanation, as in ptah).
- nolintguard runs through `go vet -vettool` with `-require-justification`. It forbids `//nolint:gosec` and `//nolint:revive` in favor of `// #nosec G204 -- reason` and `//revive:disable-next-line:<rule> reason`, and requires the reason.

Each rule was checked against a throwaway package that broke it (the `charm.land` v1 paths through stub modules wired in with `replace`), and lint failed for each. The same package with justified suppressions passed.

### 3. Dropped from ptah

- qtlint and the `qt` importas alias: kc tests use the standard `testing` package.
- `rowserrcheck`: no database code.
- The testify ban stays. kc's tests already use only the standard library, and AGENTS.md asks for standard `testing` with table-driven tests.
- The two build-tag contours, the Windows lint job, the scope job and the aggregate gate. kc has one module, no opt-in build tags and no Windows-only files, and its CI is small enough that a scope job would cost more than it saves.
- `make install-hooks`: kc has no hook scripts. It can be added with the first hook.
- The gci section `prefix(ptah.run)` becomes `prefix(github.com/sttts/kc)`, the module path in `go.mod`. It must change with the module path (#87).

### 4. Baseline through `new-from-merge-base`

`issues.new-from-merge-base: origin/main` in `.golangci.yml`, and the same value passed by `make lint`, report only lines that differ from the merge base with main. No exclusions file is needed. The existing findings are tracked in #128. `make lint-all` shows the whole tree.

On a pull request the merge base is the base of the pull request. On a push to main the merge base would be the pushed commit, which reports nothing, so the workflow passes `LINT_BASE=HEAD~1` for pushes. kc merges pull requests as single squashed commits, so that is the whole change.

nolintguard has no new-code mode, but main has no suppression comments, so it reads the whole tree.

Findings on main when this was set up (`go tool golangci-lint run --new-from-merge-base= --uniq-by-line=false ./...`):

| Linter | Findings |
| --- | --- |
| revive | 590 |
| modernize | 491 |
| gci | 264 |
| staticcheck | 79 |
| unused | 57 |
| forbidigo | 50 (41 `context.Background`/`TODO` in tests, 9 global logger) |
| gosec | 48 |
| gocyclo | 38 |
| gocognit | 33 |
| gocritic | 26 |
| nestif | 22 |
| ineffassign | 8 |
| gofmt | 5 |
| dupl | 4 |
| gochecknoinits | 4 |
| importas | 4 (`crlog` in `internal/models`) |
| nakedret | 3 |
| whitespace | 3 |
| funlen | 2 |
| goprintffuncname | 2 |
| govet | 1 |
| lll | 1 |
| depguard | 0 |

revive by rule: use-any 137, enforce-map-style 85, flag-parameter 75, enforce-slice-style 56, confusing-results 50, enforce-repeated-arg-type-style 50, unchecked-type-assertion 32, function-result-limit 26, redefines-builtin-id 24, import-shadowing 16, unnecessary-stmt 10, early-return 9, struct-tag 5, empty-block 3, empty-lines 3, error-strings 2, increment-decrement 2, unexported-naming 2, constant-logical-expr 1, indent-error-flow 1, superfluous-else 1.

### 5. Tools pinned in go.mod

golangci-lint v2.15.0 and nolintguard v0.4.2 are in the `go.mod` `tool` block and run with `go tool`. CI runs `make lint`, the same command as a developer, so there is one version to update. The tool dependencies add requirements to `go.mod` and `go.sum`, but the module versions of every package kc builds and tests (`go list -test -deps ./...`) are the same before and after.

## Alternatives considered

- **A checked-in exclusions file listing every finding.** It works without git history and lints main fully. With 1735 findings it would be a large file, and rules keyed by path and text break or hide new findings as files change under the open pull requests. `new-from-merge-base` needs no list and goes away as the baseline is fixed.
- **Fix the cheap findings in this pull request**, as the issue suggests. gci, gofmt, modernize and most revive style findings are automatic, but they touch most Go files and would conflict with #122 to #127. They move to the follow-up issue.
- **`golangci/golangci-lint-action` in CI, as ptah does.** It caches well but carries its own version pin, which would have to match `go.mod`. `make lint` keeps one pin.
- **A separate tools module (`tools/go.mod`).** It keeps tool requirements out of the main `go.mod`, but the issue asks for the `tool` block, and the build list check above shows the main module is not affected.
- **Lint step in `build.yml`.** A separate `lint.yml` keeps the build matrix unchanged and lets lint run on its own; it needs `fetch-depth: 0`, which the build does not.

## Consequences

- New code in every pull request is linted, including the open ones once they are rebased onto this change. Their new lines may report findings they did not report before.
- `make lint` depends on an `origin/main` ref. A clone without it can pass `LINT_BASE=<ref>`.
- Function-level linters (funlen, gocyclo, gocognit, nestif) report on one line. A change inside a long function that does not touch that line is not reported. #128 removes the baseline and the `new-from-merge-base` setting.
- When the module path changes (#87), the gci prefix in `.golangci.yml` changes with it.
