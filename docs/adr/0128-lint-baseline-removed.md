# 0128. Lint the whole tree: baseline removed, flag-parameter off, third_party excluded

- Status: Accepted; the third_party exclusion superseded by 0243
- Date: 2026-11-22
- Issue: #128

## Context
ADR 0118 introduced golangci-lint with `issues.new-from-merge-base: origin/main`, so the 1,735 findings on main at the time were a baseline and only new code was checked. PRs #144, #148 and #151 fixed the findings package by package. Two groups were left: 72 revive `flag-parameter` findings, and 35 findings in the vendored `third_party/bubblelayout`, which the exclusion `'third_party$'` was meant to skip but did not.

## Decision
1. The baseline is gone. `issues.new-from-merge-base` is removed from `.golangci.yml`, `LINT_BASE` and `make lint-all` from the Makefile, and the history checkout and `LINT_BASE` from `.github/workflows/lint.yml`. `make lint` checks the whole tree and must report 0 issues.
2. revive `flag-parameter` is disabled. In this TUI most bool parameters are data or Bubble Tea style setters (`SetFocus(bool)`, `renderButton(label, focused, confirm)`, `direction(c int, desc bool)`); splitting each function in two makes the code harder to read. The six `//revive:disable-next-line:flag-parameter` directives added while the rule was on are removed.
3. The exclusion pattern becomes `'(^|/)third_party/'` for linters and formatters. The paths are regular expressions matched against file paths, so `'third_party$'` matched no file. Vendored code keeps its upstream form.

## Alternatives considered
- Keep `flag-parameter` and suppress each finding: 72 directives that say "this bool is data", and one more for every new setter.
- Lint `third_party/` and fix it: the code is a copy of an upstream package; local edits make updating it harder.

## Consequences
CI fails on any finding in any file, not only in changed lines, so a new linter version or a new rule shows up at once. `make lint` no longer needs an `origin/main` ref. Function-level linters (funlen, gocyclo, gocognit, nestif) now see every function, which closes the gap ADR 0118 noted.
