# 0243. Remove the vendored bubblelayout

- Status: Accepted
- Date: 2026-11-23
- PR: #243
- Supersedes: the third_party exclusion in ADR 0128, decision 3

## Context

`third_party/bubblelayout` was copied in for a modal layout in 578733e.
5058522 replaced that modal with the shared text input modals, and since
then nothing imports the package. It still compiled with `./...`, and ADR
0128 excluded `third_party/` from linters and formatters because the copy
kept its upstream form.

## Decision

Delete `third_party/` and the two `third_party/` exclusions in
`.golangci.yml`.

## Consequences

The whole tree is linted with no path exclusions other than `builtin$`.
Vendoring code again would need a new exclusion and a new decision.
