# Installing kc

kc runs on Linux and macOS, amd64 and arm64.

## Install with Homebrew

The tap is [`stokaro/homebrew-kc`](https://github.com/stokaro/homebrew-kc). It works on macOS and Linux:

```bash
brew install --cask stokaro/kc/kc
```

The cask installs the latest release and removes the macOS quarantine attribute, so kc starts although its macOS binaries are not notarized yet.

## Download a release binary

Each release on the [GitHub Releases page](https://github.com/stokaro/kc/releases) has `.tar.gz` archives for Linux and macOS (amd64 and arm64) and a `kc_checksums.txt` file:

```bash
VERSION=X.Y.Z   # without the leading "v"
curl -LO "https://github.com/stokaro/kc/releases/download/v${VERSION}/kc_${VERSION}_linux_amd64.tar.gz"
curl -LO "https://github.com/stokaro/kc/releases/download/v${VERSION}/kc_checksums.txt"
shasum -a 256 --check --ignore-missing kc_checksums.txt
tar -xzf "kc_${VERSION}_linux_amd64.tar.gz" kc
./kc --version
```

The macOS binaries are not notarized by Apple yet. An archive downloaded with a browser carries the quarantine attribute, and macOS then refuses to start kc ("cannot verify the developer"). Remove the attribute after unpacking:

```bash
xattr -d com.apple.quarantine kc
```

`curl` and Homebrew do not set it.

The checksum file is signed with [cosign](https://docs.sigstore.dev/) by the release workflow. To check the signature, also download `kc_checksums.txt.sigstore.json` and run:

```bash
cosign verify-blob --bundle kc_checksums.txt.sigstore.json kc_checksums.txt \
  --certificate-identity-regexp '^https://github.com/stokaro/kc/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Build from source

kc needs Go 1.27 or newer.

```bash
git clone https://github.com/stokaro/kc.git
cd kc
go run ./cmd/kc            # run without building
go build -o kc ./cmd/kc    # or build a binary
./kc
```

Or install with Go 1.27 or later: `go install github.com/stokaro/kc/cmd/kc@latest` for the latest release, or `@main` for the current development version.

## Check the installed version

`kc --version` and `kc version --client` print the kc version, the client-go version, the commit and the build date. They do not read a kubeconfig or contact a cluster. `kc version` also asks the current cluster for its version, as `kubectl version` does, and fails when it cannot reach one.
