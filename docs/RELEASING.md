# Releasing kc

This document is for maintainers. kc uses [GoReleaser](https://goreleaser.com) to build archives for Linux and macOS (amd64 and arm64), publish GitHub Releases to [`stokaro/kc`](https://github.com/stokaro/kc/releases), and update the Homebrew cask in the tap [`stokaro/homebrew-kc`](https://github.com/stokaro/homebrew-kc). Users install it with `brew install --cask stokaro/kc/kc`. The decisions behind this setup are in [`docs/adr/0119-release-pipeline.md`](adr/0119-release-pipeline.md).

The configuration is `.goreleaser.yaml`; the workflow is `.github/workflows/release.yml`.

## One-time setup

These steps need admin rights on the `stokaro` organization and are not automated.

1. Make GitHub Actions run in `stokaro/kc`. Runs that stay queued forever usually mean the organization has no Actions minutes or no payment method (Organization settings, Billing and plans), or that Actions are disabled (Repository settings, Actions, General). Nothing below works until a workflow run starts.
2. The public repository `stokaro/homebrew-kc` exists with a `main` branch and a README; GoReleaser writes `Casks/kc.rb` into it.
3. Share the organization variable `PUBLISH_APP_ID` and the organization secret `PUBLISH_APP_KEY` with `stokaro/kc` (Organization settings, Secrets and variables, Actions, each entry's repository access). They belong to the `ptah-publish` GitHub App, which is installed on all `stokaro` repositories with "Contents: read and write"; ptah writes its own tap with it. The release job mints a token for `stokaro/homebrew-kc` only. Without the app, a repository secret `HOMEBREW_TAP_GITHUB_TOKEN` (a fine-grained token with "Contents: read and write" on `stokaro/homebrew-kc`) works as a fallback. The release job stops at its first steps when neither is set, before anything is published.
4. Optional: add the Apple signing and notarization secrets (see below). Without them the macOS binaries are not notarized, and the cask removes the quarantine attribute so they still start.

No other secret is needed. The GitHub Release is created with the workflow's own `GITHUB_TOKEN`, and the checksum file is signed with cosign through the workflow's OIDC identity, so there is no signing key to store.

## Secrets

| Secret | Required | Purpose |
| --- | --- | --- |
| `PUBLISH_APP_ID` (organization variable) and `PUBLISH_APP_KEY` (organization secret) | yes, or the fallback below | The `ptah-publish` GitHub App; the release job mints a token for `stokaro/homebrew-kc` that pushes `Casks/kc.rb`. |
| `HOMEBREW_TAP_GITHUB_TOKEN` | only without the app | Fallback personal access token for the tap. |
| `MACOS_SIGN_P12` | no | Base64 of the exported Developer ID Application `.p12` certificate. |
| `MACOS_SIGN_PASSWORD` | no | Password used when exporting the `.p12` file. |
| `MACOS_NOTARY_KEY` | no | Base64 of the App Store Connect API key (`.p8`). |
| `MACOS_NOTARY_KEY_ID` | no | Key ID of the App Store Connect API key. |
| `MACOS_NOTARY_ISSUER_ID` | no | Issuer ID (UUID) of that API key. |

Notarization runs only when both `MACOS_SIGN_P12` and `MACOS_NOTARY_KEY` are set and not empty.

The secrets `RELEASE_GITHUB_TOKEN`, `RELEASE_GPG_PRIVATE_KEY` and `RELEASE_GPG_PASSPHRASE` from the upstream setup are no longer used and can be deleted.

## Versions

The repository has the upstream tags up to `v0.8.0`. The first release of this fork must be newer, for example `v0.9.0`, so Homebrew and `@latest` see it as the newest version. GoReleaser takes the version from the tag and writes it into the binary, and `kc --version` prints it. A tag with a suffix such as `v0.9.0-rc.1` is published as a prerelease.

## Cutting a release

1. Make sure `main` is green. A pull request that touched the release files has run the `goreleaser check and snapshot` job of the release workflow.
2. Tag the commit on `main` and push the tag:

   ```bash
   git switch main && git pull
   git tag -a v0.9.0 -m "kc v0.9.0"
   git push origin v0.9.0
   ```

3. The tag starts the `release` job of `.github/workflows/release.yml`. It mints the tap token from the GitHub App (or takes the fallback secret) and stops when there is none, builds and archives the binaries, writes `kc_checksums.txt` and signs it with cosign (`kc_checksums.txt.sigstore.json`), notarizes the macOS binaries when the Apple secrets exist, publishes the GitHub Release, and pushes the cask to the tap.
4. Check the result on a machine without a kubeconfig:

   ```bash
   brew install --cask stokaro/kc/kc
   kc --version
   ```

   The first line must show the tag without the `v`, for example `kc Version: 0.9.0`.

If the job fails after the GitHub Release was created, fix the cause and re-run the failed job. To start over, delete the GitHub Release and the tag, then push the tag again.

## Checking the configuration without publishing

The release workflow runs on every pull request that changes `.goreleaser.yaml`, the workflow itself, `cmd/kc`, `go.mod`, `go.sum`, `LICENSE` or `README.md`. It runs `goreleaser check` (which also fails on deprecated settings), builds a snapshot of all four archives without signing or publishing, runs the extracted linux/amd64 binary with `--version` and `version --client` with an empty home directory and no kubeconfig, and checks that the generated cask points to `stokaro/kc`.

To do the same locally:

```bash
goreleaser check
goreleaser release --snapshot --clean --skip=publish,sign
mkdir -p /tmp/kc-smoke/home
tar -xzf dist/kc_*_darwin_arm64.tar.gz -C /tmp/kc-smoke kc   # pick your platform
env -i HOME=/tmp/kc-smoke/home KUBECONFIG=/tmp/kc-smoke/home/missing PATH=/usr/bin:/bin /tmp/kc-smoke/kc --version
cat dist/homebrew/Casks/kc.rb
```

A snapshot never touches GitHub or the tap. Its version is `0.0.0-SNAPSHOT-<commit>`. Without `goreleaser` installed, use `go run github.com/goreleaser/goreleaser/v2@latest` instead.

## Apple signing and notarization

macOS binaries can be signed with a Developer ID Application certificate and notarized through App Store Connect so Gatekeeper trusts them.

1. In the Apple Developer portal, create a **Developer ID Application** certificate. Import it into Keychain, export it as a password-protected `.p12`, and base64-encode it (`base64 -i Certificates.p12 | tr -d '\n' > macos_sign_p12.txt`). Store the content in `MACOS_SIGN_P12` and the export password in `MACOS_SIGN_PASSWORD`.
2. In App Store Connect, create an **API key** with notarization access. Note the issuer ID and key ID, and base64-encode the `.p8` file the same way. Store the values in `MACOS_NOTARY_KEY`, `MACOS_NOTARY_KEY_ID` and `MACOS_NOTARY_ISSUER_ID`.
3. Notarization waits up to 20 minutes, the most GoReleaser allows. After installing, check a binary with `spctl -a -vv "$(command -v kc)"`.

## Publishing manually

If CI is down, run the release from a clean checkout of the tag with cosign installed:

```bash
GITHUB_TOKEN=<token with contents:write on stokaro/kc> \
HOMEBREW_TAP_GITHUB_TOKEN=<tap token> \
  goreleaser release --clean
```

Outside GitHub Actions, cosign asks for an interactive Sigstore login to sign the checksum file. The certificate then names that account instead of the workflow, so the `cosign verify-blob` command in the README does not accept it. Prefer re-running the workflow.

Keep this document up to date whenever the release process changes.
