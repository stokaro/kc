# 0119. Release pipeline for the stokaro/kc fork

- Status: Accepted
- Date: 2026-11-22
- Issue: #119 (also covers #61)

## Context

kc is a fork of `sttts/kc`. The GoReleaser configuration, the release workflow, the README install commands, the build badge and `docs/RELEASING.md` all pointed to the upstream repository and its tap `sttts/homebrew-kc`. People who followed them installed upstream.

`goreleaser check` refused the configuration: the notarization timeout was 30m and GoReleaser allows at most 20m. The `brews` section is deprecated in GoReleaser v2, and `check` fails on deprecated settings.

The Homebrew formula test ran `kc --version`, but kc had no such flag. `kc version` loads a kubeconfig, asks the API server for its version and fails without one, so it cannot serve as a package check on a machine without a cluster.

Nothing checked the release configuration between tags, so it could break without anyone seeing it.

PR #87 tried the same work together with a module rename. The rename is #120 and is done separately; no ADR 0061 exists on main, so this record covers #61 as well.

Open questions: which tap and install command, which secrets, how to sign, what happens when the tap token is missing, how `--version` behaves, and when CI checks the release.

## Decision

1. Targets. GitHub Releases go to `stokaro/kc`. The Homebrew tap is `stokaro/homebrew-kc`, named like upstream's, and users run `brew install --cask stokaro/kc/kc`.

2. Cask only. GoReleaser publishes a cask and no formula. The cask installs on macOS and Linux. Because casks have no `test` block, the "kc --version without a kubeconfig" check moves into CI (decision 7). The cask removes the macOS quarantine attribute after install, so builds without Apple notarization still start.

3. Tap token is required. The tap is written with the repository secret `HOMEBREW_TAP_GITHUB_TOKEN`. The release job checks for it before the checkout and fails when it is missing. `skip_upload` stays `false`, so a release never finishes green with the tap left behind.

4. Signing. The checksum file is signed with cosign in keyless mode, through the workflow's GitHub OIDC identity (`kc_checksums.txt.sigstore.json`). GPG signing and its two secrets are removed. Apple signing and notarization stay optional and run only when both `MACOS_SIGN_P12` and `MACOS_NOTARY_KEY` are non-empty; the timeout is 20m.

5. Tokens and identities. The GitHub Release uses the workflow's `GITHUB_TOKEN` with `contents: write`; `RELEASE_GITHUB_TOKEN` is not used any more. Tap commits name `github-actions[bot]` with its noreply address as author, so no personal address ends up in the tap history.

6. Versions. The first fork release is a tag above upstream's last tag `v0.8.0`, for example `v0.9.0`. Snapshots are named `0.0.0-SNAPSHOT-<short commit>` so they never reuse an upstream tag. The ldflags set `main.version`, `main.commit` and `main.date` (commit date, for reproducible builds); these paths do not contain the module path, so #120 does not change them.

7. CI. `.github/workflows/release.yml` also runs on pull requests that touch `.goreleaser.yaml`, the workflow, `cmd/kc/**`, `go.mod`, `go.sum`, `LICENSE` or `README.md`. It runs `goreleaser check`, builds a snapshot of all archives without signing or publishing, runs the extracted linux/amd64 binary with `--version` and `version --client` under `env -i` with an empty home and no kubeconfig, and checks that the generated cask points at `stokaro/kc` and does not name upstream. The GoReleaser version is pinned in the workflow.

8. Version without a cluster. `kc --version` is a kong flag hook: it prints the client version and exits during argument parsing, before kc reads a kubeconfig, sets up logging or opens a connection. `kc version --client` does the same, as in kubectl. Plain `kc version` still asks the server and fails without one, like `kubectl version`; the error now names `--client` and `--version`. When the binary has no ldflags (`go build`, `go install`), kc takes the module version and the VCS revision and time from the Go build info.

9. Install methods in the README: Homebrew, release archives with checksums and the cosign check, and building from source. `go install github.com/stokaro/kc/cmd/kc@latest` is not offered until #120 changes the module path; the README says so.

## Alternatives considered

- Shared tap `stokaro/homebrew-tap` (PR #87). One tap for all tools, but issue #119 names `stokaro/homebrew-kc`, which keeps the upstream layout and the short `stokaro/kc/kc` install name. Rejected.
- Keep the `brews` formula next to the cask. It is deprecated, `goreleaser check` fails on it, and a formula and a cask with one name in one tap shadow each other. Rejected.
- Optional tap token with `skip_upload` when it is missing (PR #87). A release then succeeds with one channel missing and nobody notices. Rejected; the release fails early instead.
- Keep GPG signing. It needs a private key and a passphrase stored as secrets, rotation and a published public key. Keyless cosign ties the signature to this repository's workflow with no stored key. Rejected.
- Run the GoReleaser check on every pull request. Most PRs touch only `internal/`, and the snapshot build costs minutes. The path filter covers everything that ends up in the archive or the cask except the internal packages, whose build is already checked by the build workflow. Rejected.
- Make `kc version` succeed without a cluster. Scripts that use its exit code to test cluster access would change behavior, and it would differ from kubectl. Rejected in favor of `--version` and `--client`.
- Keep the Homebrew instructions out of the README until the tap exists (#61). The README is where users look; a note that the tap starts with the first fork release is more useful than no instruction.

## Consequences

- Before the first release, a maintainer has to make Actions run in `stokaro/kc` (billing or settings), create `stokaro/homebrew-kc`, add `HOMEBREW_TAP_GITHUB_TOKEN`, and push a tag above `v0.8.0`. `docs/RELEASING.md` lists the steps.
- The old secrets `RELEASE_GITHUB_TOKEN`, `RELEASE_GPG_PRIVATE_KEY` and `RELEASE_GPG_PASSPHRASE` can be deleted.
- Users who verified GPG signatures now verify the checksum file with `cosign verify-blob`.
- The cask was checked by generating it in a snapshot; no `brew install` from the real tap has run yet. The first tag is the end-to-end test.
- Follow-up after #120: update the README `go install` note, and check that no ldflags path picks up the module path.
- A path-filtered workflow should not be made a required check, or PRs that do not touch those paths would wait for it forever.
