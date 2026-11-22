# 0121. Origin note and attribution for the fork

- Status: Accepted
- Date: 2026-11-22
- Issue: #121

## Context

kc started as a fork of Kubernetes Commander (https://github.com/sttts/kc) and has diverged a lot from it. The GitHub fork relation will be removed later, so the docs must name the original project on their own. Apache 2.0 section 4 asks derivative works to keep the original attribution notices and to mark changed files. Releases (#119), install instructions (#61) and the Go module path (#120) are handled by separate issues.

## Decision

1. README gets a short "Origin" note directly under the build badge. It links the original repository, labels it as upstream, says this version is developed independently and has diverged, and points issues to stokaro/kc. The build badge points to stokaro/kc.
2. A NOTICE file names the original project, its URL and its author (Stefan Schimanski, as recorded in the git history), and states that the work was modified substantially. It does not contain a copyright year or holder line: LICENSE has only the unfilled template line and the upstream repository has no NOTICE, so there is no verified copyright statement to copy.
3. The Homebrew install commands stay in README and docs/RELEASING.md, with a note that the tap belongs to upstream and installs the original project. #61 and #119 replace them once this repository publishes its own packages.
4. CONTRIBUTING.md gets the same origin sentence and points forks and issues at stokaro/kc.
5. Code identifiers that still contain `sttts` (the Go module path, the `kc.sttts.github.io/node` annotation, the release workflow's git email) are not changed here. The module path is #120; the others are code changes outside a docs-only issue.

## Alternatives considered

- Put attribution in a README section only. Rejected: NOTICE is the file Apache 2.0 section 4(d) refers to, and it travels with source and binary distributions.
- Write a copyright line such as "Copyright 2025 Stefan Schimanski". Rejected: no such line exists in the original repository, and inventing one could misstate the holder or the year.
- Remove the Homebrew commands now. Rejected: #61 and #119 own the install and release paths; a labeled note keeps README honest without changing that scope.

## Consequences

Readers see on the first screen that kc is based on sttts/kc and diverged from it. Once #119 and #61 land, the upstream tap notes in README and docs/RELEASING.md should be replaced with this repository's install paths. Once #120 lands, the remaining `github.com/sttts/kc` import paths in internal/table READMEs change with it.
