# 0061. Publish the stokaro/kc fork

- Status: Accepted
- Date: 2026-11-22
- Issue: #61

## Context

kc is a fork of `sttts/kc`. The release configuration, the README install
commands, the build badge and the maintainer release guide all pointed to the
upstream repository and its Homebrew tap `sttts/homebrew-kc`. Users who
followed them installed upstream, not this fork.

The Go module path was still `github.com/sttts/kc`. The go command checks that
the module path matches the path you install from, so
`go install github.com/stokaro/kc/cmd/kc@latest` fails with a path mismatch.

The Homebrew formula test ran `kc --version`, but kc had no such flag; it only
had a `kc version` subcommand. That subcommand loads a kubeconfig and asks the
API server for its version, and it exits with an error when it cannot. A
package check on a build machine has no cluster.

`goreleaser check` also rejected the configuration: the notarization timeout
was 30m and GoReleaser allows at most 20m. The `brews` section (generated
formula) is deprecated in GoReleaser v2 in favor of `homebrew_casks`.

Open questions: whether to change the module path, which tap to publish to,
and which install methods to offer.

## Decision

1. Module path. Change it to `github.com/stokaro/kc` and update every import.
   This is the only way `go install github.com/stokaro/kc/cmd/kc@<version>`
   works. kc is an application: almost all code is under `internal/` and
   `cmd/`, and the module was never published under the stokaro path, so no
   outside importer can depend on the old path of this fork.

2. Release target. GoReleaser publishes GitHub Releases to `stokaro/kc`.

3. Homebrew tap. The tap is `stokaro/homebrew-tap`, so users run
   `brew install --cask stokaro/tap/kc`. One tap for all stokaro tools fits
   better than one tap per tool. Only a cask is published; the deprecated
   `brews` formula is removed. The tap upload needs the
   `HOMEBREW_TAP_GITHUB_TOKEN` secret; when it is not set, GoReleaser writes
   the cask into `dist/` and skips the upload, so a release still works before
   the tap exists. The cask removes the quarantine attribute after install, so
   builds without Apple notarization still start.

4. Install methods offered in the README, in this order:
   `go install github.com/stokaro/kc/cmd/kc@latest`, release archives from
   the GitHub Releases page (with checksums and GPG signatures), and Homebrew
   on macOS. The README says the Homebrew command works only after the tap
   is published. Linux users use `go install` or the archives.

5. Version without a cluster. `kc --version` prints the client version and
   exits during argument parsing, before kc reads a kubeconfig, sets up
   logging or opens a connection. `kc version --client` does the same, as in
   kubectl. Plain `kc version` keeps asking the server and still fails when
   it cannot, like `kubectl version`; the error now names `--client`. When
   the binary has no `-ldflags` version (a `go install` build), kc takes the
   module version and the VCS revision and time from the Go build info.

6. CI checks the release configuration. The build workflow runs
   `goreleaser check`, builds a snapshot archive for linux/amd64 without
   signing or publishing, extracts it and runs `kc --version` and
   `kc version --client` with an empty home directory and no kubeconfig.

## Alternatives considered

- Keep the module path `github.com/sttts/kc`. Users would have to clone and
  build, and `go install` from the fork would never work. Rejected.
- A per-tool tap `stokaro/homebrew-kc`. Mirrors upstream, but every new tool
  would need another repository. Rejected for a shared tap.
- Keep the `brews` formula next to the cask. It is deprecated, `goreleaser
  check` fails on deprecated properties, and a formula and a cask with the
  same name in one tap are ambiguous. Rejected.
- Make `kc version` succeed without a cluster (print a warning, exit 0).
  Scripts that rely on its exit code to test cluster access would silently
  change behavior, and it would differ from kubectl. Rejected in favor of
  `--version` and `--client`.
- Keep the Homebrew instructions out of the README until the tap exists.
  The README is the place users look; a clear note that the tap comes with the
  first release is more useful than no instruction at all.

## Consequences

- The maintainer has to create `stokaro/homebrew-tap`, add the
  `HOMEBREW_TAP_GITHUB_TOKEN`, `RELEASE_GITHUB_TOKEN` and GPG secrets, and cut
  the first `v*` tag. `docs/RELEASING.md` lists these steps.
- The existing tags come from upstream. The first fork release needs a tag
  newer than the highest upstream tag so `@latest` and Homebrew pick it up.
- Open branches that add imports of `github.com/sttts/kc/...` must be
  rebased and their imports rewritten.
- The `pkg/` packages now have the import path `github.com/stokaro/kc/pkg/...`.
