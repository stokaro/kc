# Releasing kc

This document is for maintainers. kc uses [GoReleaser](https://goreleaser.com) to build archives for linux and darwin (amd64 and arm64), publish GitHub Releases to [`stokaro/kc`](https://github.com/stokaro/kc/releases), and update the Homebrew cask in the tap [`stokaro/homebrew-tap`](https://github.com/stokaro/homebrew-tap). Decisions behind this setup are in [`docs/adr/0061-publish-fork.md`](adr/0061-publish-fork.md).

## One-time setup

These steps need repository admin rights and are not automated.

1. Create the public repository `stokaro/homebrew-tap` with a `main` branch (an initial README is enough). GoReleaser writes `Casks/kc.rb` into it. Users then run `brew install --cask stokaro/tap/kc`.
2. Create a fine-grained personal access token with "Contents: read and write" on `stokaro/homebrew-tap` only. Store it as the repository secret `HOMEBREW_TAP_GITHUB_TOKEN` in `stokaro/kc`. Without it, a release still succeeds but the cask is not published.
3. Create a token for the releases themselves and store it as `RELEASE_GITHUB_TOKEN` (fine-grained, "Contents: read and write" on `stokaro/kc`).
4. Create or choose a GPG signing key. Store the ASCII-armored private key as `RELEASE_GPG_PRIVATE_KEY` and its passphrase as `RELEASE_GPG_PASSPHRASE`. Publish the public key (for example in the release notes or on a key server) so users can check the `.asc` files.
5. Optional: add the Apple signing and notarization secrets (see below). Without them the macOS binaries are not notarized; the cask removes the quarantine attribute so they still start.
6. Cut the first release with a tag newer than every tag in the repository. The existing tags up to `v0.8.0` come from upstream and use the old module path `github.com/sttts/kc`, so `go install github.com/stokaro/kc/cmd/kc@latest` only works after a newer tag exists.

## Required secrets

| Secret | Required | Purpose |
| --- | --- | --- |
| `RELEASE_GITHUB_TOKEN` | yes | Creates the GitHub Release in `stokaro/kc`. |
| `RELEASE_GPG_PRIVATE_KEY` | yes | ASCII-armored private key that signs the archives and the checksum file. The workflow stops when it is missing. |
| `RELEASE_GPG_PASSPHRASE` | yes, if the key has one | Passphrase for the key above. |
| `HOMEBREW_TAP_GITHUB_TOKEN` | no | Pushes the cask to `stokaro/homebrew-tap`. When it is missing, the cask is only written to `dist/`. |
| `MACOS_SIGN_P12` | no | Base64 of the exported Developer ID Application `.p12` certificate. |
| `MACOS_SIGN_PASSWORD` | no | Password used when exporting the `.p12` file. |
| `MACOS_NOTARY_KEY` | no | Base64 of the App Store Connect API key (`.p8`). |
| `MACOS_NOTARY_KEY_ID` | no | Key ID of the App Store Connect API key. |
| `MACOS_NOTARY_ISSUER_ID` | no | Issuer ID (UUID) of that API key. |

Notarization runs only when both `MACOS_SIGN_P12` and `MACOS_NOTARY_KEY` are set.

## Cutting a release

1. Make sure `main` is green, including the `release config` job of the build workflow.
2. Tag the commit and push the tag:

   ```bash
   git tag v0.9.0
   git push origin v0.9.0
   ```

3. The push triggers `.github/workflows/release.yml`. It imports the GPG key, builds and archives the binaries, writes and signs `kc_checksums.txt`, notarizes the macOS binaries when the Apple secrets exist, publishes the GitHub Release, and pushes `Casks/kc.rb` to the tap when `HOMEBREW_TAP_GITHUB_TOKEN` is set.
4. Check the result:

   ```bash
   go install github.com/stokaro/kc/cmd/kc@v0.9.0 && kc --version
   brew install --cask stokaro/tap/kc && kc --version
   ```

## Checking the configuration without publishing

Every push and pull request runs the `release config` job in `.github/workflows/build.yml`. It runs `goreleaser check` (which fails on deprecated settings too), builds a linux/amd64 snapshot archive without signing or publishing, extracts it and runs `kc --version` and `kc version --client` with an empty home directory and no kubeconfig.

To do the same locally for all platforms:

```bash
goreleaser check
goreleaser release --snapshot --clean --skip=sign
mkdir -p /tmp/kc-smoke
tar -xzf dist/kc_*_darwin_arm64.tar.gz -C /tmp/kc-smoke   # pick your platform
env -i HOME=/tmp/kc-smoke PATH=/usr/bin:/bin /tmp/kc-smoke/kc --version
```

A snapshot never touches GitHub or the tap. To also test signing, import the key and pass `GPG_PASSPHRASE_FILE=<file>` without `--skip=sign`.

## Apple signing and notarization

macOS binaries can be signed with a Developer ID Application certificate and notarized through App Store Connect so Gatekeeper trusts them.

1. In the Apple Developer portal, create a **Developer ID Application** certificate. Import it into Keychain, export it as a password-protected `.p12`, and base64-encode it (`base64 -i Certificates.p12 | tr -d '\n' > macos_sign_p12.txt`). Store the content in `MACOS_SIGN_P12` and the export password in `MACOS_SIGN_PASSWORD`.
2. In App Store Connect, create an **API key** with notarization access. Note the issuer ID and key ID, and base64-encode the `.p8` file the same way. Store the values in `MACOS_NOTARY_KEY`, `MACOS_NOTARY_KEY_ID` and `MACOS_NOTARY_ISSUER_ID`.
3. Notarization waits up to 20 minutes, the maximum GoReleaser allows. After installing, check a binary with `spctl -a -vv "$(command -v kc)"`.

## Publishing manually

If CI is down, run the release locally from a clean checkout of the tag:

```bash
GITHUB_TOKEN=<token> HOMEBREW_TAP_GITHUB_TOKEN=<tap token> \
  GPG_PASSPHRASE_FILE=/path/to/passphrase goreleaser release --clean
```

The GPG key must be imported (`gpg --import`).

Keep this document up to date whenever the release process changes.
