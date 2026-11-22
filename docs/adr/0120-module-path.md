# 0120. Go module path github.com/stokaro/kc

- Status: Accepted
- Date: 2026-11-22
- Issue: #120

## Context
The module path in `go.mod` was still `github.com/sttts/kc`, the upstream repository. The go command refuses `go install github.com/stokaro/kc/cmd/kc@...` while the module path names another repository, and every import pointed at a project kc no longer tracks. The first attempt (#87) mixed the rename with the release setup; the release part was merged separately in #124, and the rename commit no longer applied to the current code.

## Decision
- The module path is `github.com/stokaro/kc`. Every import in the repository is rewritten; there is no compatibility shim, because nothing outside this repository imports kc packages.
- The gci local prefix in `.golangci.yml` follows the module path. The renamed files get their imports grouped by gci in the same change, since the lint baseline treats the touched import lines as new code.
- Links to the original project (README origin note, NOTICE, CONTRIBUTING, ADR 0121) keep `github.com/sttts/kc` on purpose: they name upstream, not this module.
- The existing tags up to `v0.8.0` come from upstream and carry the old module path, so `go install ...@latest` resolves to a version with the wrong path. Until a release above `v0.8.0` exists, the README offers `@main`.

## Alternatives considered
- Rebasing #87's rename commit: it touched files that have since changed in almost every merged PR; rewriting the path on the current tree gives the same result with no conflict risk.
- A vanity import path (for example a custom domain): no domain exists for kc, and it adds a redirect service to maintain.

## Consequences
Open branches that add `github.com/sttts/kc/...` imports must rewrite them after merging main (`sed 's#github.com/sttts/kc#github.com/stokaro/kc#g'` on the changed Go files, then `golangci-lint fmt` on them). The tracked binary `internal/table/table` still contains the old path; it is a build artifact and is not rebuilt here.
